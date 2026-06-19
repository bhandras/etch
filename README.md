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
- project instruction loading from `AGENTS.md`
- manual context compaction and `/context` stats
- built-in tools for `ls`, `read`, `write`, `edit`, and `bash`
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

Use a custom endpoint:

```bash
OPENAI_API_KEY=unused go run ./cmd/harness chat \
  --provider openai \
  --base-url http://localhost:11434/v1 \
  --model qwen2.5-coder
```

Inspect local sessions:

```bash
go run ./cmd/harness sessions
go run ./cmd/harness show <session-id-prefix>
```

Run a built-in tool directly:

```bash
go run ./cmd/harness tool ls .
go run ./cmd/harness tool read README.md
go run ./cmd/harness tool bash -- pwd
```

## Status

Harness is not a finished agent product. It is a small harness for learning the
shape of a good coding-agent core: explicit loops, durable logs, narrow tools,
and a plugin boundary that keeps the binary small.

The near-term direction is to improve context management, add search tools,
make plugin execution real, and keep the core boring while the edges become more
capable.
