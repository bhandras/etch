package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/model"
	"harness/internal/tool"
)

const (
	// helperEnv enables the subprocess test helper plugin.
	helperEnv = "HARNESS_PLUGIN_HELPER"

	// helperModeEnv selects a misbehaving helper mode for lifecycle tests.
	helperModeEnv = "HARNESS_PLUGIN_HELPER_MODE"
)

// testTool is a minimal in-process tool used to seed registry conflicts.
type testTool struct {
	name string
}

// Spec returns the configured model-facing name for conflict tests.
func (t testTool) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        t.name,
		Description: "Test tool.",
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}
}

// Execute returns a fixed output because conflict tests never dispatch it.
func (t testTool) Execute(ctx context.Context, arguments string) (tool.Result,
	error) {

	return tool.Result{Text: "ok"}, nil
}

// TestStartConfiguredRegistersAndExecutesTool verifies that a configured plugin
// contributes a model-callable tool to the shared registry.
func TestStartConfiguredRegistersAndExecutesTool(t *testing.T) {
	registry := tool.NewRegistry()
	clients, err := StartConfigured(
		context.Background(),
		[]config.PluginConfig{{
			Name:           "helper",
			Command:        helperCommand(),
			TimeoutSeconds: 5,
		}},
		t.TempDir(), registry,
	)
	if err != nil {
		t.Fatalf("start configured plugin: %v", err)
	}
	defer closeClients(clients)

	specs := registry.Specs()
	if len(specs) != 1 {
		t.Fatalf("expected one plugin tool, got %d", len(specs))
	}
	if specs[0].Name != "plugin_echo" {
		t.Fatalf("unexpected plugin tool name: %q", specs[0].Name)
	}

	result, err := registry.Execute(context.Background(), model.ToolCall{
		ID:        "call_7",
		Name:      "plugin_echo",
		Arguments: `{"text":"hello"}`,
	})
	if err != nil {
		t.Fatalf("execute plugin tool: %v", err)
	}
	if result.Text != "call_7:hello" {
		t.Fatalf("unexpected plugin result: %q", result.Text)
	}
}

// TestStartConfiguredSkipsDisabled verifies disabled plugin entries remain
// inert and do not start processes or register tools.
func TestStartConfiguredSkipsDisabled(t *testing.T) {
	registry := tool.NewRegistry()
	clients, err := StartConfigured(
		context.Background(),
		[]config.PluginConfig{{
			Name:     "disabled",
			Command:  "exit 1",
			Disabled: true,
		}},
		t.TempDir(), registry,
	)
	if err != nil {
		t.Fatalf("start configured plugins: %v", err)
	}
	if len(clients) != 0 {
		t.Fatalf("expected no clients, got %d", len(clients))
	}
	if len(registry.Specs()) != 0 {
		t.Fatalf("expected no registered tools, got %#v",
			registry.Specs())
	}
}

// TestStartConfiguredRejectsDuplicateToolName verifies plugins cannot shadow
// tools that already exist in the target registry.
func TestStartConfiguredRejectsDuplicateToolName(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(testTool{name: "plugin_echo"})

	clients, err := StartConfigured(
		context.Background(),
		[]config.PluginConfig{{
			Name:           "helper",
			Command:        helperCommand(),
			TimeoutSeconds: 5,
		}},
		t.TempDir(), registry,
	)
	defer closeClients(clients)

	if err == nil {
		t.Fatal("expected duplicate tool name error")
	}
	if !strings.Contains(err.Error(), "conflicts with an existing tool") {
		t.Fatalf("unexpected duplicate error: %v", err)
	}
}

// TestCloseReleasesBlockedCall verifies Close unblocks a call waiting for a
// plugin response that never arrives.
func TestCloseReleasesBlockedCall(t *testing.T) {
	client, err := Start(context.Background(), config.PluginConfig{
		Name:           "helper",
		Command:        helperCommandWithMode("hang"),
		TimeoutSeconds: 10,
	}, t.TempDir())
	if err != nil {
		t.Fatalf("start plugin: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := client.execute(context.Background(), model.ToolCall{
			ID:        "call_hang",
			Name:      "plugin_echo",
			Arguments: `{"text":"hello"}`,
		})
		done <- err
	}()

	time.Sleep(100 * time.Millisecond)
	if err := client.Close(); err != nil {
		t.Fatalf("close plugin: %v", err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected blocked call to fail after close")
		}

	case <-time.After(2 * time.Second):
		t.Fatal("blocked plugin call did not finish after close")
	}
}

