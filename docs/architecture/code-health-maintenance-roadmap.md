# Code Health Maintenance Roadmap

Status: active. M0 admits the roadmap; M1-M6 are scheduled implementation
packets. M7 is an evidence-gated architecture checkpoint, not unconditional
refactoring work.

Baseline: `origin/main` at `477f29e9` on 2026-09-08. The H0-H8 and post-H8
programs are complete. This roadmap covers the smaller correctness and
maintainability risks found by a fresh audit of the resulting architecture.

## Objective

Keep MintClaw safe to extend without starting another repository-wide rewrite.
The work should make canonical session reads fail closed, remove one duplicated
tool-result settlement path, give reloadable lazy initialization an explicit
owner, finish the remaining stable typed boundary, and reduce two hidden
process/request dependency clusters.

Each mandatory packet must preserve user-visible behavior except where its
admission section names an intentional correctness fix. Large files, cognitive
complexity scores, or similar call shapes are supporting evidence only. New
dependency-injection containers, generic workflow engines, universal lifecycle
frameworks, and speculative compatibility layers are out of scope.

## Audit Evidence

### Correctness risk: critical session reads erase their own errors

`session.SessionStore` exposes context-aware, error-returning history reads but
retains convenience `GetHistory` and `GetSummary` methods. `JSONLBackend`
implements the convenience methods by logging storage failures and returning an
empty history or summary.

Turn admission uses those empty-on-error methods to capture the initial history
length and canonical rollback point. Inbound relation classification,
unanswered-session recovery, interaction resumption, and transcript repair also
use them to make control-flow decisions. A transient or corrupt-store read can
therefore be interpreted as an empty conversation instead of stopping safely.

### Maintainability risk: hook responses own a second settlement pipeline

`HookActionRespond` performs tool start publication, feedback, write-audit and
media ownership, terminal delivery settlement, journal persistence, loopguard
handling, workspace refresh, execution recording, suspension, halt, and
steering inside `admitToolCall`. Invoked tools perform the same responsibilities
later through `persistToolCallResult`.

The enclosing file is a high-churn runtime boundary. Changes to durability,
delivery, or loop control currently have to preserve two implementations whose
behavior has already begun to diverge.

### Lifecycle risk: `sync.Once` is reset outside its `Do` synchronization

The MCP and configured-hook runtimes reset lazy initialization by assigning a
new `sync.Once` while a different mutex protects result fields. Current gateway
reload normally quiesces turns first, but the runtime type does not encode that
precondition and initialization has several other callers. Safety therefore
depends on an external lifecycle convention rather than one owned state
transition.

### Typed-boundary gap: MintClaw client session provenance remains in `Raw`

The MintClaw channel writes `session_id` into `InboundContext.Raw`, and session
allocation reads the stable key to populate typed `SessionScope.ClientSessionID`.
This is a cross-package protocol rather than adapter-specific diagnostic data,
so it belongs in the typed inbound contract. Compatibility, if inventory proves
it necessary, belongs in one bounded spool-normalization reader.

### Hidden process dependency: credential passphrase lookup is global

Credential decryption and `SecureString.MarshalYAML` consult the mutable
package-global `credential.PassphraseProvider`. Configuration repository
behavior therefore depends on ambient process state, which complicates parallel
tests and prevents independently configured repository instances.

### Request dependency cluster: command runtime construction

`commands.Runtime` contains the root configuration plus a large set of
callbacks, and `AgentLoop.buildCommandsRuntime` constructs all command domains
in one high-complexity function. Only the MCP handler consumes the root config.
The callback boundary is useful, but adding a command currently expands one
request-scoped service locator and one central builder.

### Conditional hotspots: coding controller and frontend projector

`coding/controller.coordinate` and `coding/frontend.Projector` own dense but
real state machines. Their size is not sufficient evidence for another layer:
the controller intentionally serializes actor-owned transitions, while the
projector is in the middle of a staged semantic-cell migration. They receive a
post-feature checkpoint rather than an unconditional split.

## Guardrails

- Fail closed when canonical state required for admission, recovery, or
  rollback cannot be read.
- Keep one settlement path for one semantic tool result, independent of how
  the result was produced.
- Put reset, initialization, publication, and close transitions under one
  explicit lifecycle owner.
- Keep `InboundContext.Raw` for adapter-specific extension data, not stable
  facts interpreted by multiple packages.
