package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	openaiauth "harness/internal/auth/openai"
	"harness/internal/model"
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

// TestRunUsesOpenAIProvider verifies that provider flags reach the
// OpenAI-compatible streaming client without making a network call.
func TestRunUsesOpenAIProvider(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	authFile := missingOpenAIAuthFile(t)

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
			"--auth-file", authFile,
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
	t.Setenv("OPENAI_API_KEY", "env-token")
	t.Setenv("OPENROUTER_API_KEY", "")
	authFile := missingOpenAIAuthFile(t)

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
			"--auth-file", authFile,
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

// TestRunUsesStoredOpenAIOAuthCredentialsFirst verifies stored ChatGPT/Codex
// OAuth credentials take precedence over API-key and Codex-token fallbacks.
func TestRunUsesStoredOpenAIOAuthCredentialsFirst(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-token")
	t.Setenv("OPENROUTER_API_KEY", "openrouter-token")
	t.Setenv("CODEX_ACCESS_TOKEN", "codex-token")

	root := t.TempDir()
	t.Chdir(root)

	sessionDir := filepath.Join(root, "sessions")
	var gotPath string
	var gotAuth string
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(
				w, "data: "+
					"{\"type\":\"response.output_item.added"+
					"\",\"item\":{\"type\":\"message\",\"id\":\"ms"+
					"g_1\",\"role\":\"assistant\"}}\n\n",
			)
			fmt.Fprint(
				w, "data: "+
					"{\"type\":\"response.output_text.delta"+
					"\",\"delta\":\"hi\"}\n\n",
			)
			fmt.Fprint(
				w,
				"data: {\"type\":\"response.completed\"}\n\n",
			)
		}),
	)
	defer server.Close()

	authPath, err := openaiauth.DefaultStorePath(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := openaiauth.Save(authPath, openaiauth.Credentials{
		CodexBaseURL: server.URL,
		LastRefresh:  time.Now(),
		Tokens: openaiauth.TokenData{
			AccessToken: "oauth-token",
		},
	}); err != nil {

		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{
			"--session-dir", sessionDir,
			"--provider", "openai",
			"--model", "test-model",
			"-p", "hello",
		},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("openai oauth run failed: code=%d stdout=%q stderr=%q",
			code, stdout.String(), stderr.String())
	}
	if gotPath != "/responses" {
		t.Fatalf("unexpected oauth path: %q", gotPath)
	}
	if gotAuth != "Bearer oauth-token" {
		t.Fatalf("unexpected oauth auth header: %q", gotAuth)
	}
	if strings.TrimSpace(stdout.String()) != "assistant: hi" {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

// TestRunUsesOpenRouterAPIKeyEnv verifies OpenRouter can be used as an
// OpenAI-compatible API-key fallback when no OAuth login is present.
func TestRunUsesOpenRouterAPIKeyEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "openrouter-token")
	t.Setenv("CODEX_ACCESS_TOKEN", "")
	authFile := missingOpenAIAuthFile(t)

	sessionDir := filepath.Join(t.TempDir(), "sessions")
	var gotAuth string
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
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
			"--auth-file", authFile,
			"--base-url", server.URL,
			"--model", "test-model",
			"-p", "hello",
		},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("openrouter run failed: code=%d stdout=%q stderr=%q",
			code, stdout.String(), stderr.String())
	}
	if gotAuth != "Bearer openrouter-token" {
		t.Fatalf("unexpected auth header: %q", gotAuth)
	}
}

