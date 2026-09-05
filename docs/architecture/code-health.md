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
- Node protocol v2 owns canonical number rendering for current companions;
  versioned gateway readers contain the temporary v1 compatibility edge.
- Approval snapshots require their current authority fields. GitHub Copilot
  has one SDK transport, and agent contract tests use supported fixtures rather
  than constructing private runtime state.

## Bounded compatibility

All three connected companions now negotiate node protocol v2. The deployed
gateway temporarily retains v1 parsing for 148 expired no-replay invocation
tombstones that were still present at fleet closeout. V1 parsing is therefore
a versioned persistence-edge obligation, not a second steady-state
representation; it can be removed after retention pruning and a fresh audit
show zero connected, active, or retained v1 work. The deployed inventory is
recorded in the
[Node JSON Canonicalization V2 Cutover](../operations/node-json-canonicalization-v2-cutover.md).

The obsolete provider `connect_mode` is not persisted or exposed by the
runtime. The configuration compatibility boundary accepts only omitted,
`null`, empty, or `grpc` legacy input and rejects every unsupported value before
writing. Strict approval-reader rollout evidence is recorded in the
[Interaction Record Strict-Reader Cutover](../operations/interaction-record-strict-reader-cutover.md).

## Change guardrails

New work should keep one lifecycle owner, freeze configuration and authority at
the generation or request boundary, put compatibility in an explicit
versioned edge, and test public contracts. Large files or repeated call shapes
alone do not justify a framework, generic supervisor, dependency-injection
container, or repository-wide rewrite.

Historical decisions and packet evidence remain in the archived
[Code Health Roadmap](archive/code-health-roadmap.md) and
[Architecture Simplification Roadmap](archive/architecture-simplification-roadmap.md).
