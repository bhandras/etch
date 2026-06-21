# Architecture

This document is the living design record for the agent. It should change as
the code teaches us more. Keep it practical: record the choices we have made,
the tradeoffs behind them, and the boundaries we do not want to blur.

## North Star

We are building a minimal coding agent harness in Go.

The core should be small enough to understand in one sitting, fast enough to
feel immediate, and boring enough to trust. It should build into a static binary
with no required runtime dependency chain. Advanced workflows should grow
through plugins and clear protocols, not by turning the kernel into a product
monolith.

The target shape is:

```text
a tiny static Go agent kernel
with an append-only session log,
a normalized streaming model interface,
a small set of reliable coding tools,
and an out-of-process plugin protocol.
```

We are intentionally closer to Pi's minimal harness philosophy than to a full
multi-surface product like OpenCode. We still want an OpenCode-compatible growth
path: a local server, richer clients, agent profiles, model/provider packs, and
Model Context Protocol support can all be added later. They should sit on top of
the kernel rather than define it.

## Design Principles

Keep the core narrow. The core owns the agent loop, session log, model stream,
tool registry, plugin supervisor, configuration, and prompt assembly. It should
not know about GitHub, browsers, issue trackers, databases, or project-specific
policy.

Prefer protocols over package coupling. The durable contracts are the session
log format, model event stream, tool schema, and plugin remote procedure call
protocol. Packages can change; protocols let us replay, test, debug, and replace
parts.

Make state inspectable. The agent should store important decisions as durable
events instead of hidden in-memory mutations. A developer should be able to open
a session file, read what happened, and repair it when needed.

Default to explicit trust. Project-local instructions are inert text until used
as context. Project-local executable plugins require an explicit trust decision.

Make the fast path good. The default OpenAI/Codex auth path should work for a
developer with a ChatGPT/Codex subscription. API-key usage should also be first
class, but subscription-backed local use is the primary dogfooding path.

Do less by default. Subagents, plan mode, browser automation, vector memory,
MCP, hosted sync, and desktop or IDE clients are useful, but they are not kernel
features in the first version.

## Kernel

The kernel is the small runtime that turns user input into model calls, tool
calls, and session events.

The first package layout can be simple:

```text
cmd/agent/                  CLI entrypoint
internal/core/              agent loop and turn state machine
internal/session/           JSONL append log, branches, replay
internal/model/             provider-neutral request and event types
internal/provider/openai/   bundled OpenAI and Codex provider
internal/tools/             built-in coding tools
internal/plugins/           plugin process supervisor and RPC client
internal/prompt/            prompt assembly and context sources
internal/auth/              auth interfaces and credential storage
internal/config/            global and project config
sdk/                        stable public plugin API helpers
```

The core loop should stay explicit:

```text
1. Load the current session branch.
2. Build model context from system prompt, project instructions, skills,
   summaries, recent messages, tool specs, and plugin-provided context.
3. Start the model stream.
4. Render streamed assistant output.
5. Validate and execute requested tool calls.
6. Append tool results.
7. Admit any queued steering prompts at the next safe model boundary.
8. Continue until the model stops or the user cancels.
```

Cancellation should flow through `context.Context`. The user must be able to
interrupt a model stream or a running tool call without killing the whole
session.

Interactive chat supports steering: text submitted while a turn is running can
be admitted into the same turn before a later model call. Steering is not
cancellation. It does not abort the active model stream or running tools. The
core exposes a `DrainSteering` callback and checks it only after a tool batch
has completed, immediately before the next model request. This keeps provider
tool-call protocol order valid: an assistant tool-call message is still followed
by its tool results before any new user message appears. If a turn finishes
without another model-call boundary, unconsumed steering remains queued as the
next normal prompt in the chat loop.

The loop uses a configurable safety bound rather than a tiny fixed ceiling.
`DefaultMaxToolRounds` is 32 model/tool exchange rounds per user turn. A round
is one model response followed by zero or more requested tool executions, so it
is not the same as an individual tool-call count. CLI callers can raise or
lower the bound with `--max-tool-rounds` or `.harness/config.toml`.

