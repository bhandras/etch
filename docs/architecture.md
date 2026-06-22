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
cmd/harness/                CLI entrypoint and terminal chat UI
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

Cancellation should flow through `context.Context`. In interactive chat,
pressing ESC requests cancellation of the active model stream or running tool
without killing the whole session.

Interactive chat supports steering: text submitted while a turn is running can
be admitted into the same turn before a later model call. Steering is not
cancellation. It does not abort the active model stream or running tools. The
core exposes a `DrainSteering` callback and checks it only after a tool batch
has completed, immediately before the next model request. This keeps provider
tool-call protocol order valid: an assistant tool-call message is still followed
by its tool results before any new user message appears. Slash commands, EOF,
and input errors act as ordering barriers, so later typed text is not steered
past an earlier pending command. If a turn finishes without another model-call
boundary, consecutive unconsumed steering prompts are queued as one combined
follow-up prompt in the chat loop rather than as separate independent turns.

The loop uses a configurable safety bound rather than a tiny fixed ceiling.
`DefaultMaxToolRounds` is 32 model/tool exchange rounds per user turn. A round
is one model response followed by zero or more requested tool executions, so it
is not the same as an individual tool-call count. CLI callers can raise or
lower the bound with `--max-tool-rounds` or `.harness/config.toml`.

Tool execution preserves provider-visible order while allowing safe local
parallelism. The core partitions each assistant tool-call batch into execution
groups: consecutive read-only or isolated calls such as `ls`, `read`, `find`,
`grep`, `task`, and known Go-introspection tools can run concurrently, while
side-effectful calls such as `write`, `edit`, `bash`, and unknown plugin tools
act as serial barriers. Tool results are appended to JSONL and sent back to the
model in the original assistant-call order even when the local executions
overlap. This gives subagent and read-heavy review batches real parallelism
without letting reads and writes observe an interleaved filesystem state.

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

The log should also store compaction events, model usage, model response
identity, model changes, permission decisions, plugin diagnostics, and context
changes. If a fact shaped the model's next turn, it should be recoverable from
the log.

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

Interactive prompt history should also be a projection over the session log.
The terminal editor hydrates Up/Down history from `message.user` events when a
session is continued, and live accepted prompts are added to the same in-memory
navigation list. Slash commands are local control input rather than model
context, so they do not need to become durable prompt-history records unless a
future command explicitly changes session state.

Session resume is a CLI projection over the same append-only log. `harness
resume <session-id-prefix>` resolves the local session index, opens the matched
JSONL file for append, seeds prompt history and usage counters from existing
events, and then enters the ordinary chat loop. Clean chat exits print the full
session id and a copyable resume command so users do not need to inspect the
index before continuing work later.

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
response-info
metrics
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
items. It should measure the existing HTTP/SSE path before we consider a
WebSocket transport: request body bytes, raw streamed response bytes, time to
response headers, and time to the first meaningful stream event are reported as
provider-neutral `metrics` events and folded into turn timing.

The OpenAI stream parser is frame-oriented instead of `bufio.Scanner`-based. It
reads response-body chunks, splits complete server-sent-event frames on blank
lines, joins multiline `data:` fields, ignores comments and empty frames, and
surfaces unterminated trailing frames at EOF. This keeps the stdlib-only client
close to Pi's hand-written SSE fallback shape while avoiding Scanner token
limits and making byte metrics reflect the real stream body.

Pi's OpenAI Platform path generally consumes the OpenAI SDK's typed async stream
events, while Pi's Codex path prefers a WebSocket transport with cached session
state and falls back to HTTP/SSE. Harness intentionally starts without a
WebSocket dependency: the Responses `previous_response_id` path and prompt cache
affinity give most of the measured latency benefit while keeping transport code
small and inspectable. If WebSocket support becomes necessary later, it should
arrive as a minimal provider transport swap behind the same model event stream,
not as a new core concept.
Responses API requests include the durable local session ID as
`prompt_cache_key`, clamped to OpenAI's documented 64-character limit, so Codex
and Platform Responses calls can get stable cache affinity across turns. Chat
Completions keeps the narrower compatibility request shape because many local
or OpenAI-compatible endpoints reject unknown OpenAI-specific fields.

When a prior Responses call has a durable `model.response` provider ID, the
core can send `previous_response_id` plus only the new user/tool input since
that response. The full context is still carried in the provider-neutral request
so the OpenAI client can retry without continuation if the provider rejects the
handle. Context-build hooks currently disable continuation because hooks can
rewrite message lists in ways the core cannot safely slice.

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
chain-of-thought. Chat mode renders them as muted dot-led live blocks. The core
also persists completed summaries as `model.reasoning` JSONL events so resumed
sessions can replay the recent UI context. These events are deliberately not
message events, so prompt projection still treats user, assistant, tool, and
summary context as the model-visible state.

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
tables, and hook/plugin array tables. A schema layer defines supported tables,
keys, scalar types, descriptions, and assignment behavior in one place so
runtime parsing, sample-config coverage, and CLI config references stay aligned.

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

