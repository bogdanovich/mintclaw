# Architecture Simplification Roadmap

Status: active

Audit baseline: `origin/main` at `f5c9afe9`, 2026-08-19

## Decision

MintClaw has one canonical internal implementation and a deliberately small
wire-compatibility budget.

First-party wire protocols should evolve additively when that is natural. A
gateway on the current release must normally interoperate with companions from
the immediately previous release. Older combinations may continue to work when
the protocol change was purely additive, but they are not guaranteed or tested.
An explicitly named rollout may temporarily support two previous releases; it
must include the removal release and may not become an open-ended support tail.

Compatibility must come from the shape of the canonical protocol, not copies
of historical implementations. New optional fields, stable field identities,
unknown-field tolerance at non-authority-bearing envelopes, and advertised
capability intersection are allowed. Historical schema reconstruction,
version-by-version execution switches, dual writes, deprecated aliases,
fallback engines, and no-op flags are not.

Breaking changes are allowed. They increment an explicit protocol major or
minimum-supported version and reject older peers clearly. Once all deployed
components have been upgraded, any temporary boundary adapter is deleted.

Persisted server state and configuration use one current format. An
incompatible storage release uses a coordinated cutover:

1. inspect and back up the deployed configuration and durable state;
2. stop all MintClaw processes that share those contracts;
3. convert or deliberately discard obsolete state outside the steady-state
   runtime;
4. install mutually compatible revisions on the server and first-party
   clients; and
5. restart and verify the current contract.

The product may retain a protocol major, minimum supported version, capability
set, or current storage fingerprint. Those markers select current features or
reject an incompatible peer or document; they must not select a historical
runtime implementation.

These rules follow the useful evolution properties of Protobuf, gRPC, and
Thrift without requiring MintClaw to adopt another RPC stack. The existing
transport can keep its current framing while applying the same discipline:

- add optional fields instead of renaming or changing field meaning;
- never reuse a removed field identity for a different meaning;
- ignore unknown informational envelope fields;
- keep authority-bearing action payloads strict for the action version the
  peer advertised;
- advertise supported actions and features rather than inferring them from a
  historical full schema; and
- use a major boundary for breaking semantic or security changes.

This decision does not remove independent product integrations merely because
they contain the word `compatible`. OpenAI-compatible model APIs, OneBot, OS
and architecture support, browser standards, and explicit import from another
product are current external contracts. They remain unless separately removed.

## Purpose

Recent work improved safety and durability, but several features now encode the
same fact in multiple packages. A small contract change consequently expands
across tool schemas, persisted records, gateway adapters, companion interfaces,
task projections, delivery state, and frontend reducers.

The program will reduce that change amplification while preserving these
properties:

- browser actions remain fail-closed and revalidated at the enforcing broker;
- protected values remain transient and redacted from durable records;
- outbound delivery distinguishes definite failure from ambiguous receipt;
- accepted work remains durable across restart;
- tool effects are never blindly replayed after an uncertain outcome;
- coding and channel runtimes remain separate authority profiles; and
- first-party peers negotiate the bounded current protocol and fail clearly
  when their protocol majors do not overlap.

The target is not fewer files by itself. The target is one owner and one
representation for each protocol, lifecycle, and durable fact.

## Evidence From Recent Changes

The audit identified the following change-amplification signals:

| Change | Signal |
| --- | --- |
| Browser `check` and `hover` | One action slice changed 19 files in PR #737 |
| Browser `drag` | One action slice changed 19 files in PR #738 |
| Browser file chooser | Historical schema mismatch blocked production startup and required PR #743 |
| Verified child objective outcomes | One result concept changed 33 files in PR #780 |
| Human interaction recovery | PRs #770 through #776 repeatedly crossed interaction, task, channel, pipeline, and delivery ownership |

The implementation baseline also contains:

