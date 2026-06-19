package shell

import (
	"context"
	"strings"
	"testing"
)

// TestRunCapturesStdout verifies that successful commands return exit status
// and stdout content.
func TestRunCapturesStdout(t *testing.T) {
	got, err := Run(context.Background(), RunRequest{
		Command: "printf hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "exit code: 0") {
		t.Fatalf("missing exit code: %q", got)
	}
	if !strings.Contains(got, "stdout:\nhello") {
		t.Fatalf("missing stdout: %q", got)
	}
}

// TestRunReportsNonzeroExit verifies that command failures are returned to the
// model as tool output rather than Go errors.
func TestRunReportsNonzeroExit(t *testing.T) {
	got, err := Run(context.Background(), RunRequest{
		Command: "printf nope >&2; exit 7",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "exit code: 7") {
		t.Fatalf("missing exit code: %q", got)
	}
	if !strings.Contains(got, "stderr:\nnope") {
		t.Fatalf("missing stderr: %q", got)
	}
}

// TestRunReportsTimeout verifies that long-running commands are killed and
// represented in the tool result.
func TestRunReportsTimeout(t *testing.T) {
	got, err := Run(context.Background(), RunRequest{
		Command:        "sleep 2",
		TimeoutSeconds: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "timed out: true") {
		t.Fatalf("missing timeout marker: %q", got)
	}
}

// TestRunRejectsEmptyCommand verifies that bash does not run an empty shell.
func TestRunRejectsEmptyCommand(t *testing.T) {
	_, err := Run(context.Background(), RunRequest{})
	if err == nil {
		t.Fatal("expected empty command error")
	}
}
