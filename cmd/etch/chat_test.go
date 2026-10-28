package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"etch/internal/model"
	"etch/internal/provider/openai"
	"etch/internal/session"
)

// toolLimitProvider records requests and requests tools until the test limit.
type toolLimitProvider struct {
	// mu protects requests against HTTP handler and test access.
	mu sync.Mutex

	// requests retains message roles and content sent on each model call.
	requests [][]model.Message
}

// ServeHTTP returns two tool calls followed by a final follow-up answer.
func (p *toolLimitProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}
	var messages []model.Message
	if err := json.Unmarshal(body["messages"], &messages); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}
	p.mu.Lock()
	p.requests = append(p.requests, messages)
	count := len(p.requests)
	p.mu.Unlock()

	payload := `{"choices":[{"delta":{"content":"Recovered in same session."}}]}`
	if count <= 2 {
		payload = fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{`+
			`"index":0,"id":"call_%d","type":"function",`+
			`"function":{"name":"ls",`+
			`"arguments":"{\"path\":\".\"}"}}]}}]}`,
			count)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
}

// TestRunChatContinuesAfterToolRoundLimit verifies new and resumed sessions
// survive exhaustion and pass completed tool results to a follow-up turn.
func TestRunChatContinuesAfterToolRoundLimit(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		t.Run(fmt.Sprintf("resumed=%t", resumed), func(t *testing.T) {
			testChatRoundLimitRecovery(t, resumed)
		})
	}
}

// testChatRoundLimitRecovery checks the prompt, history, and session identity
// after a bounded tool loop stops and the user submits another prompt.
func testChatRoundLimitRecovery(t *testing.T, resumed bool) {
	t.Helper()
	t.Chdir(t.TempDir())
	provider := &toolLimitProvider{}
	server := httptest.NewServer(provider)
	defer server.Close()
	cfg := cliConfig{
		command:        commandChat,
		sessionDir:     filepath.Join(t.TempDir(), "sessions"),
		provider:       openai.ProviderName,
		model:          "limit-test",
		baseURL:        server.URL,
		apiKey:         "unused",
		apiKeyExplicit: true,
		maxToolRounds:  2,
	}
	if resumed {
		store, _, err := session.Create(cfg.sessionDir, ".", "existing")
		if err != nil {
			t.Fatal(err)
		}
		cfg.sessionID = store.ID()
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr lockedBuffer
	code := runChat(
		cfg, strings.NewReader(
			"loop\n/status\ncontinue\n/exit\n",
		),
		&stdout,
		&stderr,
	)
	if code != 0 || stderr.String() != "" {
		t.Fatalf("chat failed: code=%d stderr=%q stdout=%q", code,
			stderr.String(), stdout.String())
	}
	for _, want := range []string{
		"Paused after 2 model/tool rounds",
		"Recovered in same session.",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("missing %q in output: %q", want,
				stdout.String())
		}
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.requests) != 3 {
		t.Fatalf("model requests = %d, want 3", len(provider.requests))
	}
	followup := provider.requests[2]
	var roles []string
	for _, message := range followup {
		roles = append(roles, message.Role)
	}
	if got := strings.Join(roles, ","); got !=
		"system,user,assistant,tool,assistant,tool,user" {

		t.Fatalf("follow-up lost tool history: %s", got)
	}
	if followup[len(followup)-1].Content != "continue" {
		t.Fatalf("follow-up prompt mismatch: %#v", followup)
	}
	if followup[3].Content == "" || followup[5].Content == "" {
		t.Fatalf("follow-up lost completed tool results: %#v", followup)
	}
	entries, err := session.List(cfg.sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("created another session: %#v", entries)
	}
	if resumed && entries[0].ID != cfg.sessionID {
		t.Fatalf("resumed session changed: %#v", entries[0])
	}
	if !strings.Contains(stdout.String(), "session: "+entries[0].ID) {
		t.Fatalf("missing resume hint: %q", stdout.String())
	}
}
