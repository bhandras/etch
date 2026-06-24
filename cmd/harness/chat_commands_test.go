package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/model"
	"harness/internal/session"
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

// TestRunChatContextDumpCommand verifies /context dump writes plain text
// projected context to the requested local path.
func TestRunChatContextDumpCommand(t *testing.T) {
	cfg := cliConfig{
		command:    commandChat,
		sessionDir: filepath.Join(t.TempDir(), "sessions"),
		provider:   providerEcho,
		model:      "echo-test",
	}
	path := filepath.Join(t.TempDir(), "context.txt")
	var stdout, stderr lockedBuffer
	code := runChat(
		cfg, strings.NewReader(
			"hello\n/context dump "+path+"\n/exit\n",
		),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("chat failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "context dump: "+path) {
		t.Fatalf("missing dump path: %q", stdout.String())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read context dump: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"Harness Context Dump",
		"model: echo-test",
		"===== Base Prompt =====",
		"===== Conversation Replay =====",
		"hello",
		"===== Tool Schemas =====",
		"----- Tool: read -----",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in context dump:\n%s", want, text)
		}
	}
}

// TestCompactWithFeedbackSetsComposerStatus verifies manual compaction shows
// live visual feedback while the summarization model is still running.
func TestCompactWithFeedbackSetsComposerStatus(t *testing.T) {
	store, started, err := session.Create(
		filepath.Join(
			t.TempDir(),
			"sessions",
		),
		t.TempDir(),
		"test",
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	parentID := started.ID
	for _, entry := range []struct {
		eventType string
		role      string
		text      string
	}{
		{
			session.EventUserMessage,
			session.RoleUser,
			"first",
		},
		{
			session.EventAssistantMessage,
			session.RoleAssistant,
			"one",
		},
		{
			session.EventUserMessage,
			session.RoleUser,
			"second",
		},
		{
			session.EventAssistantMessage,
			session.RoleAssistant,
			"two",
		},
	} {
		event, appendErr := store.Append(
			entry.eventType, parentID,
			session.TextMessage(entry.role, entry.text),
		)
		if appendErr != nil {
			t.Fatalf("append session event: %v", appendErr)
		}
		parentID = event.ID
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}

	client := &blockingSummaryClient{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	var stdout bytes.Buffer
	composer := &terminalChatInput{stdout: &stdout}
	done := make(chan error, 1)
	go func() {
		_, compactErr := compactWithFeedback(
			cliConfig{
				keepMessages: 1,
			}, "/compact",
			store.Path(),
			client,
			&stdout,
			nil,
			composer,
		)
		done <- compactErr
	}()

	<-client.started
	composer.mu.Lock()
	status := composer.statusText
	composer.mu.Unlock()
	if status != "Compacting" {
		t.Fatalf("unexpected compact status: %q", status)
	}
	close(client.release)
	if err := <-done; err != nil {
		t.Fatalf("compact failed: %v", err)
	}
	composer.mu.Lock()
	status = composer.statusText
	composer.mu.Unlock()
	if status != "" {
		t.Fatalf("compact status was not cleared: %q", status)
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

// blockingSummaryClient waits for release before returning a summary.
type blockingSummaryClient struct {
	// started is closed after Stream is invoked.
	started chan struct{}

	// release lets the fake stream complete.
	release chan struct{}
}

// Stream waits for release, then returns a one-chunk summary stream.
func (c *blockingSummaryClient) Stream(ctx context.Context, req model.Request) (
	<-chan model.Event, error) {

	close(c.started)
	events := make(chan model.Event, 2)
	go func() {
		defer close(events)
		select {
		case <-ctx.Done():
			events <- model.Event{
				Type: model.EventError,
				Err:  ctx.Err().Error(),
			}

		case <-c.release:
			events <- model.Event{
				Type: model.EventTextDelta,
				Text: "Goal: continue.\n\nProgress:\n- compacted",
			}
			events <- model.Event{Type: model.EventDone}
		}
	}()

	return events, nil
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
	helpAt := strings.Index(got, "Chat Commands")
	promptAt := strings.LastIndex(got, "> typed while command runs")
	if helpAt < 0 {
		t.Fatalf("help output missing: %q", got)
	}
	if strings.HasPrefix(got, "\n\n") {
		t.Fatalf("slash output had extra leading padding: %q", got)
	}
	if !strings.Contains(got, "/context dump [path]") ||
		!strings.Contains(got, "- /exit or /quit") {

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
