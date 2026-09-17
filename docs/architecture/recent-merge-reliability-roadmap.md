# Recent-Merge Reliability Roadmap

Status: active

Baseline: `origin/main` at `da256a47` on 2026-09-16.

## Objective

Close the bounded correctness and maintenance risks found while auditing the
previous two weeks of merged work. This is not a general cleanup program. Each
packet has a concrete failure mode, an acceptance gate, and a stop condition.

## R1: Bound Telegram File Ingress

Evidence: the Local Bot API path verifies confinement and file type but copies
the source without a byte limit. The cloud path also lacks one operator-owned
inbound limit.

Acceptance criteria:

- one configurable limit applies to cloud and local Telegram downloads;
- metadata, opened-file size, and streamed bytes are all enforced;
- overflow and cancellation remove partial managed files;
- exact-limit, metadata overflow, stat overflow, and growing-source tests pass.

Stop when both ingress paths have the same bounded semantics. Do not redesign
Telegram media grouping or retention.

## R2: Unify Runtime Workspace Identity

Evidence: agent instances can retain a lexical path while durable coordinators
key state by a symlink-resolved path. On macOS, `/var` and `/private/var` expose
the mismatch in recovery and path-authority tests. Document coordinator tests
also pass a noncanonical temporary path into a deliberately no-follow opener.

Acceptance criteria:

- runtime workspace ownership has one canonical identity at construction;
- durable interaction and task lookups use that identity consistently;
- macOS alias-path regression tests cover recovery and read authority;
- document fixtures use canonical paths without weakening no-follow security;
- focused agent recovery and document coordinator tests run in macOS CI.

Stop when alias and physical paths identify the same runtime owner and the
focused native tests pass. Do not permit symlink traversal inside an admitted
workspace.

## R3: Make Delayed-Admission Testing Deterministic

Evidence: the gateway reconciliation test signals completion before durable
abandonment finishes, so the assertion can race the callback.

Acceptance criteria:

- callback completion and error are observed before durable state is read;
- repeated focused runs remain green;
- production reconciliation behavior is unchanged.

Stop after the test owns its synchronization. Do not change scheduler timing to
hide the race.

## R4: Centralize PDF Resource Policy

Evidence: inspection and form discovery independently assign the same pdfcpu
resource limits, allowing future hardening to drift between untrusted-input
entry points.

Acceptance criteria:

- one helper constructs the shared pdfcpu configuration;
- callers retain their command and validation behavior;
- characterization tests cover every configured resource limit.

Stop after the duplicated policy is removed. Do not introduce a generic PDF
backend framework.

## R5: Validate Browser Capabilities At Admission

Evidence: workers expose narrow optional interfaces, but coherent interface
bundles are checked only when an action executes. A malformed worker can reach
the ready state and fail later with `ErrDriverIncompatible`.

Acceptance criteria:

- `WorkerOpenResult` carries a small immutable capability manifest;
- the broker validates declared capability bundles before publishing ready;
- compatible local and remote workers retain their current behavior;
- focused tests reject inconsistent declarations during `Open`.

Stop at admission-time validation. Keep narrow worker interfaces and do not
refactor action dispatch, placement, or the driver protocol.

## Completion

The roadmap is complete when R1-R5 are merged with focused tests, required CI
is green on each immutable head, and no actionable review thread remains.
Further refactoring requires new evidence rather than continuation of this
roadmap.
