package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunChatListsTools verifies that slash commands run without starting a
// model turn.
func TestRunChatListsTools(t *testing.T) {
	cfg := cliConfig{
		command:    commandChat,
		sessionDir: filepath.Join(t.TempDir(), "sessions"),
		provider:   providerEcho,
	}
	var stdout, stderr lockedBuffer
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

// TestRunChatShowsToolSchema verifies slash commands can inspect the exact
// model-facing schema for a registered tool.
func TestRunChatShowsToolSchema(t *testing.T) {
	cfg := cliConfig{
		command:    commandChat,
		sessionDir: filepath.Join(t.TempDir(), "sessions"),
		provider:   providerEcho,
	}
	var stdout, stderr lockedBuffer
	code := runChat(
		cfg, strings.NewReader("/tool grep\n/exit\n"), &stdout, &stderr,
	)
	if code != 0 {
		t.Fatalf("chat failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"Tool: grep",
		"Description:",
		"Parameters:",
		"```json",
		`"regex"`,
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("missing %q in tool schema output: %q", want,
				stdout.String())
		}
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
	var stdout, stderr lockedBuffer
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
	var stdout, stderr lockedBuffer
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
	var stdout, stderr lockedBuffer
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
	if strings.HasPrefix(got, "\n\n") {
		t.Fatalf("slash output had extra leading padding: %q", got)
	}
	if !strings.Contains(got, "/tools /tool /help\n\n") {
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
