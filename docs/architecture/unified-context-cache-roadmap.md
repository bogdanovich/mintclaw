# Unified Context And Prompt Cache Roadmap

Status: active

MintClaw baseline: `origin/main` at `741e2fb4`, 2026-09-20

Comparison baselines:

- OpenAI Codex at `57567b8d` (`rust-v0.155.1` was also inspected);
- Pi at `890f9208`;
- Hermes Agent at `b6a87db5`; and
- OpenClaw at `051563c9`.

## Purpose

This roadmap makes long gateway and local coding sessions preserve the largest
possible provider prompt-cache prefix without losing context fidelity. It also
defines one context assembly contract for both products.

The target is not a second context manager and not a provider-specific session
store. Gateway and MintClaw Code already share `AgentLoop`, `ContextManager`,
`ContextBuilder`, the canonical session interfaces, and Seahorse. The missing
layer is a shared, cache-aware request plan between context selection and
provider serialization.

## Research conclusion

Provider prompt caches reuse an exact request prefix, not merely a system
prompt with the same cache key. A completed turn is therefore stable context:
after its first request, the exact user message, runtime context, assistant
messages, tool calls, and tool results must be replayed without rewriting or
reordering them.

The compared harnesses converge on that invariant:

- Codex tests that the complete previous request is the prefix of the next
  request. Environment, permission, and setting changes are appended after the
  existing transcript.
- Pi keeps an append-only transcript, places Anthropic caching on the latest
  eligible conversation boundary, and rebuilds context as summary plus raw
  retained messages only after compaction.
- Hermes freezes the session system prompt and stores the exact per-turn API
  content that was originally sent, so later replay is byte-stable.
- OpenClaw separates the stable system prefix from a hidden runtime-context
  carrier. OpenAI Responses routes retain carriers append-only, and its tests
  assert prefix preservation across tool loops and later turns.

The relevant primary-source references are:

