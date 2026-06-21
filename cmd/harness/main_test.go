package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"harness/internal/session"
)

const (
	// cliPluginHelperEnv enables the subprocess plugin used by CLI tests.
	cliPluginHelperEnv = "HARNESS_CLI_PLUGIN_HELPER"
)

// TestMain runs command tests from an empty directory so project-config
// discovery cannot inherit the developer's local .harness/config.toml.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "harness-cmd-test-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create test cwd:", err)
		os.Exit(1)
	}
	if err := os.Chdir(dir); err != nil {
		fmt.Fprintln(os.Stderr, "enter test cwd:", err)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// TestRunWritesSessionAndListsIt exercises the CLI path from prompt execution
// to local session listing.
func TestRunWritesSessionAndListsIt(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "sessions")

	var runOut, runErr bytes.Buffer
	code := run(
		[]string{"--session-dir", sessionDir, "-p", "hello"}, &runOut,
		&runErr,
	)
	if code != 0 {
		t.Fatalf("run failed: code=%d stdout=%q stderr=%q", code,
			runOut.String(), runErr.String())
	}
	if strings.TrimSpace(runOut.String()) != "assistant: hello" {
		t.Fatalf("unexpected run output: %q", runOut.String())
	}

	var listOut, listErr bytes.Buffer
	code = run(
		[]string{"sessions", "--session-dir", sessionDir}, &listOut,
		&listErr,
	)
	if code != 0 {
		t.Fatalf("sessions failed: code=%d stdout=%q stderr=%q", code,
			listOut.String(), listErr.String())
	}
	if !strings.Contains(listOut.String(), "hello") {
		t.Fatalf("session list missing title: %q", listOut.String())
	}
}

// TestRunUsesProjectConfigDefaults verifies .harness/config.toml supplies CLI
// defaults before explicit flags are applied.
func TestRunUsesProjectConfigDefaults(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.Mkdir(filepath.Join(root, ".harness"), 0o755); err != nil {
		t.Fatalf("make config dir: %v", err)
	}
	writeFile(
		t, filepath.Join(root, ".harness", "config.toml"),
		"[session]\ndir = \"configured-sessions\"\nmax_tool_rounds "+
			"= 9\n",
	)

	var stdout, stderr bytes.Buffer
	code := run([]string{"-p", "hello"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}

	entries, err := os.ReadDir(filepath.Join(root, "configured-sessions"))
	if err != nil {
		t.Fatalf("read configured session dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected a session file in configured directory")
	}
}

// TestChatPromptHistoryLoadsDurableUserMessages verifies prompt navigation can
// hydrate from the same append-only session log used for resume.
func TestChatPromptHistoryLoadsDurableUserMessages(t *testing.T) {
	store, _, err := session.Create(
		filepath.Join(
			t.TempDir(),
			"sessions",
		),
		"/tmp", "first",
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close session: %v", err)
		}
	}()

	if _, err := store.Append(
		session.EventUserMessage, store.LastID(),
		session.TextMessage(session.RoleUser, "first"),
	); err != nil {

		t.Fatalf("append first prompt: %v", err)
	}
	if _, err := store.Append(
		session.EventAssistantMessage, store.LastID(),
		session.TextMessage(session.RoleAssistant, "answer"),
	); err != nil {

		t.Fatalf("append assistant message: %v", err)
	}
	if _, err := store.Append(
		session.EventUserMessage, store.LastID(),
		session.TextMessage(session.RoleUser, "second"),
	); err != nil {

		t.Fatalf("append second prompt: %v", err)
	}

	got, err := chatPromptHistory(store.Path())
	if err != nil {
		t.Fatalf("load prompt history: %v", err)
	}
	if strings.Join(got, ",") != "first,second" {
		t.Fatalf("prompt history = %q, want first,second", got)
	}
}

// TestShowRendersTranscript verifies that a listed session can be resolved by
// short ID and rendered as a readable transcript.
func TestShowRendersTranscript(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "sessions")

	var jsonOut, jsonErr bytes.Buffer
	code := run(
		[]string{"--session-dir", sessionDir, "--json", "-p", "hello"},
		&jsonOut, &jsonErr,
	)
	if code != 0 {
		t.Fatalf("json run failed: code=%d stdout=%q stderr=%q", code,
			jsonOut.String(), jsonErr.String())
	}

	var result struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(jsonOut.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SessionID == "" {
		t.Fatal("expected session id in json output")
	}

	var showOut, showErr bytes.Buffer
	code = run(
		[]string{
			"show",
			"--session-dir",
			sessionDir,
			result.SessionID[:8],
		},
		&showOut,
		&showErr,
	)
	if code != 0 {
		t.Fatalf("show failed: code=%d stdout=%q stderr=%q", code,
			showOut.String(), showErr.String())
	}

	got := strings.TrimSpace(showOut.String())
	want := "user: hello\nassistant: hello"
	if got != want {
		t.Fatalf("transcript mismatch:\nwant %q\ngot  %q", want, got)
	}
}

