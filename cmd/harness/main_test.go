package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/model"
	"harness/internal/session"
)

// TestRunWritesSessionAndListsIt exercises the CLI path from prompt execution
// to local session listing.
func TestRunWritesSessionAndListsIt(t *testing.T) {
	t.Setenv("HARNESS_PROVIDER", "")
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_API_KEY", "")

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
	t.Setenv("HARNESS_PROVIDER", "")
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_API_KEY", "")

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

// TestRunUsesOpenAIProvider verifies that provider flags reach the
// OpenAI-compatible streaming client without making a network call.
func TestRunUsesOpenAIProvider(t *testing.T) {
	t.Setenv("HARNESS_PROVIDER", "")
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_API_KEY", "")

	sessionDir := filepath.Join(t.TempDir(), "sessions")
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Fatalf("unexpected auth header: %q",
					r.Header.Get("Authorization"))
			}

			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(
				w, "data: "+
					"{\"choices\":[{\"delta\":{\"content\":\"hi"+
					"\"}}]}\n\n",
			)
			fmt.Fprint(w, "data: [DONE]\n\n")
		}),
	)
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{
			"--session-dir", sessionDir,
			"--provider", "openai",
			"--base-url", server.URL,
			"--api-key", "test-token",
			"--model", "test-model",
			"-p", "hello",
		},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("openai run failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "assistant: hi" {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

// TestRunUsesOpenAIAPIKeyEnv verifies that environment auth remains active
// when the caller does not pass an explicit API key flag.
func TestRunUsesOpenAIAPIKeyEnv(t *testing.T) {
	t.Setenv("HARNESS_PROVIDER", "")
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_API_KEY", "env-token")

	sessionDir := filepath.Join(t.TempDir(), "sessions")
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer env-token" {
				t.Fatalf("unexpected auth header: %q",
					r.Header.Get("Authorization"))
			}

			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(
				w, "data: "+
					"{\"choices\":[{\"delta\":{\"content\":\"hi"+
					"\"}}]}\n\n",
			)
			fmt.Fprint(w, "data: [DONE]\n\n")
		}),
	)
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{
			"--session-dir", sessionDir,
			"--provider", "openai",
			"--base-url", server.URL,
			"--model", "test-model",
			"-p", "hello",
		},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("openai run failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
}

// TestHelpDoesNotPrintOpenAIAPIKeyEnv verifies that flag help keeps
// environment-sourced credentials out of diagnostic output.
func TestHelpDoesNotPrintOpenAIAPIKeyEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-token")

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
		})
	}
}

// TestToolLSRunsDirectly verifies the manual builtin tool smoke path.
func TestToolLSRunsDirectly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "")

	var stdout, stderr bytes.Buffer
	code := run([]string{"tool", "ls", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("tool failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "go.mod" {
		t.Fatalf("unexpected tool output: %q", stdout.String())
	}
}

// TestToolReadRunsDirectly verifies the manual text file read smoke path.
func TestToolReadRunsDirectly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	writeFile(t, path, "alpha\nbeta")

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"tool", "read", "--limit", "1", path}, &stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("tool failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "alpha") {
		t.Fatalf("unexpected tool output: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Use offset=2 to continue") {
		t.Fatalf("missing continuation hint: %q", stdout.String())
	}
}

// TestToolFindRunsDirectly verifies the manual recursive path search smoke
// path.
func TestToolFindRunsDirectly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "")

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"tool", "find", "readme", dir}, &stdout, &stderr,
	)
	if code != 0 {
		t.Fatalf("tool failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "README.md" {
		t.Fatalf("unexpected tool output: %q", stdout.String())
	}
}

// TestToolGrepRunsDirectly verifies the manual literal search smoke path.
func TestToolGrepRunsDirectly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "note.txt"), "alpha\nNeedle\n")

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"tool", "grep", "--ignore-case", "needle", dir},
		&stdout, &stderr,
	)
	if code != 0 {
		t.Fatalf("tool failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "note.txt:2:Needle" {
		t.Fatalf("unexpected tool output: %q", stdout.String())
	}
}

// TestToolWriteRunsDirectly verifies the manual whole-file write smoke path.
func TestToolWriteRunsDirectly(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{
			"tool", "write", "--content", "hello\n", "note.txt",
		},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("tool failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Successfully wrote 6 bytes") {
		t.Fatalf("unexpected tool output: %q", stdout.String())
	}
	content, err := os.ReadFile(filepath.Join(dir, "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello\n" {
		t.Fatalf("unexpected file content: %q", string(content))
	}
}

// TestToolEditRunsDirectly verifies the manual exact replacement edit smoke
// path.
func TestToolEditRunsDirectly(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "note.txt"), "hello\n")

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{
			"tool", "edit", "--old", "hello", "--new", "goodbye",
			"note.txt",
		},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("tool failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Successfully applied 1 edit") {
		t.Fatalf("unexpected tool output: %q", stdout.String())
	}
	content, err := os.ReadFile(filepath.Join(dir, "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "goodbye\n" {
		t.Fatalf("unexpected file content: %q", string(content))
	}
}

// TestToolBashRunsDirectly verifies the manual bounded command smoke path.
func TestToolBashRunsDirectly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"tool", "bash", "--", "printf", "hello"}, &stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("tool failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "exit code: 0") {
		t.Fatalf("missing exit code: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "stdout:\nhello") {
		t.Fatalf("missing stdout: %q", stdout.String())
	}
}