Pi appears to treat tool use as part of the broader agent lifecycle: it tracks
tool calls for session statistics and relies on stop reasons, cancellation,
retries, context overflow handling, and compaction to bound real work. This
harness keeps one explicit per-turn guard because the current kernel is much
smaller, and context compaction does not protect against a model getting stuck
in a tool loop.

## Session Log

Sessions are append-only JSONL files. Each line is one event. The in-memory
session is a projection of the log, not the source of truth.

By default local sessions live under:

```text
.harness/sessions/
```

The `.harness/` directory is ignored by git because transcripts can contain
sensitive local work. Callers can override the location with `--session-dir`.

Events should include stable IDs and parent IDs so a session can branch:

```json
{"type":"message.user","id":"01H...","parentId":null,"time":"...","data":{"content":[{"type":"text","text":"fix tests"}]}}
{"type":"message.assistant","id":"01H...","parentId":"01H...","time":"...","data":{"content":[{"type":"tool_call","id":"call_1","name":"grep","arguments":{"pattern":"TODO"}}]}}
{"type":"message.tool","id":"01H...","parentId":"01H...","time":"...","data":{"toolCallId":"call_1","content":[{"type":"text","text":"..."}]}}
```

The log should also store compaction events, model changes, permission
decisions, plugin diagnostics, and context changes. If a fact shaped the model's
next turn, it should be recoverable from the log.

We should not start with a database. JSONL is easy to diff, repair, export,
sync, and replay. A server or index can be built later as a projection over the
same files.

The first local index is also JSONL:

```text
.harness/sessions/index.jsonl
```

Each new session appends an index row with the session ID, path, creation time,
working directory, and a short title derived from the first prompt. The index is
for local listing and prefix resolution only. The session file remains the
durable transcript.

## Model Stream

Providers should adapt to one internal request and event stream. The agent loop
should not know whether the model is OpenAI, Anthropic, local, or a provider
plugin.

The internal stream should represent:

```text
text start/delta/end
reasoning start/delta/end
tool-call start/delta/end
usage
error
done
```

Streaming is a UX feature, not only an API detail. The core exposes observer
callbacks for assistant text deltas, reasoning deltas, tool batches, individual
tool starts, and coarse turn timing. The minimal line-oriented terminal should
use those callbacks for live status, reasoning progress, and tool-call
progress, while rendering assistant prose from the finalized durable message.
Richer TUIs can attach a stream controller for partial assistant text without
making the core depend on terminal code.

We should begin with a small provider set:

```text
OpenAI/Codex built in
OpenAI Platform API key mode
OpenAI-compatible endpoint mode
Anthropic later, if needed
provider plugins later
```

The first real provider is `internal/provider/openai`, a stdlib-only
OpenAI-compatible streaming client. It uses plain `net/http` and server-sent
event parsing, not an SDK. Chat Completions remains the default API shape for
compatibility with local OpenAI-compatible endpoints. The same provider can use
the Responses API when callers want OpenAI reasoning summaries or richer output
items.

OpenAI-compatible usage is selected explicitly:

```bash
OPENAI_API_KEY=... go run ./cmd/harness \
  --provider openai \
  --model gpt-4.1-mini \
  -p "say hello"
```

Reasoning summaries are provider and model dependent. For OpenAI reasoning
models, callers can opt into Responses API mode and request displayable
summaries:

```bash
OPENAI_API_KEY=... go run ./cmd/harness chat \
  --provider openai \
  --openai-api responses \
  --model gpt-5.5 \
  --reasoning-effort medium \
  --reasoning-summary auto
```

The harness treats these as displayable summaries, not raw hidden
chain-of-thought. Chat mode renders them as muted dot-led live blocks and does
not persist them into the JSONL transcript in this first version.

Local or proxy-compatible endpoints can override the base URL:

