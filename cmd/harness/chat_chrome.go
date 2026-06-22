package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"harness/internal/core"
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

	// timing stores cumulative provider transport counters for the chat
	// session.
	timing core.TurnTiming
}

// newChatChrome creates the prompt footer state for one chat loop.
func newChatChrome(cfg cliConfig, cwd string,
	status chatChromeStatus) *chatChrome {

	return &chatChrome{
		cfg:    cfg,
		cwd:    cwd,
		usage:  status.Usage,
		timing: status.Timing,
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

// AddTiming records provider transport counters and returns the updated footer.
func (c *chatChrome) AddTiming(timing core.TurnTiming) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.timing = addTurnTiming(c.timing, timing)

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
	if timing := formatFooterTimingStats(c.timing); timing != "" {
		parts = append(parts, timing)
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

// chatChromeStatus stores counters used to seed the live prompt footer.
type chatChromeStatus struct {
	// Usage stores cumulative provider token counters.
	Usage model.Usage

	// Timing stores cumulative provider transport counters.
	Timing core.TurnTiming
}

// chatSessionChromeStatus loads cumulative provider counters from a session.
func chatSessionChromeStatus(path string) (chatChromeStatus, error) {
	status, err := aggregateSessionStatus(path)
	if err != nil {
		return chatChromeStatus{}, fmt.Errorf("build session "+
			"status: %w", err)
	}

	return chatChromeStatus{
		Usage:  modelUsageFromSessionStatus(status),
		Timing: turnTimingFromSessionStatus(status),
	}, nil
}

// modelUsageFromSessionStatus converts durable session counters into footer
// usage counters.
func modelUsageFromSessionStatus(status session.Status) model.Usage {
	return model.Usage{
		InputTokens:           status.Usage.InputTokens,
		CachedInputTokens:     status.Usage.CachedInputTokens,
		OutputTokens:          status.Usage.OutputTokens,
		ReasoningOutputTokens: status.Usage.ReasoningOutputTokens,
		TotalTokens:           status.Usage.TotalTokens,
	}
}

// turnTimingFromSessionStatus converts durable session metrics into the subset
// of footer timing counters that can be summed across child agents.
func turnTimingFromSessionStatus(status session.Status) core.TurnTiming {
	timing := turnTimingFromMetrics(status.Metrics)
	modelCalls := timing.ModelCalls
	if modelCalls == 0 {
		modelCalls = status.ModelCalls
	}
	timing.ModelCalls = modelCalls

	return timing
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
