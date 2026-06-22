package main

import (
	"time"

	"harness/internal/render"
	"harness/internal/session"
	"harness/internal/tool"
)

// aggregateSessionStatus builds parent status plus completed child-agent
// session counters referenced by task tool results.
func aggregateSessionStatus(path string) (session.Status, error) {
	return aggregateSessionStatusPath(path, make(map[string]bool), true)
}

// aggregateSessionStatusPath reads one session and recursively folds child
// sessions into its counters.
func aggregateSessionStatusPath(path string, seen map[string]bool,
	required bool) (session.Status, error) {

	if seen[path] {
		return session.Status{}, nil
	}
	seen[path] = true
	events, err := session.ReadAll(path)
	if err != nil {
		if required {
			return session.Status{}, err
		}

		return session.Status{}, nil
	}
	status, err := session.BuildStatus(events, time.Now())
	if err != nil {
		return session.Status{}, err
	}
	for _, childPath := range subagentSessionPaths(events) {
		child, err := aggregateSessionStatusPath(childPath, seen, false)
		if err != nil {
			return session.Status{}, err
		}
		status = addChildSessionStatus(status, child)
	}

	return status, nil
}

// subagentSessionPaths extracts child JSONL paths from durable task results.
func subagentSessionPaths(events []session.Event) []string {
	var paths []string
	seen := make(map[string]bool)
	for _, event := range events {
		if event.Type != session.EventToolMessage {
			continue
		}
		message, err := decodeMessage(event)
		if err != nil || message.Name != tool.NameTask {
			continue
		}
		display, ok := parseSubagentResult(render.MessageText(message))
		if !ok || display.SessionPath == "" ||
			seen[display.SessionPath] {

			continue
		}
		seen[display.SessionPath] = true
		paths = append(paths, display.SessionPath)
	}

	return paths
}

// addChildSessionStatus folds child-agent work into parent-visible counters.
func addChildSessionStatus(parent session.Status,
	child session.Status) session.Status {

	parent.EventCount += child.EventCount
	parent.ModelCalls += child.ModelCalls
	parent.ToolCalls += child.ToolCalls
	parent.ToolResults += child.ToolResults
	parent.ToolBatches += child.ToolBatches
	if child.LargestToolBatch > parent.LargestToolBatch {
		parent.LargestToolBatch = child.LargestToolBatch
	}
	parent.Compactions += child.Compactions
	parent.AutoCompactions += child.AutoCompactions
	parent.ManualCompactions += child.ManualCompactions
	parent.MessageBytes += child.MessageBytes
	parent.SummaryBytes += child.SummaryBytes
	parent.Usage = parent.Usage.Add(child.Usage)
	parent.Metrics = parent.Metrics.Add(child.Metrics)
	parent.ModelWait += child.ModelWait
	parent.ToolWait += child.ToolWait

	return parent
}
