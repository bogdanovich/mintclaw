# Interaction Contention Qualification

Status: bounded CI qualification for architecture finding A3. This records
current behavior and the next admission contract. It does not claim a production
incident, an OOM reproduction, or implemented production queue/deadline limits.

## Scope and method

The tests use a temporary interaction store, a real Unix file lock, and
controlled subscriber callbacks. They do not contact external services or run
production effects. Each wait has a test deadline and each producer burst is
finite. The producer worker has a ten-second harness deadline; on timeout the
callback is released before producer cleanup or test failure. Other coordination
waits have three-second guards. Each test contains three independent repetitions,
including when run by the existing full normal and race CI suites. Test
deadlines protect the harness; the current runtime APIs still have no matching
production deadline or queue cap.

Run from the repository root:

```sh
go test -tags=goolm,stdjson ./pkg/interactions \
  -run '^TestRegistryContentionQualification' -count=1 -v -timeout=1m
go test -race -tags=goolm,stdjson ./pkg/interactions \
  -run '^TestRegistryContentionQualification' -count=1 -v -timeout=1m
```

## Live lock holder

A live holder retains the store's `.lock` file for at least 1.25 seconds. A
writer reaches `lockAndReloadLocked` while holding the registry mutex. During
the controlled hold, a query and an unsubscribe operation also wait behind that
mutex. Releasing the file lock allows all three operations to finish and the
record remains committed. This establishes head-of-line blocking; it does not
establish a shutdown guarantee for a permanently unavailable lock holder.

The current `Flock(LOCK_EX)` call has no deadline, and the registry APIs do not
accept cancellation for this operation. The observed duration is a lower bound
chosen by the test, not a maximum supported wait.

## Stalled subscriber

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

## Execution evidence

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

## Next admission contract

These are engineering targets for the next focused change, not current
runtime guarantees:

| Boundary | Initial budget | Failure disposition and required evidence |
| --- | --- | --- |
| Acquiring a workspace interaction store | One second, or the earlier caller cancellation/deadline | Return a typed retryable busy/timeout result before mutating durable state. Do not poison an otherwise valid registry permanently because a live holder is busy. Queries and unsubscribe must not remain coupled to an unbounded file-lock wait. |
| Pending committed notifications | 256 observations or 4 MiB of accounted payload per registry/subscription, whichever is reached first | Preserve authoritative commit identity. Use explicit subscriber invalidation and snapshot/resynchronization, or bounded producer admission before commit; never silently drop a committed notification. Include gated snapshot subscriptions in the same accounting. |
| Unsubscribe and shutdown | One-second admission/cancellation budget | Stop accepting new deliveries, explicitly account for in-flight and committed callbacks, and return a distinct timeout/resync requirement when a consumer cannot settle. Do not report that a blocked callback was cancelled when its execution continues. |

A queue-size setting alone cannot enforce this contract: cloned records can
outlive snapshot retention, and both the registry drainer and gated observer
buffers need accounting. A timeout around an unchanged synchronous operation
also leaves the blocked goroutine and mutex alive. The implementation must put
the budget at the owner that acquires the lock or admits notification work.

Keep this work inside `pkg/interactions` and its lifecycle adapters. Preserve
revision checks, locked reload, atomic persistence, committed event ordering,
and snapshot/activation boundaries. No service split or new storage engine is
justified by this qualification.

## Acceptance before claiming the risk resolved

- A live holder beyond the configured budget returns typed busy/timeout, and
  a later attempt succeeds after release without a restart or lost mutation.
- A stalled active or gated subscriber stays within both count and accounted
  byte budgets; a committed gap requires explicit resynchronization.
- Concurrent commits, reentrant callbacks, failed persistence and recovery
  preserve committed order and never replay effects to repair observations.
- Unsubscribe and runtime shutdown distinguish settled, still-running and
  resynchronization-required work within their stated budgets.

Until that contract is implemented, A3 remains an availability and memory
hardening follow-up. The bounded tests preserve the current durability behavior
and make the remaining limits visible; they do not close the production risk.