```bash
OPENAI_API_KEY=unused go run ./cmd/harness \
  --provider openai \
  --base-url http://localhost:11434/v1 \
  --model qwen2.5-coder \
  -p "say hello"
```

The CLI reads credential material from `OPENAI_API_KEY`,
`OPENROUTER_API_KEY`, and `CODEX_ACCESS_TOKEN`. Non-secret settings such as
provider, model, base URL, reasoning options, and context limits are configured
through flags or `.harness/config.toml`. Stored Codex OAuth credentials are
loaded from `.harness/auth/openai.json` when available.

## Configuration

Project-local configuration lives at `.harness/config.toml`. The CLI discovers
the nearest config file by walking from the current working directory toward the
filesystem root. The supported TOML surface is intentionally small and parsed by
`internal/config` with only the Go standard library: scalar assignments, normal
tables, and hook array tables.

The precedence order is:

```text
compiled defaults
.harness/config.toml
explicit CLI flags
```

Credentials are the exception: API keys and access tokens come from explicit
flags or credential environment variables instead of TOML.

The current config tables are:

```toml
[session]
dir = ".harness/sessions"
max_tool_rounds = 32
keep_messages = 12

[context]
auto_compact = false
auto_compact_threshold_tokens = 120000
keep_recent_tokens = 20000

[provider]
name = "echo"
model = "gpt-5.5"

[openai]
base_url = "https://api.openai.com/v1"
api = "chat"
reasoning_effort = "minimal"
reasoning_summary = "auto"

[[plugins]]
name = "example"
command = "go run ./plugins/example/main.go"
timeout_seconds = 30
disabled = false
```

`sample-config.toml` is the canonical human-readable inventory of supported
keys. It should be updated in the same change that adds or removes config
surface.

## External Hooks

Hooks are the first executable extension point, separate from long-lived
plugins. A hook is an external command run through the platform shell. Harness
sends one JSON envelope on stdin and reads an optional JSON patch from stdout:

```json
{
  "version": 1,
  "event": "PreToolUse",
  "cwd": "/work/project",
  "payload": {
    "sessionPath": ".harness/sessions/example.jsonl",
    "tool": {
      "id": "call_1",
      "name": "bash",
      "arguments": "{\"command\":\"go test ./...\"}"
    }
  }
}
```

Hooks are configured as array tables:

```toml
[hooks]

[[hooks.PreToolUse]]
matcher = "bash|write|edit"
command = ".harness/hooks/policy.sh"
timeout_seconds = 10
disabled = false
```

The first supported events are:

```text
SessionStart
UserPromptSubmit
TurnStart
TurnComplete
ContextBuild
PreToolUse
PostToolUse
PreCompact
PostCompact
```

Hooks run sequentially in file order. This is a deliberate transformer-node
model: if one hook rewrites a prompt, context message list, tool arguments, tool
output, or compaction summary, the next hook sees that updated value. This is
simpler and more predictable than concurrent mutation hooks. Long-lived plugins
may later use JSONL-RPC and concurrent request IDs, but shell hooks should stay
small, bounded, and easy to reason about.

Matchers are regular expressions. Empty matchers and `*` match everything.
`SessionStart` matches on the start reason, currently `new` or `resume`.
`PreToolUse` and `PostToolUse` match on tool name. `PreCompact` and
`PostCompact` match on the trigger, currently `manual`. `UserPromptSubmit`,
`TurnStart`, `TurnComplete`, and `ContextBuild` ignore matchers.

Hook stdout must be either empty or a JSON object for that event. For example,
`PreToolUse` can return:

```json
{"block": true, "reason": "writes to .env are blocked"}
```

or:

```json
{"arguments": "{\"path\":\"README.md\",\"offset\":1,\"limit\":40}"}
```

Hook stderr is diagnostic text. A non-zero exit, invalid JSON result, or timeout
fails the surrounding operation. The default timeout is 30 seconds.

## Context Building

