# etch

etch is a small Go coding-agent harness. It is an experiment in keeping the
agent kernel boring, fast, inspectable, and easy to build.

The project takes inspiration from the minimalism of
[Pi](https://github.com/earendil-works/pi): a direct agent loop, practical
filesystem tools, local session state, and little ceremony between the user, the
model, and the working tree. etch follows the same spirit while leaning into
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

etch currently has:

- a line-oriented CLI chat loop with command-specific help
- local JSONL session logs under `.etch/sessions/`
- OpenAI-compatible streaming through the standard library, including Platform
  API keys, `CODEX_ACCESS_TOKEN`, and ChatGPT/Codex OAuth login
- frame-oriented OpenAI SSE parsing that reads response chunks, joins multiline
  `data:` fields, and reports raw request/response byte metrics plus
  continuation request shape
- user and project TOML config from `~/.etch/config.toml` and
  `.etch/config.toml`
- pinned user and project instruction context from `SYSTEM.md` and `AGENTS.md`
- Agent Skills-style discovery from `.etch/skills/*/SKILL.md` and
  `.agents/skills/*/SKILL.md`
- manual and automatic context compaction, `/context` projection stats, and
  `/status` session stats
- provider-reported token usage for OpenAI Chat Completions and Responses API
  streams
- durable OpenAI Responses IDs stored as `model.response` events for inspection
  and future stored-response transports
- opaque OpenAI Responses reasoning ciphertext stored as `model.provider_item`
  events and replayed only to compatible Responses requests
- durable provider transport metrics for OpenAI-compatible HTTP/SSE and
  Responses WebSocket streams,
  including request count, continuation attempts and fallbacks, payload sizes,
  response headers, first-event latency, input-message count, delta-message
  count, and tool-schema count
- Responses API prompt cache affinity keyed by the durable local session ID;
  the default plain-HTTP Responses path keeps `store:false` and resends the
  current context instead of using `previous_response_id`
- optional stdlib-only Responses WebSocket transport with cached session
  connections for delta continuation requests
- chat steering that lets prompts typed while a turn is running influence the
  next safe model-call boundary
- session-backed prompt history for Up/Down navigation in interactive chat
- live prompt footer counters for tokens, provider requests, and up/down
  transport bytes
- built-in tools for `ls`, `find`, `grep`, `read`, `write`, `edit`, and
  `bash`, including dry-run previews for exact replacement edits
- external process hooks for session, turn, prompt, context, tool, and
  compaction events
- explicit config-based stdio plugin tools over a small JSONL protocol
- config-defined subagent profiles exposed through the `task` delegation tool,
  with child runs stored as separate JSONL sessions
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
| `etch -p "prompt"` | Run one non-interactive prompt. |
| `etch chat` | Start an interactive chat session. |
| `etch resume <id-prefix>` | Continue an existing chat session. |
| `etch auth login/status/logout` | Manage local OpenAI/Codex OAuth credentials. |
| `etch tool <name>` | Run a built-in tool directly. |
| `etch sessions` | List local session logs. |
| `etch show <id-prefix>` | Render a saved transcript. |
| `etch compact --session <id>` | Append a compaction summary. |
| `etch help [command]` | Show command-specific help. |

Discover commands and flags from the binary:

```bash
go run ./cmd/etch
go run ./cmd/etch help chat
go run ./cmd/etch help tool edit
```

## Quick Start

### Offline echo mode

Run one prompt without network access:

```bash
go run ./cmd/etch -p "hello" --provider echo
```

### OpenAI-compatible API key mode

Run chat with an OpenAI-compatible endpoint:

```bash
OPENAI_API_KEY=... go run ./cmd/etch chat \
  --provider openai \
  --model gpt-4.1-mini
```

Run chat with OpenAI reasoning summaries when the selected model supports them:

```bash
OPENAI_API_KEY=... go run ./cmd/etch chat \
  --provider openai \
  --openai-api responses \
  --model gpt-5.5 \
  --reasoning-summary auto
```

### ChatGPT/Codex OAuth mode

Sign in with ChatGPT/Codex OAuth and use subscription-backed access:

```bash
go run ./cmd/etch auth login
go run ./cmd/etch chat \
  --provider openai \
  --model gpt-5.5
```

Check or remove local OAuth credentials:

```bash
go run ./cmd/etch auth status
go run ./cmd/etch auth logout
```

### OpenRouter and local endpoints

Use OpenRouter through the same OpenAI-compatible provider:

```bash
OPENROUTER_API_KEY=... go run ./cmd/etch chat \
  --provider openai \
  --base-url https://openrouter.ai/api/v1 \
  --openai-api chat \
  --model z-ai/glm-5.2 \
  --api-key "$OPENROUTER_API_KEY"
```

Use a local or custom OpenAI-compatible endpoint:

```bash
OPENAI_API_KEY=unused go run ./cmd/etch chat \
  --provider openai \
  --base-url http://localhost:11434/v1 \
  --model qwen2.5-coder
```

## Authentication

etch supports four OpenAI-compatible credential paths:

1. An invocation-scoped API key from `--api-key`.
2. A stored ChatGPT/Codex OAuth login from `etch auth login`.
3. A bearer token from `CODEX_ACCESS_TOKEN`.
4. API keys from `OPENAI_API_KEY` or `OPENROUTER_API_KEY`.

OAuth credentials are stored locally under `~/.etch/auth/openai.json` by
default. An explicit `--api-key` always wins for that invocation, which is
useful for OpenRouter, local proxies, and one-off provider tests. When no
explicit API key is passed and a stored OAuth login exists and can be refreshed,
etch uses OAuth before environment API keys. Environment API keys remain the
fallback for Platform billing, OpenRouter, local proxies, and CI.

OAuth mode defaults to the Codex backend and the Responses API shape. Explicit
`--base-url`, `--openai-api`, or config-file settings override those OAuth
defaults.

Responses API calls use HTTP/SSE by default. To try the session-reused
WebSocket transport, set `--openai-transport auto` or configure
`openai.transport = "auto"`. Auto mode attempts WebSocket first and falls back
to HTTP/SSE before any stream output is emitted.

## Configuration, Hooks, and Plugins

Configure user or project defaults:

```bash
mkdir -p .etch
cp sample-config.toml .etch/config.toml
```

`sample-config.toml` documents every supported key. The CLI first reads
`~/.etch/config.toml` when present, then merges the nearest project
`.etch/config.toml` from the current directory or an ancestor. Project scalar
values override user scalar values, while repeatable sections such as hooks,
plugins, and subagent profiles append in source order. Config values are
defaults only: explicit CLI flags override them. Credential environment
variables are read separately for API keys and access tokens.

Inspect merged configuration with:

```bash
go run ./cmd/etch config check
go run ./cmd/etch config show --effective
go run ./cmd/etch config schema
```

`config check` validates both the TOML subset and semantic settings such as
provider names, OpenAI API modes, hook events, matcher regexes, and enabled
hook/plugin commands.

Hooks are external shell commands that inspect or mutate lifecycle events.
etch sends a JSON envelope on stdin and expects either empty stdout or an
event-specific JSON object on stdout. Hooks run sequentially in config file
order, so later hooks see mutations returned by earlier hooks.

For example, this hook runs a local policy script before write-capable tools:

```toml
[[hooks.PreToolUse]]
matcher = "^(bash|write|edit)$"
command = ".etch/hooks/policy.sh"
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

Plugins are also configured explicitly. etch does not auto-discover project
executables. Each enabled plugin is trusted local code launched from the project
working directory as a child process that speaks JSONL over stdin/stdout and can
register model-callable tools:

```toml
[[plugins]]
name = "example"
command = "go run ./plugins/example/main.go"
timeout_seconds = 30
env = ["GIT_CONFIG_GLOBAL"]
disabled = false
```

The first plugin protocol supports `initialize` and `tool.execute` requests.
Plugin tools appear in the same tool list as built-ins, and their results are
stored as ordinary `message.tool` session events.

Plugin processes run with a sanitized environment by default. etch forwards
common process basics such as `PATH`, `HOME`, temporary directory variables,
and locale settings, but it does not forward model credentials such as
`OPENAI_API_KEY`, `OPENROUTER_API_KEY`, or `CODEX_ACCESS_TOKEN` unless the
plugin config explicitly lists a variable in `env`. Keep plugin env allowlists
small and purpose-specific.

Plugin calls from the same process are serialized even when the tool declares
read-only or parallel-safe behavior, while different plugin processes and
built-in read-only tools may still overlap. Timeouts and fatal protocol
failures close the plugin process and hide its tools from later model requests;
ordinary plugin-declared tool errors remain recoverable.

The repository includes a small example plugin at `plugins/example`. It uses
the thin `etch/sdk` package from [sdk/plugins.go](sdk/plugins.go), exposes
`plugin_echo` for smoke testing, and exposes `project_files` for a small
filesystem summary:

```bash
go run ./cmd/etch tool plugin_echo --args '{"text":"hello"}'
go run ./cmd/etch tool project_files --args '{"path":".","limit":200}'
```

The repository also includes a standard-library-only Go intelligence plugin at
`plugins/go-intel`. It is intentionally a plugin, not core harness behavior. It
uses `go/parser`, `go/ast`, and `go/token` to expose one model-facing
`go_inspect` tool. The tool searches package paths, displayed repo-relative
file paths, root-relative file paths, and symbol names with case-insensitive Go
regular expressions, then returns either package maps, compact symbol rows,
summary declarations, or full source declarations. `go_inspect` is also the
preferred Go source reader once a caller can narrow by package, file, or symbol:
`detail:"full"` returns the actual Go source declaration, including complete
function and method bodies, without a separate `read` call. Use
`detail:"package"` or `detail:"none"` first for broad maps, `summary` after
narrowing by file or name, and `full` for exact declarations that need source
bodies:

```toml
[[plugins]]
name = "go-intel"
command = "go run ./plugins/go-intel/main.go"
timeout_seconds = 30
```

```bash
go run ./cmd/etch tool go_inspect --args '{"paths":["internal/session"],"detail":"none"}'
go run ./cmd/etch tool go_inspect --args '{"paths":["internal/session"],"detail":"package","includeUnexported":true}'
go run ./cmd/etch tool go_inspect --args '{"paths":["internal/session"],"file":"internal/session/store\\.go$","detail":"none"}'
go run ./cmd/etch tool go_inspect --args '{"paths":["internal/session"],"name":"^Store\\.","includeUnexported":true}'
go run ./cmd/etch tool go_inspect --args '{"paths":["cmd/etch","internal/config"],"package":"config|main","name":"plugin","includeUnexported":true}'
go run ./cmd/etch tool go_inspect --args '{"paths":["internal/session"],"name":"^Store\\.Append$","includeUnexported":true,"detail":"full"}'
```

## Subagents

Subagents are configured child-agent profiles. When enabled, etch registers a
model-callable `task` tool. The parent model sees the configured profile names
and descriptions, then delegates isolated work to one of them. Each delegated
task runs through the same core turn loop as the parent, but writes its own JSONL
child session and returns only a compact result to the parent. Child sessions
fork the parent conversation before the assistant message that requested the
`task` call, so a subagent sees the parent’s discoveries up to the delegation
point without inheriting the unfinished parent tool-call batch.

```toml
[subagents]
enabled = true
max_per_turn = 4
max_concurrent = 2

[[subagents.profile]]
name = "explore"
description = "Read-only exploration for finding relevant files and likely causes."
system_prompt = "Explore independently and return concise findings for the parent."
allowed_tools = ["ls", "read", "find", "grep", "go_inspect"]
max_tool_rounds = 16
auto_compact = true
```

Profiles can override provider, model, OpenAI API mode, reasoning settings,
system prompt, allowed tools, child tool-loop limits, and child compaction
limits. Empty provider fields inherit from the parent chat configuration. The
tool allowlist can include built-ins and configured plugin tools. If a profile
explicitly includes `task`, nested subagents inherit a registry capped by the
parent profile's allowlist. The parent model cannot override a profile's
`max_tool_rounds` at runtime; subagent loop budgets are owned by config.

The direct tool path is useful for smoke testing a profile without waiting for a
parent model to choose it:

```bash
go run ./cmd/etch tool task \
  --args '{"profile":"explore","task":"Find where config validation lives."}'
```

The parent-visible task result includes the child session id plus `etch show`
and `etch resume` commands for inspecting or continuing the child transcript.
Interactive prompt footers fold in child-agent token usage, provider request
counts, and byte counters as child model calls finish; final turn summaries add
the completed child tool totals. Delegated work remains visible in the parent
turn instead of disappearing into child logs. Because the fork pointer is stored
in the child session metadata, resuming a child session rebuilds the inherited
parent context before appending new child turns.

## Sessions and Chat Commands

Inspect local sessions:

```bash
go run ./cmd/etch sessions
go run ./cmd/etch show <session-id-prefix>
go run ./cmd/etch resume <session-id-prefix>
```

On clean exit, chat prints the session id and a copyable `etch resume`
command. `etch resume <id-prefix>` is equivalent to starting chat with the
matching session preloaded, including prompt history, usage counters, compacted
summaries, and prior model response identity metadata when the provider can
expose it.

Inside chat, use slash commands for local session and context operations:

```text
/help                Show readable chat command help.
/status              Show session age, turns, model calls, tool calls, and usage.
/context             Show projected context size and pinned context layers.
/context dump [path] Write logical model context to a plain-text file.
/compact [notes]     Append a model-written summary for older history.
/show                Render the active session transcript.
/sessions            List known local sessions.
/tools               List registered tool names.
/tool NAME           Show one tool description and JSON parameter schema.
/new                 Start a fresh session in the same chat process.
/exit or /quit       Leave chat.
```

`/context` estimates the next model request. It reports pinned instruction
layers, tool schema size, active summaries, raw replay size, and approximate
token counts.
`/context dump [path]` writes the same logical pre-hook context projection in a
plain-text layered format. Without `path`, etch writes a timestamped
`context-YYYYMMDD-HHMMSS.txt` file in the current directory.

`/status` reports what has already happened in the session: age, turns, model
calls, tool calls, tool batches, compactions, message bytes, approximate timing
from JSONL event gaps, provider-reported token usage, and provider transport
metrics. When providers report usage, etch appends `model.usage` events to
the JSONL log and sums them for `/status`, including input, cached input,
output, reasoning output, and total tokens when available. When providers
report transport measurements, etch appends `model.metrics` events with
the selected transport, request counts, WebSocket connection and reuse counts,
continuation attempts, continuation fallbacks, the latest continuation fallback
diagnostic, request/response byte totals, per-request byte averages,
first-event timing, and request-shape counters. Live chat footers can receive
those counters from running subagents before their task result is appended, and
chat status folds in completed subagent sessions referenced by `task` results,
including nested child sessions when their logs are still present.

Interactive chat uses the active session log for prompt history. Up and Down
cycle through prior user prompts from the current session, including prompts
loaded through `--session`, while the draft being edited is restored when moving
past the newest history entry.

## Built-In Tools

Run a built-in tool directly:

```bash
go run ./cmd/etch tool ls .
go run ./cmd/etch tool find readme .
go run ./cmd/etch tool find --glob '**/*_test.go' '' .
go run ./cmd/etch tool grep etch README.md
go run ./cmd/etch tool grep --regex --context 2 'func Test[A-Za-z0-9_]+' .
go run ./cmd/etch tool read README.md
go run ./cmd/etch tool bash -- pwd
```

The model-facing `read` tool also accepts a `files` array for several
independent ranges in one call, so agents can retrieve known follow-up context
without spending a model round per file. When `files` is non-empty, it wins
over top-level `path`, `offset`, and `limit` fields so model-filled mixed
requests still behave as batched reads:

```json
{
  "files": [
    {"path": "internal/plugins/client.go", "offset": 47, "limit": 170},
    {"path": "internal/tool/tool.go", "offset": 204, "limit": 60}
  ]
}
```

The model-facing `grep` tool accepts `paths` for multi-root searches such as
`["cmd/etch", "internal/config"]`. It also recovers the common
space-separated `path` mistake when every split root exists.

Preview an exact replacement edit without modifying the file:

```bash
go run ./cmd/etch tool edit \
  --old "old text" \
  --new "new text" \
  --dry-run \
  README.md
```

`write` and `edit` return human-readable diffs. In chat mode, etch renders
mutation previews live so the user can see exactly what changed.

## Project Context

etch builds prompt context in layers:

```text
base system prompt
project prompt from .etch/config.toml, when configured
SYSTEM.md files, parent directory before child directory
~/.etch/AGENTS.md, when present
AGENTS.md files, parent directory before child directory
compact skill catalog
latest compacted session summary, when present
recent raw session messages
```

Use `[prompt]` in `.etch/config.toml` for project/operator prompt policy
that belongs next to provider, tool, plugin, and subagent configuration. Use
`SYSTEM.md` for project-specific agent identity and durable behavior. Use
`AGENTS.md` for repository workflow, coding, documentation, verification, and
commit-message rules. AGENTS.md files are loaded in full, and these layers are
pinned ahead of compacted conversation history.

Manual compaction is available through `/compact` and `etch compact`.
Automatic compaction can be enabled in `.etch/config.toml`:

```toml
[context]
auto_compact = true
auto_compact_threshold_tokens = 120000
keep_recent_tokens = 20000
```

etch checks the projected context before chat model calls. When the estimate
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
go run ./cmd/etch compact --session <id-prefix> \
  --instructions "focus on modified files and failing tests"
```

Skills follow the Agent Skills `SKILL.md` convention. etch discovers skill
metadata from `.etch/skills/*/SKILL.md` and `.agents/skills/*/SKILL.md` in
the current directory and its ancestors. The default prompt includes only the
skill catalog: name, description, and path. Full skill bodies and reference
files remain outside the prompt until a later on-demand loading path reads them.

## Development

Build the binary:

```bash
make build
```

Install the `etch` command into `GOBIN` or `GOPATH/bin`, and install the
bundled `go-intel` plugin binary into `~/.etch/bin/go-intel`:

```bash
make install
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

etch is not a finished agent product. It is a small harness for learning the
shape of a good coding-agent core: explicit loops, durable logs, narrow tools,
and a plugin boundary that keeps the binary small.

The near-term direction is to keep hardening the local kernel: better provider
coverage, sharper tool safety, richer plugin/process boundaries, and context
management that stays inspectable rather than magical.