[subagents]
enabled = false
max_per_turn = 4
max_concurrent = 2

[[subagents.profile]]
name = "explore"
description = "Read-only codebase exploration."
allowed_tools = ["ls", "read", "find", "grep"]
max_tool_rounds = 16
auto_compact = true
```

`sample-config.toml` is the canonical human-readable inventory of supported
keys. It should be updated in the same change that adds or removes config
surface. The CLI also exposes `harness config check`, `harness config show
--effective`, and `harness config schema` so a user or agent can validate the
discovered project config, inspect merged defaults, and see the model-neutral
schema without reading the source. Config validation includes scalar TOML
parsing plus semantic checks for provider names, OpenAI API modes, reasoning
options, hook events, hook matcher regexes, and enabled hook/plugin commands.

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

Model clients may emit displayable reasoning summaries when a provider exposes
them. The core stores those summaries as `model.reasoning` JSONL events chained
before the assistant message or tool-call message they explain. Resume rendering
can replay them as muted thinking blocks, while context projection ignores them
because they are UI transcript metadata rather than model-visible conversation
turns.

Model clients should also emit provider response identity when available. The
core stores those identifiers as `model.response` JSONL events chained after the
assistant message and any usage event for the same model pass. That state makes
sessions auditable and lets Responses providers continue from a prior response
without resending the whole visible transcript when the local session history is
still a safe suffix of that response.

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

Credential resolution honors an explicit `--api-key` first because a command
line credential is an invocation-scoped provider choice. Without an explicit API
key, Harness prefers the user's local OAuth login when `.harness/auth/openai.json`
exists and can be refreshed. If no stored OAuth login exists, Harness falls back
to `CODEX_ACCESS_TOKEN`, then environment API-key credentials. This makes
`harness auth login` the default local identity while still allowing
OpenAI-compatible providers such as OpenRouter in projects or environments
without OAuth state.

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
bounded unified-style diff so the model and user can inspect what changed. The
diff must be hunked around changed regions, not a whole-file before/after dump,
so unchanged file content does not pollute later model context.
Mutation tools are anchored to the current working directory and refuse to
modify internal `.git` or `.harness` paths.

The fourth operation is `edit`, an exact-replacement mutation tool for existing
text files. Each `oldText` must match exactly one region in the original file;
missing, ambiguous, empty, whitespace-only, or overlapping edits fail instead
of guessing. All edits are located against the original file before any
replacement is applied, then written from the end of the file backward through
the same atomic replacement helper used by `write`. Successful edits return a
compact, unified-style line diff with a 20KB output cap so the model can inspect
what changed without flooding the transcript or the next model prompt. The
optional `dryRun` flag runs the same validation and diff generation but returns
a preview without modifying the file. The first version intentionally avoids
fuzzy matching; that can be considered later after the sharp exact-replacement
contract is proven. Adding a line is still an exact replacement: the model
should replace a unique neighboring block with that same block plus the inserted
line. Empty files and full rewrites should use `write`. Live chat renders
mutation diffs with line-number gutters, conventional red deletion rows, and
green insertion rows.

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
deterministically, respects the root `.gitignore` subset, supports basename and
recursive `**` glob filters, caps recursive descent depth, and emits explicit
no-match, truncation, and skipped-directory notices.

The seventh operation is `grep`, a recursive literal text search tool
implemented in pure Go. It returns compact `path:line:text` matches, skips
internal directories, respects the root `.gitignore` subset, supports basename
and recursive `**` glob filters, caps recursive descent depth, skips
binary-looking files, skips unusually large files, caps total and per-file
matches, truncates long rendered lines, and supports opt-in case-insensitive
matching, context lines, and Go RE2 regular expressions. Literal search remains
the default because most agent discovery tasks need identifiers, error strings,
TODOs, config keys, and neighboring line numbers before they need a full search
language.

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

## Subagents

Subagents are configured child-agent profiles exposed to the parent model
through the `task` tool. They are not plugins and they are not extra threads in
the parent context. A subagent is another ordinary core turn running in a child
JSONL session with its own provider/model settings, system instructions, tool
allowlist, tool-loop limit, and compaction settings.

Profiles live in `.harness/config.toml`:

```toml
[subagents]
enabled = true
max_per_turn = 4
max_concurrent = 2

