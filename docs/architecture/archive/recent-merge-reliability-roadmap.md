# Recent-Merge Reliability Roadmap

Status: completed 2026-09-17

Completion: R0 through R5 merged; final implementation PR `#1264`, merge
commit `623b98aeb67bb81ed662b4f75063a28f04f68f99`.

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

## Completion Evidence

| Packet | Pull request and merge | Result |
| --- | --- | --- |
| R0 | [#1252](https://github.com/bogdanovich/mintclaw/pull/1252), `58c0f0ca8e94763d028da7cc10b8ee6f9395e3fc` | Published this bounded roadmap and its stop criteria. |
| R1 | [#1255](https://github.com/bogdanovich/mintclaw/pull/1255), `484337e39f92bea00fb5d4fa358b16f45d14af88` | Unified and bounded cloud and Local Bot API Telegram file ingress. |
| R2 | [#1258](https://github.com/bogdanovich/mintclaw/pull/1258), `c5d0156f79d58e6b3046d307713523a2addf085c` | Canonicalized runtime workspace identity without weakening no-follow checks. |
| R3 | [#1261](https://github.com/bogdanovich/mintclaw/pull/1261), `dc9a35b223c6ed33350457a11f3c437857899628` | Made delayed-admission abandonment assertions deterministic. |
| R4 | [#1262](https://github.com/bogdanovich/mintclaw/pull/1262), `f05a46e0ffd92c3e540bb22c76dcda2b5a73d4a1` | Centralized the shared pdfcpu resource policy with characterization coverage. |
| R5 | [#1264](https://github.com/bogdanovich/mintclaw/pull/1264), `623b98aeb67bb81ed662b4f75063a28f04f68f99` | Added fail-closed browser worker capability admission, including privileged execution. |

R1-R5 merged with focused tests, required CI green on each immutable head, and
no actionable review thread left unresolved. Further refactoring requires new
evidence rather than continuation of this roadmap.
