package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"harness/internal/model"
	"harness/internal/plugins"
	"harness/internal/tool"
)

// configuredToolRegistry returns builtins plus configured plugin tools.
func configuredToolRegistry(ctx context.Context, cfg cliConfig, cwd string) (
	*tool.Registry, func(), error) {

	registry := tool.DefaultRegistry()
	if len(activeSubagentProfiles(cfg.subagents)) > 0 {
		if err := registry.RegisterStrict(
			newTaskTool(cfg, cwd, registry),
		); err != nil {
			return nil, nil, err
		}
	}
	clients, err := plugins.StartConfigured(ctx, cfg.plugins, cwd, registry)
	if err != nil {
		return nil, nil, err
	}
	closePlugins := func() {
		for _, client := range clients {
			_ = client.Close()
		}
	}

	return registry, closePlugins, nil
}

// runTool executes one registered tool directly for local smoke testing.
func runTool(cfg cliConfig, stdout io.Writer, stderr io.Writer) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "error: get working directory:", err)

		return 1
	}
	registry, closePlugins, err := configuredToolRegistry(
		context.Background(), cfg, cwd,
	)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}
	defer closePlugins()

	arguments, err := toolArguments(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 2
	}

	result, err := registry.Execute(context.Background(), model.ToolCall{
		ID:        "manual",
		Name:      cfg.toolName,
		Arguments: arguments,
	})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)

		return 1
	}

	if cfg.jsonOutput {
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			fmt.Fprintln(stderr, "error: encode json output:", err)

			return 1
		}

		return 0
	}

	fmt.Fprintln(stdout, result.Text)

	return 0
}

// toolArguments converts direct CLI tool flags into raw JSON arguments.
func toolArguments(cfg cliConfig) (string, error) {
	switch cfg.toolName {
	case tool.NameLS:
		args := struct {
			Path  string `json:"path,omitempty"`
			Limit int    `json:"limit,omitempty"`
		}{
			Path:  cfg.toolPath,
			Limit: cfg.toolLimit,
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("marshal ls arguments: %w", err)
		}

		return string(encoded), nil

	case tool.NameRead:
		args := struct {
			Path   string `json:"path"`
			Offset int    `json:"offset,omitempty"`
			Limit  int    `json:"limit,omitempty"`
		}{
			Path:   cfg.toolPath,
			Offset: cfg.toolOffset,
			Limit:  cfg.toolLimit,
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("marshal read arguments: %w", err)
		}

		return string(encoded), nil

	case tool.NameFind:
		args := struct {
			Path  string `json:"path,omitempty"`
			Query string `json:"query,omitempty"`
			Glob  string `json:"glob,omitempty"`
			Limit int    `json:"limit,omitempty"`
		}{
			Path:  cfg.toolPath,
			Query: cfg.toolQuery,
			Glob:  cfg.toolGlob,
			Limit: cfg.toolLimit,
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("marshal find arguments: %w", err)
		}

		return string(encoded), nil

	case tool.NameGrep:
		args := struct {
			Path       string `json:"path,omitempty"`
			Pattern    string `json:"pattern"`
			Regex      bool   `json:"regex,omitempty"`
			Glob       string `json:"glob,omitempty"`
			Context    int    `json:"context,omitempty"`
			Limit      int    `json:"limit,omitempty"`
			IgnoreCase bool   `json:"ignoreCase,omitempty"`
		}{
			Path:       cfg.toolPath,
			Pattern:    cfg.toolQuery,
			Regex:      cfg.toolRegex,
			Glob:       cfg.toolGlob,
			Context:    cfg.toolContext,
			Limit:      cfg.toolLimit,
			IgnoreCase: cfg.toolIgnoreCase,
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("marshal grep arguments: %w", err)
		}

		return string(encoded), nil

	case tool.NameWrite:
		args := struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}{
			Path:    cfg.toolPath,
			Content: cfg.toolContent,
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("marshal write arguments: %w",
				err)
		}

		return string(encoded), nil

	case tool.NameEdit:
		args := struct {
			Path  string `json:"path"`
			Edits []struct {
				OldText string `json:"oldText"`
				NewText string `json:"newText"`
			} `json:"edits"`
			DryRun bool `json:"dryRun,omitempty"`
		}{
			Path: cfg.toolPath,
			Edits: []struct {
				OldText string `json:"oldText"`
				NewText string `json:"newText"`
			}{{
				OldText: cfg.toolOldText,
				NewText: cfg.toolNewText,
			}},
			DryRun: cfg.toolDryRun,
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("marshal edit arguments: %w", err)
		}

		return string(encoded), nil

	case tool.NameBash:
		args := struct {
			Command        string `json:"command"`
			TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
		}{
			Command:        cfg.toolCommand,
			TimeoutSeconds: cfg.toolTimeout,
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("marshal bash arguments: %w", err)
		}

		return string(encoded), nil

	default:
		raw := strings.TrimSpace(cfg.toolRawArguments)
		if raw == "" {
			raw = "{}"
		}
		if err := validateRawJSONObject(raw); err != nil {
			return "", fmt.Errorf("plugin tool %s arguments: %w",
				cfg.toolName, err)
		}

		return raw, nil
	}
}

// validateRawJSONObject ensures direct plugin tool arguments are an object.
func validateRawJSONObject(raw string) error {
	var value map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &value); err != nil ||
		value == nil {
		return fmt.Errorf("must be a JSON object")
	}

	return nil
}