// TestRunChatProcessesMultipleTurns verifies the minimal line-oriented chat
// loop keeps a session alive across prompts.
func TestRunChatProcessesMultipleTurns(t *testing.T) {
	t.Setenv("HARNESS_PROVIDER", "")
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_API_KEY", "")

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

// TestRunChatListsTools verifies that slash commands run without starting a
// model turn.
func TestRunChatListsTools(t *testing.T) {
	cfg := cliConfig{
		command:    commandChat,
		sessionDir: filepath.Join(t.TempDir(), "sessions"),
		provider:   providerEcho,
	}
	var stdout, stderr bytes.Buffer
	code := runChat(
		cfg, strings.NewReader("/tools\n/exit\n"), &stdout, &stderr,
	)
	if code != 0 {
		t.Fatalf("chat failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "bash") ||
		!strings.Contains(stdout.String(), "edit") {

		t.Fatalf("missing tools: %q", stdout.String())
	}
}

// TestRunChatContextAndCompactCommands verifies the context stats and manual
// compaction slash commands.
func TestRunChatContextAndCompactCommands(t *testing.T) {
	cfg := cliConfig{
		command:      commandChat,
		sessionDir:   filepath.Join(t.TempDir(), "sessions"),
		provider:     providerEcho,
		keepMessages: 1,
	}
	var stdout, stderr bytes.Buffer
	code := runChat(
		cfg, strings.NewReader(
			"one\ntwo\n/context\n/compact\n/context\n/exit\n",
		),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("chat failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "summary: inactive") {
		t.Fatalf("missing inactive context stats: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "compacted context:") {
		t.Fatalf("missing compact result: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "summary: active") {
		t.Fatalf("missing active context stats: %q", stdout.String())
	}
}

// TestRunChatStatusCommand verifies the status slash command reports durable
// session activity without starting a separate model turn.
func TestRunChatStatusCommand(t *testing.T) {
	cfg := cliConfig{
		command:    commandChat,
		sessionDir: filepath.Join(t.TempDir(), "sessions"),
		provider:   providerEcho,
	}
	var stdout, stderr bytes.Buffer
	code := runChat(
		cfg, strings.NewReader("hello\n/status\n/exit\n"), &stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("chat failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "session age:") {
		t.Fatalf("missing session age: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "turns: 1") {
		t.Fatalf("missing turn count: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "model calls: 1") {
		t.Fatalf("missing model call count: %q", stdout.String())
	}
	if !strings.Contains(
		stdout.String(),
		"actual model usage: not recorded yet",
	) {

		t.Fatalf("missing usage placeholder: %q", stdout.String())
	}
}

// TestChatObserverRendersToolEvents verifies that live chat feedback pairs each
// local tool call with its result instead of batching call headers first.
func TestChatObserverRendersToolEvents(t *testing.T) {
	var stdout bytes.Buffer
	observer := &chatObserver{
		renderer: newLiveChatRenderer(&stdout, false),
	}

	observer.EventAppended(messageEvent(t,
		session.EventAssistantMessage,
		session.AssistantToolCallMessage("", []session.ToolCallData{{
			ID:        "call_1",
			Name:      "bash",
			Arguments: `{"command":"go test ./..."}`,
		}}),
	))
	observer.ToolCallStarted(model.ToolCall{
		ID:        "call_1",
		Name:      "bash",
		Arguments: `{"command":"go test ./..."}`,
	})
	observer.EventAppended(
		messageEvent(
			t, session.EventToolMessage,
			session.ToolMessage("call_1", "bash", "exit code: 0\n"),
		),
	)

	got := stdout.String()
	want := "• Ran bash go test ./...\n\n   exit code: 0\n"
	if got != want {
		t.Fatalf("render mismatch:\nwant %q\ngot  %q", want, got)
	}
}

// TestChatObserverSeparatesAssistantReplies verifies that final assistant text
// is visually separated from prior tool output.
func TestChatObserverSeparatesAssistantReplies(t *testing.T) {
	var stdout bytes.Buffer
	observer := &chatObserver{
		renderer: newLiveChatRenderer(&stdout, false),
	}

	observer.ReasoningCompleted("checking the README")
	observer.ToolCallStarted(model.ToolCall{
		ID:        "call_1",
		Name:      "read",
		Arguments: `{"path":"README.md"}`,
	})
	observer.EventAppended(
		messageEvent(
			t, session.EventToolMessage,
			session.ToolMessage("call_1", "read", "hello\n"),
		),
	)
	observer.EventAppended(
		messageEvent(
			t, session.EventAssistantMessage,
			session.TextMessage(session.RoleAssistant, "done"),
		),
	)

	got := stdout.String()
	if !strings.Contains(
		got, "• checking the README\n\n• Ran read README.md\n\n   "+
			"read 1 lines\n\n• done\n",
	) {

		t.Fatalf("assistant reply was not separated: %q", got)
	}
}

// messageEvent creates one durable message event for CLI rendering tests.
func messageEvent(t *testing.T, eventType string,
	message session.MessageData) session.Event {

	t.Helper()
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}

	return session.Event{
		Type: eventType,
		ID:   "event_1",
		Time: time.Now().UTC(),
		Data: raw,
	}
}

// writeFile creates a small file fixture for CLI tests.
func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