- two browser action representations plus repeated action-kind switches;
- complete generators for several historical browser schemas;
- separate prompt/final delivery state inside interactions and the durable
  outbox;
- duplicate completion, deliverable, objective, and receipt structures in
  tools and tasks;
- an `AgentLoop` that owns nearly every runtime subsystem, plus a `Pipeline`
  adapter layer rebuilt from that same object;
- compatibility config loaders for versions 0, 1, and 2 while version 3 is
  current;
- legacy session-key aliases and JSON-to-JSONL runtime migration;
- legacy subagent execution and `spawn_status` fallback paths; and
- a coding frontend with distributed-client synchronization machinery even
  though its current producer and TUI consumer are in one process.

## Canonical Contract And Bounded Compatibility Rules

Every implementation packet in this roadmap follows these rules.

### Wire protocols

- Keep one canonical protocol model in production code.
- Guarantee the current and immediately previous first-party release when
  their protocol majors overlap.
- Prefer additive optional fields and capability negotiation; do not add a
  branch for every release that omitted the field.
- Send an action or feature only when the peer advertises it.
- Ignore unknown informational envelope fields, but strictly validate the
  known authority-bearing payload for the advertised action.
- Reject a non-overlapping protocol major with an actionable upgrade error.
- Do not infer capabilities from an old generated schema or accept both an old
  and new field name.

### Runtime configuration and persisted inputs

- Accept only the current MintClaw config and persisted record shape.
- Reject a missing, older, or newer storage version with an actionable error.
- Do not infer omitted fields for compatibility. A default is allowed only
  when it is the documented current semantic default.

### Persistence

- Write only one representation.
- Read only the current representation.
- Do not keep dual-read, dual-write, alias, historical-schema, or lazy
  migration code in the running product.
- A coordinated deployment may use a one-time external conversion. That
  converter is not linked into normal startup and is removed after the
  cutover.
- State whose authority cannot be safely converted is invalidated and
  recreated rather than heuristically reinterpreted.

### APIs and tools

- Remove deprecated methods and fields after all in-repository callers move.
- Do not keep adapter methods solely for old tests; update the fixtures.
- Tool names are product APIs. When a replacement is chosen, remove the old
  tool and update prompts, docs, allowlists, schemas, and tests in the same
  packet.
- An exact current-version rejection check is allowed; a fallback execution
  path is not.

### Compatibility budget and deletion cadence

- The guaranteed rolling window is current plus one previous release.
- Supporting two previous releases requires a named rollout owner and deletion
  release in the implementing PR.
- Additive compatibility that requires no old implementation may continue
  naturally beyond the guarantee.
- A temporary adapter lives only at one process boundary, is covered by a
  removal test or tracking item, and is deleted as soon as the fleet is
  upgraded.
- Periodically declare a coordinated compatibility reset: upgrade every
  first-party component, delete all pre-reset adapters and fixtures, and begin
  the rolling window again from that release.
- Every release that changes an internal wire protocol audits expired adapters
  and old capability aliases.

### Deployment

- Dependent first-party binaries are normally upgraded as a set, but the
  current gateway may roll while immediately previous companions remain
  online.
- A deploy is blocked when a registered peer has no overlapping protocol major
  or requires an authority-bearing capability whose current semantics differ.
- Rollback restores the matching binary and its backed-up state as a unit. It
  does not require the new binary to understand the old state.

## Bird's-Eye View Of Coding Frontend Complexity

Before packet C1, the coding frontend was shaped like a small client/server
system even though no process boundary existed:

```text
AgentLoop runtime events
        |
        v
agent adapter
        |
        v
projector -> revisioned snapshot + retained deltas
        |                         |
        |                         +-> gap detection / replay window
        v
controller watch
        |
        v
reducer -> terminal UI model -> renderer
```

This architecture solves real distributed-client problems:

- a web or IPC client can disconnect and request changes since revision N;
- multiple consumers can rebuild the same authoritative state;
- a slow consumer can detect a missed delta and resynchronize; and
- runtime internals do not leak directly into a user interface.

