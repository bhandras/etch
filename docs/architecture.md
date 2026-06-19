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
sdk/agentapi/               stable plugin API types
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
7. Continue until the model stops or the user cancels.
```

Cancellation should flow through `context.Context`. The user must be able to
interrupt a model stream or a running tool call without killing the whole
session.

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

Streaming is a UX feature, not only an API detail. The terminal should render
partial assistant text and tool-call progress as soon as it arrives.

We should begin with a small provider set:

```text
OpenAI/Codex built in
OpenAI Platform API key mode
OpenAI-compatible endpoint mode
Anthropic later, if needed
provider plugins later
```

The first real provider is `internal/provider/openai`, a stdlib-only
OpenAI-compatible Chat Completions streaming client. It uses plain `net/http`
and server-sent event parsing, not an SDK. Echo remains the default provider for
offline development and tests.

OpenAI-compatible usage is selected explicitly:

```bash
OPENAI_API_KEY=... go run ./cmd/harness \
  --provider openai \
  --model gpt-4.1-mini \
  -p "say hello"
```

Local or proxy-compatible endpoints can override the base URL:

```bash
OPENAI_API_KEY=unused go run ./cmd/harness \
  --provider openai \
  --base-url http://localhost:11434/v1 \
  --model qwen2.5-coder \
  -p "say hello"
```

The CLI also reads `HARNESS_PROVIDER`, `OPENAI_MODEL`, `OPENAI_BASE_URL`, and
`OPENAI_API_KEY`. Codex OAuth and token refresh remain separate future auth
work; this step only proves the provider HTTP stream behind the existing model
interface.

## Context Building

Context is the bounded model request assembled from durable state and local
project inputs. It is not the same thing as the full session log. The session
log remains the append-only source of truth, while `internal/prompt` projects
the active session messages and instruction files into provider-neutral model
messages.

The first context builder is deliberately small. It prepends a base coding-agent
system prompt with tool-use guidance, then loads `AGENTS.md` files from the
current working directory and its ancestors. Parent instructions appear before
child instructions so more local files can refine broader rules. Each
instruction file is capped at 32KB to keep project guidance from dominating the
first context implementation.

Session continuation replays prior user, assistant, and tool messages into the
next model request. Assistant tool calls and tool results are preserved in the
provider-neutral message shape so OpenAI-compatible history remains valid.

Manual compaction appends a `context.summary` event to the same JSONL log. The
full session history remains on disk, but future context projection includes the
latest summary plus recent raw message events instead of replaying the summarized
prefix. The first compaction path is explicit rather than automatic:

```bash
go run ./cmd/harness compact --session <id-prefix>
```

Inside `harness chat`, `/compact` appends a summary for the active session and
`/context` prints approximate context stats such as total events, whether a
summary is active, raw replay events, and projected context bytes. Future
automatic compaction should build on this append-only event shape rather than
rewriting or deleting older JSONL events.

## OpenAI And Codex Auth

OpenAI support is bundled, not a third-party plugin. It is the main dogfooding
path.

The built-in OpenAI provider should support:

```text
openai-codex-oauth
  ChatGPT Plus/Pro/Business/Enterprise subscription auth through the Codex-style
  browser or device flow.

openai-api-key
  Platform API key auth through OPENAI_API_KEY or stored credentials.

openai-codex-token
  CODEX_ACCESS_TOKEN or an explicitly imported Codex-compatible token.

codex-cli-import
  Explicit import from an existing Codex CLI auth cache when available.
```

These modes must stay distinct. API-key usage follows Platform billing and
Platform organization policy. Codex OAuth usage follows the user's ChatGPT or
workspace Codex entitlement, rate limits, and data controls.

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
`.harness`, `node_modules`, and `vendor`, and emits explicit truncation or
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
returns a compact byte-count success message instead of echoing content back
into the model context. Mutation tools are anchored to the current working
directory and refuse to modify internal `.git` or `.harness` paths.

The fourth operation is `edit`, an exact-replacement mutation tool for existing
text files. Each `oldText` must match exactly one region in the original file;
missing, ambiguous, empty, whitespace-only, or overlapping edits fail instead
of guessing. All edits are located against the original file before any
replacement is applied, then written from the end of the file backward through
the same atomic replacement helper used by `write`. Successful edits return a
compact, unified-style line diff with a 20KB output cap so the model can inspect
what changed without flooding the transcript. The first version intentionally
avoids fuzzy matching; that can be considered later after the sharp
exact-replacement contract is proven. Adding a line is still an exact
replacement: the model should replace a unique neighboring block with that same
block plus the inserted line. Empty files and full rewrites should use `write`.

The fifth operation is `bash`, a bounded command execution tool for
verification and local diagnostics. It runs `bash -lc` in the current working
directory, defaults to a 30-second timeout, caps caller-provided timeouts at 120
seconds, captures stdout and stderr separately, and caps each output stream at
64KB. Non-zero exits are returned as tool output with their exit code instead
of aborting the harness turn. This tool intentionally depends on the user's
local shell environment because executing external programs is its purpose; the
agent core and read/write/search tools should still avoid hidden binary
dependencies.

The builtin tool registry lives under `internal/tool`. Registered tools wrap
`internal/tools/fs` operations as model-callable functions, and the CLI exposes
the same operations for direct smoke testing:

```bash
go run ./cmd/harness tool ls .
go run ./cmd/harness tool ls --limit 20 .
go run ./cmd/harness tool read AGENTS.md
go run ./cmd/harness tool read --offset 20 --limit 40 AGENTS.md
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

Plugins are native extensions of the agent runtime. They can register tools and
commands, provide context, observe events, add policy hooks, customize
compaction, or eventually contribute UI panels and model providers.

Plugins are out-of-process executables. A plugin can be its own Go module and
can have its own dependencies without polluting the core binary. The same
protocol can later support plugins written in Rust, Python, JavaScript, or any
language that can read stdin and write stdout.

The first transport is:

```text
JSONL-RPC over stdio
```

That means the core starts the plugin as a child process and communicates over
the plugin's standard input and output. Standard error is reserved for logs.

```text
core -> plugin stdin:  one JSON request per line
plugin stdout -> core: one JSON response or notification per line
plugin stderr:         human-readable logs only
```

The protocol is request/response plus notifications:

```json
{"id":"1","method":"initialize","params":{"protocolVersion":"0.1.0"}}
{"id":"1","result":{"name":"git","tools":[],"hooks":[]}}
{"id":"2","method":"tool.execute","params":{"callID":"call_1","name":"git_status","arguments":{}}}
{"id":"2","result":{"content":[{"type":"text","text":"clean"}]}}
{"method":"tool.update","params":{"callID":"call_1","message":"reading status"}}
```

Request IDs allow concurrent calls even though stdio is a single stream in each
direction. The core should have one read loop, a pending-response map, and a
mutex around writes.

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

The core should expose hooks such as:

```text
before_tool_call
after_tool_result
context_build
session_event
```

Policy plugins can allow, deny, rewrite, or ask the user before sensitive tool
calls. The first built-in policy can be simple: warn before writes outside the
workspace, dangerous shell commands, or project-local executable plugins in an
untrusted repository.

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

The terminal should stream assistant text, show tool calls compactly, collapse
large tool output by default, and support cancellation.

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