Context is the bounded model request assembled from durable state and local
project inputs. It is not the same thing as the full session log. The session
log remains the append-only source of truth, while `internal/prompt` projects
the active session messages and instruction files into provider-neutral model
messages.

The first context builder is deliberately small. It prepends a base coding-agent
system prompt with tool-use guidance, then loads `SYSTEM.md` and `AGENTS.md`
files from the current working directory and its ancestors. Parent files appear
before child files so more local files can refine broader rules. `SYSTEM.md`
extends the agent's project-specific identity and durable behavior, while
`AGENTS.md` carries repository workflow, coding, documentation, and verification
instructions. Each file is capped at 32KB to keep project guidance from
dominating the first context implementation. These files are pinned context:
they are prepended before any compacted conversation summary and are not removed
by session compaction.

Skill packages follow the Agent Skills `SKILL.md` convention and are discovered
as metadata, not eagerly loaded as full prompt text. The first supported project
roots are `.harness/skills/*/SKILL.md` and `.agents/skills/*/SKILL.md` in the
current working directory and its ancestors. The harness parses a small
stdlib-only frontmatter subset, currently `name` and `description`, and enforces
the core naming rules: lowercase letters, numbers, and hyphens only, no leading,
trailing, or consecutive hyphens, a 64-character maximum, and a name that matches
the parent directory. Descriptions are required and capped at 1024 characters.
The prompt receives only a compact catalog with each skill path. Full skill
bodies remain outside the default context until a later on-demand loading path
reads the referenced `SKILL.md`.

Session continuation replays prior user, assistant, and tool messages into the
next model request. Assistant tool calls and tool results are preserved in the
provider-neutral message shape so OpenAI-compatible history remains valid.

Compaction appends a `context.summary` event to the same JSONL log. The full
session history remains on disk, but future context projection includes the
latest summary plus recent raw message events instead of replaying the
summarized prefix. The summary event records whether the trigger was `manual` or
`auto` so hooks, status output, and later tooling can explain why the context
changed. Manual compaction is available from the CLI:

```bash
go run ./cmd/harness compact --session <id-prefix>
```

Inside `harness chat`, `/compact` appends a summary for the active session and
`/context` prints approximate context stats such as total events, whether a
summary is active, raw replay events, projected context bytes, pinned system
files, pinned instruction files, auto-compaction settings, and available skills.
Automatic compaction is opt-in through `[context]` config or chat flags. It runs
after the current user message is appended and before the model request is
built. If the projected context reaches the configured approximate token
threshold, the core summarizes the older prefix, keeps the latest
`context.keep_recent_tokens` approximate tokens raw, appends a
`context.summary` event with `trigger = "auto"`, and then builds the model
request from that updated projection. If the token budget is disabled,
`session.keep_messages` remains as a compatibility fallback. Pinned context
such as the base prompt, `SYSTEM.md`, `AGENTS.md`, and the skill catalog is
never compacted away.

The text-summary compaction backend follows Pi's shape: it wraps serialized
conversation history in tags, uses a structured checkpoint format, passes the
previous summary back in for update-style compactions, preserves exact paths and
error messages, and stores deterministic read/modified file lists in summary
metadata. Manual chat compaction accepts optional focus text with
`/compact <instructions>`, which is appended as an additional soft instruction
to the summarizer.

Automatic compaction avoids repeated summaries when an already-active summary
or pinned context dominates the projected size; in that case, another
compaction would not materially reduce the next request. Chat renders a muted
terminal notice with before and after token estimates whenever automatic
compaction appends a summary.

Context stats show both bytes and approximate token counts. The first token
estimator is deliberately stdlib-only rather than provider-exact: it treats
ASCII word-like runs as roughly one token per four characters and counts
punctuation-like runes as individual tokens. This is enough to compare context
layers and spot growth without adding model-specific tokenizer tables to the
core binary.

Session status is separate from context projection. The `/status` command reads
the same JSONL log and reports operational counters such as session age, event
count, user turns, model calls, tool calls, tool batches, auto/manual
compactions, message bytes, approximate timing from event gaps, and
provider-reported token usage when available.

