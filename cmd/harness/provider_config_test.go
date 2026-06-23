package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	openaiauth "harness/internal/auth/openai"
	harnessconfig "harness/internal/config"
)

// TestConfigSessionDirUsesHomeDefaultForHomeConfig verifies a global config
// does not scatter default session logs into whichever project launched chat.
func TestConfigSessionDirUsesHomeDefaultForHomeConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := configSessionDir(harnessconfig.Config{
		Path: filepath.Join(
			home, harnessconfig.ProjectConfigDir,
			harnessconfig.ConfigFileName,
		),
	})
	want := filepath.Join(home, ".harness", "sessions")
	if dir != want {
		t.Fatalf("unexpected session dir: got %q want %q", dir, want)
	}
}

// TestConfigSessionDirUsesHomeDefaultForMergedHomeConfig verifies a project
// config merged with home defaults still keeps default logs under home.
func TestConfigSessionDirUsesHomeDefaultForMergedHomeConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := configSessionDir(harnessconfig.Config{
		Path: filepath.Join(
			t.TempDir(), harnessconfig.ProjectConfigDir,
			harnessconfig.ConfigFileName,
		),
		Paths: []string{
			filepath.Join(
				home, harnessconfig.ProjectConfigDir,
				harnessconfig.ConfigFileName,
			),
			filepath.Join(
				t.TempDir(), harnessconfig.ProjectConfigDir,
				harnessconfig.ConfigFileName,
			),
		},
	})
	want := filepath.Join(home, ".harness", "sessions")
	if dir != want {
		t.Fatalf("unexpected session dir: got %q want %q", dir, want)
	}
}

// TestConfigSessionDirExpandsHome verifies user-level configs can spell paths
// portably without embedding an absolute home directory.
func TestConfigSessionDirExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := configSessionDir(harnessconfig.Config{
		Session: harnessconfig.SessionConfig{
			Dir: "~/.harness/sessions",
		},
	})
	want := filepath.Join(home, ".harness", "sessions")
	if dir != want {
		t.Fatalf("unexpected session dir: got %q want %q", dir, want)
	}
}

// TestConfigSessionDirKeepsRelativePaths verifies explicit project config
// paths remain relative to the launch cwd for compatibility.
func TestConfigSessionDirKeepsRelativePaths(t *testing.T) {
	dir := configSessionDir(harnessconfig.Config{
		Session: harnessconfig.SessionConfig{
			Dir: "configured-sessions",
		},
	})
	if dir != "configured-sessions" {
		t.Fatalf("unexpected relative session dir: %q", dir)
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
	t.Setenv("HOME", root)
	t.Chdir(root)

	sessionDir := filepath.Join(root, "sessions")
	var gotPath string
	var gotAuth string
	var gotAccountID string
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("Authorization")
			gotAccountID = r.Header.Get("chatgpt-account-id")
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
			AccountID:   "account_123",
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
	if gotAccountID != "account_123" {
		t.Fatalf("unexpected account header: %q", gotAccountID)
	}
	if strings.TrimSpace(stdout.String()) != "assistant: hi" {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

// TestRunExplicitAPIKeyOverridesStoredOAuth verifies an invocation-scoped API
// key can target OpenAI-compatible providers even when local OAuth exists.
func TestRunExplicitAPIKeyOverridesStoredOAuth(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("CODEX_ACCESS_TOKEN", "")

	root := t.TempDir()
	t.Setenv("HOME", root)
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
					"{\"choices\":[{\"delta\":{\"content\":\"hi"+
					"\"}}]}\n\n",
			)
			fmt.Fprint(w, "data: [DONE]\n\n")
		}),
	)
	defer server.Close()

	authPath, err := openaiauth.DefaultStorePath(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := openaiauth.Save(authPath, openaiauth.Credentials{
		CodexBaseURL: "https://chatgpt.invalid/backend-api/codex",
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
			"--base-url", server.URL,
			"--openai-api", "chat",
			"--api-key", "explicit-token",
			"--model", "test-model",
			"-p", "hello",
		},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("openai explicit key run failed: code=%d stdout=%q "+
			"stderr=%q", code, stdout.String(), stderr.String())
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("unexpected api-key path: %q", gotPath)
	}
	if gotAuth != "Bearer explicit-token" {
		t.Fatalf("unexpected auth header: %q", gotAuth)
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

// missingOpenAIAuthFile returns a nonexistent auth path for fallback tests.
func missingOpenAIAuthFile(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), "missing-openai-auth.json")
}