// TestAuthStatusDoesNotPrintCodexAccessToken verifies auth diagnostics reveal
// credential source but not bearer token material.
func TestAuthStatusDoesNotPrintCodexAccessToken(t *testing.T) {
	t.Setenv("CODEX_ACCESS_TOKEN", "secret-codex-token")

	var stdout, stderr bytes.Buffer
	code := run([]string{"auth", "status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("auth status failed: code=%d stdout=%q stderr=%q",
			code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "CODEX_ACCESS_TOKEN") {
		t.Fatalf("missing env token source: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "secret-codex-token") {
		t.Fatalf("auth status leaked token: %q", stdout.String())
	}
}

// TestTopLevelHelpExitsSuccessfully verifies no-argument and help invocations
// print command discovery text without reporting an error.
func TestTopLevelHelpExitsSuccessfully(t *testing.T) {
	tests := []struct {
		// name describes the help invocation being exercised.
		name string

		// args are the CLI arguments passed to run.
		args []string
	}{
		{
			name: "empty",
			args: nil,
		},
		{
			name: "long flag",
			args: []string{
				"--help",
			},
		},
		{
			name: "short flag",
			args: []string{
				"-h",
			},
		},
		{
			name: "help command",
			args: []string{
				"help",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(test.args, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("help failed: code=%d stdout=%q "+
					"stderr=%q", code, stdout.String(),
					stderr.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("help wrote stderr: %q",
					stderr.String())
			}
			if !strings.Contains(stdout.String(), "Commands:") ||
				!strings.Contains(stdout.String(), "chat") ||
				!strings.Contains(stdout.String(), "auth") {

				t.Fatalf("missing command help: %q",
					stdout.String())
			}
		})
	}
}

// TestSubcommandHelpExitsSuccessfully verifies direct and help-command forms
// render command-specific usage without an error prefix.
func TestSubcommandHelpExitsSuccessfully(t *testing.T) {
	tests := []struct {
		// name describes the help form being exercised.
		name string

		// args are the CLI arguments passed to run.
		args []string

		// want is a short usage fragment expected in output.
		want string
	}{
		{name: "chat flag", args: []string{
			"chat",
			"--help",
		},
			want: "Usage of chat:"},
		{name: "help chat", args: []string{
			"help",
			"chat",
		},
			want: "Usage of chat:"},
		{name: "help auth", args: []string{
			"help",
			"auth",
		},
			want: "harness auth login"},
		{name: "help tool", args: []string{
			"help",
			"tool",
		},
			want: "Tools:"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(test.args, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("help failed: code=%d stdout=%q "+
					"stderr=%q", code, stdout.String(),
					stderr.String())
			}
			combined := stdout.String() + stderr.String()
			if strings.Contains(combined, "error:") {
				t.Fatalf("help printed error: stdout=%q "+
					"stderr=%q", stdout.String(),
					stderr.String())
			}
			if !strings.Contains(combined, test.want) {
				t.Fatalf("missing %q in help output: "+
					"stdout=%q stderr=%q", test.want,
					stdout.String(), stderr.String())
			}
		})
	}
}

// TestUnknownCommandReportsCommandError verifies command-like positional input
// does not fall through to the implicit prompt runner.
func TestUnknownCommandReportsCommandError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"hat"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("unexpected code: %d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("unknown command wrote stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), `unknown command "hat"`) {
		t.Fatalf("missing unknown command error: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "harness help") {
		t.Fatalf("missing help hint: %q", stderr.String())
	}
}

// TestHelpDoesNotPrintOpenAIAPIKeyEnv verifies that flag help keeps
// environment-sourced credentials out of diagnostic output.
func TestHelpDoesNotPrintOpenAIAPIKeyEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-token")
	t.Setenv("OPENROUTER_API_KEY", "openrouter-env-token")
	t.Setenv("CODEX_ACCESS_TOKEN", "codex-env-token")

	tests := []struct {
		// name describes the command path being parsed.
		name string

		// args are the CLI arguments that should request help.
		args []string
	}{
		{
			name: "run",
			args: []string{
				"-h",
			},
		},
		{
			name: "chat",
			args: []string{
				"chat",
				"-h",
			},
		},
		{
			name: "compact",
			args: []string{
				"compact",
				"-h",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if _, err := parseFlags(
				test.args, &stderr,
			); err == nil {

				t.Fatal("expected help parse error")
			}
			if strings.Contains(stderr.String(), "env-token") {
				t.Fatalf("help leaked API key: %q",
					stderr.String())
			}
			if strings.Contains(stderr.String(), "codex-env-token") {
				t.Fatalf("help leaked Codex token: %q",
					stderr.String())
			}
			if strings.Contains(
				stderr.String(),
				"openrouter-env-token",
			) {

				t.Fatalf("help leaked OpenRouter token: %q",
					stderr.String())
			}
		})
	}
}

