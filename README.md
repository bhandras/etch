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

- a line-oriented CLI chat loop
- local JSONL session logs under `.harness/sessions/`
- OpenAI-compatible streaming through the standard library
- project-local TOML config from `.harness/config.toml`
- pinned project context from `SYSTEM.md` and `AGENTS.md`
- Agent Skills-style discovery from `.harness/skills/*/SKILL.md` and
  `.agents/skills/*/SKILL.md`
- manual context compaction, `/context` projection stats, and `/status` session
  stats
- provider-reported token usage for OpenAI Chat Completions and Responses API
  streams
- built-in tools for `ls`, `find`, `grep`, `read`, `write`, `edit`, and
  `bash`
- external process hooks for prompt, context, tool, and compaction events
- human-readable tool call and tool result rendering

The default provider is an offline echo model, so the CLI can run without
network access. Use the OpenAI-compatible provider explicitly when talking to a
real model.

## Usage

Run one prompt with the echo provider:

```bash
go run ./cmd/harness -p "hello"
```

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

Use a custom endpoint:

```bash
OPENAI_API_KEY=unused go run ./cmd/harness chat \
  --provider openai \
  --base-url http://localhost:11434/v1 \
  --model qwen2.5-coder
```

Configure project defaults:

```bash
mkdir -p .harness
cp sample-config.toml .harness/config.toml
```

`sample-config.toml` documents every supported key. The CLI reads the nearest
`.harness/config.toml` from the current directory or an ancestor. Config values
are defaults only: environment variables override config, and explicit CLI flags
override both.

Hooks are configured in the same TOML file. A hook is an external shell command
that receives a JSON event envelope on stdin and may write a JSON patch to
stdout. For example, a `PreToolUse` hook can block a tool call or rewrite its
arguments before the tool runs.

Inspect local sessions:

```bash
go run ./cmd/harness sessions
go run ./cmd/harness show <session-id-prefix>
```

Inside chat, use slash commands for local session and context operations:

```text
/status    Show session age, turns, model calls, tool calls, and token usage.
/context   Show projected context size and pinned context layers.
/compact   Append a model-written summary for older session history.
/show      Render the active session transcript.
/sessions  List known local sessions.
/tools     List built-in tool names.
/new       Start a fresh session in the same chat process.
/help      Show available chat commands.
/exit      Leave chat.
```

Run a built-in tool directly:

```bash
go run ./cmd/harness tool ls .
go run ./cmd/harness tool find readme .
go run ./cmd/harness tool grep Harness README.md
go run ./cmd/harness tool read README.md
go run ./cmd/harness tool bash -- pwd
```

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

Skills follow the Agent Skills `SKILL.md` convention. Harness discovers skill
metadata from `.harness/skills/*/SKILL.md` and `.agents/skills/*/SKILL.md` in
the current directory and its ancestors. The default prompt includes only the
skill catalog: name, description, and path. Full skill bodies and reference
files remain outside the prompt until a later on-demand loading path reads
them.

## Status

Harness is not a finished agent product. It is a small harness for learning the
shape of a good coding-agent core: explicit loops, durable logs, narrow tools,
and a plugin boundary that keeps the binary small.

The near-term direction is to improve context management, make plugin execution
real, and keep the core boring while the edges become more capable.
