package main

import (
	"fmt"
	"strings"
	"time"

	"harness/internal/core"
	"harness/internal/model"
	"harness/internal/session"
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
		parts = append(
			parts,
			fmt.Sprintf(
				"%s tools",
				textutil.FormatCount(stats.ToolCalls),
			),
		)
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
				parts,
				fmt.Sprintf(
					"%s requests",
					textutil.FormatCount(timing.ModelCalls),
				),
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

	return parts
}

// formatFooterTimingStats returns compact transport counters for prompt chrome.
func formatFooterTimingStats(timing core.TurnTiming) string {
	parts := footerTimingStatParts(timing)
	if len(parts) == 0 {
		return ""
	}

	return strings.Join(parts, " · ")
}

// footerTimingStatParts returns the transport counters useful in a live footer.
func footerTimingStatParts(timing core.TurnTiming) []string {
	var parts []string
	if timing.ModelCalls > 0 {
		if timing.ModelCalls == 1 {
			parts = append(parts, "1 req")
		} else {
			parts = append(
				parts,
				fmt.Sprintf(
					"%s req",
					textutil.FormatCount(timing.ModelCalls),
				),
			)
		}
	}
	if timing.RequestBytes > 0 {
		parts = append(
			parts, textutil.FormatBytes(timing.RequestBytes)+" up",
		)
	}
	if timing.ResponseBytes > 0 {
		parts = append(
			parts,
			textutil.FormatBytes(timing.ResponseBytes)+" down",
		)
	}

	return parts
}

// formatUsageStats returns compact provider token counters without a leading
// separator.
func formatUsageStats(usage model.Usage) string {
	return strings.Join(usageStatParts(usage), " · ")
}

// turnTimingFromMetrics converts durable metrics into live footer counters.
func turnTimingFromMetrics(metrics session.MetricsData) core.TurnTiming {
	modelCalls := metrics.Requests
	if modelCalls == 0 && !metrics.Empty() {
		modelCalls = 1
	}

	return core.TurnTiming{
		ModelCalls:    modelCalls,
		RequestBytes:  metrics.RequestBytes,
		ResponseBytes: metrics.ResponseBytes,
		TimeToHeaders: time.Duration(metrics.TimeToHeadersMillis) *
			time.Millisecond,
		TimeToFirstEvent: time.Duration(metrics.TimeToFirstEventMillis) *
			time.Millisecond,
	}
}

// turnTimingFromModelMetrics converts live model metrics into footer counters.
func turnTimingFromModelMetrics(metrics model.Metrics) core.TurnTiming {
	modelCalls := metrics.Requests
	if modelCalls == 0 && !metrics.Empty() {
		modelCalls = 1
	}

	return core.TurnTiming{
		ModelCalls:       modelCalls,
		RequestBytes:     metrics.RequestBytes,
		ResponseBytes:    metrics.ResponseBytes,
		TimeToHeaders:    metrics.TimeToHeaders,
		TimeToFirstEvent: metrics.TimeToFirstEvent,
	}
}

// usageStatParts returns compact token counter phrases for terminal chrome.
func usageStatParts(usage model.Usage) []string {
	if usage.Empty() {
		return nil
	}
	parts := []string{
		fmt.Sprintf("%s in", textutil.FormatCount(usage.InputTokens)),
	}
	if usage.CachedInputTokens > 0 {
		parts = append(
			parts,
			fmt.Sprintf(
				"%s cached",
				textutil.FormatCount(usage.CachedInputTokens),
			),
		)
	}
	parts = append(
		parts,
		fmt.Sprintf(
			"%s out", textutil.FormatCount(usage.OutputTokens),
		),
	)

	return parts
}
