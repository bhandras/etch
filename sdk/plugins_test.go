package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestServePluginIOInitializesAndExecutesTool verifies the SDK serves the two
// core protocol methods over JSONL streams.
func TestServePluginIOInitializesAndExecutesTool(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	input := strings.Join([]string{
		`{"id":"1","method":"initialize","params":{"protocolVersion":"0.1.0"}}`,
		`{"id":"2","method":"tool.execute","params":{"callID":"call_1","name":"echo","arguments":{"text":"hello"}}}`,
		"",
	}, "\n")

	err := ServePluginIO(
		context.Background(), testPlugin(), strings.NewReader(input),
		&stdout, &stderr,
	)
	if err != nil {
		t.Fatalf("serve plugin: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}

	responses := decodeResponses(t, stdout.String())
	if len(responses) != 2 {
		t.Fatalf("expected two responses, got %d", len(responses))
	}
	if responses[0].ID != "1" || responses[0].Error != nil {
		t.Fatalf("unexpected initialize response: %#v", responses[0])
	}
	var initialized InitializeResult
	mustRemarshal(t, responses[0].Result, &initialized)
	if initialized.Name != "test" || len(initialized.Tools) != 1 {
		t.Fatalf("unexpected initialize result: %#v", initialized)
	}

	if responses[1].ID != "2" || responses[1].Error != nil {
		t.Fatalf("unexpected execute response: %#v", responses[1])
	}
	var result ToolResult
	mustRemarshal(t, responses[1].Result, &result)
	if len(result.Content) != 1 ||
		result.Content[0].Text != "call_1:hello" {

		t.Fatalf("unexpected tool result: %#v", result)
	}
}

// TestServePluginIOReportsUnknownTool verifies dispatch failures become
// protocol error responses instead of process-level errors.
func TestServePluginIOReportsUnknownTool(t *testing.T) {
	var stdout bytes.Buffer
	input := `{"id":"1","method":"tool.execute","params":{"callID":"call_1","name":"missing","arguments":{}}}` +
		"\n"

	err := ServePluginIO(
		context.Background(), testPlugin(), strings.NewReader(input),
		&stdout, &bytes.Buffer{},
	)
	if err != nil {
		t.Fatalf("serve plugin: %v", err)
	}

	responses := decodeResponses(t, stdout.String())
	if len(responses) != 1 {
		t.Fatalf("expected one response, got %d", len(responses))
	}
	if responses[0].Error == nil ||
		!strings.Contains(responses[0].Error.Message, "unknown tool") {

		t.Fatalf("expected unknown tool error, got %#v", responses[0])
	}
}

// TestServePluginIORejectsDuplicateTools verifies SDK validation catches
// ambiguous tool registration before serving requests.
func TestServePluginIORejectsDuplicateTools(t *testing.T) {
	plugin := Plugin{
		Name: "test",
		Tools: []Tool{
			testTool("echo"),
			testTool("echo"),
		},
	}
	err := ServePluginIO(
		context.Background(), plugin, strings.NewReader(""),
		&bytes.Buffer{}, &bytes.Buffer{},
	)
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("expected duplicate tool error, got %v", err)
	}
}

// testPlugin returns a tiny plugin fixture for SDK protocol tests.
func testPlugin() Plugin {
	return Plugin{
		Name: "test",
		Tools: []Tool{
			testTool("echo"),
		},
	}
}

// testTool returns a tool that echoes its call id and text argument.
func testTool(name string) Tool {
	return Tool{
		Name:        name,
		Description: "Echoes text.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{
					"type": "string",
				},
			},
		},
		Handler: func(ctx context.Context, call ToolCall) (ToolResult,
			error) {

			var args struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(
				call.Arguments, &args,
			); err != nil {
				return ToolResult{}, err
			}

			return TextResult(call.CallID + ":" + args.Text), nil
		},
	}
}

// decodeResponses decodes JSONL response text for assertions.
func decodeResponses(t *testing.T, text string) []response {
	t.Helper()
	var responses []response
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		var resp response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("decode response %q: %v", line, err)
		}
		responses = append(responses, resp)
	}

	return responses
}

// mustRemarshal converts generic decoded result values into a typed target.
func mustRemarshal(t *testing.T, value any, target any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		t.Fatal(err)
	}
}