Today, however, the runtime, projector, controller, reducer, and terminal UI
are in one process. There is no network connection to drop and no independent
web client to reconnect. The TUI receives a delta only to reconstruct state
that the producer already holds. That adds protocol versions, 23 delta kinds,
revision counters, retained change windows, snapshot resynchronization, and
two layers of state mutation before rendering.

The simpler current target is:

```text
AgentLoop runtime events
        |
        v
coding presentation store
        |  owns one current immutable view
        v
terminal UI subscription -> renderer
```

The store preserves the valuable boundary: the TUI still consumes coding
presentation state rather than `AgentLoop` internals. It publishes the latest
bounded view in process and does not retain a transport replay protocol.

If an actual web or IPC client is later admitted, that work starts at the
presentation-store boundary. It must justify and introduce a real transport,
authentication, connection lifecycle, consumer identity, backpressure, and
resynchronization contract together. The current runtime should not pay those
costs in anticipation.

## Ordered Implementation Packets

Each packet is one focused PR unless its exit criteria reveal a smaller safe
split. Dependent packets start from the merge of their prerequisite. Every PR
records production/test/documentation additions and deletions at first review.
Repeated cross-package fixes trigger an architecture checkpoint rather than
another compatibility layer.

### S0 — Admit the simplification policy and roadmap

Scope:

- publish this roadmap;
- make the canonical-contract and bounded-compatibility policy explicit; and
- record the coding frontend decision in plain language.

Exit criteria:

- the architecture index links this roadmap;
- later PRs can cite one compatibility budget and deletion policy; and
- no runtime behavior changes.

### B1 — Delete historical browser persistence compatibility

Scope:

- delete legacy browser input and output schema generators;
- delete stored-schema epoch enumeration and exact comparison against
  historical generated JSON;
- remove legacy prepared-dialog fields and lazy migration;
- retain one current browser capability schema under the existing protocol
  range;
- allow a companion to advertise a smaller current action set without
  reconstructing a historical schema;
- make startup reject obsolete persisted browser authority or prepared-action
  state;
- preserve old dispatched no-replay evidence only as bounded opaque tombstones;
  and
- document the deployed-state cutover and evidence.

Likely owners:

- `pkg/nodes/browser.go`
- `pkg/nodes/registry_file.go`
- `pkg/browser/types.go`
- `pkg/browser/file_store.go`

Tests:

- the current schema round-trips;
- an older or missing persisted authority version fails closed with an
  actionable error;
- a compatible companion can advertise its smaller current action set and
  continue serving those actions;
- current approvals and protected values retain their security properties; and
- no test constructs an old schema as accepted input.

Exit criteria:

- `rg` finds no legacy browser schema generator, epoch, or migration;
- adding a browser action does not require editing historical schemas; and
- the deployed system contains only current browser authority records before
  rollout, while connected companions may expose the bounded compatible action
  subset; old dispatched tombstones remain opaque until retention removes
  them.

### B2 — Establish one browser action protocol

Depends on: B1

Scope:

- create one dependency-light canonical action envelope used by browser, nodes,
  gateway, and companion code;
- replace repeated validation and capability switches with one action
  descriptor table or typed per-action implementation;
- replace per-action companion host interfaces with one `Act` request;
- derive model-visible schemas and capability metadata from the current
  protocol definition; and
- retain independent enforcement at the browser broker without translating
  into another action type.

Tests:

- every action has validation, schema, approval, effect, and host dispatch
  coverage;
- the descriptor set and emitted tool schema cannot drift;
- unknown or unadvertised actions fail closed at every trust boundary; and
- local and companion placements execute the same contract tests.

Exit criteria:

- exactly one internal browser action wire type;
- no action-kind conversion switch between browser, gateway, and nodes; and
- adding a non-special action changes its implementation, descriptor, and
  focused tests rather than a broad package chain.

