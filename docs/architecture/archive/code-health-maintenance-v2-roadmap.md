# Code Health Maintenance V2 Roadmap

Status: complete. R0-R3 delivered the admitted documentation, ownership, and
compatibility work. R4 was intentionally skipped because none of its admission
triggers appeared through R3 completion.

Audit baseline: `origin/main` at `8ca7d2487` on 2026-09-21. The comparison
window starts at the archived recent-merge reliability closeout `cc844ebde` on
2026-09-17.

Implementation completion head: `origin/main` at `5b9f8b6c5` on 2026-09-22.

## Objective

Keep MintClaw reliable after the recent PDF, browser, coding, skills, and
context work without starting another repository-wide rewrite. The admitted
work should converge one duplicated document-delivery transition boundary,
give mutable coding model and reviewer state one lifecycle owner, and finish
bounded compatibility readers only when deployed-state evidence makes removal
safe.

This roadmap is complete when every mandatory packet is merged and the
conditional packet has either met its trigger and been delivered or has a
recorded no-change decision. File size, churn, or an abstract complexity score
is never sufficient admission evidence.

## Audit Scope And Evidence

The four-day comparison window contains 391 changed files, 34,085 additions,
and 2,835 deletions across 191 commits. Of those additions, 14,848 are
production Go, 11,805 are tests or test data, and 5,106 are documentation.
The largest growth clusters are the agent runtime, document support, node
companion, coding frontend and worker, browser support, and skills.

The current head passed the repository lint suite and focused document, tools,
browser, coding, skills, bus, companion, and live-evidence tests during the
audit. The GitHub build for the audited head was also green. A broad local
agent-package run reached its ten-minute timeout while another agent and the
linter were exercising the same checkout and disk. That contaminated timing is
not an admitted performance defect; test optimization requires an isolated CI
timing baseline first.

### Document delivery has more than one transition bridge

The document write journal and protected form-job store intentionally retain
different state machines. A write operation owns artifact production and
outbox delivery evidence. A form job additionally owns protected answers,
review, approval, and the user-visible conversational lifecycle.

The adapter boundary currently translates delivery outcomes in several
places:

- direct form fill constructs commit and settlement closures;
- conversational form commit constructs a second pair and additionally
  settles the form job;
- live resume maps write terminal state back to form terminal state; and
- recovered outbox admission and settlement repeat the same mapping.

Recent delivery fixes covered replay, terminal status, expired recovery, and
single settlement. The risk is not the number of states; it is that one outbox
fact can be translated differently by live and recovered paths. R1 admits one
concrete bridge at the existing tools/document boundary. It does not admit a
generic workflow engine or replacement of either durable store.

### Coding runtime has a mutable model-session ownership cluster

The local coding composition root currently combines AgentLoop construction,
turn and interaction control, model/provider/reasoning selection, reviewer
construction and close, metadata persistence, repository evidence, transcript
projection, and frontend controller adaptation. These are not all one
lifecycle.

The actionable cluster is narrower: selected model, provider, reasoning,
reviewer, retained reviewer provider, runtime status, persistence ordering, and
close behavior change together under one mutex. New model, reasoning, skill,
and yolo work all cross this cluster. R2 admits one concrete model-session
owner after overlapping P7.7 work is reconciled. It does not admit a dependency
injection container, generic runtime host, or mechanical file split.

### Compatibility readers have explicit but unfinished removal gates

Inbound spool normalization still hydrates pre-M4 MintClaw client-session
provenance and pre-F3 interaction projections. The companion invocation ledger
can still migrate its previous persisted record schema. Each reader is bounded
and fail-closed, so its presence is not a current correctness defect.

The debt is the unfinished evidence gate. R3 must inventory every maintained
deployment, pending inbound spool record, and companion ledger before removing
anything. A nonzero or unavailable inventory closes the packet with the reader
retained and a concrete retention or deployment gate. It must not reinterpret,
drop, or blindly rewrite durable no-replay evidence.

### Live-evidence correlation is complex but remains acceptance tooling

The live-evidence collector can reconstruct delegated continuation traces from
workspace, turn, agent, redacted session, and bounded time evidence. Recent
fixes made ambiguous matches fail closed. It does not own production execution
or delivery.

