# Architecture Simplification Roadmap

Status: active; implementation is merged through P1 and X3.60, the node
identity bridge is merged and deployed to the two local canaries, and the
remaining companion rollout, coordinated compatibility reset, deployment, and
Z1 remain open

Original audit baseline: `origin/main` at `f5c9afe9`, 2026-08-19

Re-audit baseline: `origin/main` at `d864bb7e`, 2026-08-24

## Current Execution Objective

Finish the simplification program with one canonical internal runtime and
persisted contract. Preserve only bounded additive current-plus-previous
first-party wire compatibility; remove historical readers and schema
generators, deprecated aliases, implicit old-state inference, duplicate
ownership, and service-locator layers. Preserve legitimate failover, product
aliases, and external protocols. Align prose instructions with the `AGENTS.md`
standard after separating personal profile metadata from prose, delete all
registered rollout debt at a coordinated reset, and complete the final audit,
explicitly authorized deployment, and rollback exercise.

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

At the original audit baseline, production also contained:

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

## Progress And Re-audit Evidence

Roadmap status uses three distinct meanings:

- **merged** means the implementation is present on `origin/main`;
- **deployed** means the active server and dependent first-party clients run a
  mutually compatible merged revision; and
- **reset complete** means temporary rollout adapters have also been deleted
  and the removal release has been deployed and verified.

Merging a packet does not by itself satisfy its deployed-state or compatibility
reset criteria.

| Packet | Merged evidence | Re-audit status |
| --- | --- | --- |
| S0 | #784 | Merged |
| B1 | #787 | Merged, but PR #865 later introduced temporary browser schema-generation debt |
| B2 | #788 and #790 | Merged; the canonical action contract remains, with PR #865 isolated at catalogue admission |
| T1 | #791, with correctness follow-ups #806 and #824 | Merged |
| T2a | #792 | Merged |
| T2b | #794 | Merged |
| D1 | #795 and #796 | Merged |
| D2 | #798 and #799 | Merged |
| A1 | #800 and #801, completed by X3.30-X3.42 | Merged |
| A2 | #808, #809, and #863 | Merged |
| C1 | #802; later coding work #836, #838, and #840 preserved the admitted boundary | Merged |
| X1 | #797, completed across the current-contract X3 packets | Merged; deployed config inspection passed |
| X2 | #803 and #807 | Merged |
| X3.1-X3.60 | #810-#816, #818-#823, #826-#835, #837, #839, #843, #845-#846, #848-#856, #858-#862, #864, #866, #868, #872, #878, #880, #885-#896 | Merged |
| P1 | #881 | Merged; deployed config and profile cutover remains in R1 |
| R1 node-identity bridge | #899 | Merged and deployed to local `p5a-canary` and `p3-canary`; remaining companion rollout and adapter removal remain in R1 |
| Z1 | Not yet applicable | Open |

The X3 item-to-PR mapping is: 1-7 to #810-#816; 8-13 to #818-#823;
14 to #826; 15 to #827; 16-23 to #828-#835; 24 to #837; 25 to #839;
26 to #843; 27 to #845; 28 to #846; 29-37 to #848-#856; 38 to #858;
39-42 to #859-#862; 43 to #864; 44 to #866; 45 to #868; 46 to #872;
47 to #878; 48 to #880; 49 to #885; 50 to #886; 51 to #887; 52 to #888;
53 to #889; 54 to #890; 55 to #891; 56 to #892; 57 to #893; 58 to #894;
59 to #895; and 60 to #896.

The 2026-08-24 read-only deployed audit established these rollout facts at
that time:

- the installed server remains at `1adb08f7`, so merged architecture work after
  that revision is not yet deployed;
- all five active configurations use the current Delta Chat and nested channel
  security shapes;
- all five active personal profiles have root `AGENT.md`; one extra root
  `AGENTS.md` is shadowed by the current personal-profile reader;
- 5,570 retained messages across the active stores omit `created_at`, while the
  current writer assigns it to new messages; and
- all six retained node-registry records omit `key_algorithm` and therefore
  still rely on the empty-value-to-Ed25519 reader.

