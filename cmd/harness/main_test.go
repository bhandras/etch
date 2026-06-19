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
			if r.Header.Get("Authorization") != "Bearer secret" {
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
			"--api-key", "secret",
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
	if !strings.Contains(stdout.String(), "assistant: hello") {
		t.Fatalf("missing first answer: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "assistant: follow-up") {
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

// writeFile creates a small file fixture for CLI tests.
func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