Model clients should emit provider-reported token usage when available. The
core stores those counters as `model.usage` JSONL events so `/status` can report
actual input, cached input, output, reasoning output, and total tokens across
the session. These actual counters complement `/context` estimates: usage says
what a completed provider call consumed, while context stats estimate what the
next prompt projection contains.

## OpenAI And Codex Auth

OpenAI support is bundled, not a third-party plugin. It is the main dogfooding
path.

The built-in OpenAI provider supports:

```text
openai-codex-oauth
  ChatGPT Plus/Pro/Business/Enterprise subscription auth through a
  Codex-style device flow. `harness auth login` stores credentials in
  `.harness/auth/openai.json`.

openai-api-key
  Platform or OpenAI-compatible API key auth through `OPENAI_API_KEY`,
  `OPENROUTER_API_KEY`, or `--api-key`.

openai-codex-token
  Codex-compatible bearer token auth through `CODEX_ACCESS_TOKEN`.

codex-cli-import
  Explicit import from an existing Codex CLI auth cache. This remains future
  work.
```

These modes must stay distinct. API-key usage follows Platform billing and
Platform organization policy. Codex OAuth usage follows the user's ChatGPT or
workspace Codex entitlement, rate limits, and data controls.

Credential resolution prefers the user's local OAuth login when
`.harness/auth/openai.json` exists and can be refreshed. If no stored OAuth
login exists, Harness falls back to `CODEX_ACCESS_TOKEN`, then API-key
credentials. This makes `harness auth login` the default local identity while
still allowing OpenAI-compatible providers such as OpenRouter in projects or
environments without OAuth state.

OAuth mode defaults to the Codex backend at
`https://chatgpt.com/backend-api/codex` and the Responses API shape. Explicit
`--base-url` or `.harness/config.toml` settings override that backend for local
testing and compatible servers. Explicit `--openai-api` or config values
override the OAuth default API shape.

The UI should always make the active auth mode visible:

```text
OpenAI: ChatGPT/Codex subscription
OpenAI: Platform API key
OpenAI: Codex access token
```

Credentials should be stored with `0600` file permissions in the first version.
Keychain support can be added later through an optional module. Tokens must never
be logged, sent to the model, exposed to plugins, or included in tool output.

## Built-In Tools

The first built-in tools should be boring and reliable:

```text
read
write
edit
bash
grep
find
ls
```

`edit_file` should begin with conservative exact replacement. More flexible
patching can come later once the simple path is proven.

Read-only filesystem operations should copy Pi's semantics but not its backend
dependencies. Pi uses wrappers around tools such as `rg` and `fd`; this harness
keeps the builtin core self-contained.

The first operation is `ls`, implemented in pure Go under `internal/tools/fs`.
It lists one directory, includes ordinary dotfiles, sorts case-insensitively,
marks directories with `/`, skips internal directories such as `.git`,
`.harness`, `bin`, `node_modules`, and `vendor`, and emits explicit truncation or
empty-directory notices.

The second operation is `read`, also implemented in pure Go under
`internal/tools/fs`. It reads text files with Pi-style `offset` and `limit`
arguments, uses 1-indexed line offsets, caps default output at 2000 lines or
50KB, and appends continuation hints when more content remains. Image support is
intentionally left out for now because it needs MIME detection, resizing, and
model multimodal content handling that should not bloat the first core tool
slice.

The third operation is `write`, a whole-file create or overwrite tool. It
creates parent directories, writes through a temporary file in the same
directory, preserves existing file permissions when replacing a file, and
returns a compact byte-count success message instead of echoing full content
back into the model context. When replacing an existing file, it also returns a
bounded unified-style diff so the model and user can inspect what changed.
Mutation tools are anchored to the current working directory and refuse to
modify internal `.git` or `.harness` paths.

