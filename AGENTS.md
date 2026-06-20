# Agent Guidelines

This repository is for a small, fast, minimal Go coding-agent harness. Keep the
core boring and explicit. Prefer simple protocols, durable state, and small
interfaces over broad frameworks.

## Workflow

- Make small incremental commits. Each commit should contain one coherent change.
- Keep unrelated cleanup out of feature commits.
- Read the surrounding code before adding new abstractions.
- Prefer the standard library unless a dependency removes real complexity.
- Keep generated or vendored files clearly attributed.

## Documentation

- Treat `docs/` as the source of truth for architecture and design decisions.
- Keep documents up to date in the same change that alters the behavior or
  architecture they describe.
- Update [docs/architecture.md](docs/architecture.md) when changing core
  boundaries, plugin behavior, auth behavior, session format, tooling policy, or
  other durable design choices.

## Go Style

- Every package must have a `doc.go` file with meaningful package
  documentation.
- Every function must have a meaningful godoc comment, including unexported
  helpers and test functions.
- Every constant must have a meaningful godoc comment, exported or unexported.
- Every exported variable, exported struct type, and exported struct field must
  have a meaningful godoc comment.
- Comments should explain purpose, invariants, or tradeoffs. Avoid empty
  narration that only repeats the identifier.
- Prefer small functions with narrow responsibilities. If a helper is hard to
  document clearly, split it or rename it.

## Formatting

Use `llformat` for Go formatting, installed locally through the Makefile in the
same style as `lightninglabs/darepo-client`.

```bash
make fmt
make fmt-check
```

Do not rely on a globally installed formatter. The Makefile installs
`github.com/bhandras/llformat/cmd/llformat` into
`tools/llformat/bin/llformat`.

## Commit Messages

Commit subjects must use:

```text
subsystem: short desc
```

Use a concrete subsystem such as `core`, `session`, `plugins`, `auth`, `tools`,
`docs`, `build`, or `tests`. Use `multi` when one commit intentionally spans
multiple packages or subsystems.

Good examples:

```text
docs: describe plugin transport
plugins: add stdio request routing
auth: store codex credentials
multi: bootstrap repository tooling
```

The body is required for all non-trivial commits. Write sufficiently detailed
messages, usually 3-10 sentences, so a future reader can understand what
changed and why without reconstructing the whole diff. Use separate paragraphs
when a message needs to distinguish motivation, implementation details,
tradeoffs, migration notes, or verification. Format bodies as normal paragraphs
with real newlines, not literal `\n` sequences, and run them through the commit
message formatter when needed.

Run the commit-message linter before publishing changes:

```bash
make commitmsg-lint commit=HEAD
make commitmsg-lint range=origin/main..HEAD
make commitmsg-lint file=/tmp/commit-msg
```

The linter is a pure-Go, stdlib-only reimplementation of the
`lightninglabs/darepo-client` commit message convention. It enforces
`<subsystem>: <summary>`, subject/body wrapping, one blank line before the body,
and real newlines.

## Verification

Before handing off changes or making a commit, run the narrowest checks that
match the edit. For documentation-only changes, run the commit-message linter
against any message file you changed or intend to use. For Go code, run
`make fmt`, `make lint`, and add or run focused tests before broader test
suites. Use `make test` before committing changes that touch shared behavior,
cross-package contracts, CLI flows, or plugin SDK/protocol behavior.