// TestCLIPluginHelperProcess runs as a subprocess plugin for CLI smoke tests.
func TestCLIPluginHelperProcess(t *testing.T) {
	if os.Getenv(cliPluginHelperEnv) != "1" {
		return
	}
	runCLIPluginHelper()
	os.Exit(0)
}

// TestRunChatProcessesMultipleTurns verifies the minimal line-oriented chat
// loop keeps a session alive across prompts.
func TestRunChatProcessesMultipleTurns(t *testing.T) {
	cfg := cliConfig{
		command:    commandChat,
		sessionDir: filepath.Join(t.TempDir(), "sessions"),
		provider:   providerEcho,
	}
	var stdout, stderr bytes.Buffer
	code := runChat(
		cfg, strings.NewReader("hello\nfollow-up\n/exit\n"), &stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("chat failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "• hello") {
		t.Fatalf("missing first answer: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "> \n• hello") {
		t.Fatalf("assistant output was not separated from prompt: %q",
			stdout.String())
	}
	if !strings.Contains(stdout.String(), "• follow-up") {
		t.Fatalf("missing second answer: %q", stdout.String())
	}
}

// TestRunChatSkipsWhitespacePrompt verifies whitespace-only input is ignored
// after trimming rather than started as a model turn.
func TestRunChatSkipsWhitespacePrompt(t *testing.T) {
	cfg := cliConfig{
		command:    commandChat,
		sessionDir: filepath.Join(t.TempDir(), "sessions"),
		provider:   providerEcho,
	}
	var stdout, stderr bytes.Buffer
	code := runChat(cfg, strings.NewReader("  \n/exit\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("chat failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}

	got := stdout.String()
	if strings.Contains(got, "•") {
		t.Fatalf("whitespace prompt reached model: %q", got)
	}
}

// cliPluginHelperCommand returns a shell command that starts this test binary
// as a minimal plugin process.
func cliPluginHelperCommand() string {
	return cliPluginHelperEnv + "=1 " + strconv.Quote(os.Args[0]) +
		" -test.run=TestCLIPluginHelperProcess --"
}

// runCLIPluginHelper serves the tiny JSONL protocol needed by CLI tests.
func runCLIPluginHelper() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     string          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			fmt.Fprintf(os.Stderr, "decode request: %v\n", err)

			return
		}
		switch req.Method {
		case "initialize":
			writeCLIPluginLine(map[string]any{
				"id": req.ID,
				"result": map[string]any{
					"name": "cli-helper",
					"tools": []map[string]any{{
						"name":        "plugin_echo",
						"description": "Echoes text through a plugin.",
						"parameters": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"text": map[string]any{
									"type": "string",
								},
							},
						},
					}},
				},
			})

		case "tool.execute":
			writeCLIPluginLine(map[string]any{
				"id": req.ID,
				"result": map[string]any{
					"content": []map[string]any{{
						"type": "text",
						"text": "plugin direct",
					}},
				},
			})

		default:
			writeCLIPluginLine(map[string]any{
				"id": req.ID,
				"error": map[string]any{
					"message": "unknown method",
				},
			})
		}
	}
}

// writeCLIPluginLine writes one JSONL response from the CLI helper plugin.
func writeCLIPluginLine(value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "encode response: %v\n", err)

		return
	}
	fmt.Fprintln(os.Stdout, string(encoded))
}

// writeFile creates a small file fixture for CLI tests.
func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