The fourth operation is `edit`, an exact-replacement mutation tool for existing
text files. Each `oldText` must match exactly one region in the original file;
missing, ambiguous, empty, whitespace-only, or overlapping edits fail instead
of guessing. All edits are located against the original file before any
replacement is applied, then written from the end of the file backward through
the same atomic replacement helper used by `write`. Successful edits return a
compact, unified-style line diff with a 20KB output cap so the model can inspect
what changed without flooding the transcript. The optional `dryRun` flag runs
the same validation and diff generation but returns a preview without modifying
the file. The first version intentionally avoids fuzzy matching; that can be
considered later after the sharp exact-replacement contract is proven. Adding a
line is still an exact replacement: the model should replace a unique
neighboring block with that same block plus the inserted line. Empty files and
full rewrites should use `write`. Live chat renders mutation diffs with
conventional red deletion lines and green insertion lines while keeping diff
headers and context muted.

The fifth operation is `bash`, a bounded command execution tool for
verification and local diagnostics. It runs `bash -lc` in the current working
directory, defaults to a 30-second timeout, caps caller-provided timeouts at 120
seconds, captures stdout and stderr separately, and caps each output stream at
64KB. Non-zero exits are returned as tool output with their exit code instead
of aborting the harness turn. This tool intentionally depends on the user's
local shell environment because executing external programs is its purpose; the
agent core and read/write/search tools should still avoid hidden binary
dependencies.

The sixth operation is `find`, a recursive path discovery tool implemented in
pure Go. It matches case-insensitive substrings against slash-separated relative
paths, includes files and directories, skips internal directories such as
`.git`, `.harness`, `bin`, `node_modules`, and `vendor`, sorts output
deterministically, and emits explicit no-match, truncation, and
skipped-directory notices.

The seventh operation is `grep`, a recursive literal text search tool
implemented in pure Go. It returns compact `path:line:text` matches, skips
internal directories, skips binary-looking files, skips unusually large files,
caps total and per-file matches, and supports opt-in case-insensitive matching.
The first version intentionally avoids regexp semantics; most agent discovery
tasks need identifiers, error strings, TODOs, config keys, and neighboring line
numbers before they need a full search language.

The builtin tool registry lives under `internal/tool`. Registered tools wrap
`internal/tools/fs` operations as model-callable functions, and the CLI exposes
the same operations for direct smoke testing:

```bash
go run ./cmd/harness tool ls .
go run ./cmd/harness tool ls --limit 20 .
go run ./cmd/harness tool read AGENTS.md
go run ./cmd/harness tool read --offset 20 --limit 40 AGENTS.md
go run ./cmd/harness tool find main .
go run ./cmd/harness tool grep DefaultMaxToolRounds .
go run ./cmd/harness tool write --content 'hello\n' notes/hello.txt
go run ./cmd/harness tool edit --old hello --new goodbye notes/hello.txt
go run ./cmd/harness tool bash -- go test ./...
```

When using the OpenAI-compatible provider, the CLI includes builtin function
schemas in model requests. If the model calls one, the core executes the
pure-Go tool, appends a `message.tool` event, and sends the tool result back to
the model for a final answer. Ordinary tool failures are also appended as
`message.tool` events with a `tool error:` prefix instead of aborting the turn,
so the model can recover by choosing a better tool call or asking for more
context. Context cancellation still aborts the turn.

The model should see one unified tool list regardless of where a tool comes
from:

```text
built-in tool
native plugin tool
MCP-imported tool
```

Internally each tool should still carry its source, timeout, permission policy,
and output limits.

## Plugins

Plugins are native extensions of the agent runtime. The first implemented slice
is intentionally narrow: explicit out-of-process tools configured in
`.harness/config.toml`. Plugins can later grow to commands, context providers,
event observers, policy hooks, compaction customizers, UI panels, and model
providers, but those are not kernel features in this version.

Plugins are configured, not discovered. Harness does not scan project
directories for executables. Each plugin must appear in config:

```toml
[[plugins]]
name = "example"
command = "go run ./plugins/example/main.go"
timeout_seconds = 30
disabled = false
```