// TestRunUsesCodexAccessTokenEnv verifies an explicit Codex access token is a
// usable OAuth-mode bearer credential without a stored login.
func TestRunUsesCodexAccessTokenEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("CODEX_ACCESS_TOKEN", "codex-token")
	authFile := missingOpenAIAuthFile(t)

	sessionDir := filepath.Join(t.TempDir(), "sessions")
	var gotAuth string
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(
				w, "data: "+
					"{\"type\":\"response.output_item.added"+
					"\",\"item\":{\"type\":\"message\",\"id\":\"ms"+
					"g_1\",\"role\":\"assistant\"}}\n\n",
			)
			fmt.Fprint(
				w, "data: "+
					"{\"type\":\"response.output_text.delta"+
					"\",\"delta\":\"hi\"}\n\n",
			)
			fmt.Fprint(
				w,
				"data: {\"type\":\"response.completed\"}\n\n",
			)
		}),
	)
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{
			"--session-dir", sessionDir,
			"--provider", "openai",
			"--auth-file", authFile,
			"--base-url", server.URL,
			"--model", "test-model",
			"-p", "hello",
		},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("codex token run failed: code=%d stdout=%q stderr=%q",
			code, stdout.String(), stderr.String())
	}
	if gotAuth != "Bearer codex-token" {
		t.Fatalf("unexpected auth header: %q", gotAuth)
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

// TestToolEditDryRunRunsDirectly verifies the manual exact replacement preview
// path without mutating the target file.
func TestToolEditDryRunRunsDirectly(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "note.txt"), "hello\n")

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{
			"tool", "edit", "--old", "hello", "--new", "goodbye",
			"--dry-run", "note.txt",
		},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("tool failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Previewed 1 edit") {
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

// TestToolPluginRunsDirectly verifies the direct tool smoke path can execute a
// configured plugin tool with raw JSON arguments.
func TestToolPluginRunsDirectly(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.Mkdir(filepath.Join(root, ".harness"), 0o755); err != nil {
		t.Fatalf("make config dir: %v", err)
	}
	writeFile(
		t, filepath.Join(root, ".harness", "config.toml"),
		fmt.Sprintf(
			"[[plugins]]\nname = \"helper\"\ncommand = %q\n",
			cliPluginHelperCommand(),
		),
	)

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{
			"tool",
			"plugin_echo",
			"--args",
			`{"text":"hello"}`,
		},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("plugin tool failed: code=%d stdout=%q stderr=%q",
			code, stdout.String(), stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "plugin direct" {
		t.Fatalf("unexpected plugin output: %q", stdout.String())
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

// TestRunChatAutoCompactsLargeContext verifies chat can compact session
// history automatically before a large follow-up turn reaches the model.
func TestRunChatAutoCompactsLargeContext(t *testing.T) {
	cfg := cliConfig{
		command:          commandChat,
		sessionDir:       filepath.Join(t.TempDir(), "sessions"),
		provider:         providerEcho,
		keepMessages:     1,
		autoCompact:      true,
		autoCompactLimit: 20,
	}
	prompt := strings.Repeat("alpha ", 60) + "\n" +
		strings.Repeat("beta ", 60) + "\n/status\n/exit\n"
	var stdout, stderr bytes.Buffer
	code := runChat(cfg, strings.NewReader(prompt), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("chat failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Compacted context:") {
		t.Fatalf("missing auto compact notice: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "compactions: 1 (1 auto") {
		t.Fatalf("missing auto compact status: %q", stdout.String())
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
	if !strings.Contains(stdout.String(), "- age:") {
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
		"Actual Model Usage\n- not recorded yet",
	) {

		t.Fatalf("missing usage placeholder: %q", stdout.String())
	}
}

// TestRunChatCommandWithOutputRedrawsPrompt verifies slash-command output
// moves the live composer aside and redraws it after command text.
func TestRunChatCommandWithOutputRedrawsPrompt(t *testing.T) {
	t.Setenv("COLUMNS", "32")
	var stdout, stderr bytes.Buffer
	composer := &terminalChatInput{
		stdout: &stdout,
		input:  []rune("typed while command runs"),
	}
	if err := composer.renderLocked(); err != nil {
		t.Fatalf("initial composer render failed: %v", err)
	}
	stdout.Reset()

	keepGoing, nextPath := runChatCommandWithOutput(
		composer, cliConfig{}, "/help", "", nil, nil, &stdout, &stderr,
		nil,
	)
	if !keepGoing {
		t.Fatalf("help command stopped chat")
	}
	if nextPath != "" {
		t.Fatalf("help command changed session path: %q", nextPath)
	}

	got := stdout.String()
	helpAt := strings.Index(got, "/exit /quit")
	promptAt := strings.LastIndex(got, "> typed while command runs")
	if helpAt < 0 {
		t.Fatalf("help output missing: %q", got)
	}
	if !strings.Contains(got, "\n/exit /quit") {
		t.Fatalf("slash output missing leading padding: %q", got)
	}
	if !strings.Contains(got, "/tools /help\n\n") {
		t.Fatalf("slash output missing trailing padding: %q", got)
	}
	if promptAt < 0 {
		t.Fatalf("composer was not redrawn: %q", got)
	}
	if promptAt < helpAt {
		t.Fatalf("composer was redrawn before slash output: %q", got)
	}
	if strings.Count(got, "> typed while command runs") != 1 {
		t.Fatalf("composer redraw count mismatch: %q", got)
	}
}

// TestChatChromeFormatsFooter verifies prompt footer metadata stays compact
// while usage accumulates across model calls.
func TestChatChromeFormatsFooter(t *testing.T) {
	chrome := newChatChrome(cliConfig{
		provider:        "openai",
		model:           "gpt-5.5",
		reasoningEffort: "high",
	}, filepath.Join(string(os.PathSeparator), "tmp", "harness"),
		model.Usage{})

	got := chrome.Footer()
	if !strings.Contains(got, "gpt-5.5 high") ||
		!strings.Contains(got, "/tmp/harness") {

		t.Fatalf("footer missing mode or cwd: %q", got)
	}

	got = chrome.AddUsage(model.Usage{
		InputTokens:           100,
		CachedInputTokens:     64,
		OutputTokens:          20,
		ReasoningOutputTokens: 5,
		TotalTokens:           120,
	})
	want := "100 in · 64 cached · 20 out"
	if !strings.Contains(got, want) {
		t.Fatalf("footer missing usage:\nwant %q\ngot  %q", want, got)
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

// TestChatObserverRendersFinalAfterNoiseOnlyDeltas verifies filtered live
// stream fragments do not suppress the durable final assistant answer.
func TestChatObserverRendersFinalAfterNoiseOnlyDeltas(t *testing.T) {
	var stdout bytes.Buffer
	observer := &chatObserver{
		renderer: newLiveChatRenderer(&stdout, false),
	}

	observer.ModelTextDelta(".\n:\n`.\n")
	observer.EventAppended(
		messageEvent(
			t, session.EventAssistantMessage, session.TextMessage(
				session.RoleAssistant, "real final answer",
			),
		),
	)

	got := stdout.String()
	if strings.Contains(got, "\n  .") ||
		strings.Contains(got, "\n  :") ||
		strings.Contains(got, "\n  `.") {

		t.Fatalf("noise-only delta became visible: %q", got)
	}
	if !strings.Contains(got, "• real final answer\n") {
		t.Fatalf("final assistant answer was suppressed: %q", got)
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

// missingOpenAIAuthFile returns a nonexistent auth path for fallback tests.
func missingOpenAIAuthFile(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), "missing-openai-auth.json")
}

// writeFile creates a small file fixture for CLI tests.
func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
