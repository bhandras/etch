package main

import (
	"fmt"
	"strings"
	"time"

	"harness/internal/core"
	"harness/internal/model"
	"harness/internal/textutil"
)

// liveTurnStats stores compact per-turn counters for the footer.
type liveTurnStats struct {
	// ToolCalls is the number of local tool calls executed in the turn.
	ToolCalls int

	// Usage stores provider-reported token counters for the turn.
	Usage model.Usage

	// Timing stores coarse model/tool timing for the turn.
	Timing core.TurnTiming
}

// formatElapsed returns a compact human duration for the turn footer.
func formatElapsed(elapsed time.Duration) string {
	seconds := int(elapsed.Round(time.Second).Seconds())
	if seconds < 1 {
		return "<1s"
	}
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	seconds = seconds % 60

	return fmt.Sprintf("%dm %ds", minutes, seconds)
}

// formatTurnStats returns optional compact counters for the turn footer.
func formatTurnStats(stats liveTurnStats) string {
	var parts []string
	if stats.ToolCalls == 1 {
		parts = append(parts, "1 tool")
	} else if stats.ToolCalls > 1 {
		parts = append(parts, fmt.Sprintf("%d tools", stats.ToolCalls))
	}
	parts = append(parts, usageStatParts(stats.Usage)...)
	if stats.Timing.ModelDuration > 0 {
		parts = append(
			parts,
			"model "+formatElapsed(stats.Timing.ModelDuration),
		)
	}
	parts = append(parts, timingStatParts(stats.Timing)...)
	if len(parts) == 0 {
		return ""
	}

	return " · " + strings.Join(parts, " · ")
}

// timingStatParts returns compact provider transport measurements.
func timingStatParts(timing core.TurnTiming) []string {
	var parts []string
	if timing.ModelCalls > 0 {
		if timing.ModelCalls == 1 {
			parts = append(parts, "1 request")
		} else {
			parts = append(
				parts, fmt.Sprintf("%d requests",
					timing.ModelCalls),
			)
		}
	}
	if timing.RequestBytes > 0 || timing.ResponseBytes > 0 {
		parts = append(
			parts,
			fmt.Sprintf(
				"%s up · %s down",
				textutil.FormatBytes(timing.RequestBytes),
				textutil.FormatBytes(timing.ResponseBytes),
			),
		)
	}
	if timing.TimeToHeaders > 0 {
		parts = append(
			parts, "headers "+formatElapsed(timing.TimeToHeaders),
		)
	}
	if timing.TimeToFirstEvent > 0 {
		parts = append(
			parts,
			"first event "+formatElapsed(timing.TimeToFirstEvent),
		)
	}

	return parts
}

// formatUsageStats returns compact provider token counters without a leading
// separator.
func formatUsageStats(usage model.Usage) string {
	return strings.Join(usageStatParts(usage), " · ")
}

// usageStatParts returns compact token counter phrases for terminal chrome.
func usageStatParts(usage model.Usage) []string {
	if usage.Empty() {
		return nil
	}
	parts := []string{
		fmt.Sprintf("%d in", usage.InputTokens),
	}
	if usage.CachedInputTokens > 0 {
		parts = append(
			parts, fmt.Sprintf("%d cached",
				usage.CachedInputTokens),
		)
	}
	parts = append(parts, fmt.Sprintf("%d out", usage.OutputTokens))

	return parts
}