The command runs through the platform shell from the current project working
directory. The plugin is a child process with its own dependencies, so it can be
written as a separate Go module or in any language that can read stdin and write
stdout. The default timeout is 30 seconds and applies to initialization and tool
calls when `timeout_seconds` is omitted.

The first transport is:

```text
JSONL-RPC over stdio
```

That means the core starts the plugin as a child process and communicates over
the plugin's standard input and output. Standard error is reserved for logs.

```text
core -> plugin stdin:  one JSON request per line
plugin stdout -> core: one JSON response per line
plugin stderr:         human-readable logs only
```

The first protocol version is `0.1.0` and supports initialization plus tool
execution:

```json
{"id":"1","method":"initialize","params":{"protocolVersion":"0.1.0"}}
{"id":"1","result":{"name":"git","tools":[{"name":"git_status","description":"Show git status.","parameters":{"type":"object","properties":{}}}]}}
{"id":"2","method":"tool.execute","params":{"callID":"call_1","name":"git_status","arguments":{}}}
{"id":"2","result":{"content":[{"type":"text","text":"clean"}]}}
```

The harness registers plugin tools in the same model-facing registry as builtin
tools. The model sees one sorted tool list, and the core appends plugin tool
results as ordinary `message.tool` session events. Tool-call hooks still wrap
plugin tools because hooks run around the unified registry dispatch.

The public helper layer lives in `sdk/plugins.go`. It defines the stable plugin
authoring types and `ServePlugin`, which lets simple Go plugins declare tools
without copying the JSONL wire server. The internal plugin client still owns
process startup, timeouts, schema validation, and registry wiring.

The repository carries one standalone example plugin under `plugins/example`.
It is its own Go module, imports `harness/sdk`, and exposes two tools:

- `plugin_echo` echoes text and reports basic text statistics, making it useful
  for smoke testing the plugin path.
- `project_files` summarizes file counts, byte size, extension buckets, and
  sample paths under a directory.

The implementation currently serializes calls to one plugin process. This keeps
the first protocol client small and predictable. The request IDs are still part
of the wire format so a later supervisor can add a read loop, a
pending-response map, and concurrent calls without changing plugin messages.

Unix sockets, named pipes, TCP, or HTTP can be added later behind the same
transport interface. They are not needed for the first version.

## Plugins Versus MCP

Plugins extend the agent. MCP servers expose external tools, resources, and
prompts over a standard protocol.

The layering should be:

```text
agent core
  -> native plugin protocol
      -> MCP bridge plugin
          -> MCP servers
```

MCP is valuable because it gives the agent access to a wider ecosystem. It
should not define the kernel. A native plugin can participate in session
lifecycle, compaction, permission policy, context assembly, and UI. An MCP server
usually provides capabilities for the model to call.

In short:

```text
Plugins are how the agent grows.
MCP is how the agent borrows tools.
```

## Trust And Permissions

The permission model should be small but extensible.

The current hook system can already enforce simple policy at the process
boundary and observe lifecycle milestones:

```text
SessionStart     -> prepare or audit session-local state
UserPromptSubmit -> block prompt admission
TurnStart        -> record per-turn start metadata
TurnComplete     -> notify or audit final turn output
PreToolUse       -> block or rewrite tool arguments
PostToolUse      -> redact or rewrite tool output
PreCompact       -> cancel compaction or provide a custom summary
```

This is enough for project-local policy scripts such as protected paths,
dangerous shell checks, audit logging, and custom compaction. Project hooks are
powerful because they run arbitrary commands with the user's permissions. A
future trust store should hash hook definitions and require explicit trust
before project-local hooks run, following the same basic safety shape as Codex.

Policy plugins can later allow, deny, rewrite, or ask the user before sensitive
tool calls with richer UI than shell hooks can provide. The first built-in
policy can stay simple: warn before writes outside the workspace, dangerous
shell commands, or project-local executable plugins in an untrusted repository.