R4 is conditional. Another correlation defect, a second runtime consumer, or a
requirement to use this evidence for a production decision admits an explicit
trace-lineage field and exact lookup. Without one of those triggers, adding a
new trace schema would cost more than the acceptance-only heuristic it replaces.

## Guardrails

- Preserve the write journal, form-job store, outbox, and channel delivery as
  separate authorities with one explicit translation boundary.
- Keep settlement idempotent and bind every transition to the exact operation,
  owner, artifact, and delivery identity.
- Persist a coding model selection before publishing it to the live runtime;
  failed preparation or persistence must leave the previous selection usable.
- Give every retained provider and reviewer exactly one close owner.
- Remove compatibility only from a zero-legacy supported fleet; absence of
  evidence is not evidence of absence.
- Keep active shared-skills, unified-context-cache, and P7.7 product contracts
  authoritative. This roadmap must not redesign their behavior.
- Do not optimize tests from a contended local wall-clock observation. Capture
  isolated package timings and repeated CI evidence first.
- Do not introduce generic workflow, lifecycle, supervisor, service-locator,
  or dependency-injection frameworks.

## Delivery Packets

### R0: publish the maintenance contract

Scope:

- publish and index this roadmap;
- mark the preceding maintenance roadmap as completed historical work;
- record mandatory, conditional, and excluded scope; and
- establish the ordered merge and stop criteria for later packets.

Completion gate:

- current architecture documentation names exactly one active code-health
  maintenance roadmap;
- every later packet has a concrete trigger and bounded acceptance criteria;
- conditional work cannot silently become mandatory; and
- local documentation validation passes.

### R1: converge document delivery settlement

Scope:

- introduce one concrete settlement translation and coordinator at the
  existing tools/document boundary;
- use it for direct fill, conversational form delivery, live resume, recovered
  admission, and recovered terminal settlement;
- retain the two durable state machines and existing outbox authority; and
- remove duplicated status-to-state and write-to-form mappings made obsolete
  by the coordinator.

Completion gate:

- delivered, definitely failed, ambiguous, pending, abandoned, repeated, and
  conflicting-identity cases have table-driven characterization tests;
- live and recovered paths produce the same terminal transitions;
- direct fill does not acquire a synthetic form-job dependency;
- no delivery can settle a different owner, operation, artifact, or outbox
  identity; and
- document, tools, agent delivery, lint, and CI checks pass.

### R2: own coding model-session lifecycle

Dependency: R1 is merged. Start only after refreshing from `origin/main` and
reconciling merged P7.7 changes that touch the same composition root.

Scope:

- extract one concrete owner for selected model, provider, reasoning profile,
  reviewer, retained provider resource, and their runtime status;
- make selection a prepare, persist, swap, publish sequence with explicit
  rollback and close ownership;
- let turn execution and review take immutable snapshots from that owner; and
- leave AgentLoop, controller operation serialization, metadata storage, and
  frontend projection in their existing domains.

Completion gate:

- failed provider/reviewer construction or metadata persistence preserves the
  previous usable runtime state;
- successful replacement closes the previous retained provider exactly once;
- close racing with status reads or an admitted operation cannot publish a
  partially replaced selection;
- resumed, interactive, exec, review, model, and reasoning behavior remains
  unchanged; and
- focused coding lifecycle tests, broader coding and agent tests, lint, and CI
  pass.

### R3: close bounded compatibility with deployment evidence

Status: complete. The deployed audit proved zero pre-M4/pre-F3 records across
all maintained gateway spools, so those hydration readers were removed behind
fail-closed spool rejection. Two maintained companions still retained version
1 invocation ledgers, so that transactional migration remains in place behind
an explicit upgrade-and-audit gate.

Dependency: R2 is merged. The audit may produce removal or a documented
retention decision; both are valid only when backed by current evidence.

Scope:

- inventory maintained gateway inbound spools for pre-M4 client-session and
  pre-F3 interaction raw keys;
- inventory every maintained companion invocation ledger version and pending
  migration state;
- inspect configured retention and offline-node coverage;
- remove a reader and its legacy-only tests only when inventory is zero across
  supported deployments; and
- otherwise record the exact remaining deployment, record count or retention
  gate and keep the reader unchanged.

Completion gate:

- the audit commands, redacted results, date, deployed build, and decision are
  recorded in operations evidence;
