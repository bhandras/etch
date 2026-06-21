package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/model"
	"harness/internal/session"
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

// TestChatPromptHistoryLoadsDurableUserMessages verifies prompt navigation can
// hydrate from the same append-only session log used for resume.
func TestChatPromptHistoryLoadsDurableUserMessages(t *testing.T) {
	store, _, err := session.Create(
		filepath.Join(
			t.TempDir(),
			"sessions",
		),
		"/tmp", "first",
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close session: %v", err)
		}
	}()

	if _, err := store.Append(
		session.EventUserMessage, store.LastID(),
		session.TextMessage(session.RoleUser, "first"),
	); err != nil {

		t.Fatalf("append first prompt: %v", err)
	}
	if _, err := store.Append(
		session.EventAssistantMessage, store.LastID(),
		session.TextMessage(session.RoleAssistant, "answer"),
	); err != nil {

		t.Fatalf("append assistant message: %v", err)
	}
	if _, err := store.Append(
		session.EventUserMessage, store.LastID(),
		session.TextMessage(session.RoleUser, "second"),
	); err != nil {

		t.Fatalf("append second prompt: %v", err)
	}

	got, err := chatPromptHistory(store.Path())
	if err != nil {
		t.Fatalf("load prompt history: %v", err)
	}
	if strings.Join(got, ",") != "first,second" {
		t.Fatalf("prompt history = %q, want first,second", got)
	}
}
