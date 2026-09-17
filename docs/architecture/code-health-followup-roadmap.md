# Post-H8 Code Health Roadmap

Status: implementation complete. F0-F4 delivered the admitted changes. F5 was
intentionally skipped because its evidence gate was not met.

Baseline: `origin/main` at `16bd8536` on 2026-09-06. The H0-H8 program is
complete; this roadmap covers the smaller correctness and maintainability
risks found by the follow-up audit.

## Objective

Keep MintClaw easy to extend after H8 without starting another repository-wide
rewrite. The work should make inbound message interpretation deterministic,
remove stable cross-package protocols from untyped maps, and simplify the one
remaining controller state cluster whose legal transitions are hard to see.

Every packet must preserve behavior unless its admission section names an
intentional correction. New generic supervisors, dependency-injection
containers, universal event frameworks, and compatibility layers are out of
scope without new evidence.

## Audit Evidence

### Correctness risk: relation inference uses processing time

Inbound event boundaries survive the durable spool, but relation metadata does
not. `pkg/agent/prompt_turn.go` and the fallback path in
`pkg/agent/context.go` classify adjacent media using `time.Now()`. A message
processed immediately can therefore be related to the previous user message,
while the same message processed after a queue delay or restart can become
standalone.

This is the only admitted high-priority item because it changes interpretation
according to runtime delay rather than inbound facts.

### Typed-boundary gap: durable ingress carries stable facts in `Raw`

`pkg/bus.InboundMessage` and `pkg/bus.InboundContext` do not carry an event
timestamp or relation descriptor. Telegram media-group facts and interaction
projections use stable string keys in `InboundContext.Raw`, even though code
outside the adapter depends on their meaning.

`Raw` remains appropriate for platform-specific diagnostics and extension
data. It should not be the primary contract for facts interpreted by multiple
packages.

### Maintainability risk: coding controller primary-operation state

Before F4, `pkg/coding/controller/controller.go` coordinated turn, compaction,
review, cancel, evidence, and close paths with several related booleans and
repeated admission checks. `primaryOperation` now owns that mutually exclusive
state and its start, cancel, commit, and finish transitions. Evidence queueing
remains separate because it is genuinely concurrent. The reviewer and thread
package boundaries remain unchanged; no generic runtime supervisor was added.

### Conditional layout issue: human interaction ingress

`pkg/agent/human_interaction_inbound.go` still combines classification,
resume-flight coordination, delivery, and transcript repair. Ownership already
belongs to `interactionService` and `interactionResumeService`, so another
abstraction layer is not admitted. A mechanical phase-oriented file split is
allowed only when a concrete change must touch this area and the split reduces
the patch's mixed concerns.

### Documentation drift

The root roadmap still describes completed channel-lifecycle and model-binding
work as future work. The Codex-style steering roadmap also reports active
implementation after its acceptance criteria were merged. Documentation must
describe the deployed architecture before it can guide further changes.

## Guardrails

- Preserve each inbound platform event as a distinct durable event.
- Derive structural relations from event facts, never text semantics or an
  LLM.
- Use the inbound event timestamp for bounded adjacency. Wall-clock processing
  time is not an input once the event has been admitted.
- Classify once at an owned runtime boundary and preserve the result through
  queueing, replay, claiming, and steering.
- Keep compatibility at explicit readers and remove it after a bounded cutover;
  do not add permanent dual representations.
- Characterize controller behavior before changing its state representation.
- Prefer named concrete contracts over general-purpose frameworks.
- Treat line count as supporting evidence, not a reason to refactor by itself.

## Delivery Packets

### F0: restore documentation truth

Scope:

- admit this roadmap and link it from the architecture index;
- mark the Codex-style steering plan complete;
- record the current partial state of inbound relations;
- update completed channel-lifecycle and model-binding entries in the root
  roadmap in the next code-bearing or mixed documentation PR.

Completion gate:

- active and archived plans have accurate statuses;
- current architecture pages do not present already-deployed work as future
  work.

### F1: add typed inbound event facts

Status: implemented. Typed facts are assigned before durable spooling,
classified after route/session admission, persisted back to processing or
released records, and retained by replay.

Scope:

- add a normalized received/event timestamp to the durable inbound contract;
- add the smallest typed relation descriptor needed for the three existing
  states: standalone, explicit reply, and adjacent media follow-up;
- classify from the current event timestamp and recent session facts at an
  owned admission boundary;
- preserve the facts through spool serialization and restart replay;
- define explicit behavior for old spool records without the new fields.

The first version must not add speculative confidence scores, semantic kinds,
or policy frameworks.

Completion gate:

