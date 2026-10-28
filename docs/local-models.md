# Local models

etch already works with local inference through its `openai` provider. The
server loads the weights and converts model-specific tool syntax into OpenAI
Chat Completions `tool_calls`. etch runs those calls and appends their results
to the session. No provider or kernel change was needed for LFM3 text inference.

## Verified LFM3 setup

Tested on 2026-10-28 with an Apple M2 Pro, 19-core GPU, 32 GB unified memory,
and macOS 26.6.2:

| Component | Version |
| --- | --- |
| Model | `LiquidAI/LFM3-1.7B-GGUF`, `LFM3-1.7B-Q4_K_M.gguf` |
| Model revision | `b3f2ae5d73cd3c91bf40e8ba752c648aa05dffdf` |
| Model size | 1,099,627,808 bytes |
| Model SHA-256 | `3793e1e6e2266919d6e015287221b41100175a98eddaba92e111aed3b05c93c4` |
| Engine | llama.cpp `b11946`, commit `8ca4060b7`, Metal GPU offload |
| Server context | 262,144 tokens (256K), one slot |
| Sampling | Temperature 0.1, top-k 50, repeat penalty 1.1 |
| Reasoning | Disabled for these trials |

The server uses the model's full 256K context. etch starts compaction at an
estimated 200,000 tokens and retains about 65,536 recent tokens after
compaction. The remaining capacity leaves room for output and token-estimation
error. Context includes instructions, tool schemas, history, and output.
Long prompts take more time to process; capacity does not establish retrieval
accuracy across the whole window.

### Download

Run from the repository root. This pins the official Apple Silicon binary and
model in the git-ignored `.etch/local/` directory. The previously installed
Homebrew build `9180` predates LFM3; installing an engine is not sufficient
unless that build supports the model architecture.

```bash
mkdir -p .etch/local/runtime .etch/local/models

curl -fL --retry 3 \
  https://github.com/ggml-org/llama.cpp/releases/download/b11946/llama-b11946-bin-macos-arm64.tar.gz \
  -o .etch/local/runtime/llama-b11946-bin-macos-arm64.tar.gz
echo 'd618da5e7bbb19077f086e6a7e1d77eab55b9bfd481f7531aadada86d13db9ed  .etch/local/runtime/llama-b11946-bin-macos-arm64.tar.gz' | shasum -a 256 -c -
tar -xzf .etch/local/runtime/llama-b11946-bin-macos-arm64.tar.gz \
  -C .etch/local/runtime

curl -fL --retry 3 --continue-at - \
  https://huggingface.co/LiquidAI/LFM3-1.7B-GGUF/resolve/b3f2ae5d73cd3c91bf40e8ba752c648aa05dffdf/LFM3-1.7B-Q4_K_M.gguf \
  -o .etch/local/models/LFM3-1.7B-Q4_K_M.gguf
echo '3793e1e6e2266919d6e015287221b41100175a98eddaba92e111aed3b05c93c4  .etch/local/models/LFM3-1.7B-Q4_K_M.gguf' | shasum -a 256 -c -
```

### Serve

Keep this process running in a terminal. Ctrl-C stops it.

```bash
.etch/local/runtime/llama-b11946/llama-server \
  -m .etch/local/models/LFM3-1.7B-Q4_K_M.gguf \
  --alias lfm3-1.7b --host 127.0.0.1 --port 8081 \
  -c 262144 -np 1 -ngl 99 --jinja \
  --temp 0.1 --top-k 50 --repeat-penalty 1.1 \
  --reasoning off -n 2048
```

`--jinja` uses the model's bundled chat template. This engine build recognizes
LFM3's XML tool syntax and returns structured tool calls. The 2,048-token output
cap bounds each model response; it is independent of etch's tool-round limit.

Check readiness from another terminal:

```bash
curl -fsS http://127.0.0.1:8081/health
make build
bin/etch --provider openai --model lfm3-1.7b \
  --base-url http://127.0.0.1:8081/v1 --openai-api chat \
  --openai-transport http --api-key unused \
  --reasoning-effort '' --reasoning-summary '' \
  --max-tool-rounds 6 \
  -p 'Use read to inspect go.mod and report the module and Go version.'
```

Pass `--api-key unused` explicitly. Stored OAuth credentials take precedence
over environment API keys. An explicit dummy key selects API-key mode before
loading OAuth credentials. Explicit API, transport, and reasoning flags also
override any inherited Codex defaults. Existing hooks and plugins still load
from ordinary user and project configuration.

### Chat

```bash
bin/etch chat --provider openai --model lfm3-1.7b \
  --base-url http://127.0.0.1:8081/v1 --openai-api chat \
  --openai-transport http --api-key unused \
  --reasoning-effort '' --reasoning-summary '' \
  --max-tool-rounds 100 --auto-compact \
  --auto-compact-threshold-tokens 200000 --keep-recent-tokens 65536
```

Start with short tasks and a bounded tool loop. Existing tools execute with
the user's normal project access. A local model changes where inference runs;
it does not add a tool sandbox.

The LFM3 chat launcher allows 100 model/tool rounds per user turn. Reaching
that limit pauses the turn and returns to the prompt in the same session.
Send another prompt to continue from the saved tool results, or `/exit` to
leave. Non-interactive prompt runs still return an error when the limit is
reached before a final response.

## Measured results

These are smoke tests, not an agent benchmark or a comparison of engines.
The initial trials below used a 32K server context before the server was
expanded to 256K.