Plugins are more privileged than MCP servers. A native plugin may see session
events and shape agent behavior. MCP servers should receive only the calls or
resources routed to them.

## Terminal And Server

The first interface should be a CLI with a minimal interactive mode:

```text
agent
agent -p "question"
agent --json -p "question"
agent sessions
agent sessions --json
agent show <session-id-prefix>
agent show --json <session-id-prefix>
```

The terminal should show dot-led assistant, tool, and reasoning blocks
compactly, collapse large tool output by default, and display a small animated
working line with elapsed time while model or tool work is active. The
line-oriented chat renderer intentionally renders assistant prose from the
final durable message rather than raw stream deltas, because provider delta
events can be too fragmented for reliable line-mode presentation. Live chat
rendering is intentionally separate from transcript rendering: chat can use
terminal tone, small markdown styling, capped tool output, grouped tool-call
batches, colored diffs, and turn footers, while `show` remains a plain durable
transcript view. Full interactive interruption should be implemented with
context cancellation and raw terminal input instead of being
hidden in the renderer.

A local HTTP server can come later. If we add one, it should expose the same
session log, model stream, and tool registry as the terminal uses. The server
must be a client boundary, not a new source of truth.

## First Executable Slice

The first runnable deliverable is intentionally smaller than a useful coding
agent. It wires together the durable session log, provider-neutral model stream,
and core turn runner through a non-interactive CLI:

```bash
go run ./cmd/harness -p "hello"
go run ./cmd/harness --json -p "hello"
go run ./cmd/harness sessions
go run ./cmd/harness show <session-id-prefix>
```

The model is an echo client. That is deliberate. The echo client lets the
project test prompt admission, streaming response collection, parent-linked
session events, JSONL persistence, and CLI rendering without network access,
OpenAI auth, tools, plugins, or MCP. The first OpenAI-compatible provider now
attaches to this loop through the same model interface instead of shaping the
kernel.

## What Not To Put In The Kernel Yet

These features are useful, but they should begin outside the kernel:

```text
subagents
plan mode
browser automation
MCP server management
desktop and IDE clients
long-term vector memory
hosted sync
large provider catalogs
complex workflow DSLs
rich TUI layout systems
```

If one of these becomes essential, we can promote the smallest stable interface
into the core after a plugin proves the shape.

## Commit Discipline

Development should move in small incremental commits. Each commit should explain
one coherent change and should pass the commit-message linter. The linter is a
pure-Go, stdlib-only implementation of the `lightninglabs/darepo-client` commit
message convention, so the repository does not need Python, Node, or shell
package managers to check commit messages.

Commit subjects use:

```text
subsystem: short desc
```

The body is optional but encouraged when the reason is not obvious. Separate the
subject from the body with one blank line and wrap body text at 72 columns.

Example:

```text
plugins: add stdio transport skeleton

This introduces the JSONL-RPC transport boundary without registering any real
plugin capabilities yet. Keeping the transport separate from plugin discovery
lets tests exercise request routing before process supervision exists.
```

## Go Coding Style

The codebase favors unusually explicit documentation because this project is
part implementation and part design lab. The documentation rule is simple:
every Go package gets a `doc.go`, every function gets a meaningful godoc
comment, and every constant gets a meaningful godoc comment. Exported variables,
exported struct types, and exported struct fields also need meaningful godocs.

This rule applies to tests and unexported helpers. A test comment should explain
the behavior or invariant the test protects. An unexported helper comment should
explain why the helper exists, not merely restate its name.

Go source is formatted with `github.com/bhandras/llformat/cmd/llformat`. The
Makefile installs it into `tools/llformat/bin/llformat` through the
`tools/llformat` module and then runs it over handwritten Go source:

```bash
make fmt
make fmt-check
```

The `tools/` directory is only a container for independent tool modules. The
commit-message linter lives in `tools/commitmsg`; formatter pinning lives in
`tools/llformat`. These developer tool dependencies are not runtime
dependencies of the agent core.