### T1 — Make deliverable and objective outcome canonical

Scope:

- move deliverable, objective outcome, receipt, and artifact references into
  one leaf domain package;
- store those exact types in the task registry;
- delete `Completion` and all completion-to-deliverable conversion;
- delete duplicate tool/task projections; and
- make child agents, delegated tasks, interactions, and status tools consume
  the canonical result.

Tests:

- successful, partial, failed, and unverifiable outcomes round-trip through
  the task registry;
- receipts preserve verification semantics; and
- no caller can populate mutually inconsistent completion and deliverable
  fields.

Exit criteria:

- one definition for every task-result fact;
- no `completionPayloadForLegacyStorage`; and
- no persisted or rendered `Legacy completion` path.

### T2a — Remove legacy task and subagent APIs

Depends on: T1

Scope:

- remove the in-memory `RunToolLoop` subagent fallback;
- require the current `AgentLoop` child runner;
- remove `spawn_status` and use `task_status` as the sole status tool;
- remove spawn-only projections and compatibility wording;
- update tool allowlists, prompts, schemas, docs, and fixtures atomically.

Tests:

- spawn, delegate, wait, cancel, and status use only the durable task path;
- unavailable child execution fails explicitly instead of falling back.

Exit criteria:

- one task registry, one child execution path, and one status tool;
- no test-only compatibility adapter remains in production code.

### T2b — Separate tool output, control, and delivery

Depends on: T2a

Scope:

- split stable execution output from suspension/task control and delivery
  directives in `ToolResult`;
- make each layer consume only the part of the result it owns; and
- update tool schemas, prompts, and fixtures atomically.

Tests:

- task output cannot express impossible legacy/current combinations; and
- suspension and delivery directives do not mutate produced output.

Exit criteria:

- `ToolResult` does not contain deprecated delivery or completion fields; and
- output, control, and delivery each have one explicit owner.

Implemented shape:

- stable output remains on `ToolResult`;
- async and suspension directives live under `ToolResult.Control`;
- one enum plus prepared outbound state live under `ToolResult.Delivery`; and
- the unused message send callback and boolean delivery projections are
  deleted instead of retained as adapters.

### D1 — Make outbox the sole delivery lifecycle owner

Scope:

- remove prompt and final delivery attempt state from interaction records;
- store outbox identities on interactions;
- route interaction prompt, final answer, and resumed completion through the
  same durable delivery coordinator;
- remove direct definite-retry-only sends owned by interaction code; and
- centralize receipt, ambiguous failure, retry, and restart recovery policy.

Tests:

- crash before send, during ambiguous send, after receipt, and during retry;
- duplicate callback and duplicate channel update handling;
- restart during prompt and final delivery; and
- no interaction state transition can claim delivery without an outbox
  receipt.

Exit criteria:

- one delivery state machine;
- interaction records contain domain and routing state, not retry machinery;
  and
- channel-specific delivery outcomes enter one typed coordinator.

Implementation sequence:

- Prompt delivery binds one deterministic outbox ID to the interaction before
  publication. The outbox owns transport attempts, definite failure,
  ambiguity, retry exhaustion, abandonment, and restart recovery; the
  interaction moves to `waiting` only after the admission-specific receipt
  reports delivery. Retry deadlines are honored, and recovered prompts are
  revalidated against their current interaction immediately before replay and
  settled from that exact admission's terminal receipt.
- Final answers and resumed task completions now bind their exact deterministic
  outbox IDs before intent creation, publish through the same coordinator, and
  resolve only from exact terminal receipts. Interaction records retain only
  those IDs; retry counters, transport timestamps, ambiguity flags, and the
  direct channel-send path are deleted. Recovery derives every delivery
  decision from the outbox and revalidates stale final intents before replay.

### D2 — Unify interaction, task, and continuation ownership

Depends on: D1 and T1

Scope:

