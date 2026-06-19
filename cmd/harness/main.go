package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"harness/internal/core"
	"harness/internal/model"
	"harness/internal/session"
)

const (
	// defaultSessionDir is the local directory used for JSONL session logs
	// when the caller does not provide one.
	defaultSessionDir = ".harness/sessions"

	// commandRun is the implicit command used when the caller passes -p.
	commandRun = "run"

	// commandSessions lists known local session index entries.
	commandSessions = "sessions"

	// commandShow renders one local session transcript.
	commandShow = "show"
)

// cliConfig stores parsed command-line options for one invocation.
type cliConfig struct {
	command    string
	prompt     string
	sessionDir string
	jsonOutput bool
	sessionID  string
}

// main runs the command and exits with the returned status code.
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses flags, executes one agent turn, and renders the result.
func run(args []string, stdout io.Writer, stderr io.Writer) int {
	cfg, err := parseFlags(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 2
	}

	switch cfg.command {
	case commandRun:
		return runPrompt(cfg, stdout, stderr)

	case commandSessions:
		return listSessions(cfg, stdout, stderr)

	case commandShow:
		return showSession(cfg, stdout, stderr)

	default:
		fmt.Fprintf(stderr, "error: unknown command %q\n", cfg.command)

		return 2
	}
}

// runPrompt executes the default non-interactive prompt path.
func runPrompt(cfg cliConfig, stdout io.Writer, stderr io.Writer) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "error: get working directory:", err)

		return 1
	}

	result, err := core.RunTurn(context.Background(), core.TurnRequest{
		Prompt:     cfg.prompt,
		SessionDir: cfg.sessionDir,
		CWD:        cwd,
		Model:      model.EchoClient{},
	})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	if cfg.jsonOutput {
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			fmt.Fprintln(stderr, "error: encode json output:", err)

			return 1
		}

		return 0
	}

	fmt.Fprintf(stdout, "assistant: %s\n", result.AssistantText)

	return 0
}

// listSessions renders the local session index in text or JSON form.
func listSessions(cfg cliConfig, stdout io.Writer, stderr io.Writer) int {
	entries, err := session.List(cfg.sessionDir)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	if cfg.jsonOutput {
		if err := json.NewEncoder(stdout).Encode(entries); err != nil {
			fmt.Fprintln(stderr, "error: encode json output:", err)

			return 1
		}

		return 0
	}

	if len(entries) == 0 {
		fmt.Fprintln(stdout, "no sessions")

		return 0
	}

	table := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, entry := range entries {
		fmt.Fprintf(
			table, "%s	%s	%s\n",
			formatSessionTime(entry.CreatedAt), shortID(entry.ID),
			entry.Title,
		)
	}
	if err := table.Flush(); err != nil {
		fmt.Fprintln(stderr, "error: render session list:", err)

		return 1
	}

	return 0
}

// showSession renders one local session by exact ID or unambiguous ID prefix.
func showSession(cfg cliConfig, stdout io.Writer, stderr io.Writer) int {
	entry, err := session.Resolve(cfg.sessionDir, cfg.sessionID)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	events, err := session.ReadAll(entry.Path)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	if cfg.jsonOutput {
		if err := json.NewEncoder(stdout).Encode(events); err != nil {
			fmt.Fprintln(stderr, "error: encode json output:", err)

			return 1
		}

		return 0
	}

	for _, event := range events {
		if !isMessageEvent(event.Type) {
			continue
		}
		message, err := decodeMessage(event)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)

			return 1
		}
		fmt.Fprintf(
			stdout, "%s: %s\n", message.Role, messageText(message),
		)
	}

	return 0
}

// parseFlags converts CLI arguments into the command configuration.
func parseFlags(args []string, stderr io.Writer) (cliConfig, error) {
	if len(args) > 0 {
		switch args[0] {
		case commandSessions:
			return parseSessionsFlags(args[1:], stderr)

		case commandShow:
			return parseShowFlags(args[1:], stderr)
		}
	}

	return parseRunFlags(args, stderr)
}

// parseRunFlags converts default command flags into a run configuration.
func parseRunFlags(args []string, stderr io.Writer) (cliConfig, error) {
	var cfg cliConfig
	cfg.command = commandRun
	fs := flag.NewFlagSet("harness", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.prompt, "p", "", "prompt to run non-interactively")
	fs.StringVar(
		&cfg.sessionDir, "session-dir", defaultSessionDir,
		"session log directory",
	)
	fs.BoolVar(
		&cfg.jsonOutput, "json", false, "print the turn result as JSON",
	)
	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	if cfg.prompt == "" {
		return cliConfig{}, fmt.Errorf("provide a prompt with -p")
	}

	return cfg, nil
}

// parseSessionsFlags converts sessions subcommand flags into configuration.
func parseSessionsFlags(args []string, stderr io.Writer) (cliConfig, error) {
	cfg := cliConfig{
		command:    commandSessions,
		sessionDir: defaultSessionDir,
	}
	fs := flag.NewFlagSet(commandSessions, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(
		&cfg.sessionDir, "session-dir", defaultSessionDir,
		"session log directory",
	)
	fs.BoolVar(
		&cfg.jsonOutput, "json", false,
		"print the session list as JSON",
	)
	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}

	return cfg, nil
}

// parseShowFlags converts show subcommand flags and arguments into
// configuration.
func parseShowFlags(args []string, stderr io.Writer) (cliConfig, error) {
	cfg := cliConfig{
		command:    commandShow,
		sessionDir: defaultSessionDir,
	}
	fs := flag.NewFlagSet(commandShow, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(
		&cfg.sessionDir, "session-dir", defaultSessionDir,
		"session log directory",
	)
	fs.BoolVar(
		&cfg.jsonOutput, "json", false,
		"print the raw session events as JSON",
	)
	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	if fs.NArg() != 1 {
		return cliConfig{}, fmt.Errorf("provide exactly one session " +
			"id or prefix")
	}
	cfg.sessionID = fs.Arg(0)

	return cfg, nil
}

// isMessageEvent reports whether an event contains user-visible message text.
func isMessageEvent(eventType string) bool {
	return eventType == session.EventUserMessage ||
		eventType == session.EventAssistantMessage
}

// decodeMessage unmarshals a message event payload into its typed shape.
func decodeMessage(event session.Event) (session.MessageData, error) {
	var message session.MessageData
	if err := json.Unmarshal(event.Data, &message); err != nil {
		return session.MessageData{}, fmt.Errorf("decode message "+
			"event: %w", err)
	}

	return message, nil
}

// messageText joins text content parts for human transcript rendering.
func messageText(message session.MessageData) string {
	var parts []string
	for _, part := range message.Content {
		if part.Type == session.ContentText {
			parts = append(parts, part.Text)
		}
	}

	return strings.Join(parts, "")
}

// formatSessionTime renders index timestamps for compact terminal lists.
func formatSessionTime(createdAt time.Time) string {
	return createdAt.Local().Format("2006-01-02 15:04")
}

// shortID returns the display prefix used in human session lists.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}

	return id[:8]
}
