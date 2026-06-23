package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
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
	if !registry.ParallelSafe(model.ToolCall{Name: "plugin_echo"}) {
		t.Fatal("declared read-only plugin tool was not parallel-safe")
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

// TestStartConfiguredRejectsInvalidToolName verifies plugin tool names are
// checked before providers receive the shared tool schema.
func TestStartConfiguredRejectsInvalidToolName(t *testing.T) {
	registry := tool.NewRegistry()
	clients, err := StartConfigured(
		context.Background(),
		[]config.PluginConfig{{
			Name:           "helper",
			Command:        helperCommandWithMode("invalid-name"),
			TimeoutSeconds: 5,
		}},
		t.TempDir(), registry,
	)
	defer closeClients(clients)

	if err == nil || !strings.Contains(err.Error(), "invalid tool name") {
		t.Fatalf("expected invalid tool name error, got %v", err)
	}
}

// TestPluginEnvironmentIsSanitized verifies sensitive parent environment
// variables are not inherited by plugin processes by default.
func TestPluginEnvironmentIsSanitized(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "secret")
	t.Setenv("HARNESS_TEST_FORWARD", "forwarded")
	client, err := Start(context.Background(), config.PluginConfig{
		Name:           "helper",
		Command:        helperCommandWithMode("env"),
		TimeoutSeconds: 5,
	}, t.TempDir())
	if err != nil {
		t.Fatalf("start plugin: %v", err)
	}
	defer client.Close()

	result, err := client.execute(context.Background(), model.ToolCall{
		ID:        "call_env",
		Name:      "plugin_echo",
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("execute env plugin: %v", err)
	}
	if strings.Contains(result.Text, "OPENAI_API_KEY=secret") ||
		strings.Contains(result.Text, "HARNESS_TEST_FORWARD=forwarded") {

		t.Fatalf("plugin inherited unsanitized env:\n%s", result.Text)
	}
}

// TestPluginEnvironmentForwardsAllowedVariables verifies explicit env
// allowlists can pass non-secret variables through the sanitized environment.
func TestPluginEnvironmentForwardsAllowedVariables(t *testing.T) {
	t.Setenv("HARNESS_TEST_FORWARD", "forwarded")
	client, err := Start(context.Background(), config.PluginConfig{
		Name:           "helper",
		Command:        helperCommandWithMode("env"),
		Env:            []string{"HARNESS_TEST_FORWARD"},
		TimeoutSeconds: 5,
	}, t.TempDir())
	if err != nil {
		t.Fatalf("start plugin: %v", err)
	}
	defer client.Close()

	result, err := client.execute(context.Background(), model.ToolCall{
		ID:        "call_env",
		Name:      "plugin_echo",
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("execute env plugin: %v", err)
	}
	if !strings.Contains(result.Text, "HARNESS_TEST_FORWARD=forwarded") {
		t.Fatalf("plugin did not receive allowed env:\n%s", result.Text)
	}
	if strings.Contains(result.Text, "OPENAI_API_KEY=secret") {
		t.Fatalf("plugin received unrelated secret env:\n%s",
			result.Text)
	}
}

// TestPluginEnvironmentForwardsLowercaseUnixVariables verifies explicit env
// allowlists preserve case-sensitive Unix variable names.
func TestPluginEnvironmentForwardsLowercaseUnixVariables(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows environment names are case-insensitive")
	}
	t.Setenv("harness_test_lower", "lower")
	client, err := Start(context.Background(), config.PluginConfig{
		Name:           "helper",
		Command:        helperCommandWithMode("env"),
		Env:            []string{"harness_test_lower"},
		TimeoutSeconds: 5,
	}, t.TempDir())
	if err != nil {
		t.Fatalf("start plugin: %v", err)
	}
	defer client.Close()

	result, err := client.execute(context.Background(), model.ToolCall{
		ID:        "call_env",
		Name:      "plugin_echo",
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("execute env plugin: %v", err)
	}
	if !strings.Contains(result.Text, "harness_test_lower=lower") {
		t.Fatalf("plugin did not receive lowercase env:\n%s",
			result.Text)
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

// TestTimeoutHidesPluginTools verifies a timed-out plugin call is removed from
// future model tool schemas instead of remaining advertised as healthy.
func TestTimeoutHidesPluginTools(t *testing.T) {
	registry := tool.NewRegistry()
	clients, err := StartConfigured(
		context.Background(),
		[]config.PluginConfig{{
			Name:           "helper",
			Command:        helperCommandWithMode("hang"),
			TimeoutSeconds: 1,
		}},
		t.TempDir(), registry,
	)
	if err != nil {
		t.Fatalf("start configured plugin: %v", err)
	}
	defer closeClients(clients)

	_, err = registry.Execute(context.Background(), model.ToolCall{
		ID:        "call_hang",
		Name:      "plugin_echo",
		Arguments: `{"text":"hello"}`,
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if len(registry.Specs()) != 0 {
		t.Fatalf("timed-out plugin stayed advertised: %#v",
			registry.Specs())
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

// TestCloseLetsCooperativePluginExit verifies Close gives stdin-driven plugins
// a chance to exit normally before kill fallback.
func TestCloseLetsCooperativePluginExit(t *testing.T) {
	client, err := Start(context.Background(), config.PluginConfig{
		Name:           "helper",
		Command:        helperCommand(),
		TimeoutSeconds: 5,
	}, t.TempDir())
	if err != nil {
		t.Fatalf("start plugin: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close cooperative plugin: %v", err)
	}
}

// TestReadResponseRejectsOversizedLine verifies malformed plugin stdout cannot
// grow memory without a response frame delimiter.
func TestReadResponseRejectsOversizedLine(t *testing.T) {
	client, err := Start(context.Background(), config.PluginConfig{
		Name:           "helper",
		Command:        helperCommandWithMode("oversized-line"),
		TimeoutSeconds: 5,
	}, t.TempDir())
	if err != nil {
		t.Fatalf("start plugin: %v", err)
	}
	defer client.Close()

	_, err = client.execute(context.Background(), model.ToolCall{
		ID:        "call_large",
		Name:      "plugin_echo",
		Arguments: `{}`,
	})
	if err == nil ||
		!strings.Contains(err.Error(), "response line exceeds") {

		t.Fatalf("expected oversized line error, got %v", err)
	}
	if client.Available() {
		t.Fatal("fatal oversized response left plugin advertised")
	}
}

// TestMalformedResponseHidesPluginTools verifies protocol desync failures hide
// plugin tools from later model requests.
func TestMalformedResponseHidesPluginTools(t *testing.T) {
	registry := tool.NewRegistry()
	clients, err := StartConfigured(
		context.Background(),
		[]config.PluginConfig{{
			Name: "helper",
			Command: helperCommandWithMode(
				"malformed-response",
			),
			TimeoutSeconds: 5,
		}},
		t.TempDir(), registry,
	)
	if err != nil {
		t.Fatalf("start configured plugin: %v", err)
	}
	defer closeClients(clients)

	_, err = registry.Execute(context.Background(), model.ToolCall{
		ID:        "call_bad",
		Name:      "plugin_echo",
		Arguments: `{}`,
	})
	if err == nil || !strings.Contains(err.Error(), "decode plugin") {
		t.Fatalf("expected malformed response error, got %v", err)
	}
	if len(registry.Specs()) != 0 {
		t.Fatalf("malformed plugin stayed advertised: %#v",
			registry.Specs())
	}
}

// TestUnknownContentTypeHidesPluginTools verifies unsupported content parts are
// surfaced as fatal plugin protocol errors.
func TestUnknownContentTypeHidesPluginTools(t *testing.T) {
	registry := tool.NewRegistry()
	clients, err := StartConfigured(
		context.Background(),
		[]config.PluginConfig{{
			Name:           "helper",
			Command:        helperCommandWithMode("unknown-content"),
			TimeoutSeconds: 5,
		}},
		t.TempDir(), registry,
	)
	if err != nil {
		t.Fatalf("start configured plugin: %v", err)
	}
	defer closeClients(clients)

	_, err = registry.Execute(context.Background(), model.ToolCall{
		ID:        "call_content",
		Name:      "plugin_echo",
		Arguments: `{}`,
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported content") {
		t.Fatalf("expected unsupported content error, got %v", err)
	}
	if len(registry.Specs()) != 0 {
		t.Fatalf("bad-content plugin stayed advertised: %#v",
			registry.Specs())
	}
}

// TestExecuteRejectsOversizedTextResult verifies valid plugin responses still
// cannot feed unbounded tool output into sessions and model context.
func TestExecuteRejectsOversizedTextResult(t *testing.T) {
	client, err := Start(context.Background(), config.PluginConfig{
		Name:           "helper",
		Command:        helperCommandWithMode("oversized-result"),
		TimeoutSeconds: 5,
	}, t.TempDir())
	if err != nil {
		t.Fatalf("start plugin: %v", err)
	}
	defer client.Close()

	_, err = client.execute(context.Background(), model.ToolCall{
		ID:        "call_large",
		Name:      "plugin_echo",
		Arguments: `{}`,
	})
	if err == nil || !strings.Contains(err.Error(), "text result exceeds") {
		t.Fatalf("expected oversized result error, got %v", err)
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
			toolName := "plugin_echo"
			if mode == "invalid-name" {
				toolName = "bad name"
			}
			writeHelperResponse(req.ID, initializeResult{
				Name: "helper",
				Tools: []toolSpec{{
					Name:           toolName,
					Description:    "Echoes text through a plugin.",
					ParallelSafety: tool.ParallelSafetyReadOnly,
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
			if mode == "oversized-line" {
				fmt.Fprint(
					os.Stdout, strings.Repeat(
						"x", responseLineLimitBytes+1,
					),
				)
				os.Exit(0)
			}
			if mode == "malformed-response" {
				fmt.Fprintln(os.Stdout, "{not-json")
				os.Exit(0)
			}
			var params toolExecuteParams
			if err := decodeHelperParams(
				req.Params, &params,
			); err != nil {

				writeHelperError(req.ID, err.Error())
				continue
			}
			if mode == "env" {
				writeHelperResponse(req.ID, toolExecuteResult{
					Content: []contentPart{{
						Type: contentTypeText,
						Text: strings.Join([]string{
							"OPENAI_API_KEY=" + os.Getenv(
								"OPENAI_API_KEY",
							),
							"HARNESS_TEST_FORWARD=" + os.Getenv(
								"HARNESS_TEST_FORWARD",
							),
							"harness_test_lower=" + os.Getenv(
								"harness_test_lower",
							),
						}, "\n"),
					}},
				})

				continue
			}
			if mode == "oversized-result" {
				writeHelperResponse(req.ID, toolExecuteResult{
					Content: []contentPart{{
						Type: contentTypeText,
						Text: strings.Repeat(
							"x",
							resultTextLimitBytes+1,
						),
					}},
				})

				continue
			}
			if mode == "unknown-content" {
				writeHelperResponse(req.ID, toolExecuteResult{
					Content: []contentPart{{
						Type: "image",
						Text: "ignored",
					}},
				})

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
