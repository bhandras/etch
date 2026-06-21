# Harness

Harness is a small Go coding-agent harness. It is an experiment in keeping the
agent kernel boring, fast, inspectable, and easy to build.

The project takes inspiration from the minimalism of
[Pi](https://github.com/earendil-works/pi): a direct agent loop, practical
filesystem tools, local session state, and little ceremony between the user, the
model, and the working tree. Harness follows the same spirit while leaning into
Go's strengths: static binaries, good standard-library coverage, simple
concurrency, and a low-dependency core.

This repository is work in progress. Interfaces will move, tools will sharpen,
and the CLI is intentionally still small.

## Goals

- Keep the core stdlib-first and dependency-free at runtime.
- Build a single local binary that can run without a language runtime beside it.
- Store sessions as append-only JSONL so behavior is inspectable and replayable.
- Provide a small set of reliable built-in coding tools before adding breadth.
- Let plugins live out of process as their own Go modules with their own
  dependencies.
- Treat Model Context Protocol support as an adapter layer, not as the kernel.

The deeper design record lives in [docs/architecture.md](docs/architecture.md).

## Current Shape

Harness currently has:

- a line-oriented CLI chat loop with command-specific help
- local JSONL session logs under `.harness/sessions/`
- OpenAI-compatible streaming through the standard library, including Platform
  API keys, `CODEX_ACCESS_TOKEN`, and ChatGPT/Codex OAuth login
- frame-oriented OpenAI SSE parsing that reads response chunks, joins multiline
  `data:` fields, and reports raw request/response byte metrics
- project-local TOML config from `.harness/config.toml`
- pinned project context from `SYSTEM.md` and `AGENTS.md`
- Agent Skills-style discovery from `.harness/skills/*/SKILL.md` and
  `.agents/skills/*/SKILL.md`
- manual and automatic context compaction, `/context` projection stats, and
  `/status` session stats
- provider-reported token usage for OpenAI Chat Completions and Responses API
  streams
- durable OpenAI Responses IDs stored as `model.response` events and used for
  conservative `previous_response_id` continuation
- provider transport timing for OpenAI-compatible HTTP/SSE streams, including
  request count, payload sizes, response headers, and first-event latency
- Responses API prompt cache affinity keyed by the durable local session ID
- chat steering that lets prompts typed while a turn is running influence the
  next safe model-call boundary
- session-backed prompt history for Up/Down navigation in interactive chat
- built-in tools for `ls`, `find`, `grep`, `read`, `write`, `edit`, and
  `bash`, including dry-run previews for exact replacement edits
- external process hooks for session, turn, prompt, context, tool, and
  compaction events
- explicit config-based stdio plugin tools over a small JSONL protocol
- streaming terminal feedback with an animated working line, grouped tool-call
  batches, compact tool output, and line-numbered colored live diffs for file
  edits and replacements
- provider-aware working labels: OpenAI Responses reasoning summaries can name
  the active work, while OpenAI-compatible endpoints use neutral canned labels
- a chat CLI split into small pieces for runtime setup, command handling,
  terminal input, rendering, turn orchestration, and footer/status formatting

The compiled default provider is an offline echo model, so the CLI can run
without network access. Project config can change that default. Use the
OpenAI-compatible provider explicitly when talking to a real model. Implicit
echo mode prints a warning; explicit `--provider echo` remains quiet for tests
and fixtures.

## Commands

| Command | Purpose |
| --- | --- |
| `harness -p "prompt"` | Run one non-interactive prompt. |
| `harness chat` | Start an interactive chat session. |
| `harness auth login/status/logout` | Manage local OpenAI/Codex OAuth credentials. |
| `harness tool <name>` | Run a built-in tool directly. |
| `harness sessions` | List local session logs. |
| `harness show <id-prefix>` | Render a saved transcript. |
| `harness compact --session <id>` | Append a compaction summary. |
| `harness help [command]` | Show command-specific help. |

Discover commands and flags from the binary:

```bash
go run ./cmd/harness
go run ./cmd/harness help chat
go run ./cmd/harness help tool edit
```

## Quick Start

### Offline echo mode

Run one prompt without network access:

```bash
go run ./cmd/harness -p "hello" --provider echo
```

### OpenAI-compatible API key mode

Run chat with an OpenAI-compatible endpoint:

```bash
OPENAI_API_KEY=... go run ./cmd/harness chat \
  --provider openai \
  --model gpt-4.1-mini
```

Run chat with OpenAI reasoning summaries when the selected model supports them:

```bash
OPENAI_API_KEY=... go run ./cmd/harness chat \
  --provider openai \
  --openai-api responses \
  --model gpt-5.5 \
  --reasoning-summary auto
```

### ChatGPT/Codex OAuth mode

Sign in with ChatGPT/Codex OAuth and use subscription-backed access:

```bash
go run ./cmd/harness auth login
go run ./cmd/harness chat \
  --provider openai \
  --model gpt-5.5
```

Check or remove local OAuth credentials:

```bash
go run ./cmd/harness auth status
go run ./cmd/harness auth logout
```

### OpenRouter and local endpoints

Use OpenRouter through the same OpenAI-compatible provider:

```bash
OPENROUTER_API_KEY=... go run ./cmd/harness chat \
  --provider openai \
  --base-url https://openrouter.ai/api/v1 \
  --openai-api chat \
  --model z-ai/glm-5.2 \
  --api-key "$OPENROUTER_API_KEY"
```

Use a local or custom OpenAI-compatible endpoint:

```bash
OPENAI_API_KEY=unused go run ./cmd/harness chat \
  --provider openai \
  --base-url http://localhost:11434/v1 \
  --model qwen2.5-coder
```

## Authentication

Harness supports four OpenAI-compatible credential paths:

1. An invocation-scoped API key from `--api-key`.
2. A stored ChatGPT/Codex OAuth login from `harness auth login`.
3. A bearer token from `CODEX_ACCESS_TOKEN`.
4. API keys from `OPENAI_API_KEY` or `OPENROUTER_API_KEY`.

OAuth credentials are stored locally under `.harness/auth/openai.json` by
default. An explicit `--api-key` always wins for that invocation, which is
useful for OpenRouter, local proxies, and one-off provider tests. When no
explicit API key is passed and a stored OAuth login exists and can be refreshed,
Harness uses OAuth before environment API keys. Environment API keys remain the
fallback for Platform billing, OpenRouter, local proxies, and CI.

OAuth mode defaults to the Codex backend and the Responses API shape. Explicit
`--base-url`, `--openai-api`, or `.harness/config.toml` settings override those
OAuth defaults.

## Configuration, Hooks, and Plugins

Configure project defaults:

```bash
mkdir -p .harness
cp sample-config.toml .harness/config.toml
```

`sample-config.toml` documents every supported key. The CLI reads the nearest
`.harness/config.toml` from the current directory or an ancestor. Config values
are defaults only: explicit CLI flags override them. Credential environment
variables are read separately for API keys and access tokens.

Hooks are external shell commands that inspect or mutate lifecycle events.
Harness sends a JSON envelope on stdin and expects either empty stdout or an
event-specific JSON object on stdout. Hooks run sequentially in config file
order, so later hooks see mutations returned by earlier hooks.

For example, this hook runs a local policy script before write-capable tools:

```toml
[[hooks.PreToolUse]]
matcher = "^(bash|write|edit)$"
command = ".harness/hooks/policy.sh"
timeout_seconds = 10
```

A `PreToolUse` hook can block the tool call:

```json
{"block": true, "reason": "writes to .env are blocked"}
```

It can also rewrite the raw JSON arguments before the tool runs:

```json
{"arguments": "{\"command\":\"go test ./...\",\"timeoutSeconds\":60}"}
```

Supported hook events are `SessionStart`, `UserPromptSubmit`, `TurnStart`,
`TurnComplete`, `ContextBuild`, `PreToolUse`, `PostToolUse`, `PreCompact`, and
`PostCompact`. See [sample-config.toml](sample-config.toml) for matchers,
execution order, payloads, and result shapes.

Plugins are also configured explicitly. Harness does not auto-discover project
executables. Each enabled plugin is a child process that speaks JSONL over
stdin/stdout and can register model-callable tools:

```toml
[[plugins]]
name = "example"
command = "go run ./plugins/example/main.go"
timeout_seconds = 30
disabled = false
```

The first plugin protocol supports `initialize` and `tool.execute` requests.
Plugin tools appear in the same tool list as built-ins, and their results are
stored as ordinary `message.tool` session events.

The repository includes a small example plugin at `plugins/example`. It uses
the thin `harness/sdk` package from [sdk/plugins.go](sdk/plugins.go), exposes
`plugin_echo` for smoke testing, and exposes `project_files` for a small
filesystem summary:

```bash
go run ./cmd/harness tool plugin_echo --args '{"text":"hello"}'
go run ./cmd/harness tool project_files --args '{"path":".","limit":200}'
```

## Sessions and Chat Commands

Inspect local sessions:

```bash
go run ./cmd/harness sessions
go run ./cmd/harness show <session-id-prefix>
```

Inside chat, use slash commands for local session and context operations:

```text
/status    Show session age, turns, model calls, tool calls, and token usage.
/context   Show projected context size, pinned layers, and auto compact config.
/compact   Append a model-written summary for older session history.
/show      Render the active session transcript.
/sessions  List known local sessions.
/tools     List built-in tool names.
/new       Start a fresh session in the same chat process.
/help      Show available chat commands.
/exit      Leave chat.
```

`/context` estimates the next model request. It reports pinned instruction
layers, active summaries, raw replay size, and approximate token counts.

`/status` reports what has already happened in the session: age, turns, model
calls, tool calls, tool batches, compactions, message bytes, approximate timing
from JSONL event gaps, and provider-reported token usage. When providers report
usage, Harness appends `model.usage` events to the JSONL log and sums them for
`/status`, including input, cached input, output, reasoning output, and total
tokens when available.

Interactive chat uses the active session log for prompt history. Up and Down
cycle through prior user prompts from the current session, including prompts
loaded through `--session`, while the draft being edited is restored when moving
past the newest history entry.

## Built-In Tools

Run a built-in tool directly:

```bash
go run ./cmd/harness tool ls .
go run ./cmd/harness tool find readme .
go run ./cmd/harness tool find --glob '**/*_test.go' '' .
go run ./cmd/harness tool grep Harness README.md
go run ./cmd/harness tool grep --regex --context 2 'func Test[A-Za-z0-9_]+' .
go run ./cmd/harness tool read README.md
go run ./cmd/harness tool bash -- pwd
```

Preview an exact replacement edit without modifying the file:

```bash
go run ./cmd/harness tool edit \
  --old "old text" \
  --new "new text" \
  --dry-run \
  README.md
```

`write` and `edit` return human-readable diffs. In chat mode, Harness renders
mutation previews live so the user can see exactly what changed.

## Project Context

Harness builds prompt context in layers:

```text
base system prompt
SYSTEM.md files, parent directory before child directory
AGENTS.md files, parent directory before child directory
compact skill catalog
latest compacted session summary, when present
recent raw session messages
```

Use `SYSTEM.md` for project-specific agent identity and durable behavior. Use
`AGENTS.md` for repository workflow, coding, documentation, verification, and
commit-message rules. Both files are pinned ahead of compacted conversation
history and capped at 32KB per file.

Manual compaction is available through `/compact` and `harness compact`.
Automatic compaction can be enabled in `.harness/config.toml`:

```toml
[context]
auto_compact = true
auto_compact_threshold_tokens = 120000
keep_recent_tokens = 20000
```

Harness checks the projected context before chat model calls. When the estimate
reaches the threshold, it appends a `context.summary` event with
`trigger = "auto"` and keeps roughly `context.keep_recent_tokens` of recent raw
context. The text-summary backend follows Pi's checkpoint style: repeated
compactions update the previous summary, preserve exact paths and errors, and
record read/modified file lists as summary metadata. The original JSONL history
remains on disk.

Inside chat, `/compact <instructions>` passes optional focus text to the
summarizer:

```text
/compact focus on files changed and test failures
```

The non-interactive compact command accepts the same focus as a flag:

```bash
go run ./cmd/harness compact --session <id-prefix> \
  --instructions "focus on modified files and failing tests"
```

Skills follow the Agent Skills `SKILL.md` convention. Harness discovers skill
metadata from `.harness/skills/*/SKILL.md` and `.agents/skills/*/SKILL.md` in
the current directory and its ancestors. The default prompt includes only the
skill catalog: name, description, and path. Full skill bodies and reference
files remain outside the prompt until a later on-demand loading path reads them.

## Development

Build the binary:

```bash
make build
```

Run tests:

```bash
make test
```

Format Go code with the project-pinned formatter:

```bash
make fmt
make fmt-check
```

## Status

Harness is not a finished agent product. It is a small harness for learning the
shape of a good coding-agent core: explicit loops, durable logs, narrow tools,
and a plugin boundary that keeps the binary small.

The near-term direction is to keep hardening the local kernel: better provider
coverage, sharper tool safety, richer plugin/process boundaries, and context
management that stays inspectable rather than magical.
