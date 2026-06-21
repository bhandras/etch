package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"harness/internal/textutil"
)

const (
	// DefaultTimeoutSeconds caps command runtime when callers do not
	// provide a timeout.
	DefaultTimeoutSeconds = 30

	// MaxTimeoutSeconds caps caller-provided command runtimes.
	MaxTimeoutSeconds = 120

	// DefaultOutputMaxBytes caps each stdout or stderr stream in tool
	// output.
	DefaultOutputMaxBytes = 64 * 1024
)

// RunRequest describes one bounded shell command execution.
type RunRequest struct {
	// Command is the bash command line to execute.
	Command string `json:"command"`

	// TimeoutSeconds is the optional command timeout in seconds.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
}

// Run executes one bash command and returns a model-friendly result summary.
func Run(ctx context.Context, req RunRequest) (string, error) {
	command := strings.TrimSpace(req.Command)
	if command == "" {
		return "", fmt.Errorf("command is required")
	}

	timeout := normalizedTimeout(req.TimeoutSeconds)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	duration := time.Since(start)
	exitCode := 0
	timedOut := ctx.Err() == context.DeadlineExceeded
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else if timedOut {
			exitCode = -1
		} else {
			return "", fmt.Errorf("run command: %w", err)
		}
	}

	return renderResult(
		exitCode, timedOut, duration, stdout.String(), stderr.String(),
	), nil
}

// normalizedTimeout clamps caller-provided timeout values.
func normalizedTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = DefaultTimeoutSeconds
	}
	if seconds > MaxTimeoutSeconds {
		seconds = MaxTimeoutSeconds
	}

	return time.Duration(seconds) * time.Second
}

// renderResult formats exit status, duration, and captured output.
func renderResult(exitCode int, timedOut bool, duration time.Duration,
	stdout string, stderr string) string {

	var out strings.Builder
	fmt.Fprintf(&out, "exit code: %d\n", exitCode)
	fmt.Fprintf(&out, "duration: %s\n", duration.Round(time.Millisecond))
	if timedOut {
		out.WriteString("timed out: true\n")
	}
	writeSection(&out, "stdout", stdout)
	writeSection(&out, "stderr", stderr)

	return strings.TrimRight(out.String(), "\n")
}

// writeSection appends one capped process output stream.
func writeSection(out *strings.Builder, name string, content string) {
	if content == "" {
		return
	}

	capped, truncated := capText(content, DefaultOutputMaxBytes)
	fmt.Fprintf(out, "\n%s:\n%s", name, capped)
	if !strings.HasSuffix(capped, "\n") {
		out.WriteByte('\n')
	}
	if truncated {
		fmt.Fprintf(
			out, "[%s truncated to %s]\n", name,
			textutil.FormatBytes(DefaultOutputMaxBytes),
		)
	}
}

// capText truncates text to a maximum byte count.
func capText(text string, maxBytes int) (string, bool) {
	return textutil.TruncateUTF8Bytes(text, maxBytes)
}
