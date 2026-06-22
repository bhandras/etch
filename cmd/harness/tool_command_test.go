package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/session"
)

// TestToolLSRunsDirectly verifies the manual builtin tool smoke path.
func TestToolLSRunsDirectly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "")

	var stdout, stderr bytes.Buffer
	code := run([]string{"tool", "ls", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("tool failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "go.mod" {
		t.Fatalf("unexpected tool output: %q", stdout.String())
	}
}

// TestToolReadRunsDirectly verifies the manual text file read smoke path.
func TestToolReadRunsDirectly(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "note.txt"), "alpha\nbeta")

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"tool", "read", "--limit", "1", "note.txt"}, &stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("tool failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "alpha") {
		t.Fatalf("unexpected tool output: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Use offset=2 to continue") {
		t.Fatalf("missing continuation hint: %q", stdout.String())
	}
}

// TestToolFindRunsDirectly verifies the manual recursive path search smoke
// path.
func TestToolFindRunsDirectly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "")

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"tool", "find", "readme", dir}, &stdout, &stderr,
	)
	if code != 0 {
		t.Fatalf("tool failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "README.md" {
		t.Fatalf("unexpected tool output: %q", stdout.String())
	}
}

// TestToolFindAcceptsGlobFlag verifies the direct find command exposes the
// same glob filter as the model-facing tool.
func TestToolFindAcceptsGlobFlag(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "cmd", "main.go"), "")
	writeFile(t, filepath.Join(dir, "cmd", "main_test.go"), "")

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{
			"tool", "find", "--glob", "**/*_test.go", "", dir,
		},
		&stdout, &stderr,
	)
	if code != 0 {
		t.Fatalf("tool failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "cmd/main_test.go" {
		t.Fatalf("unexpected tool output: %q", stdout.String())
	}
}

// TestToolGrepRunsDirectly verifies the manual literal search smoke path.
func TestToolGrepRunsDirectly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "note.txt"), "alpha\nNeedle\n")

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"tool", "grep", "--ignore-case", "needle", dir},
		&stdout, &stderr,
	)
	if code != 0 {
		t.Fatalf("tool failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "note.txt:2:Needle" {
		t.Fatalf("unexpected tool output: %q", stdout.String())
	}
}

// TestToolGrepAcceptsRegexAndContextFlags verifies direct grep exposes regex
// and surrounding context controls.
func TestToolGrepAcceptsRegexAndContextFlags(t *testing.T) {
	dir := t.TempDir()
	writeFile(
		t, filepath.Join(dir, "note.txt"),
		"before\nNeedle42\nafter\n",
	)

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{
			"tool", "grep", "--regex", "--context", "1",
			`Needle\d+`, dir,
		},
		&stdout, &stderr,
	)
	if code != 0 {
		t.Fatalf("tool failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	if !strings.Contains(got, "note.txt-1:before") ||
		!strings.Contains(got, "note.txt:2:Needle42") ||
		!strings.Contains(got, "note.txt-3:after") {

		t.Fatalf("unexpected tool output: %q", stdout.String())
	}
}

// TestToolWriteRunsDirectly verifies the manual whole-file write smoke path.
func TestToolWriteRunsDirectly(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{
			"tool", "write", "--content", "hello\n", "note.txt",
		},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("tool failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Successfully wrote 6 bytes") {
		t.Fatalf("unexpected tool output: %q", stdout.String())
	}
	content, err := os.ReadFile(filepath.Join(dir, "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello\n" {
		t.Fatalf("unexpected file content: %q", string(content))
	}
}

// TestToolEditRunsDirectly verifies the manual exact replacement edit smoke
// path.
func TestToolEditRunsDirectly(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "note.txt"), "hello\n")

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{
			"tool", "edit", "--old", "hello", "--new", "goodbye",
			"note.txt",
		},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("tool failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Successfully applied 1 edit") {
		t.Fatalf("unexpected tool output: %q", stdout.String())
	}
	content, err := os.ReadFile(filepath.Join(dir, "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "goodbye\n" {
		t.Fatalf("unexpected file content: %q", string(content))
	}
}

// TestToolEditDryRunRunsDirectly verifies the manual exact replacement preview
// path without mutating the target file.
func TestToolEditDryRunRunsDirectly(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, filepath.Join(dir, "note.txt"), "hello\n")

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{
			"tool", "edit", "--old", "hello", "--new", "goodbye",
			"--dry-run", "note.txt",
		},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("tool failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Previewed 1 edit") {
		t.Fatalf("unexpected tool output: %q", stdout.String())
	}
	content, err := os.ReadFile(filepath.Join(dir, "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello\n" {
		t.Fatalf("unexpected file content: %q", string(content))
	}
}

// TestToolBashRunsDirectly verifies the manual bounded command smoke path.
func TestToolBashRunsDirectly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"tool", "bash", "--", "printf", "hello"}, &stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("tool failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "exit code: 0") {
		t.Fatalf("missing exit code: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "stdout:\nhello") {
		t.Fatalf("missing stdout: %q", stdout.String())
	}
}

// TestToolPluginRunsDirectly verifies the direct tool smoke path can execute a
// configured plugin tool with raw JSON arguments.
func TestToolPluginRunsDirectly(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.Mkdir(filepath.Join(root, ".harness"), 0o755); err != nil {
		t.Fatalf("make config dir: %v", err)
	}
	writeFile(
		t, filepath.Join(root, ".harness", "config.toml"),
		fmt.Sprintf(
			"[[plugins]]\nname = \"helper\"\ncommand = %q\n",
			cliPluginHelperCommand(),
		),
	)

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{
			"tool",
			"plugin_echo",
			"--args",
			`{"text":"hello"}`,
		},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("plugin tool failed: code=%d stdout=%q stderr=%q",
			code, stdout.String(), stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "plugin direct" {
		t.Fatalf("unexpected plugin output: %q", stdout.String())
	}
}

// TestToolTaskRunsConfiguredSubagentDirectly verifies the task tool can launch
// a configured child session through the direct tool smoke path.
func TestToolTaskRunsConfiguredSubagentDirectly(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.Mkdir(filepath.Join(root, ".harness"), 0o755); err != nil {
		t.Fatalf("make config dir: %v", err)
	}
	writeFile(
		t, filepath.Join(root, ".harness", "config.toml"),
		`
[provider]
name = "echo"

[subagents]
enabled = true
max_per_turn = 2

[[subagents.profile]]
name = "explore"
description = "Echo-backed test profile."
system_prompt = "Return the delegated task."
allowed_tools = ["ls"]
max_tool_rounds = 3
`,
	)

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{
			"tool",
			"task",
			"--args",
			`{"profile":"explore","task":"say hello"}`,
		},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("task tool failed: code=%d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"Task explore completed.",
		"Result:",
		"say hello",
		"Inspect: harness show ",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("task output missing %q:\n%s", want, got)
		}
	}
	sessionID := taskOutputSessionID(t, got)
	events, err := session.ReadAll(
		filepath.Join(
			root, ".harness", "sessions", sessionID+".jsonl",
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	var started session.StartedData
	if err := json.Unmarshal(events[0].Data, &started); err != nil {
		t.Fatal(err)
	}
	if started.SubagentProfile != "explore" ||
		started.ParentToolCallID != "manual" {

		t.Fatalf("unexpected child session metadata: %#v", started)
	}
}

// taskOutputSessionID extracts the child session id from task output.
func taskOutputSessionID(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		value, ok := strings.CutPrefix(line, "Session: ")
		if ok {
			return strings.TrimSpace(value)
		}
	}
	t.Fatalf("task output missing session id:\n%s", output)

	return ""
}