- Resolve and encrypt credentials through an explicitly supplied repository or
  resolver dependency.
- Prefer small concrete capability groups over a generic service container or
  an interface per callback.
- Characterize externally visible behavior before moving state-machine code.
- Keep compatibility at one versioned reader and remove it only after its
  deployment or retention gate is proven.

## Delivery Packets

### M0: restore documentation truth

Scope:

- admit and link this roadmap from the current code-health architecture page;
- mark the implemented TUI.1-TUI.5 packets accurately while preserving the
  remaining staged migration plan;
- record each later packet's merge evidence and final disposition here.

Completion gate:

- current architecture pages point at the active maintenance plan;
- implemented TUI packets are not presented as future work;
- conditional work is clearly distinguished from scheduled work.

### M1: make canonical session snapshot reads fail closed

Intentional correction: a canonical storage read failure stops the dependent
operation instead of being reinterpreted as an empty session.

Scope:

- add a context-aware, error-returning read contract for the history and
  summary needed to capture one turn restore point;
- implement the contract for persistent and memory session stores without
  weakening existing snapshot ownership;
- migrate turn admission, canonical restore-point refresh, inbound relation
  classification, unanswered-session recovery, interaction resume/authority
  checks, and recovery setup where they make correctness decisions;
- keep convenience reads only for genuinely passive or administrative callers;
- emit actionable error context without exposing message contents or secrets.

Completion gate:

- injected read failures cannot start a turn with an invented empty history or
  create an empty rollback point;
- recovery and interaction paths do not silently skip work on read failure;
- existing history, rollback, recovery, and interaction behavior remains
  unchanged on successful reads;
- focused session and agent tests, race-sensitive tests, formatting, and lint
  pass.

### M2: unify tool-result settlement

Dependencies: M1, so the two high-churn agent changes do not overlap.

Scope:

- represent hook-produced responses as a typed execution outcome or source on
  the existing per-call state;
- keep admission, policy, approval, and invocation differences before the
  common result boundary;
- route hook and invoked results through one durability, delivery, media,
  loopguard, workspace-refresh, execution-record, suspension, and terminal
  settlement path;
- delete the duplicated hook-only finalization branch instead of wrapping it in
  a second abstraction.

Completion gate:

- hook and invoked results share one settlement implementation;
- characterization tests cover final-handled delivery, protected results,
  write audit, task suspension, loopguard halt, and steering;
- existing tool, hook, delivery, journal, and turn-terminal tests pass;
- production control-flow complexity decreases without a new workflow engine.

### M3: own reloadable lazy-initialization state

Dependencies: M2, because it also changes agent runtime lifecycle code.

Scope:

- replace resettable MCP and hook `sync.Once` values with a small explicit
  mutex-owned state or generation contract;
- make initialize, reset, result publication, error publication, and old
  resource close ordering visible in that contract;
- preserve retry and reload behavior and keep MCP and hook domain-specific
  loading outside the lifecycle primitive;
- add concurrent reset-versus-initialize tests under the race detector.

Completion gate:

- no used `sync.Once` is replaced or copied;
- reset and initialization cannot publish a stale generation;
- successful and failed reload behavior is preserved;
- focused agent tests and race tests pass.

### M4: type MintClaw client session provenance

This packet is independent after M1 and may proceed once no active PR owns the
same inbound contract files.

Scope:

- add the smallest typed client-session provenance field to `InboundContext`;
- populate it in the MintClaw channel and consume it in session allocation;
- preserve it through clone, normalization, durable spool serialization, and
  replay;
- inventory supported persisted inbound records before adding a compatibility
  reader; if required, read the legacy `Raw["session_id"]` key only during
  normalization and do not dual-write it.

Completion gate:

- current MintClaw ingress no longer writes or depends on the stable `Raw` key;
- typed provenance round-trips through the durable ingress path;
- non-MintClaw allocation behavior is unchanged;
- adapter, bus, session, and replay tests pass.

### M5: inject credential passphrase resolution

Scope:

- make the passphrase source an explicit dependency of credential resolution
  and configuration persistence;
- move environment lookup to process composition and keep repository instances
  independently configurable;
- perform secret encryption at the repository/security-document boundary
  rather than through ambient state hidden in generic YAML marshaling;
- preserve `enc://`, `file://`, missing-passphrase, and wrong-passphrase
  behavior and avoid persisting resolved plaintext accidentally;