- immediate, delayed, and restart-replayed copies of the same inbound event
  produce the same relation;
- explicit replies remain stronger than adjacency;
- serialization round trips preserve event time and relation metadata;
- focused bus, ingress, and classifier tests pass.

### F2: make prompt assembly a relation consumer

Status: implemented. Prompt-build requests carry the admitted typed relation;
prompt assembly only renders that relation. Legacy/unset dispatches default
once before frozen turn input, and synthetic prompts receive an explicit
standalone shape without consulting history or processing time.

Scope:

- pass the typed relation into prompt-build requests;
- render context from the supplied relation rather than calling the classifier
  with `time.Now()`;
- remove prompt-local and context-fallback relation guessing;
- classify legacy/unset messages once before prompt assembly, if compatibility
  is still needed during the cutover.

Completion gate:

- no prompt construction path uses processing time to infer a relation;
- prompt behavior for current immediate messages is unchanged;
- delayed and replay tests prove stable prompt input.

### F3: normalize stable platform grouping and interaction facts

Status: implemented. Telegram media-group identity and membership and stable
interaction projection facts are typed and survive durable replay without
dual-writing legacy `Raw` keys. One normalization-only compatibility reader
hydrates pre-F3 spool records and must be removed after those records have
drained from supported deployments.

Scope:

- represent Telegram media-group identity and membership with typed inbound
  fields;
- replace stable cross-package interaction projection keys with a typed
  contract;
- keep `Raw` only for genuinely adapter-specific extension data;
- remove migrated string-key readers after their bounded compatibility window.

Completion gate:

- album and interaction behavior survives durable replay without reconstructing
  core meaning from string keys;
- no permanent dual write remains;
- adapter, bus, and agent tests cover round trips and missing legacy fields.

### F4: make coding primary-operation state explicit

Status: implemented. One concrete actor-owned state now represents idle, turn,
compaction, or review, exposes the admission conflict matrix in one place, and
owns cancellation and settlement invariants. Existing controller lifecycle
tests and focused state characterization tests cover the preserved behavior.

Scope:

- write characterization tests for turn, compaction, review, cancel, evidence,
  and close admission conflicts;
- replace the related boolean cluster with one concrete primary-operation state
  and centralized start, cancel, and finish transitions;
- keep evidence queueing separate where it is genuinely concurrent;
- preserve public controller events and thread-store behavior.

Completion gate:

- the legal operation matrix is visible in one place;
- impossible combinations are unrepresentable or rejected at one boundary;
- existing native-turn, compaction, review, restart, and close tests pass;
- no generic supervisor or new cross-package framework is introduced.

### F5: conditionally split human-interaction phases

This packet is admitted only as a conditional cleanup, not scheduled work.

Status: intentionally skipped. The F3 typed-projection work did not require a
mixed change across resume-flight coordination, delivery, and transcript
repair, so a mechanical file split would not have made that change smaller or
easier to review.

Trigger:

- a concrete interaction change touches two or more of classification,
  resume-flight coordination, delivery, and transcript repair; and
- a mechanical file move makes the behavioral diff smaller or easier to
  review.

Allowed change:

- move existing methods into phase-oriented files without changing ownership,
  persistence, or delivery contracts.

Stop condition:

- if the split needs a new service container, interface family, compatibility
  facade, or broad rename, leave the layout alone and document why.

## Execution Order

1. F0 lands first so later PRs point at an admitted contract.
2. F1 establishes durable event facts and deterministic classification.
3. F2 removes the old prompt-time inference after F1 is merged.
4. F3 migrates stable `Raw` protocols in separately reviewable slices.
5. F4 follows with its own characterization tests and may proceed only after
   the inbound correctness track is stable.
6. F5 runs only when its trigger is met; it is not required to close the
   roadmap.

Each non-stacked packet starts from the latest `origin/main`, uses a focused PR,
and runs formatting, changed-package lint, and affected tests. Deployment or
data migration work needs its own operational evidence; code merge alone does
not prove a runtime cutover.

## Global Completion Criteria

The roadmap is complete when:

- inbound relation behavior is invariant under queue delay and restart replay;
- prompt assembly consumes durable typed relation facts;
- stable cross-package inbound facts no longer depend on `Raw` string keys;
- coding controller primary-operation transitions have one explicit owner;
- current architecture and root roadmap documentation match deployed behavior;
- no new framework was introduced solely to make files or call graphs look
  uniform.

F5 may remain intentionally skipped if its trigger never occurs. The temporary
node protocol v1 reader removal is tracked separately by its retention and
zero-legacy gate and is not part of this roadmap.
