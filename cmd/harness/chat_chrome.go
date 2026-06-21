package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"harness/internal/model"
	"harness/internal/render"
	"harness/internal/session"
)

// chatChrome owns the prompt footer text shared across chat turns.
type chatChrome struct {
	// mu serializes usage updates from model events with prompt redraws.
	mu sync.Mutex

	// cfg stores the provider and reasoning settings shown in the footer.
	cfg cliConfig

	// cwd stores the working directory shown in compact form.
	cwd string

	// usage stores cumulative provider counters for the chat session.
	usage model.Usage
}

// newChatChrome creates the prompt footer state for one chat loop.
func newChatChrome(cfg cliConfig, cwd string, usage model.Usage) *chatChrome {
	return &chatChrome{
		cfg:   cfg,
		cwd:   cwd,
		usage: usage,
	}
}

// Footer returns the current prompt footer text.
func (c *chatChrome) Footer() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.footerLocked()
}

// AddUsage records one provider usage event and returns the updated footer.
func (c *chatChrome) AddUsage(usage model.Usage) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.usage = c.usage.Add(usage)

	return c.footerLocked()
}

// footerLocked formats the current prompt footer while c.mu is held.
func (c *chatChrome) footerLocked() string {
	parts := []string{
		chatFooterMode(c.cfg),
		displayCWD(c.cwd),
	}
	if usage := formatUsageStats(c.usage); usage != "" {
		parts = append(parts, usage)
	}

	return strings.Join(parts, " · ")
}

// chatFooterMode formats the selected model and reasoning effort.
func chatFooterMode(cfg cliConfig) string {
	label := cfg.model
	if label == "" {
		label = cfg.provider
	}
	if label == "" {
		label = providerEcho
	}
	if cfg.reasoningEffort != "" && cfg.reasoningEffort != "none" {
		label += " " + cfg.reasoningEffort
	}

	return label
}

// displayCWD returns cwd with the user's home directory collapsed to "~".
func displayCWD(cwd string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return cwd
	}
	rel, err := filepath.Rel(home, cwd)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		if rel == "." {
			return "~"
		}

		return cwd
	}

	return filepath.Join("~", rel)
}

// chatSessionUsage loads cumulative provider usage from an existing session.
func chatSessionUsage(path string) (model.Usage, error) {
	events, err := session.ReadAll(path)
	if err != nil {
		return model.Usage{}, fmt.Errorf("read session usage: %w", err)
	}
	status, err := session.BuildStatus(events, time.Now())
	if err != nil {
		return model.Usage{}, fmt.Errorf("build session usage: %w", err)
	}

	return model.Usage{
		InputTokens:           status.Usage.InputTokens,
		CachedInputTokens:     status.Usage.CachedInputTokens,
		OutputTokens:          status.Usage.OutputTokens,
		ReasoningOutputTokens: status.Usage.ReasoningOutputTokens,
		TotalTokens:           status.Usage.TotalTokens,
	}, nil
}

// chatPromptHistory loads durable user prompts for interactive history.
func chatPromptHistory(path string) ([]string, error) {
	events, err := session.ReadAll(path)
	if err != nil {
		return nil, fmt.Errorf("read prompt history: %w", err)
	}
	prompts := []string{}
	for _, event := range events {
		if event.Type != session.EventUserMessage {
			continue
		}
		message, err := decodeMessage(event)
		if err != nil {
			return nil, err
		}
		if message.Role != session.RoleUser {
			continue
		}
		text := render.MessageText(message)
		if strings.TrimSpace(text) == "" {
			continue
		}
		prompts = append(prompts, text)
	}

	return prompts, nil
}
