package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestLintMessageAcceptsWrappedSubsystemCommit proves that a well-formed
// subsystem subject and wrapped body pass without warnings.
func TestLintMessageAcceptsWrappedSubsystemCommit(t *testing.T) {
	msg := strings.Join([]string{
		"docs: add architecture notes",
		"",
		"Record the initial architecture choices for the minimal Go coding",
		"agent harness and the commit discipline used to keep future changes",
		"reviewable.",
		"",
	}, "\n")

	issues := lintMessage(msg, options{
		subjectWidth: defaultSubjectWidth,
		bodyWidth:    defaultBodyWidth,
	})
	if len(issues) != 0 {
		t.Fatalf("expected no issues, got %#v", issues)
	}
}

// TestLintMessageRejectsBadSubject keeps the subject grammar strict enough to
// preserve searchable subsystem history.
func TestLintMessageRejectsBadSubject(t *testing.T) {
	issues := lintMessage("bad subject\n", options{
		subjectWidth: defaultSubjectWidth,
		bodyWidth:    defaultBodyWidth,
	})
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %#v", issues)
	}
	if issues[0].msg != `subject must match "<package>: <summary>"` {
		t.Fatalf("unexpected issue: %q", issues[0].msg)
	}
}

// TestLintMessageRejectsLiteralNewlines catches the common shell quoting
// mistake where a body contains backslash-n text instead of real line breaks.
func TestLintMessageRejectsLiteralNewlines(t *testing.T) {
	issues := lintMessage(
		"docs: add notes\n\nbody with \\n literal\n", options{
			subjectWidth: defaultSubjectWidth,
			bodyWidth:    defaultBodyWidth,
		},
	)
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %#v", issues)
	}
	if issues[0].msg != `found literal "\n"; use real newlines in commit body` {
		t.Fatalf("unexpected issue: %q", issues[0].msg)
	}
}

// TestFormatMessageWrapsBody verifies that formatting turns a long body
// paragraph into the canonical 72-column shape.
func TestFormatMessageWrapsBody(t *testing.T) {
	msg := "docs: add architecture notes\n\n" +
		"Record the initial architecture choices for the minimal Go coding agent harness and the commit discipline used to keep future changes reviewable.\n"

	got := formatMessage(msg, options{
		subjectWidth: defaultSubjectWidth,
		bodyWidth:    defaultBodyWidth,
	}, false)

	want := strings.Join([]string{
		"docs: add architecture notes",
		"",
		"Record the initial architecture choices for the minimal Go coding agent",
		"harness and the commit discipline used to keep future changes",
		"reviewable.",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("formatted message mismatch\nwant:\n%q\ngot:\n%q",
			want, got)
	}
}

// TestRunLintReadsFile exercises the command runner path used by Makefile
// targets that lint a commit message file.
func TestRunLintReadsFile(t *testing.T) {
	file := t.TempDir() + "/msg"
	msg := "docs: add notes\n\nKeep the body short.\n"
	if err := osWriteFile(file, msg); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"lint", "--file", file}, strings.NewReader(""),
		&stdout, &stderr,
	)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d stdout=%q stderr=%q", code,
			stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), ": OK") {
		t.Fatalf("expected OK output, got %q", stdout.String())
	}
}

// osWriteFile keeps the file-writing setup in command-runner tests compact.
func osWriteFile(path, data string) error {
	return os.WriteFile(path, []byte(data), 0o644)
}