- replace copied interaction-to-task lifecycle projection with references to
  one authoritative interaction/task relation;
- create one continuation executor for approval and question resumes;
- remove partial pipeline construction and manual tool replay from inbound
  interaction handling;
- serialize session-scoped tool feedback through the owning continuation or
  delivery component; and
- remove generation counters, fallback routing, and compatibility claims made
  unnecessary by the single owner.

Tests:

- steering before, during, and after pending approval;
- cancel/answer/retry races;
- duplicate and stale callbacks;
- restart at every durable transition; and
- task status always derives from the same authoritative relation.

Exit criteria:

- one owner for continuation state;
- no best-effort projection between durable registries; and
- interaction resume does not instantiate an ad hoc pipeline.

### A1 — Replace the `AgentLoop` service bag with owned coordinators

Depends on: T2 and D2

Scope:

- extract `TurnRunner`, `InteractionCoordinator`, `IngressCoordinator`, and
  `DeliveryCoordinator` with explicit state ownership;
- construct them once during runtime initialization;
- remove pass-through `Pipeline` service wrappers whose only implementation is
  `AgentLoop`;
- retain narrow interfaces only at a real consumer or implementation boundary;
  and
- keep behavior unchanged while moving ownership.

Tests:

- current turn, retry, compaction, steering, delivery, and restart contract
  suites run against the extracted coordinators; and
- ownership tests prove mutable registries are not shared by unrelated
  components.

Exit criteria:

- `AgentLoop` composes coordinators instead of directly owning every mutable
  subsystem;
- `NewPipeline(al)` no longer reconstructs dependency bags per run; and
- pass-through interfaces and nil-check wrappers are deleted.

### A2 — Replace `processOptions` with explicit turn requests

Depends on: A1

Scope:

- define explicit current request types for user turns, child turns,
  interaction resumes, coding turns, and recovery;
- normalize them once into a small internal turn specification;
- remove boolean combinations that describe modes indirectly; and
- make invalid combinations unrepresentable or rejected at construction.

Tests:

- a table covers every admitted entrypoint and its normalized semantics;
- no mode depends on an omitted boolean's historical default; and
- ordering hooks such as turn readiness belong to one explicit phase.

Exit criteria:

- callers select a request type instead of constructing a boolean bag; and
- `processOptions` is removed.

Implementation sequence:

1. Remove the compatibility-shaped field mirrors from `processOptions` and
   make `DispatchRequest` the only owner of routing, session, inbound-message,
   and media facts. Rename the normalized runtime shape to `turnSpec` so it is
   not mistaken for an entrypoint API.
2. Make each admitted entrypoint select one explicit `turnMode` and construct
   its `turnSpec` from the mode's canonical behavior. An architecture checkpoint
   rejected separate request wrappers because they added more production
   scaffolding than they removed.
3. Delete remaining behavior fields when the mode constructor makes them
   redundant; retain fields only when their value genuinely varies within one
   mode, without storing speculative mode state on each turn.

### C1 — Collapse the in-process coding frontend protocol

Depends on: A1

Status: implemented

Scope:

- replace projector, retained revision deltas, gap recovery, and reducer
  replay with one in-process coding presentation store;
- let the TUI subscribe to immutable current views through one bounded,
  coalescing subscription;
- retain thread persistence and runtime events as source systems, not as a
  speculative IPC protocol;
- remove current protocol-version compatibility and `ChangesSince`; and
- update the coding roadmap to require a separate admission before adding a
  web or IPC transport.

Tests:

- initial render, streaming progress, tool updates, workspace refresh,
  compaction, cancellation, and terminal restoration;
- slow UI subscribers converge to the latest view without replay; and
- durable thread resume remains independent from frontend presentation state.

Exit criteria:

- one current presentation state in process;
- no retained delta log, gap recovery, or frontend protocol version; and
- future remote clients have a clear boundary but no speculative runtime cost.