- remove the mutable global after all production and test callers migrate.

Completion gate:

- parallel repositories can use distinct passphrase sources without shared
  mutable state;
- serialization has no implicit process-global passphrase lookup;
- resolver-isolation, config round-trip, secure-string, and web config tests
  pass;
- no plaintext credential appears in fixtures, diffs, logs, or persisted
  output unless the existing no-passphrase contract explicitly permits it.

### M6: compose command runtime capabilities

Scope:

- characterize commands across model selection, session lifecycle, MCP, goals,
  feedback, reload, stop, and context statistics;
- divide runtime construction into a few concrete domain capability builders or
  value groups while retaining request-scoped state;
- replace the command package's root `*config.Config` dependency with the
  narrow MCP availability fact it actually consumes;
- keep command registration, output, error text, and public behavior stable;
- do not introduce a reflection container, interface family, or generic
  dependency injector.

Completion gate:

- no command handler imports the root config solely through `Runtime`;
- adding a command in one domain does not require editing unrelated domain
  construction code;
- `buildCommandsRuntime` no longer owns every callback body;
- focused command and agent tests pass.

### M7: post-feature state-machine checkpoint

This is an evidence gate, not scheduled refactoring. Run it after the active
coding worker protocol and semantic TUI migration packets that touch the
controller or projector have settled on `main`.

Checkpoint:

- recompute churn and complexity and inspect bug-fix history for
  `Controller.coordinate` and `frontend.Projector`;
- identify whether one concrete subset of state has independent invariants and
  repeated cross-cutting edits;
- if proven, extract only that actor-owned coordinator state or stream rollback
  reducer with characterization tests;
- otherwise record a no-change decision and leave ownership intact.

Stop condition:

- if the proposed split requires a generic supervisor, event framework,
  duplicated state, cross-package interface family, or migration facade, do
  not implement it.

Completion gate:

- the evidence and change/no-change decision are recorded in this roadmap;
- any admitted extraction is delivered in its own PR with public behavior and
  concurrency tests preserved.

## Execution Order

1. M0 lands first as a docs-only PR.
2. M1, M2, and M3 land sequentially because they touch adjacent agent runtime
   boundaries.
3. M4 and M5 are otherwise independent but each starts from the latest merged
   `origin/main`.
4. M6 follows after the agent runtime packets so its characterization tests see
   the final lifecycle shape.
5. M7 runs only after the named coding feature churn settles; a documented
   no-change decision satisfies the packet when extraction is not justified.

Each non-stacked packet starts from the latest `origin/main`, uses a focused PR,
and runs formatting, changed-package lint, affected tests, and broader tests in
proportion to persistence, routing, delivery, or concurrency risk.

## Delivery Evidence

- M0 — merged in #1111 (`f4ad1954`): admitted this roadmap, linked it from the
  active code-health architecture page, and reconciled the implemented TUI
  milestones with the remaining migration plan.
- M1 — merged in #1113 (`50451481`): made canonical snapshot reads strict and
  context-aware across turn admission, rollback, recovery, relations, and
  interaction paths while retaining tolerant reads only for passive callers.
- M2 — merged in #1123 (`3ab777be`): replaced the duplicated hook-response
  state machine with a typed result source and one shared journaling, delivery,
  suspension, loopguard, and terminal settlement path.

## Separate Compatibility Closeouts

The following work remains deliberately outside this roadmap:

- node protocol v1 reader removal stays in its retention-gated PR and must not
  bypass transactional prune or zero-legacy production audits;
- the pre-F3 interaction projection reader remains until supported inbound
  spools have drained;
- the old TUI compatibility projection and renderer remain until the semantic
  migration's TUI.15 parity gate.

These are bounded deletion gates, not alternative steady-state architectures.

## Global Completion Criteria

The roadmap is complete when:

- every mandatory M1-M6 packet is merged and its evidence is recorded;
- canonical session read failures cannot masquerade as empty state;
- tool results have one settlement owner;
- reloadable MCP and hook initialization has one race-safe lifecycle owner;
- MintClaw client session provenance is typed at ingress;
- credential resolution no longer depends on mutable package-global state;
- command runtime construction is composed from narrow concrete capabilities;
- M7 records an evidence-backed change or no-change decision;
- no new framework or permanent compatibility representation was introduced
  solely to reduce file size or metric scores.