After expansion, the server's health, properties, and model metadata endpoints
confirmed 262,144 context tokens and one slot. A synthetic 39,073-token prompt
completed in 41.2 seconds and recovered all three markers placed at its start,
middle, and end. This checks requests larger than the previous 32K limit; it
does not test retrieval quality at the full 256K limit.

| Trial | Observed result |
| --- | --- |
| Direct HTTP request | Returned the exact requested marker |
| etch streaming | Returned the exact requested marker |
| etch tool round trip | Called `read` on `go.mod`, then correctly reported `etch` and Go `1.25`; two model calls |
| Simple chat with tools available | Sometimes called `bash` unnecessarily, including `ls -la` after a model-identity question; also confused its own identity with the Go harness |
| Autonomous small Go fix | Failed within 12 rounds: identified the off-by-one bug, repeatedly mismatched edit whitespace, then copied displayed line-number prefixes into the disposable file |
| Explicitly guided replacement | Called `read`, `edit`, and `bash`; changed `i := 1` to `i := 0`, ran `go test ./...`, and reported the actual passing result; four model calls |

The repository's `make test` suite also passed, including both bundled plugins.
These runs used the existing working tree; no Go implementation changed for
local inference.

`llama-bench -p 512 -n 128 -r 3 -ngl 99` measured 1,488 ± 4 prompt tokens/sec
and 116.7 ± 2.1 generated tokens/sec with this Q4_K_M file. These are short
synthetic inputs, not 32K-context throughput or end-to-end agent speed.

The full launch thread describes six text, vision, audio, omni, and decision
variants. etch's current message contract carries text only. Vision and audio
would need input support in the harness as well as a compatible inference
server. Decision models expose a different task interface and are better
evaluated as specialized tools first.

Good initial uses are local document extraction, log summaries, constrained
tool routing, repository lookups, and guided edits. Treat open-ended coding
as experimental. Liquid AI's model card also advises against agentic coding
and complex multi-step reasoning for this small model.

## Choosing an engine

Status checked on 2026-10-28. Verify support for the exact architecture,
quantization, template, and tool parser before assuming API compatibility is
enough.

| Engine | Useful role here | Evidence or limit |
| --- | --- | --- |
| llama.cpp | Reproducible LFM3 baseline; portable GGUF serving | Text and tool calls verified on this Mac with `b11946` |
| MLX / mlx-lm | Apple Silicon inference and model experimentation | Liquid AI publishes an official MLX 4-bit checkpoint; not measured in this trial |
| Ollama | Model downloads and convenient local serving | Already installed; the installed 0.33.2 was not used for these trials |
| Kapitsa | Apple Silicon serving optimized for supported Qwen, Nemotron, and Flint models | Version 0.2.23 explicitly rejects architecture `lfm3`; use a supported model for a separate trial |
| vLLM / SGLang | GPU server workloads and concurrent requests | Liquid AI documents LFM3 GPU serving; not measured on this Mac |

Kapitsa speaks OpenAI Chat Completions, so its supported models can use the
same etch provider with a different base URL and model name. Use `--private`
for a local trial: Kapitsa otherwise advertises itself on the network and can
route work to peers. Set `KAPITSA_NO_AUTO_UPDATE=1` when pinning an experiment.
Its published speedups depend on the model, Mac, and speculative decoder;
they do not establish LFM3 performance or M2 Pro performance.

### Kapitsa trial

The official Kapitsa 0.2.23 Apple Silicon binary was also run on this Mac.
`doctor` identified the M2 Pro and its 32 GB memory correctly. Loading the LFM3
GGUF failed with an explicit unsupported-architecture error.

A separate `4b` download fetched Qwen3.5-4B, its multi-token prediction head,
and a DFlash drafter, about 4.57 GB total. With 32K context, one slot, and
KFlash enabled, Kapitsa reported 3.95 GB allocated at startup. etch then called
`read` on `go.mod` and returned the correct module and Go version through
Kapitsa in two model calls. This verifies the integration, not Kapitsa's
published throughput claims.

The experimental binaries and downloads live under `.etch/local/`. To reuse
this exact local installation from the repository root:

```bash
KAPITSA_HOME="$PWD/.etch/local/kapitsa-home" KAPITSA_NO_AUTO_UPDATE=1 \
  .etch/local/runtime/kapitsa/kapitsa-0.2.23-darwin-arm64/kapitsa serve \
  --model 4b --private --port 8082 --context 32k --slots 1 --disk-cache 0
```

Connect with the same etch command, changing `--base-url` to
`http://127.0.0.1:8082/v1`, `--model` to `qwen3.5-4b`, and
`--reasoning-effort` to `none`. Kapitsa enables thinking by default for Chat
Completions; use its documented `reasoning_effort` or `chat_template_kwargs`
controls when a short bounded answer is needed.

## Sources

- [Liquid AI launch thread](https://x.com/liquidai/status/2115128329003303107)
- [LFM3 model card](https://huggingface.co/LiquidAI/LFM3-1.7B)
- [Official GGUF files](https://huggingface.co/LiquidAI/LFM3-1.7B-GGUF)
- [llama.cpp b11946](https://github.com/ggml-org/llama.cpp/releases/tag/b11946)
- [Liquid AI llama.cpp guide](https://docs.liquid.ai/deployment/on-device/llama-cpp)
- [Official MLX checkpoint](https://huggingface.co/LiquidAI/LFM3-1.7B-MLX-4bit)
- [Kapitsa models and serving options](https://github.com/nace-ai/kapitsa/blob/main/docs/usage.md)
- [Kapitsa releases](https://github.com/nace-ai/kapitsa/releases)
