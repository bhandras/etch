package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestRenderChatCancelNoticeRedrawsPrompt verifies cancellation notices move
// the live composer aside instead of being printed inside the prompt island.
func TestRenderChatCancelNoticeRedrawsPrompt(t *testing.T) {
	t.Setenv("COLUMNS", "32")
	var stdout bytes.Buffer
	composer := &terminalChatInput{
		stdout: &stdout,
		input:  []rune("next input"),
	}
	if err := composer.renderLocked(); err != nil {
		t.Fatalf("initial composer render failed: %v", err)
	}
	stdout.Reset()

	renderChatCancelNotice(composer, &stdout)

	got := stdout.String()
	cancelAt := strings.Index(got, "• Canceled")
	promptAt := strings.LastIndex(got, "> next input")
	if cancelAt < 0 {
		t.Fatalf("cancel notice missing: %q", got)
	}
	if !strings.Contains(got, "\n• Canceled\n\n") {
		t.Fatalf("cancel notice missing padding: %q", got)
	}
	if promptAt < 0 {
		t.Fatalf("composer was not redrawn: %q", got)
	}
	if promptAt < cancelAt {
		t.Fatalf("composer redrew before cancel notice: %q", got)
	}
}

// TestChatCancelNoticeStylesMuted verifies the terminal tone used by cancel
// notices matches the muted dot-led block style.
func TestChatCancelNoticeStylesMuted(t *testing.T) {
	style := terminalStyle{enabled: true}

	got := style.wrapTone("• Canceled", terminalTone{muted: true})
	if !strings.Contains(got, "• Canceled") {
		t.Fatalf("cancel notice missing text: %q", got)
	}
	if !strings.Contains(got, ansiDim) {
		t.Fatalf("cancel notice was not muted: %q", got)
	}
}