Implemented shape:

- `Projector` owns one bounded `ThreadSnapshot` and atomically returns that
  view with a subscription to later views;
- each subscriber has one coalescing slot, so a slow UI receives the newest
  view without blocking the running turn;
- the TUI replaces its presentation view directly instead of reconstructing
  it through a reducer; and
- protocol versions, revisions, delta variants, retained changes, and gap
  recovery are absent from this in-process boundary.

### X1 — Require the current configuration only

Scope:

- remove V0, V1, and V2 runtime config migration;
- remove deprecated field stripping, legacy bindings, old GitHub skill
  settings, provider/model shorthand, old channel field names, and legacy
  defaults;
- require the current version and current field names;
- update example configs, documentation, fixtures, and deployed config before
  rollout; and
- retain only explicit current semantic defaults.

Exit criteria:

- startup reads one config schema;
- no read path writes a migration or backup;
- no deprecated config field exists in current Go structures; and
- the deployed config passes strict validation before the new binary starts.

### X2 — Require current session and state storage only

Status: implemented

Scope:

- remove legacy `agent:...` session-key parsing and alias resolution;
- remove runtime JSON-session to JSONL migration;
- remove old state-location discovery and move-on-start behavior;
- remove compatibility fallbacks between `SessionManager` and the current
  store; and
- convert or retire deployed legacy state during the coordinated cutover.

Implementation sequence:

1. Make the current opaque key and structured scope the only session identity:
   delete textual-key parsing, generated aliases, metadata alias scans, and
   history promotion transactions. Existing alias history is intentionally not
   imported at runtime after this cutover.
2. Make JSONL and the current state directory the only startup storage:
   delete JSON-to-JSONL migration, the in-memory store fallback, old-location
   discovery, and move-on-start behavior.

Exit criteria:

- one session-key format and one persistent runtime store;
- startup never searches for or rewrites old locations;
- no `.migrated` lifecycle in runtime code; and
- current state survives restart and compaction contract tests.

Implemented shape:

- opaque keys and the current structured scope version are the only runtime
  session identities;
- JSONL is the only persistent session store, and initialization failure stops
  checked startup and reload construction;
- the removed JSON snapshot backend has been replaced by an explicitly
  non-persistent test and benchmark store;
- startup ignores removed JSON snapshots and the old workspace-level
  `state.json` location; and
- frontend session lookup uses only current `ClientSessionIDs` metadata.

### X3 — Remove remaining internal compatibility surfaces

Scope:

- inventory remaining production references to `legacy`, `deprecated`,
  backward compatibility, fallbacks, aliases, and migrations;
- remove legacy cron defaults, ASR discovery fallback, old metadata maps,
  remote-workspace argument aliases, old channel delivery mappings, and
  deprecated helper APIs;
- classify every remaining use of `compatible` as either a current external
  integration, platform support, or an error;
- update tests that preserve an old contract; and
- delete obsolete migration documentation after its operational evidence is
  no longer needed, retaining historical PR records in the archive when
  useful.

Implementation sequence:

1. Delete the test-only in-agent event subscription adapter, legacy event
   envelope, and event-kind aliases. Tests subscribe to the canonical
   `pkg/events` bus directly.
2. Delete the producerless synthetic async-completion system-message reader.
   Current producers pass typed `AsyncCompletionInput` directly to the
   delivery coordinator.
3. Delete the channel-name ASR capability fallback. Voice-capable channels
   advertise ASR through `VoiceCapabilityProvider`; TTS remains derivable from
   the current `MediaSender` contract.
4. Delete the redundant `InjectSteering` and `InjectFollowUp` names plus the
   unsafe session-ambiguous `InterruptHard`; callers use `Steer` and
   `HardAbort(sessionKey)` directly.
5. Delete unused exported source aliases for Seahorse ingestion and Google
   schema sanitization; tests and callers use the canonical method names.
