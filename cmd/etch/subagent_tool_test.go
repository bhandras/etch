package main

import (
	"testing"

	harnessconfig "etch/internal/config"
	"etch/internal/model"
	"etch/internal/tool"
)

// TestTaskToolParallelSafeReflectsProfileExistence verifies configured
// subagent calls may run concurrently regardless of child tool capabilities.
func TestTaskToolParallelSafeReflectsProfileExistence(t *testing.T) {
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
	if !task.ParallelSafe(model.ToolCall{
		Name:      tool.NameTask,
		Arguments: `{"profile":"writer","task":"edit"}`,
	}) {

		t.Fatal("write-capable profile was not parallel-safe")
	}
	if task.ParallelSafe(model.ToolCall{
		Name:      tool.NameTask,
		Arguments: `{"profile":"missing","task":"inspect"}`,
	}) {

		t.Fatal("unknown profile was parallel-safe")
	}
}

// TestTaskToolChildRegistryHonorsNestedTaskAllowlist verifies nested
// delegation is available only when the profile explicitly allows task.
func TestTaskToolChildRegistryHonorsNestedTaskAllowlist(t *testing.T) {
	parent := tool.DefaultRegistry()
	task := newTaskTool(cliConfig{}, ".", parent)
	parent.Register(task)

	withTask, err := task.childToolRegistry(
		harnessconfig.SubagentProfileConfig{
			Name:         "plain",
			AllowedTools: []string{"read", "task"},
		},
	)
	if err != nil {
		t.Fatalf("build child registry: %v", err)
	}
	if !withTask.Has(tool.NameTask) {
		t.Fatalf("task was not available when explicitly allowed")
	}

	plain, err := task.childToolRegistry(
		harnessconfig.SubagentProfileConfig{
			Name:         "plain",
			AllowedTools: []string{"read"},
		},
	)
	if err != nil {
		t.Fatalf("build plain registry: %v", err)
	}
	if plain.Has(tool.NameTask) {
		t.Fatalf("task was available without explicit allowlist entry")
	}
}
