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
introduce sidecars. These corruptions must fail. Duplicate IDs within one
active tool batch are rejected; a provider may reuse an ID after its prior
batch completed, without hiding an orphaned duplicate result. Scripted-provider capture
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

## Lifecycle contracts (C6b)

The bounded runner is `bash scripts/test-context-cache-contracts.sh --all`.
It verifies that every named gate exists before running it, builds fresh worker
and companion binaries, and requires subprocess tests rather than accepting
missing-binary skips. `--unit-only` explicitly omits subprocess/WSS evidence.
The macOS CI job runs its unit gates plus the existing native-worker/WSS job;
Linux runs all package tests and the integration runner.

| Lifecycle | Executable evidence |
| --- | --- |
| Tool loops and ordered pairs | Shared corpus; `TestNativeCodingCommandEditsAndResumesAcrossProcessBoundary`; mutation-negative oracle controls |
| Steering and interruption | `TestNativeMintClawWorkerStartsSteersResumesAndShutsDown`; real companion mutation phase in `TestRemoteCodingTaskTelegramToNativeCompanionVerticalSlice` |
| Human suspension and answer | `TestNativeMintClawWorkerProjectsAndAnswersDurableQuestion`; real companion question/answer phase in the vertical slice |
| Attachments and missing bytes | `TestNativeCodingAttachmentsRemainLazySelectableAndDiagnosableAcrossRestart`; unsupported-image admission regression |
| Durable restart/resume | All corpus command/worker restart cases; native worker crash/lease recovery regression |
| Provider fallback | `TestFallbackAttemptUsesActualProviderAndModelLineage`; `TestCodingPromptKeepsFrozenContextAcrossCrossProviderFallback` |
| Instruction refresh | `TestNativeCodingContextPrefixCorpus/instruction-refresh`; workspace retry/next-root and gateway memory/coding AGENTS lineage regressions |
| Compaction and retained context | `TestCodingLongSessionCompactionContinuity`; ordered checkpoint, generation, pressure/hysteresis and no-op/failure lifecycle regressions |

This is a coverage map, not a claim that every category is a full five-entry-
point Cartesian product. Shared production contracts are tested at their owner;
the corpus separately proves that each real entry point composes them.

Compatible continuation checks compare every message/tool schema. An
instruction refresh intentionally replaces system instructions and rotates
lineage, but its entire old non-system transcript must remain unchanged. An
interrupt may narrow the admitted tool set; worker tests still compare every
historical message, including system text, into that authority boundary. Its
terminal hint is intentionally transient control, not durable user history.
The separate steering-only case compares its complete tool-loop request to the
resumed request, without excluding any message or schema.

Steering's interpretation contract is frozen in the existing message envelope,
so JSONL/Seahorse replay does not lose it when in-memory prompt metadata is
gone. Canonical user text stays raw for UI/search and is not duplicated in the
hidden contract. Durable replay, detached/idempotent freezing and worker
presentation have explicit regression checks; no new state store is added.

Media retirement and one-shot tool projections are intentional request-shape
boundaries. The common cache scope derives opaque compatibility digests from
the existing admitted canonical snapshot and active live-tool-context owner.
No blob is read for lineage and no second journal, cache store or context
manager is added. Ordinary history appends and tool loops do not rotate it;
historical media admission and one-shot context presence/removal do. A missing
live projection disables cache intent. Non-media keys remain unchanged because
the additional digest fields are omitted when absent.

An eagerly inspected image becomes lazy on the next coding root. Selecting it
again adds a one-shot projection, then removes it after the model call. The
regression requires lineage boundaries for both transitions while retaining
the durable reference and reporting missing bytes without replaying stale
image data. These are expected misses, not fabricated cache hits.

Compaction fixtures use deterministic summaries and the real canonical JSONL
and derived Seahorse database. They protect goals, constraints, file paths,
test failures, tool evidence and cross-thread isolation across compaction and
derived-store rebuild. Pressure/no-op tests bound when compaction is allowed.
They do not establish live-model semantic quality or provider cache usage.

## Remaining C6 work

Live provider cache usage, operator rollback diagnostics and production
canaries remain separate acceptance gates; passing this deterministic suite
does not complete all C6.