The 2026-08-25 read-only R1 preflight superseded the installed-revision fact
above and refined the reset scope:

- the gateway fleet is healthy but split across two effective revisions: the
  main gateway runs `4ecd74c3`, while four other gateways still run
  `631aaff8` from deleted executable inodes; rollback must therefore capture
  the effective binary for each process rather than only the current file at
  its configured path;
- the sole connected browser-capable companion already runs `4ecd74c3` and
  advertises the complete current browser catalogue. A temporary package test
  proved that all six retained node identities deterministically normalize to
  Ed25519 and that their derived identifiers remain unchanged;
- current Ed25519 companion construction still omits `key_algorithm`; only the
  gateway-side reader supplies Ed25519. R1 therefore needs a bridge packet that
  makes companions emit the explicit field while the gateway still accepts the
  omitted form, followed by a fleet upgrade before that reader is deleted;
- the node registry contains four connected, one pending-pairing, and one
  revoked record. Two connected Linux companions are local canary services at
  `631aaff8` and `74998a25`; another connected Linux companion remains on
  `03b08be2`. They must be upgraded or deliberately retired before the gateway
  requires explicit `key_algorithm`;
- the five version 3 configurations describe 21 agents across 20 distinct
  personal workspaces. Every workspace has `AGENT.md`; the machine metadata to
  transfer comprises 21 names, 21 descriptions, 20 tool policies, and one MCP
  policy, with no deployed Markdown model or skill override. A copy-free
  conversion rehearsal produced version 4 documents that passed current
  strict decoding and validation for every profile;
- one spouse workspace already contains a different, currently shadowed
  `AGENTS.md`. The cutover must back up both documents and resolve that collision
  explicitly instead of silently overwriting or concatenating instructions;
- active task events use `task_event.v2`, outbox entries use version 2, and the
  active interaction registry uses the version 1 snapshot and event contracts.
  Session JSONL and metadata plus Seahorse SQLite remain current stores, so the
  structural inventory found no additional persisted-shape conversion gate;
  and
- the active profile trees occupy about 6.3 GiB while the filesystem has about
  7.5 GiB free. Fifty-one earlier backup directories occupy about 26 GiB, so R1
  requires a capacity gate and explicit authorization before moving or deleting
  any retained backup.

The authorized 2026-08-25 local bridge rollout completed the first two
companion upgrades and is recorded in
[R1 Local Node-Identity Bridge Rollout](../operations/architecture-simplification-r1-node-bridge.md):

- `p5a-canary` and `p3-canary` now run the exact `71ad3e53` node build and are
  connected as `v0.1.0-p8a.2-814-g71ad3e53`;
- the privileged P3 service helper is a same-release deployment unit with the
  P3 node. A node-only attempt failed closed while loading the helper snapshot,
  the previous pair was restored and verified, and the coordinated current
  node/helper upgrade then succeeded;
- both local nodes remained stable with zero service restarts and empty
  warning-level journals after the final start, while the gateway fleet,
  profiles, and registry records were not manually changed;
- the connected Darwin `ab-local-test` companion remains at `4ecd74c3`, the
  connected Linux `vpn` companion remains at `03b08be2`, and the older pending
  Darwin and revoked Linux records still require an explicit upgrade,
  conversion, retirement, or removal decision; and
- all six persisted registry records still omit `key_algorithm`, including the
  two upgraded local peers. The external record conversion in R1 therefore
  remains mandatory before the empty-algorithm reader can be deleted.

### Re-audit corrections

The original keyword inventory was useful for discovery but too broad as an
architectural rule. `alias`, `fallback`, and `compatible` also name legitimate
current concepts: configured model and node aliases, provider failover, default
values inside one current contract, and external OpenAI-compatible APIs. They
must not be removed merely because of their spelling.

The deciding question is whether a branch preserves a historical MintClaw
representation or creates a second owner for a current fact. Z1 therefore uses
semantic classification in addition to keyword search.