[[subagents.profile]]
name = "review"
description = "Read-only reviewer for correctness bugs and missing tests."
provider = "openai"
model = "gpt-5.5"
openai_api = "responses"
reasoning_effort = "medium"
reasoning_summary = "auto"
system_prompt_file = ".harness/subagents/review.md"
allowed_tools = ["ls", "read", "find", "grep", "go_package_symbols"]
max_tool_rounds = 20
auto_compact = true
auto_compact_threshold_tokens = 80000
```

The parent model sees the configured profile names and descriptions in the
`task` tool schema. A task call includes a profile name, a concrete task, and
optional focused context. The child prompt is admitted into a new session and
the child result is returned to the parent as one compact tool result containing
the child session id, duration, model/tool counts, final answer, and copyable
inspection commands.

Child sessions extend `session.started` with optional `parentSessionId`,
`parentToolCallId`, and `subagentProfile` fields. This keeps subagent work
inspectable without mixing the full child transcript into the parent context.
The parent session stores the compact `message.tool` result; the child session
stores the full exploration, tool calls, reasoning summaries, usage, and
compaction events.

Tool access is allowlist-based. A profile can allow built-in tools and
configured plugin tools by model-facing name. The first implementation removes
`task` from child allowlists, so subagents do not spawn nested subagents even if
the profile lists it. Subagent tool-loop budgets are config-owned: the parent
model can choose the profile and task, but it cannot lower or raise
`max_tool_rounds` at runtime.

The terminal presentation should stay compact. The parent UI renders `task` as
a subagent activity block and appends the compact task result. The working
status line also includes a quiet count of active subagents. Full child output
stays in the child JSONL session and can be inspected with `harness show
<child-id>` or continued with `harness resume <child-id>`. A richer future
terminal can show per-subagent status rows, but it should still avoid streaming
every child event into the parent transcript by default.

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
stdout. On Unix systems, Harness starts each plugin shell in its own process
group and terminates that group during shutdown so shell-launched plugin
children cannot keep stdio pipes open after cancellation. The default timeout is
30 seconds and applies to initialization and tool calls when `timeout_seconds`
is omitted.

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

The repository also carries a Go intelligence plugin under `plugins/go-intel`.
It is deliberately a plugin rather than core behavior. The plugin uses only the
Go standard library parser packages plus `harness/sdk` to expose symbol-listing
and source-lookup tools (`go_list_symbols`, `go_package_symbols`,
`go_file_symbols`, and `go_symbol`). `go_symbol` returns structured godoc and
function or method signatures, and can include the full declaration source when
the caller needs implementation context. This keeps language-specific
intelligence behind the plugin boundary while giving the project a practical
richer-plugin example. Because it is its own Go module, local configs should
start it with a command such as `go run ./plugins/go-intel/main.go` rather than
`go run ./plugins/go-intel` from the root module. Naming the file keeps the
plugin's working directory at the project root while avoiding Go's
nested-module package resolution error.

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
harness
harness -p "question"
harness --json -p "question"
harness chat
harness resume <session-id-prefix>
harness sessions
harness sessions --json
harness show <session-id-prefix>
harness show --json <session-id-prefix>
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
transcript view. Non-interactive prompt output reuses structural markdown
rendering for tables so single-shot answers keep the same readable shape without
changing JSON output. Interactive ESC cancellation is implemented with context
cancellation and raw terminal input instead of being hidden in the renderer.
The same prompt island handles Up/Down history navigation, preserving the
current draft and drawing history from the active session transcript.

Transient working labels are provider-aware. Native OpenAI Responses reasoning
summaries can supply concise labels for the animated status line when reasoning
summaries are configured, but OpenAI-compatible providers and custom base URLs
use neutral canned labels such as `Working`, `Thinking`, `Responding`, and
`Running tools`. This keeps the UI useful for OpenRouter and local gateway
models whose reasoning streams may not follow OpenAI's summary shape.

The `cmd/harness` package should stay sliced by responsibility as it grows.
`main.go` owns process entry and top-level dispatch, `flags.go` owns CLI flag
projection, chat runtime setup owns non-terminal dependencies, slash-command
files own local command execution, input files own raw terminal state, rendering
files own display, and turn files own the bridge between interactive input and
`internal/core`. This is intentionally still one command package, but behavior
that can be tested without terminal scaffolding should keep moving away from
the renderer.

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

The first model was an echo client. That remains deliberate as a test fixture:
the echo client lets the project test prompt admission, streaming response
collection, parent-linked session events, JSONL persistence, and CLI rendering
without network access, OpenAI auth, tools, plugins, or MCP. The current
OpenAI-compatible provider, tools, hooks, plugins, context projection,
compaction, steering, and terminal renderer all attach to this loop through the
same model and session interfaces instead of shaping the kernel.

## What Not To Put In The Kernel Yet

These features are useful, but they should begin outside the kernel:

```text
nested subagents
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