6. Delete ASR auto-discovery and the empty audio-channel default. Current
   configurations select one named voice model, and current audio producers
   supply their source channel explicitly.
7. Delete unstructured and multi-format channel allowlist matching. Every
   inbound producer supplies `SenderInfo`, and `allow_from` contains exact
   platform IDs or the explicit `*` wildcard.
8. Delete Seahorse startup schema upgrades and metadata backfill. Active
   databases have the current schema, and every conversation has a current
   canonical-history reconciliation watermark.
9. Delete frontend readers for the superseded detail-visibility preference,
   content-encoded tool calls, and task completion result. Consolidate session
   history media into the current attachment response at the API boundary.
10. Delete the completed launcher-token migration and the diagnostic-only
    boolean-origin state. The active launcher config has no token, and its
    SQLite credential store is initialized; runtime behavior depends only on
    the current effective config values.
11. Delete the gateway invocation JSON engine, startup importer, marker
    protocol, and downgrade exporter. The active SQLite database passes the
    current schema and integrity checks; runtime and tests use that one store,
    while rollback restores a matching binary and same-time SQLite backup.

Exit criteria:

- the zero-legacy audit below passes;
- no production branch selects a historical implementation based on the peer's
  release; bounded feature subsets come from current capability negotiation;
  and
- current third-party integrations are explicitly named and do not depend on
  internal compatibility shims.

### Z1 — Final zero-legacy audit

Depends on: all preceding packets

Audit every production match for:

```text
legacy
deprecated
backward compatibility
compatibility fallback
migration
alias
old schema
old version
```

Each surviving match must be one of:

- a current external protocol or import product;
- current operating-system or architecture portability;
- historical documentation under an archive; or
- a strict rejection message for an unsupported version.

No surviving match may:

- parse historical persisted state or select a historical runtime
  implementation; bounded wire peers may omit current optional fields;
- translate old state during normal startup;
- write both old and new representations;
- silently infer current meaning from an obsolete field;
- keep a deprecated API callable; or
- exist only to keep an old fixture passing.

The final report records deleted production lines, removed types and entrypoints,
the remaining current external integrations, full test and lint results, and
deployed cutover evidence.

## Validation For Every Code Packet

Each code PR must run:

1. focused tests for the changed package and contract;
2. race tests when lifecycle, registry, delivery, or concurrency changes;
3. integration tests for every crossed process or persistence boundary;
4. `make fmt` for changed Go files;
5. `scripts/pre-push-lint.sh --changed` or the broader required lint target;
6. the affected build and CLI smoke tests; and
7. a final diff audit that separates production deletion from new scaffolding.

A simplification PR is not successful when it only moves compatibility logic
behind a new interface. It should normally delete more production branches,
fields, conversions, or state transitions than it adds.

## Stop Conditions

Pause a packet and redesign it when:

- one current fact still needs two durable owners;
- removal requires a new general-purpose framework larger than the code being
  deleted;
- review fixes spread into two unrelated subsystems;
- security or delivery correctness is weakened to reduce line count;
- deployed current state has not been inspected before deleting its reader;
  or
- the first real web or IPC coding client appears before C1 merges.

The last condition does not automatically preserve the current coding
protocol. It triggers a fresh admission based on the real client's transport
and lifecycle requirements.

## Completion Definition

The roadmap is complete when:

- every first-party component implements the canonical internal contract and
  bounded wire peers interoperate through additive fields and capabilities;
- browser actions and capabilities have one definition and no historical
  schema machinery;
- tasks have one result model, one registry, one child runner, and one status
  tool;
- interactions reference the one delivery owner and the one continuation
  owner;
- `AgentLoop` composes explicit coordinators and has explicit turn entrypoints;
- the coding TUI consumes one in-process presentation store;
- configuration and sessions accept only their current formats;
- the zero-legacy audit passes; and
- the coordinated deployment and rollback procedure has been exercised with
  the current server and clients.
