package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

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

// TestResumeCommandContinuesSession verifies the resume command re-enters chat
// on an existing session and prints a copyable continuation command on exit.
func TestResumeCommandContinuesSession(t *testing.T) {
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

	cfg, err := parseFlags(
		[]string{
			"resume",
			"--session-dir",
			sessionDir,
			"--provider",
			providerEcho,
			result.SessionID[:8],
		},
		&bytes.Buffer{},
	)
	if err != nil {
		t.Fatalf("parse resume flags: %v", err)
	}

	var chatOut, chatErr bytes.Buffer
	code = runChat(
		cfg, strings.NewReader("follow-up\n/exit\n"), &chatOut,
		&chatErr,
	)
	if code != 0 {
		t.Fatalf("resume failed: code=%d stdout=%q stderr=%q", code,
			chatOut.String(), chatErr.String())
	}
	if !strings.Contains(
		chatOut.String(), "continuing session "+
			shortID(result.SessionID),
	) {

		t.Fatalf("missing resume notice: %q", chatOut.String())
	}
	if !strings.Contains(
		chatOut.String(), "resume: harness resume "+
			"--session-dir "+sessionDir+" "+result.SessionID,
	) {

		t.Fatalf("missing resume hint: %q", chatOut.String())
	}

	var showOut, showErr bytes.Buffer
	code = run(
		[]string{"show", "--session-dir", sessionDir, result.SessionID},
		&showOut, &showErr,
	)
	if code != 0 {
		t.Fatalf("show failed: code=%d stdout=%q stderr=%q", code,
			showOut.String(), showErr.String())
	}
	got := strings.TrimSpace(showOut.String())
	for _, want := range []string{
		"user: hello",
		"assistant: hello",
		"user: follow-up",
		"assistant: follow-up",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("transcript missing %q:\n%s", want, got)
		}
	}
}

// TestResumeCommandRequiresSession verifies resume rejects missing IDs before
// opening provider or tool state.
func TestResumeCommandRequiresSession(t *testing.T) {
	_, err := parseFlags([]string{"resume"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "resume requires") {
		t.Fatalf("unexpected resume parse error: %v", err)
	}
}

// TestChatResumeCommandQuotesSessionDir verifies printed resume commands stay
// copyable when the session directory contains shell metacharacters.
func TestChatResumeCommandQuotesSessionDir(t *testing.T) {
	got := chatResumeCommand(cliConfig{
		sessionDir: "/tmp/harness sessions/$USER's",
	}, "abc123")
	want := "harness resume --session-dir '/tmp/harness " +
		"sessions/$USER'\"'\"'s' abc123"
	if got != want {
		t.Fatalf("resume command mismatch:\nwant %q\ngot  %q", want,
			got)
	}
}
