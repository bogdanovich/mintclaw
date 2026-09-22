# Code Health Architecture State

Status: current. The H0-H8 code-health program is complete.

The program replaced the highest-change implicit dependencies with small,
owned contracts while preserving the existing durability and authority
boundaries. The implementation through H8.3 is merged at `82d7b398`; H8.4
archives the execution ledgers and records the deployed state.

## Current ownership

- Configuration loading owns explicit runtime projections and secret
  resolution. Product code does not discover secrets by serializing the root
  config object.
- Each web launcher owns one `GatewayProcessManager`; lifecycle operations do
  not coordinate through package-global process state.
- Frontend model editing uses one typed contract and one shared form instead
  of provider-specific copies.
- `interactionService` and the durable interaction registry own interaction
  creation, resumption, cancellation, and recovery.
- A turn enters through an immutable request, keeps mutation in one runtime
  owner, returns typed step outcomes, and reaches terminal state through one
  finalization gateway. Phase-oriented files expose those boundaries.
- Browser, node, tool, and provider policy are projected into immutable,
  generation-owned snapshots rather than read repeatedly from the root config.
- Node protocol v2 is the sole admitted, persisted, and canonicalized node
  representation. Omitted and v1 snapshot or plan versions fail closed.
- Approval snapshots require their current authority fields. GitHub Copilot
  has one SDK transport, and agent contract tests use supported fixtures rather
  than constructing private runtime state.
- Document write and protected-form delivery remain separate durable
  authorities. One concrete `documentDeliveryCoordinator` translates direct,
  conversational, live-resume, and recovered-outbox settlement between them.
- The local coding runtime's selected model, provider, reasoning profile,
  reviewer, retained provider, status, replacement, persistence, and close
  behavior belong to one `codingModelSession`; turn and review paths consume
  immutable snapshots from it.

## Bounded compatibility

All three connected companions negotiate node protocol v2. Protocol-v1
support was retired only after retention pruning and a fresh audit could show
zero connected, active, or retained v1 work. The fleet cutover inventory and
the reader-removal gate are recorded in the
[Node JSON Canonicalization V2 Cutover](../operations/node-json-canonicalization-v2-cutover.md).

The obsolete provider `connect_mode` is not persisted or exposed by the
runtime. The configuration compatibility boundary accepts only omitted,
`null`, empty, or `grpc` legacy input and rejects every unsupported value before
writing. Strict approval-reader rollout evidence is recorded in the
[Interaction Record Strict-Reader Cutover](../operations/interaction-record-strict-reader-cutover.md).

Pre-M4 MintClaw session and pre-F3 interaction metadata are no longer hydrated
from durable inbound records. A zero-state deployed audit admitted their
removal, and the spool now rejects either retired shape without rewriting or
deleting it. Companion invocation-ledger version 1 migration remains because
two maintained deployments still retain v1 no-replay identities. The inventory
and exact upgrade gate are recorded in the
[Code Health V2 R3 Compatibility Audit](../operations/code-health-v2-r3-compatibility-audit.md).

## Change guardrails

New work should keep one lifecycle owner, freeze configuration and authority at
the generation or request boundary, put compatibility in an explicit
versioned edge, and test public contracts. Large files or repeated call shapes
alone do not justify a framework, generic supervisor, dependency-injection
container, or repository-wide rewrite.

Historical decisions and packet evidence remain in the archived
[Code Health Roadmap](archive/code-health-roadmap.md) and
[Architecture Simplification Roadmap](archive/architecture-simplification-roadmap.md).

The completed follow-up program made inbound relations deterministic, moved
stable inbound facts to typed boundaries, and gave coding controller primary
operations explicit state. Its evidence and intentionally skipped layout gate
are recorded in the
[Post-H8 Code Health Roadmap](code-health-followup-roadmap.md).

The completed [Code Health Maintenance Roadmap](code-health-maintenance-roadmap.md)
removed the remaining strict-read, duplicated-settlement, reload lifecycle,
typed-boundary, credential, command-composition, and controller queue risks.

The completed 2026-09-21 maintenance sequence converged document delivery
settlement, coding model-session ownership, and evidence-gated inbound
compatibility retirement. Its conditional trace-lineage packet was not admitted
because no trigger appeared. Final packet evidence and stop criteria are
recorded in the archived
[Code Health Maintenance V2 Roadmap](archive/code-health-maintenance-v2-roadmap.md).

No code-health maintenance roadmap is currently active. New work requires a
fresh evidence-backed admission decision rather than extending a completed
program.