- [Codex prompt-caching tests](https://github.com/openai/codex/blob/main/codex-rs/core/tests/suite/prompt_caching.rs);
- [Pi compaction contract](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/compaction.md);
- [Pi Anthropic cache placement](https://github.com/earendil-works/pi/blob/main/packages/ai/src/api/anthropic-messages.ts);
- [Hermes context compression and caching](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/developer-guide/context-compression-and-caching.md);
- [Hermes prompt assembly](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/developer-guide/prompt-assembly.md); and
- [OpenClaw prompt caching](https://github.com/openclaw/openclaw/blob/main/docs/reference/prompt-caching.md).

## Current architecture and defect

The existing ownership model is mostly correct:

```text
gateway channel ─┐
                 ├─> AgentLoop/Pipeline ─> ContextManager ─> Seahorse
mintclaw code ───┘            │                    │
                              │                    └─ derived selection,
                              │                       search, compaction
                              └─ ContextBuilder ─> provider adapter
```

Canonical JSONL is the durable conversation authority. Seahorse SQLite is a
derived, rebuildable selection, search, and compaction index. Coding uses a
thread-specific state layout and coding summary policy, but not a separate
context-management implementation.

The defect is in shared request assembly. `ContextBuilder` currently rebuilds
one leading system message on every request:

```text
static prompt
active overlays and skills
current time, runtime, channel, chat, and sender
current Seahorse summary
historical messages
current user message
```

For OpenAI-compatible prefix caching, a changed timestamp or sender ends the
matching prefix before historical messages. A stable `prompt_cache_key` cannot
repair different bytes. Anthropic can reuse the first marked system block, but
MintClaw does not yet place a cache boundary at the latest completed
conversation transaction. Cached-token usage is not represented in the shared
usage contract, so production cannot prove whether the intended reuse occurs.

The separate `Summary` output also encourages putting a changing checkpoint in
the system prompt rather than at its chronological transcript boundary.

## Target ownership model

The shared context pipeline will have four explicit responsibilities:

1. **Canonical journal** — JSONL stores complete semantic turns and the hidden,
   provider-visible context originally attached to each accepted turn.
2. **Context selector** — Seahorse selects summaries/checkpoints and raw recent
   transactions within the budget. It does not decide provider cache markers.
3. **Request planner** — one provider-neutral component creates an ordered,
   immutable request plan for gateway and coding sessions.
4. **Provider compiler** — each adapter serializes that plan and applies only
   provider-supported cache keys, retention, and breakpoints.

The normal request shape is:

```text
stable system snapshot
stable tool definitions
compaction checkpoint, when present
completed raw turns with their frozen runtime carriers
current user message
current runtime carrier
current tool-loop continuation
```

The relative placement of the current user and current runtime carrier may be
provider-specific, but all new dynamic data is after the complete previous
request prefix. Once a current turn is sent, its provider-visible content is
frozen and becomes historical prefix material.

If a provider cannot represent an append-only change safely, MintClaw starts a
new explicit cache lineage rather than silently claiming a cache hit.

## Core invariants

1. **Append-only replay:** absent compaction or an explicit lineage reset,
   request `N` is an exact prefix of the historical portion of request `N+1`.
2. **Frozen turns:** time, sender, route, retrieved memory, active-skill text,
   and other per-turn context are never recomputed inside an old turn.
3. **Stable ordering:** system blocks, tool definitions, messages, attachments,
   tool calls, and tool results use deterministic ordering and serialization.
4. **One canonical authority:** JSONL remains authoritative. Seahorse and cache
   metadata are rebuildable and cannot erase accepted history.
5. **One request planner:** gateway, interactive coding, coding exec, resumed
   coding threads, and remote coding workers use the same ordered plan.
6. **Transactional tool history:** a tool result never moves before or becomes
   detached from its matching call for cache or compaction purposes.
7. **Explicit discontinuity:** compaction, provider/model changes, incompatible
   tool-schema changes, or prompt-schema migrations create an observable cache
   lineage boundary.
8. **Presentation separation:** hidden runtime carriers are not rendered as
   user transcript text, but their exact provider-visible representation is
   durable and inspectable through redacted diagnostics.
9. **No cache-dependent correctness:** a cache miss changes cost and latency,
   never model-visible meaning or recovery semantics.
10. **Measured behavior:** cache reads, cache writes, lineage changes, prefix
    fingerprints, and safe miss reasons are observable without logging prompt
    contents.

## Seahorse decision

Seahorse remains the shared context selector and compactor. Replacing it would
duplicate mature reconciliation, retrieval, transaction grouping, absolute
budgets, and coding restart behavior without solving prompt ordering.

Seahorse does require bounded changes:

- round-trip the canonical hidden turn envelope or its provider-neutral source;
- return a compaction checkpoint as an ordered context item rather than a
  string that `ContextBuilder` must insert into the leading system message;
- preserve complete recent transactions and exact raw message order;
- keep runtime carriers out of semantic search and summary prose while counting
  their provider-visible tokens in request budgets; and
- expose a stable checkpoint generation so compaction creates one intentional
  cache-lineage reset.

Provider cache keys, cache breakpoints, prompt TTL, and model-specific wire
formats do not belong in Seahorse.

## Delivery roadmap

Every stage is a separate reviewable PR unless two adjacent changes remain
smaller and safer together. A later stage must not rely on an unmerged earlier
stage.

### C0 — Contract, characterization, and baselines

Define the provider-neutral request-plan vocabulary and capture the current
gateway and coding request shapes without changing model-visible behavior.

Work:

- add a shared request-prefix fingerprint helper over canonicalized system,
  tools, and message segments;
- record separate fingerprints for stable system, tools, historical transcript,
  and dynamic tail;
- add gateway and coding characterization tests using the same assertion
  helpers;
- add a regression fixture demonstrating the current timestamp-before-history
  prefix break; and
- document the cache-lineage inputs and privacy rules.

Done criteria:

- both gateway and coding tests produce the same segment model;
- fingerprints are deterministic across map iteration and process restart;
- diagnostics contain hashes and counts only, never prompt text or secrets;
- the current prefix break is reproduced by a failing-invariant fixture while
  existing behavior remains unchanged; and
- no provider or Seahorse persistence migration is introduced in this stage.

### C1 — Session cache lineage and cache usage truth

Replace the agent-wide cache bucket with a bounded, privacy-safe session
lineage and make provider cache usage visible.

Work:

- derive the cache key from agent, opaque session identity, provider family,
  model, prompt schema, tool-schema fingerprint, and compaction generation;
- hash and length-bound the external key;
- extend shared usage with cache-read and cache-write input tokens;
- parse supported OpenAI, Codex, Anthropic, and CLI usage fields; and
- emit redacted lineage-change and cache-hit/miss observations.

Done criteria:

- two sessions owned by one agent never share a conversation cache key;
- resume of the same unchanged session reproduces the key;
- provider/model, incompatible tools, compaction generation, and prompt schema
  changes produce a new key;
- supported provider fixtures retain cache-read/cache-write counts end to end;
- unsupported providers report `unknown`, not a fabricated zero; and
- gateway and coding use the identical lineage builder.

### C2 — Durable frozen turn envelopes

Introduce a provider-neutral hidden runtime carrier and persist the exact
context associated with each accepted root turn.

Work:

- add a versioned canonical turn-envelope sidecar rather than modifying visible
  user text;
- move current time, sender, route, current-turn retrieval, and active-skill
  instructions into that tail envelope where applicable;
- freeze the envelope at admission and replay it byte-stably after restart;
- keep display/search content separate from provider-visible replay content;
- round-trip the sidecar through JSONL and Seahorse reconciliation; and
- define backward-compatible behavior for old transcript rows.

Done criteria:

- changing the wall clock between turns does not change any earlier request
  segment;
- a resumed process reconstructs the same historical provider-visible bytes;
- gateway multi-sender attribution remains correct;
- coding project instructions and workspace observations retain their existing
  trust and refresh behavior;
- Seahorse summaries and search do not quote hidden carrier markup;
- token budgets include the carrier; and
- legacy sessions resume without migration-time data loss.

### C3 — Stable system and capability snapshots

Make the leading system and tool prefix stable for one lineage.

Work:

- snapshot stable identity, hierarchy, workspace definitions, skill catalogue,
  output policy, and tool schemas deterministically;
- represent later instruction, memory, workspace, and skill changes as ordered
  transcript updates when supported;
- start a new lineage when a provider cannot safely replay an update;
- sort provider tool schemas and canonicalize JSON schema serialization; and
- constrain hooks so a prefix mutation is either append-only or an explicit
  lineage reset.

Done criteria:

- unchanged sessions reproduce identical system/tool fingerprints;
- active skills no longer rewrite bytes ahead of completed history;
- coding AGENTS changes and gateway workspace/memory changes are either ordered
  updates or explicit lineage resets;
- a tool-profile change cannot reuse an incompatible cache lineage; and
- hooks cannot silently mutate a previously fingerprinted prefix.

### C4 — Ordered Seahorse checkpoints and compaction policy

Align compaction with chronological replay and avoid unnecessary cache resets.

Work:

- replace `AssembleResponse.Summary` system injection with an ordered checkpoint
  item followed by retained raw transactions;
- retain a context-window-aware raw tail, respecting complete tool pairs and
  root-turn boundaries;
- use pressure, output reserve, tool/prompt reserve, and hysteresis instead of
  compacting merely because a fixed small tail can be produced;
- increment checkpoint generation only when the active context actually
  changes; and
- disable cache writes for one-off summarization prompts where supported.

Done criteria:

- one compaction causes at most one intentional lineage reset;
- the second request after compaction reuses the new checkpoint prefix;
- no-op compaction changes neither checkpoint generation nor cache key;
- raw recent turns survive with exact ordering and tool pairing;
- protected goals, constraints, modified files, validation state, and unresolved
  failures survive gateway and coding long-session fixtures; and
- compaction trigger tests use real effective model windows and reserves.

### C5 — Provider cache planners

Apply cache policy at each provider boundary without changing the shared
transcript.

Work:

- OpenAI/Codex: stable session lineage, supported retention, and full-prefix
  replay tests;
- Anthropic: stable system/tools markers plus the latest legal completed
  transaction boundary;
- Gemini: provider-native stable system/tool caching where supported, with the
  dynamic carrier kept in the current tail;
- compatible third-party endpoints: capability-gated behavior with no unknown
  request fields; and
- CLI providers: preserve reported cache usage but do not claim control of the
  subprocess's internal cache.

Done criteria:

- provider request fixtures assert exact marker/key placement;
- unsupported APIs receive no cache-only fields;
- retries and provider fallback use an intentional compatible lineage or start
  a new one;
- cache policy never changes semantic message order; and
- cache usage remains attributable to the actual provider attempt.

### C6 — Cross-runtime invariant suite and rollout

Prove the feature in both products and deploy it safely.

Work:

- run one shared scenario corpus through gateway, interactive coding, coding
  exec, resume, and remote coding worker composition roots;
- cover tool loops, steering, human interaction suspension, attachments,
  restart, fallback, instruction refresh, and compaction;
- add live OpenAI/Codex cache evidence with redacted request fingerprints;
- expose operator diagnostics and rollback configuration; and
- update user and operations documentation.

Done criteria:

- for an unchanged lineage, request `N` is an exact prefix of request `N+1` up
  to the newly appended tail in every composition root;
- an ordinary second turn reports non-zero cached input on a supporting live
  provider;
- restart/resume preserves the same historical fingerprint;
- compaction shows one expected miss followed by renewed cache reuse;
- no transcript UI exposes hidden carrier markup;
- context-quality and compaction-retention evaluations do not regress;
- Linux and macOS coding paths pass; and
- deployed gateway and coding canaries record rollback-ready evidence.

## Global completion criteria

This roadmap is complete only when:

1. gateway and all coding entry points use one request planner and one cache
   lineage contract;
2. no current timestamp, sender, active-skill payload, or mutable summary is
   rendered ahead of replayed historical turns;
3. completed raw turns are immutable provider-prefix material until an explicit
   compaction or compatibility boundary;
4. Seahorse remains rebuildable from canonical JSONL and emits ordered
   checkpoints without owning provider policy;
5. cache usage and safe miss reasons are visible in passive diagnostics;
6. cross-runtime restart, tool-loop, compaction, and fallback tests pass; and
7. live evidence demonstrates cache reuse after an ordinary turn and after the
   first post-compaction request.

## Stop conditions

Stop and revise the architecture before proceeding if implementation requires:

- a second canonical transcript or context manager for coding;
- provider wire payloads becoming the sole durable conversation authority;
- logging raw prompts, cache keys derived from unhashed user identifiers, or
  hidden carrier contents;
- Seahorse learning provider-specific cache APIs;
- rewriting historical turns to apply new runtime data;
- dropping tool results, attachments, or protected state merely to improve a
  cache-hit metric; or
- one provider's role restrictions changing the provider-neutral transcript
  semantics for every other provider.
