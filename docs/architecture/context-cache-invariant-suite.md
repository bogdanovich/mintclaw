# Context/cache invariant suite

This is the deterministic part of unified-context-cache C6, not live cache-hit
or deployment evidence. All model responses are scripted. Real tools, runtime
constructors, durable journals and worker subprocesses remain in the path.

## Shared corpus (C6a)

`pkg/testharness/llmscenario.PrefixCorpus` drives one read-only conversation:
read a fixture through `read_file`, answer with its marker, then answer a new
turn using the prior evidence without another read. Three captured requests
must have identical model/tool compatibility and an exact historical-message
prefix. JSON object-key order is irrelevant; array order, content, images,
tool arguments, signatures and paired results are not normalized away.

Composition roots:

| Entry point | Executable regression |
| --- | --- |
| Gateway inbound routing and restart | `TestGatewayContextPrefixCorpusAcrossRestart` |
| Interactive coding command and native frontend controller | `TestNativeCodingContextPrefixCorpus/interactive` |
| Headless `code exec` and `code exec resume` | `TestNativeCodingContextPrefixCorpus/exec` |
| Plain coding command and `resume --prompt` | `TestNativeCodingContextPrefixCorpus/resume` |
| Native worker subprocess, HTTP adapter and process restart | `TestNativeMintClawWorkerContextPrefixCorpus` |

Gateway restart changes admission time and sender without rewriting old turns.
Coding command restart creates a new runtime over the same external thread
root. Interactive and worker presentation checks reject hidden carrier markup.

`SnapshotCall` captures the provider-neutral Chat boundary. It excludes only
canonical provenance (`model_name`, `created_at`, `tool_result_status`) and
runtime-only tool feedback; it rejects leaked canonical sidecars and requires
the runtime cache-lineage key. `SnapshotJSON` captures the actual HTTP boundary,
without rewriting message fields. Localhost/third-party OpenAI-compatible
endpoints deliberately omit cache routing fields: those captures prove message
prefix reuse, not internal lineage or an actual provider cache hit. Emitted
wire cache keys, if any, must still be unchanged.

The oracle does not call the production fingerprint implementation. Negative
controls rewrite system/user/result content, media, arguments, tool schemas,
model, lineage and message order. They also remove/reorder tool results and
introduce sidecars. These corruptions must fail. Scripted-provider capture
ownership is separately tested so later runtime or test-reader mutation cannot
retroactively alter the recorded request.

## Run locally

```sh
go test -count=1 -tags goolm,stdjson \
  -run 'Test(PrefixOracle.*|ScriptedProviderCaptures.*|GatewayContextPrefixCorpusAcrossRestart|NativeCodingContextPrefixCorpus)$' \
  ./pkg/testharness/llmscenario ./pkg/agent ./cmd/mintclaw/internal/coding

# Use a task-specific temporary output directory.
context_test_root=$(mktemp -d)
go build -tags goolm,stdjson -o "$context_test_root/mintclaw" ./cmd/mintclaw
MINTCLAW_CODING_WORKER_TEST_BINARY="$context_test_root/mintclaw" \
MINTCLAW_REQUIRE_CODING_WORKER_E2E=1 \
go test -count=1 -tags goolm,stdjson,integration \
  -run '^TestNativeMintClawWorkerContextPrefixCorpus$' ./pkg/coding/workerprocess
```

The required-worker switch makes a missing binary an error, not a successful
skip. CI runs the corpus on Linux and macOS. The integration runner also
includes the native-worker case. No API credential or live model is required.

## Remaining C6 work

C6b adds lifecycle evidence to this shared assertion contract. Live provider
cache usage, operator rollback diagnostics and production canaries remain
separate acceptance gates; passing this suite does not complete all C6.
