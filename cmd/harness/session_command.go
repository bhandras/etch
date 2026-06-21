package main

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"harness/internal/render"
	"harness/internal/session"
)

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

	if err := renderEvents(events, stdout); err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	return 0
}

// renderSessionPath renders one session transcript from a JSONL path.
func renderSessionPath(path string, stdout io.Writer) error {
	events, err := session.ReadAll(path)
	if err != nil {
		return err
	}

	return renderEvents(events, stdout)
}

// printSessionStatus renders durable activity statistics for a session.
func printSessionStatus(path string, stdout io.Writer) error {
	events, err := session.ReadAll(path)
	if err != nil {
		return err
	}
	status, err := session.BuildStatus(events, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, session.FormatStatus(status))

	return nil
}

// renderEvents renders model-visible session messages as transcript lines.
func renderEvents(events []session.Event, stdout io.Writer) error {
	for _, event := range events {
		if !isMessageEvent(event.Type) {
			continue
		}
		message, err := decodeMessage(event)
		if err != nil {
			return err
		}
		for _, line := range render.MessageLines(message) {
			fmt.Fprintln(stdout, line)
		}
	}

	return nil
}

// isMessageEvent reports whether an event contains user-visible message text.
func isMessageEvent(eventType string) bool {
	return eventType == session.EventUserMessage ||
		eventType == session.EventAssistantMessage ||
		eventType == session.EventToolMessage
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

// decodeUsage decodes a durable model usage event.
func decodeUsage(event session.Event) (session.UsageData, error) {
	var data session.UsageData
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return session.UsageData{}, fmt.Errorf("decode usage: %w", err)
	}

	return data, nil
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
