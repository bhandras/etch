package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/model"
)

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