// TestCloseReportsCrashedPlugin verifies Close surfaces a plugin that exited
// before the harness intentionally killed it.
func TestCloseReportsCrashedPlugin(t *testing.T) {
	client, err := Start(context.Background(), config.PluginConfig{
		Name:           "helper",
		Command:        helperCommandWithMode("exit-after-init"),
		TimeoutSeconds: 5,
	}, t.TempDir())
	if err != nil {
		t.Fatalf("start plugin: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	err = client.Close()
	if err == nil {
		t.Fatal("expected crashed plugin close error")
	}
	if !strings.Contains(err.Error(), "wait plugin helper") {
		t.Fatalf("unexpected close error: %v", err)
	}
}

// TestValidateToolParametersRequiresObjectSchema verifies plugin schemas must
// be objects with the provider-compatible type marker.
func TestValidateToolParametersRequiresObjectSchema(t *testing.T) {
	tests := []struct {
		// name describes the schema shape being tested.
		name string

		// parameters is the raw JSON schema returned by the plugin.
		parameters json.RawMessage

		// wantErr reports whether validation should fail.
		wantErr bool
	}{
		{
			name:       "valid object",
			parameters: json.RawMessage(`{"type":"object"}`),
		},
		{
			name:       "array schema",
			parameters: json.RawMessage(`[]`),
			wantErr:    true,
		},
		{
			name:       "missing type",
			parameters: json.RawMessage(`{"properties":{}}`),
			wantErr:    true,
		},
		{
			name:       "wrong type",
			parameters: json.RawMessage(`{"type":"string"}`),
			wantErr:    true,
		},
		{
			name:       "invalid json",
			parameters: json.RawMessage(`{`),
			wantErr:    true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateToolParameters("helper", toolSpec{
				Name:       "plugin_echo",
				Parameters: test.parameters,
			})
			if test.wantErr && err == nil {
				t.Fatal("expected validation error")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

// TestPluginHelperProcess runs as a subprocess plugin for protocol tests.
func TestPluginHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	runHelperPlugin()
	os.Exit(0)
}

// helperCommand returns a shell command that runs this test binary as a plugin.
func helperCommand() string {
	return helperCommandWithMode("")
}

// helperCommandWithMode returns a helper command with an optional behavior.
func helperCommandWithMode(mode string) string {
	modePrefix := ""
	if mode != "" {
		modePrefix = helperModeEnv + "=" + strconv.Quote(mode) + " "
	}

	return helperEnv + "=1 " + modePrefix + strconv.Quote(os.Args[0]) +
		" -test.run=TestPluginHelperProcess --"
}

// runHelperPlugin serves the minimal JSONL protocol used by tests.
func runHelperPlugin() {
	mode := os.Getenv(helperModeEnv)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			fmt.Fprintf(os.Stderr, "decode request: %v\n", err)

			return
		}
		switch req.Method {
		case methodInitialize:
			writeHelperResponse(req.ID, initializeResult{
				Name: "helper",
				Tools: []toolSpec{{
					Name:        "plugin_echo",
					Description: "Echoes text through a plugin.",
					Parameters: json.RawMessage(`{
						"type":"object",
						"properties":{"text":{"type":"string"}}
					}`),
				}},
			})
			if mode == "exit-after-init" {
				fmt.Fprintln(
					os.Stderr, "helper crashed after init",
				)
				os.Exit(7)
			}

		case methodToolExecute:
			if mode == "hang" {
				time.Sleep(time.Hour)
			}
			var params toolExecuteParams
			if err := decodeHelperParams(
				req.Params, &params,
			); err != nil {

				writeHelperError(req.ID, err.Error())
				continue
			}
			var args struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(
				params.Arguments, &args,
			); err != nil {

				writeHelperError(req.ID, err.Error())
				continue
			}
			writeHelperResponse(req.ID, toolExecuteResult{
				Content: []contentPart{{
					Type: contentTypeText,
					Text: params.CallID + ":" + args.Text,
				}},
			})

		default:
			writeHelperError(req.ID, "unknown method "+req.Method)
		}
	}
}

// decodeHelperParams remarshal-decodes the generic request params.
func decodeHelperParams(params any, out any) error {
	encoded, err := json.Marshal(params)
	if err != nil {
		return err
	}

	return json.Unmarshal(encoded, out)
}

// writeHelperResponse writes one successful protocol response.
func writeHelperResponse(id string, result any) {
	encoded, err := json.Marshal(result)
	if err != nil {
		writeHelperError(id, err.Error())

		return
	}
	writeHelperLine(response{ID: id, Result: encoded})
}

// writeHelperError writes one failed protocol response.
func writeHelperError(id string, message string) {
	writeHelperLine(response{
		ID: id,
		Error: &responseError{
			Message: message,
		},
	})
}

// writeHelperLine writes one JSONL response to plugin stdout.
func writeHelperLine(value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "encode response: %v\n", err)

		return
	}
	fmt.Fprintln(os.Stdout, string(encoded))
}
