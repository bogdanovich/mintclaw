# Interaction Contention Qualification

Status: the A3 production admission and observer settlement contract is merged.

- Store admission: [PR #1821](https://github.com/bogdanovich/mintclaw/pull/1821), merge `cd6f355ae258e81dc2a05561b3414ebb68aea9b2`.
- Backlog admission: [PR #1823](https://github.com/bogdanovich/mintclaw/pull/1823), merge `a2469826d753cc67589177772ef46a4764f3a47e`.
- Subscription and runtime shutdown: [PR #1825](https://github.com/bogdanovich/mintclaw/pull/1825), merge `2f285871b7aabcf05f1979a9f3538761c6a56cd2`.

## Runtime contract

| Boundary | Default budget | Outcome |
| --- | --- | --- |
| Store admission | One second, configurable through `Options.StoreLockTimeout`; context-aware entry points honor earlier caller cancellation/deadlines | `StoreBusyError` preserves `ErrStoreBusy` and its context cause before mutation. File locks use nonblocking retries before taking the registry mutex; competing writers have independent budgets. |
| Outstanding observations | 256 unique observations or 4 MiB of accounted payload per registry, whichever fills first | A single ledger retains each observation through the registry drainer, gated buffers and running callbacks. `ObserverBacklogError` / `ErrObserverBacklog` reject before persistence; retry after consumers settle. |
| Intrinsically oversized observation | The same configured payload budget | `ErrObserverPayloadOverBudget` distinguishes an observation that cannot fit from transient backlog. Adjust its payload, fanout or explicit budget. |
| Unsubscribe and subscription settlement | Unsubscribe immediately stops new delivery admission; `Subscription.CloseContext` waits up to one second or the earlier caller deadline | Accepted producers and active callbacks remain accounted until completion. `ObserverDrainError` preserves timeout/cancellation, `ErrObserverPending` and `ErrObserverResyncRequired`. |
| Registry/runtime observer shutdown | Registry close has a one-second cap. Runtime workers and all workspace observers share one second, or the earlier caller deadline | Registry close seals new writes and subscriptions. Incomplete worker/observer drain leaves dependent sinks, outboxes and sessions available for a later teardown retry. Other teardown phases keep the caller context. |

`CreateContext`, `PruneContext` and `Refresh` accept caller context. Existing
context-free mutation methods use the default store admission budget. A busy
constructor remains retryable through a later mutation or refresh; corrupt
startup snapshots retain fail-closed behavior. Cached reads remain available
after registry close, while new write/observer admission returns `ErrStoreClosed`.

The payload charge is the serialized immutable event/record snapshot plus a
full isolated callback copy for each subscriber. Gated buffers share that
immutable snapshot; the ledger releases it only after the registry drainer and
every accepted subscriber finish or explicitly abandon their share. This bounds
accounted payload rather than exact allocator overhead or whole-process heap.
Per-subscriber pending observations cannot exceed the global registry count budget.
Retired subscriptions release callback captures as soon as accepted work settles;
registry membership compaction can wait until the next admission.

Producer admission precedes atomic persistence. Rejection rolls back staged
records, revisions and commit identity; failed persistence releases reservations.
Expiry recovery commits batches that fit available capacity and can continue on
later polls. Reentrant producers receive an immediate admission error at capacity.

Legacy unsubscribe stops new admission without waiting for the registry mutex.
An already accepted producer may still commit and deliver after unsubscribe.
The handle APIs expose explicit settlement. Canceling an accepted gated stream
requires a fresh authoritative snapshot and activation, without replaying
committed effects. A callback that exceeds the close budget continues executing;
the timeout reports pending work. File-system read/write syscalls and arbitrary
callback code are outside the lock-admission cancellation mechanism.

## Verification and current measurements

The qualification uses a real Unix lock and controlled subscribers. The count
probe uses an in-memory store to isolate notification admission, with retained
records/events kept small. Separate file-backed byte-budget cases use an 8 KiB
configured limit and verify byte-for-byte durable rollback and retry. The same
admission owner enforces the default 4 MiB limit; the count workload below fills
the event budget first. No external services or production effects are exercised.

```sh
go test -tags=goolm,stdjson ./pkg/interactions -count=1 -timeout=2m
go test -race -tags=goolm,stdjson ./pkg/interactions -count=1 -timeout=2m
go test -race -tags=goolm,stdjson ./pkg/agent \
  -run '^(TestAgentLoopCloseContextBoundsInteractionObservers|TestInteractionCoordinatorShutdownSharesWorkspaceBudget|TestCloseContextDrainsProtectedRecoveryBeforeClosingSink|TestClosedLoopRefusesLateInteractionRecoveryBeforeRegistryMutation)$' \
  -count=1 -timeout=2m
```

CI validated source head `0bd8ce6568990b4f80b628f2fe464f2e8a598182` with
`go version go1.26.9 linux/amd64` on `ubuntu-latest`. Its [full normal job](https://github.com/bogdanovich/mintclaw/actions/runs/39212915451/job/117930175116) and
[race job](https://github.com/bogdanovich/mintclaw/actions/runs/39212915451/job/117930175129) passed, including the complete interaction
package and the selected runtime/recovery lifecycle cases. All code PRs merged
after full current-head CI, substantive review with no must-fix findings and
owner PR-level rocket approval.

| Attempt | Admitted | Producer elapsed | Outstanding | Accounted payload bytes | Count limit | Byte limit | Retained records | Retained events |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | 256 | 25.320687ms | 256 | 586644 | 256 | 4194304 | 2 | 2 |
| 2 | 256 | 24.168101ms | 256 | 586644 | 256 | 4194304 | 2 | 2 |
| 3 | 256 | 18.810921ms | 256 | 586644 | 256 | 4194304 | 2 | 2 |

| Attempt | Concurrent writers | Admission elapsed | Retry revision |
| --- | --- | --- | --- |
| 1 | 4 | 1.002832161s | 1 |
| 2 | 4 | 1.000712314s | 1 |
| 3 | 4 | 1.000363306s | 1 |

Reads and unsubscribe remained available while the real lock was held. Every
busy writer returned before durable mutation, and retry succeeded after release.
Registry callback shutdown returned explicit pending work in `1.000864196s`;
runtime callback shutdown did so in `1.000558505s`. Both later settled on
retry after callback release. These durations include scheduler overhead around
the one-second deadline; they are controlled CI measurements.

Final local source passed formatting, lint, full interaction normal/race tests
and selected runtime/recovery/task-input race cases. Local full agent suites
passed on the original head and the composed base before the final callback
capture cleanup; the final head passed the full CI suite. Local Windows amd64
cross-compilation passed on an earlier head; the final CI Windows build passed.
Earlier local runs of the baseline disk-heavy observer probe exceeded its
harness deadline on this Mac; the unchanged merged baseline reproduced the same
failure. The new count probe isolates notification admission and leaves durable rollback to the
file-backed tests. Earlier startup-stalled runs remain inconclusive below.

This is source/CI qualification. Deployment, a service restart, real-account
workflows, Windows runtime lock-contention measurements and paid evaluations
remain outside this change.

## Historical baseline before enforcement

The following measurements and test behavior are frozen at source head
`1255e094a5b3cbb28bb370021106c12f6d1bcc82`. They explain the admission decision;
references to baseline behavior describe that source, before the limits above.

### Scope and method

The tests use a temporary interaction store, a real Unix file lock, and
controlled subscriber callbacks. They do not contact external services or run
production effects. Each wait has a test deadline and each producer burst is
finite. The producer worker has a ten-second harness deadline; on timeout the
callback is released before producer cleanup or test failure. Other coordination
waits have three-second guards. Each test contains three independent repetitions,
including when run by the existing full normal and race CI suites. Test
deadlines protect the harness; the baseline runtime APIs had no matching
production deadline or queue cap.

### Live lock holder

A live holder retains the store's `.lock` file for at least 1.25 seconds. A
writer reaches `lockAndReloadLocked` while holding the registry mutex. During
the controlled hold, a query and an unsubscribe operation also wait behind that
mutex. Releasing the file lock allows all three operations to finish and the
record remains committed. This establishes head-of-line blocking; it does not
establish a shutdown guarantee for a permanently unavailable lock holder.

The baseline `Flock(LOCK_EX)` call had no deadline, and its registry APIs did not
accept cancellation for this operation. The observed duration is a lower bound
chosen by the test, not a maximum supported wait.

### Stalled subscriber

One callback holds the notification drainer while a finite producer commits
128 create/cancel pairs. Terminal records are pruned using a controlled clock;
the store retains at most two records, four events and a 16 KiB snapshot budget.
The queued observations retain the already committed record snapshots even as
those records leave the durable retention window.

Unsubscribe returns while the callback is blocked. Already committed deliveries
still drain exactly once in commit order after release; new subscriptions or a
lossy diagnostic bus cannot substitute for that contract. The test measures the
serialized observations in the queue. Those bytes describe workload size and
are not an exact process heap or retained-memory measurement.

### Execution evidence

The initial local qualification attempt was inconclusive: macOS stalled the
new test binary at `_dyld_start` before Go runtime entry, with a 12 KiB process
footprint and no test output. That attempt was stopped without changing test
assertions or production behavior. It is not counted as a normal or race pass.

CI validated source head `1255e094a5b3cbb28bb370021106c12f6d1bcc82` on
ubuntu-latest. The runner reported `go version go1.26.9 linux/amd64`.
The [normal job](https://github.com/bogdanovich/mintclaw/actions/runs/39202805170/job/117899233013)
and [race job](https://github.com/bogdanovich/mintclaw/actions/runs/39202805170/job/117899233072) both passed.
This head includes both merged runtime fixes, so its full CI checks their
combined result. The normal JSON log records all six subcase passes and both
parent test passes; the race job runs the complete interaction package. Each
test contains three independent repetitions.

| Attempt | Queued observations | Serialized queue bytes | Durable snapshot bytes | Retained records/events | Producer elapsed |
| --- | --- | --- | --- | --- | --- |
| 1 | 256 | 293691 | 1705 / 16384 | 1 / 1 | 619.415581ms |
| 2 | 256 | 293691 | 1705 / 16384 | 1 / 1 | 890.363601ms |
| 3 | 256 | 293691 | 1705 / 16384 | 1 / 1 | 1.002209634s |

These are serialized payload bytes, not exact retained heap. The observations
outlive the pruned records and snapshot limits; ordered delivery drains after
the callback releases. This confirms that durable snapshot capacity is not a
notification-backlog budget.

| Attempt | Controlled live-lock hold | Total elapsed through recovery |
| --- | --- | --- |
| 1 | 1.251500433s | 1.252572217s |
| 2 | 1.252015674s | 1.252936464s |
| 3 | 1.251556594s | 1.252915371s |

The hold duration is selected by the test. It is a measured lower bound on
blocking for this workload, not a supported maximum latency. The committed
record survives release and recovery. No permanently stalled holder, process
OOM, production incident, or deployed guarantee is claimed.