The re-audit also found that `AGENTS.md` must not be described generically as a
legacy file. It is the [cross-tool coding-instruction convention](https://agents.md/)
supported by [Codex](https://learn.chatgpt.com/docs/agent-configuration/agents-md),
[Jules](https://jules.google/docs/), GitHub Copilot, and other coding agents,
and MintClaw's coding runtime already discovers it hierarchically. MintClaw's
singular `AGENT.md` is a separate PicoClaw-derived personal profile manifest
with typed frontmatter. Packet X3.44 removed a dual reader at that
personal-profile boundary; packet P1 below owns the final metadata/prose
separation and standards alignment.

### Open compatibility and simplification debt

| Debt | Classification | Current evidence | Owner and removal gate |
| --- | --- | --- | --- |
| Previous and older browser catalogue schemas | Temporary first-party wire adapter | PR #865 reconstructs the previous streamed-snapshot schema and retains the earlier session-open output schema after a companion rollout failed; the sole connected browser-capable companion now advertises the current catalogue | Architecture simplification owner; verify the current companion remains connected through cutover, then delete the historical generators in R1 |
| Empty node `key_algorithm` | Temporary first-party wire and persisted-state adapter | PR #899 makes current Ed25519 companion construction emit the explicit field; local `p5a-canary` and `p3-canary` run that bridge, but all six retained records omit the field and the connected Darwin `ab-local-test` and Linux `vpn` peers remain on pre-bridge builds | Architecture simplification owner; upgrade or retire the remaining connected companions, convert every retained record or deliberately remove it, then require the field in R1 |
| Deployed version 3 personal profiles | Coordinated persisted-config and workspace cutover | PR #881 makes version 4 config the sole machine authority and root `AGENTS.md` the sole personal prose file; five deployed configs, 21 agents, and 20 personal workspaces still use the pre-cutover shape | Architecture simplification owner; convert and validate every configured agent and distinct workspace while stopped in R1 before installing the version 4 binary |

The current implementation also has one non-blocking observability follow-up,
not a compatibility adapter: an exact deny rule can be reported as an unknown
tool or MCP server after policy filtering removes the denied capability from
the discovered catalogue. A later agent-policy diagnostics packet should make
configured denies quiet while continuing to warn about genuinely unknown
allow or deny patterns.

PR #885 classifies the scope-less tool-feedback lookup as obsolete inference
left behind after session-stable ownership replaced the older turn-scoped
design. It deletes the active-entry scan; cleanup now requires the same
explicit session or trace identity used by publication.

PR #886 deletes the unused `ToolRegistry.SetAllowlist` API, its registration
filter, clone state, and discovery-tool exception. Typed
`AgentCapabilityPolicy` is the sole production owner of per-agent tool
authorization; the generic registry owns only catalogue membership and
lifecycle.

PR #887 deletes the unused exported `SaveConfig` alias and its private wrapper.
`Repository` is the sole config-persistence owner. Its unconditional `Save`
operation remains current at the explicit OpenClaw import and temporary
MCP-edit boundaries, while managed config mutation continues to use `Update`
or revision-checked `Replace`; tests use the same repository contract through
package-local setup helpers.

PR #888 deletes the caller-free `Manager.SendToChannel` API and its direct
transport fallback. Active synchronous and queued delivery continue through
the delivery runtime's canonical worker, which owns rate limiting, retries,
ordering, tool feedback, and outcome publication.

PR #889 deletes the caller-free Ed25519-only `IdentityProof.Verify` wrapper.
Node admission and current tests use the algorithm-aware `VerifyIdentity`
contract; Ed25519 remains supported, and the empty-algorithm wire and storage
adapter remains isolated for the coordinated R1 cutover.

PR #890 deletes six exported OpenClaw import-model helpers with no production
callers and the private provider type used only by that facade. The explicit
`LoadOpenClawConfig` to `ConvertToMintClaw` path remains the sole typed import
contract and continues to read channel, provider, and agent fields directly.

Other matches remain candidates, not automatic deletions. The final audit must
prove whether raw outbound metadata, benchmark baselines, and stale `Legacy`
names express required current semantics or obsolete compatibility.

The 2026-08-25 source-only pre-R1 audit found no additional unregistered
compatibility adapter:

- a Git-tracked zero-caller scan after PR #896 found no further caller-free
  production facade; the remaining low-reference exports are active entry
  points, documented extension contracts, or deliberate test and integration
  seams;
- the historical browser schema generators and empty node-key-algorithm
  normalization remain the only source-level historical MintClaw
  implementations, and both already have R1 removal gates above;
- task deliverable normalization constructs the current canonical report at
  the registry owner for both new mutations and loaded snapshots; it does not
  select a historical report implementation, but R1 must still inventory the
  deployed task registry before rollout rather than assuming its shape; and
- the other production keyword matches are benchmark baselines, external
  provider or platform protocols, provider failover and current defaults, or
  strict rejection of removed inputs. A few current helper names and comments
  still use `legacy` descriptively; Z1 must rename that stale terminology or
  justify it without preserving an alternate path.

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

Depends on: T2b and D2

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

- make user turns, child turns, interaction resumes, coding turns, and recovery
  select one explicit current turn mode;
- normalize that mode once into a small internal turn specification;
- remove boolean combinations that describe modes indirectly; and
- make invalid combinations unrepresentable or rejected at construction.

Tests:

- a table covers every admitted entrypoint and its normalized semantics;
- no mode depends on an omitted boolean's historical default; and
- ordering hooks such as turn readiness belong to one explicit phase.

Exit criteria:

- callers select a turn mode instead of constructing a boolean bag; and
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
3. Retain the one selected `turnMode` on the normalized specification and
   derive command dispatch, scheduled agent projection, and initial steering
   polling from it. Delete the three independent booleans that could disagree
   with that mode.
4. Retain only fields whose values genuinely vary within a mode, plus
   value-bearing data such as the caller's empty-response text. Do not add
   separate request wrappers or another turn-policy object.

### C1 — Collapse the in-process coding frontend protocol

Depends on: A1

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

Downstream P4 integration rules:

- the resume picker is a separate pre-controller catalogue screen with one
  replaceable bounded page; it does not extend `ThreadSnapshot`, introduce a
  reducer, or treat discovery observations as write authority;
- selected threads still pass through canonical metadata reload, project
  validation, and OS-backed lease acquisition before an active presentation
  store is constructed; and
- in-app commands consume the subscribed current view and the typed controller
  sink instead of recreating command-specific mirrors or a delta protocol.

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
12. Delete the pre-`kind` thought boolean from MintClaw Protocol reads and
    writes. Current gateways, the web frontend, CLI, and client channel use the
    typed `kind: "thought"` discriminator as the single message contract.
13. Delete arbitrary-turn `GetActiveTurn` and `InterruptGraceful` APIs. Command
    runtimes bind active-turn inspection to their known workspace and session;
    graceful interruption is session-scoped and fails closed on ambiguity.
14. Make skill registries one name-keyed current contract. Delete the sibling
    GitHub settings, list-shaped registry input, direct security entries,
    merge/PATCH fallbacks, and the JSON-first metadata parser; configuration,
    security overlays, and runtime lookup use the same registry map.
15. Make cron mutations one error-returning current contract. Delete implicit
    legacy defaults and ensure failed persistence cannot leave an in-memory job
    mutation committed.
16. Make configured `model_name` the sole model selector identity. Reject
    dangling static references during config load, reject unknown workspace
    frontmatter at agent construction, and delete raw model-ID,
    `provider/model`, provider-alias, and default-provider inference from agent
    resolution and gateway restart signatures.
17. Require every model entry to store one explicit provider plus its
    provider-native model ID. Delete steady-state Web API normalization,
    provider-prefix/default-provider inference, and the unused fallback parser
    APIs that preserve those alternate representations.
18. Make the canonical descriptor the sole source of optional provider
    capabilities and the descriptor-based image interface the sole image
    operation. A provider without an optional descriptor advertises no optional
    features; runtime code no longer infers streaming, thinking, search, or
    image support from legacy method shapes.
19. Make event streaming the sole provider streaming contract. Delete the
    accumulated-text operation, its wrapper adapters, and the event-mode flag;
    providers declare one streaming boolean and emit `StreamChunk` values
    directly.
20. Make typed text delivery the sole channel text contract. Delete the
    tuple-returning channel method and optional typed sender; every channel
    returns confirmed IDs, retryable remainder, acceptance, and errors through
    `DeliveryResult`.
21. Make typed media delivery the sole optional channel media contract. Delete
    the tuple-returning media method and optional typed fallback; every
    media-capable channel returns confirmed IDs, retryable remainder,
    acceptance, and errors through `DeliveryResult`.
22. Make `tools.image_generate.model` the sole image-generator selector.
    Delete the unused agent-default image fallback fields and the cross-section
    runtime fallback; removed fields are rejected by current-schema decoding.
23. Make `model_list[].enabled` the sole model-activation contract. Delete
    API-key and `local-model` inference, persist the boolean explicitly, and
    carry it through every model creation and multi-key expansion path.
24. Make `session.dimensions` the sole persisted session-partition contract.
    Delete the bidirectional scope synchronization machinery, keep only a
    one-release Web boundary adapter, and remove that adapter at the next
    coordinated compatibility reset.
25. Make JSON string arrays the sole persisted list shape. Delete the
    `FlexibleStringSlice` scalar, mixed-array, and custom text decoders; retain
    friendly scalar normalization only at the current Web request boundary and
    use the standard environment parser for comma-separated overrides.
26. Make the node registry validate stored command schemas directly. Persist
    compact current JSON at the writer, delete load-time command-schema
    compaction and browser-schema reconstruction, and reject catalogs whose
    authority-bearing descriptors no longer match the current contract.
27. Make MCP configuration one explicit current contract. Require exactly one
    of `stdio`, `http`, or `sse`, reject fields from another transport, delete
    transport aliases and inference, and make every interrupted session-loss
    call uncertain after reconnect without configurable replay.
28. Delete unused compatibility helper APIs and implicit internal call modes.
    Remove the uncalled browser click-effect and Web local-IP aliases, and make
    every Seahorse leaf-compaction caller select its current force mode
    explicitly instead of relying on a variadic default.
29. Scope runtime profiles to the coding frontend that actually uses their
    isolated roots and restart-only lifecycle. Delete the unused personal
    profile path, its policy switches, and its storage-cutover plan; keep the
    current hot-reloadable gateway workspace lifecycle explicit.
30. Delete the single-implementation `turnRuntimeHost` callback interface and
    its nil-safe pass-through wrappers. The turn runner retains process and
    spool-settlement lifecycle; in-turn abort, steering, filtering, events,
    and final rendering use the immutable pipeline snapshot directly.
31. Flatten `PipelineRuntimeServices` into direct pipeline-owned dependencies.
    Keep narrow bus and event interfaces where alternate implementations
    exist; use concrete active-request and abort owners, and delete their
    single-implementation interfaces and test-only `AgentLoop` pass-throughs.
32. Keep the semantic `PipelineContextServices` grouping, but replace its
    background-compaction, model-execution, and terminal-task interfaces with
    concrete runtime-generation owners. Preserve the context-runtime,
    steering, and media seams because they have real substitutes, and delete
    model-execution `AgentLoop` pass-throughs made unused by the concrete owner.
33. Keep the semantic `PipelineInteractionServices` grouping, but make fallback
    execution and asynchronous tool completion concrete. Delete their
    single-implementation interfaces and the optional fallback-observer type
    assertion; preserve interaction seams with real test substitutes.
34. Confirm that inbound coordination is already constructed once per
    `AgentLoop.Run` and remains correctly run-scoped. Delete the test-only
    `AgentLoop` reasoning component factory and pass-through façade; test the
    pipeline-owned reasoning component directly.
35. Make constructor-owned active-request, background-compaction, and
    model-execution components explicit. Delete their lazy `AgentLoop`
    accessors, the runtime-event adapter factory, and the unused synchronous
    delivery factory/pass-through; preserve nil handling at actual call
    boundaries without creating alternate owners.
36. Complete the planned Web session-contract compatibility reset. Delete the
    one-release `session.dm_scope` response alias and PUT/PATCH translator;
    emit and accept only `session.dimensions`, with removed fields rejected by
    current-schema validation.
37. Replace the pointer-backed `tool_feedback.subagents` compatibility default
    with the current boolean contract. Keep the default explicit in
    `DefaultConfig`, apply the same defaults at file and API decode boundaries,
    preserve explicit `false` during serialization, and remove the runtime nil
    fallback.
38. Make `bus.Streamer` the sole channel streaming interface. Delete the
    `channels.Streamer` source alias retained for older implementations and
    update every current channel and fixture to name the canonical owner.
39. Make one concrete `turnRuntime` own admission, active session and route
    claims, pending stops, request tracking, sequencing, inbound spool
    settlement, and runner generations. Delete the corresponding mutable
    `AgentLoop` fields and runner/admission pass-throughs, and remove the unused
    context API that exposed `AgentLoop` as a child-tool service locator.
40. Make each `turnRunner` generation construct and retain one reasoning,
    feedback, synchronous delivery, asynchronous delivery, and interaction
    component set. Give `Pipeline` those same instances, route host-side work
    through the current runner owner, and delete the `AgentLoop` factories and
    delivery pass-throughs that rebuilt equivalent components per call.
41. Extract one process-wide `interactionCoordinator` to own the interaction
    registry cache, resolution callbacks, resume-flight deduplication,
    workspace catalog serialization, and recovery admission. Delete those six
    mutable fields from `AgentLoop`, and make every reloadable interaction
    component reference the same stable owner.
42. Extract one process-wide `taskCoordinator` to own task registries,
    restart reconciliation, terminal-task context, durable delivery state, and
    async-completion admission. Make it depend directly on the interaction
    owner, delete the two remaining task/completion maps from `AgentLoop`,
    replace the pipeline's terminal-task `AgentLoop` service locator, and
    remove its three task-state callbacks, config callback, and duplicate
    user-delivery callback.
43. Make `TaskPrompt` the sole child-turn instruction field. Delete the unused
    `ActualSystemPrompt` shadow, its producerless runtime override and prompt
    source, and the historically inverted `SystemPrompt` name from both sides
    of the in-repository tool/agent boundary.
44. Remove the dual reader from the MintClaw-specific personal workspace
    profile boundary. Make root `AGENT.md` the sole immediate structured
    personal definition, delete the personal `AGENTS.md` and `IDENTITY.md`
    readers, the definition-source enum, and source-dependent prompt and cache
    branches, and translate the current OpenClaw import product's personal
    `AGENTS.md` at that explicit import boundary. A read-only deployed audit
    confirmed that all five active profiles already have root `AGENT.md`; an
    extra root `AGENTS.md` was shadowed and unused. This packet does not
    classify the standard as legacy: project and skill-level coding
    `AGENTS.md` files remain the current cross-tool instruction contract. P1
    owns the later separation of profile metadata from standard prose.
45. Make the Delta Chat account store the sole owner of mailbox credentials.
    Delete MintClaw's password, IMAP, and SMTP settings plus its account
    configuration and drift-reconciliation paths. Full email addresses must
    identify an already configured account; the explicit chatmail bootstrap
    flow remains the current account-creation path.
46. Require timestamp evidence before classifying inbound media as an adjacent
    follow-up. A missing or zero `created_at` remains readable history but does
    not prove recency. Delete the redundant current-turn relation aliases,
    input wrapper, and second classifier layer so one canonical relation type
    and classifier own the decision.
47. Make prompt-source registration fail closed. Register every current static
    and dynamic contributor before collection, reject an unknown source instead
    of warning and accepting it in compatibility mode, and retain placement
    validation on the same registry owner.
48. Remove historical objective-result inference. A successful child outcome
    must carry the current bounded `result`; do not reinterpret an old
    `explanation` as terminal output merely because a receipt exists. Reconcile
    this packet with the focused objective-receipt accounting work before
    changing the shared parser.
49. Delete scope-less tool-feedback target inference. Publication, pause,
    terminalization, and cleanup use one explicit session key or trace scope;
    missing identity must not scan concurrent coordinator state and guess the
    only currently active scoped carrier.
50. Delete the unused generic tool-registry allowlist. Per-agent authorization
    remains at the typed agent-policy boundary; the registry no longer owns a
    second policy map, registration branch, clone path, or discovery-tool
    exception.
51. Make `Repository` the sole config-persistence owner. Delete the unused
    exported `SaveConfig` alias and its private wrapper, retain unconditional
    `Save` only at explicit import and temporary-config boundaries, migrate
    test setup to the repository contract, and simplify enforcement to reject
    only direct config-file mutation outside the repository package.
52. Delete the unused `Manager.SendToChannel` entrypoint and its private direct
    transport fallback. Keep synchronous and queued delivery on the canonical
    per-channel worker so no callable path bypasses its rate limiting, retries,
    ordering, tool-feedback, or outcome-publication ownership.
53. Delete the unused Ed25519-only `IdentityProof.Verify` compatibility
    wrapper. Keep `VerifyIdentity` as the sole algorithm-aware proof verifier;
    this callable API cleanup does not remove Ed25519 or the separately tracked
    empty-algorithm wire and storage adapter before R1.
54. Delete the zero-caller OpenClaw import-model helper facade: its generic
    enabled, directory-load, provider, channel, allow-list, and agent helpers
    plus their facade-only tests. Keep the explicit source-path loader and
    typed `ConvertToMintClaw` conversion as the one import boundary.
55. Delete the zero-caller OpenAI-compatible provider constructor wrappers and
    provider-name option. Keep `NewProvider` with functional options as the
    sole constructor, retain direct option coverage, and keep the factory's
    current `SetProviderName` path instead of a second configuration API.
56. Make image-generation provider resolution one direct current dependency.
    Delete the unused exported resolver type and option plus the per-tool
    callback field. When no concrete provider is injected for testing, lazily
    call the canonical provider factory once on first execution.
57. Delete the caller-free `pkg/agent/adapters` forwarding package. Concrete
    message-bus and channel-manager owners already implement the retained
    agent interfaces directly, and tests use real substitutes; the two wrapper
    types add a second implementation layer without providing substitutability.
58. Delete the caller-free turn and tool-feedback helper facades: the exported
    accessor returning a private turn-state type, the superseded feedback
    finalization classifier, and the no-title working-summary wrapper. Keep the
    private context owner, live dismissal classifier, and title-aware renderer.
59. Delete caller-free convenience wrappers at external integration
    boundaries: contextless Anthropic usage, default-option browser login,
    provider-specific Groq transcription, pre-buffered HTTP error normalization,
    default self-update, and tag/nightly release URL lookup. Keep the
    context-aware, configurable, and canonical factory entry points used by
    current callers.
60. Finish the tracked zero-caller utility cleanup. Delete unused package-level
    logger variants and console-level setter, adaptive-any host helpers, the
    Discord voice-receive predicate, the Web language getter, and two LOCOMO
    selectors. Retain documented channel-registry discovery, the console
    restoration test seam, active loopback helpers, and third-party logger
    interface methods.

Exit criteria:

- the zero-legacy audit below passes;
- no production branch selects a historical implementation based on the peer's
  release; bounded feature subsets come from current capability negotiation;
  and
- current third-party integrations are explicitly named and do not depend on
  internal compatibility shims.

### P1 — Separate personal profile metadata from standard instructions

Depends on: X3.44

Scope:

- make the current agent configuration the sole owner of machine-interpreted
  identity, model, skill, tool, and MCP policy fields;
- make standard Markdown `AGENTS.md` the prose instruction document rather
  than embedding a MintClaw-only configuration schema in it;
- preserve `SOUL.md` and `USER.md` as explicitly MintClaw-specific personal
  context layers;
- keep personal-profile roots and coding-project instruction roots explicit in
  their respective runtime profiles so identical filenames do not imply shared
  authority; and
- advance the sole accepted config schema to version 4 so a version 3 profile
  cannot start after silently losing Markdown-owned identity or deny policy;
  the coordinated profile conversion writes all current fields before the new
  binary is started; and
- cut every configured agent and distinct personal workspace in the five
  deployed profiles over once, delete the singular `AGENT.md` parser and
  frontmatter ownership, and avoid a steady-state dual reader.

Tests:

- agent identity and policy resolve from one current configuration owner;
- personal prose and hierarchical coding-project instructions load only in
  their admitted runtime scopes;
- an unknown machine field in Markdown cannot affect runtime authority; and
- the OpenClaw importer preserves prose at its explicit import boundary without
  adding another runtime format.

Exit criteria:

- `AGENTS.md` has standard prose semantics;
- one typed config contract owns every machine-interpreted profile field;
- version 3 configuration is rejected rather than interpreted under version 4
  authority rules;
- no runtime reads personal `AGENT.md` or parses Markdown frontmatter as
  authority; and
- deployed personal workspaces contain the current prose filename before a
  binary without the old reader is installed.

Implemented shape:

- PR #881 makes config version 4 the sole owner of personal identity, model,
  skills, tool policy, and MCP-server policy;
- root `AGENTS.md`, `SOUL.md`, and `USER.md` are loaded as prose without
  machine-interpreted frontmatter;
- the runtime no longer reads root `AGENT.md` or `IDENTITY.md`, and onboarding
  preserves existing workspace prose while creating missing templates; and
- merge completion does not satisfy the deployed cutover criterion: the five
  active configs, their 21 agent entries, and their 20 distinct personal
  workspaces still require the stopped-service R1 conversion before this
  binary may be deployed.

### R1 — Execute the coordinated first-party compatibility reset

Depends on: P1 and X3.46-X3.60

Deployment requires explicit user authorization.

Scope:

1. satisfy the capacity gate, then back up the current configuration, the
   effective running binary for every process, all personal Markdown, and all
   MintClaw durable state, explicitly including sessions, tasks, interactions,
   outbox, cron, node registry, browser state, media indexes and assets, and
   invocation state; do not assume that the file currently present at a
   configured executable path matches a process running a deleted inode;
2. treat the PR #865 browser bridge cycle as satisfied for the sole connected
   browser-capable companion and verify that it remains on the current
   streamed-snapshot contract;
3. stage the merged PR #899 node-identity bridge release, which makes Ed25519
   companions send explicit `key_algorithm` while the gateway still accepts
   the omitted form, then upgrade or deliberately retire every older connected
   companion;
4. convert every retained node record to explicit Ed25519 or deliberately
   remove it, and verify every connected companion sends `key_algorithm`;
5. perform the P1 cutover for all 21 configured agent entries and 20 distinct
   personal workspaces, resolve the one pre-existing `AGENTS.md` collision
   explicitly, and validate every active profile before restart;
6. delete previous and older browser schema generators, empty-algorithm
   normalization, expired wire aliases, and their old fixtures in one removal
   release; and
7. deploy and verify the removal release, then exercise rollback using the
   matching binary and same-time state backup.

Exit criteria:

- every registered first-party peer uses the current protocol major and current
  authority-bearing capabilities;
- no production code reconstructs an older browser catalogue;
- every persisted and wire node identity names its key algorithm;
- no pre-reset personal-profile reader remains; and
- deployed verification and rollback evidence are recorded in the final audit.

### Z1 — Final zero-legacy audit

Depends on: all preceding packets, including R1

Audit every production match for:

```text
legacy
deprecated
backward compatibility
compatibility fallback
migration
old schema
old version
```

Also search semantically for historical-schema generators, omitted-field
normalization, version-selected implementations, dual readers or writers,
deprecated callable entrypoints, and names such as `previous*Schema`. Search
for `alias` and `fallback` as discovery aids, but do not treat a match as debt
until its semantics are classified.

Each surviving match must be one of:

- a current external protocol or import product;
- a current product concept such as configured aliases, provider failover, or
  a documented default inside the sole current contract;
- additive first-party wire compatibility inside the current rolling window,
  with no historical implementation and with any temporary adapter registered
  with an owner and removal gate;
- current operating-system or architecture portability;
- a development benchmark whose historical baseline is the behavior being
  measured rather than a product input contract;
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
- typed configuration owns personal profile metadata while standard
  `AGENTS.md` owns prose instructions;
- the compatibility-debt register is empty after the coordinated reset;
- the zero-legacy audit passes; and
- the coordinated deployment and rollback procedure has been exercised with
  the current server and clients.
