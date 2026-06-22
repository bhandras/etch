package main

import (
	"testing"

	harnessconfig "harness/internal/config"
	"harness/internal/model"
	"harness/internal/tool"
)

// TestTaskToolParallelSafeReflectsProfileAllowlist verifies child profiles
// that may mutate the workspace are serial barriers for parent tool grouping.
func TestTaskToolParallelSafeReflectsProfileAllowlist(t *testing.T) {
	task := newTaskTool(cliConfig{
		subagents: harnessconfig.SubagentConfig{
			Enabled: true,
			Profiles: []harnessconfig.SubagentProfileConfig{
				{
					Name:         "reader",
					Description:  "Read only.",
					AllowedTools: []string{"ls", "read", "grep"},
				},
				{
					Name:         "writer",
					Description:  "Can write.",
					AllowedTools: []string{"read", "write"},
				},
			},
		},
	}, ".", tool.DefaultRegistry())

	if !task.ParallelSafe(model.ToolCall{
		Name:      tool.NameTask,
		Arguments: `{"profile":"reader","task":"inspect"}`,
	}) {

		t.Fatal("read-only profile was not parallel-safe")
	}
	if task.ParallelSafe(model.ToolCall{
		Name:      tool.NameTask,
		Arguments: `{"profile":"writer","task":"edit"}`,
	}) {

		t.Fatal("write-capable profile was parallel-safe")
	}
	if task.ParallelSafe(model.ToolCall{
		Name:      tool.NameTask,
		Arguments: `{"profile":"missing","task":"inspect"}`,
	}) {

		t.Fatal("unknown profile was parallel-safe")
	}
}