- any removal has focused legacy-rejection/current-format tests and a rollback
  note;
- no pending or retained no-replay evidence is discarded; and
- affected bus, node, recovery, lint, and CI checks pass when code changes.

### R4: conditionally replace heuristic trace correlation

Status: intentionally skipped. No admission trigger appeared between the
audited baseline and R3 completion.

Trigger requires at least one of:

- another confirmed live-evidence correlation defect after the audited head;
- a second independent consumer of delegated continuation lineage; or
- a production admission, recovery, billing, or delivery decision that needs
  exact continuation identity.

Allowed scope after admission:

- add the smallest stable parent or continuation trace identity at the trace
  creation boundary;
- query that identity exactly from the collector;
- preserve redaction and fail-closed ambiguity behavior; and
- remove only the fallback made obsolete by the new field.

No-trigger completion gate:

- record that no trigger appeared through completion of R3;
- retain the current collector without speculative schema work; and
- close R4 as intentionally skipped.

The closeout audit found no changes after `8ca7d2487` to
`cmd/mintclaw/internal/agent/live_evidence.go` or its continuation-correlation
tests. The collector remains the sole caller of its continuation lookup. The
only later diagnostic-trace schema change, `c6d485a17`, records provider and
prompt-cache usage; it neither introduces a continuation-lineage consumer nor
uses heuristic correlation for a production admission, recovery, billing, or
delivery decision. R4 therefore remains unadmitted.

## Order And Autonomous PR Policy

1. R0 merges as a docs-only PR.
2. R1 starts from the latest merged `origin/main` and merges before R2.
3. R2 starts from refreshed main after overlapping coding work is reconciled.
4. R3 audits deployed state only after code packets are merged, so its evidence
   names the build that would remove or retain compatibility.
5. R4 is evaluated after R3 and cannot delay completion when its trigger is
   absent.

Every code-bearing packet is a focused ready-for-review PR. It runs `make fmt`,
targeted tests, proportionate broader tests, `make lint`, and required CI. It
merges only after no actionable review threads remain and an authorized rocket
approval is present. Review-driven scope growth follows the autonomous PR
architecture checkpoint rules.

## Stop Conditions

The roadmap stops when:

- R0-R3 have merged or R3 has merged an evidence-backed retain decision;
- R4 is merged after a valid trigger or recorded as intentionally skipped;
- the architecture index and this document record final packet evidence; and
- no mandatory acceptance criterion remains open.

The roadmap does not expand because another file is large, another subsystem
has churn, a test can be made faster, or a compatibility reader exists behind
an unmet removal gate. New correctness evidence requires a separate admission
decision rather than silently extending this goal.

## Delivery Evidence

- R0 — merged in [#1331](https://github.com/bogdanovich/mintclaw/pull/1331)
  (`78ac1dc096d6fc9b9a01a3224a7ae5e84d3d3ff2`): published the bounded
  maintenance contract, dependencies, exclusions, and stop conditions.
- R1 — merged in [#1333](https://github.com/bogdanovich/mintclaw/pull/1333)
  (`dc8108553d128fc9b620b1b77926d389e35589b9`): introduced one concrete
  document-delivery coordinator for direct, conversational, live-resume, and
  recovered-outbox transitions while preserving both durable authorities.
- R2 — merged in [#1339](https://github.com/bogdanovich/mintclaw/pull/1339)
  (`9386c6f4088bf894ea03545932fd2f08c0650236`): gave coding model,
  provider, reasoning, reviewer, status, persistence, replacement, and close
  behavior one model-session lifecycle owner.
- R3 — merged in [#1341](https://github.com/bogdanovich/mintclaw/pull/1341)
  (`5b9f8b6c5d5ce4d4acf620d632a8b3f8d0b65944`): removed only the
  zero-state inbound hydration readers, retained companion v1 migration, and
  recorded the inventory, decision, rollback, and remaining deployment gate in
  the [R3 compatibility audit](../../operations/code-health-v2-r3-compatibility-audit.md).
- R4 — intentionally skipped on 2026-09-22. No post-baseline correlation
  defect, second continuation-lineage consumer, or production-decision
  dependency was found.

All mandatory packets are merged, the conditional packet is closed without
speculative schema work, and no acceptance criterion remains open.
