package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestAuthStatusDoesNotPrintCodexAccessToken verifies auth diagnostics reveal
// credential source but not bearer token material.
func TestAuthStatusDoesNotPrintCodexAccessToken(t *testing.T) {
	t.Setenv("CODEX_ACCESS_TOKEN", "secret-codex-token")

	var stdout, stderr bytes.Buffer
	code := run([]string{"auth", "status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("auth status failed: code=%d stdout=%q stderr=%q",
			code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "CODEX_ACCESS_TOKEN") {
		t.Fatalf("missing env token source: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "secret-codex-token") {
		t.Fatalf("auth status leaked token: %q", stdout.String())
	}
}
