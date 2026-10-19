# Interaction Contention Qualification

Status: bounded local qualification for architecture finding A3. This records
current behavior and the next admission contract. It does not claim a production
incident, an OOM reproduction, or implemented production queue/deadline limits.

## Scope and method

The tests use a temporary interaction store, a real Unix file lock, and
controlled subscriber callbacks. They do not contact external services or run
production effects. Each wait has a test deadline and each producer burst is
finite. Each test contains three independent repetitions, including when run
by the existing full normal and race CI suites. Test deadlines protect the harness; the current runtime APIs still have
no matching production deadline or queue cap.

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

The initial local qualification attempt was inconclusive: the new test binary
stalled at macOS `_dyld_start`, before Go runtime entry, with a 12 KiB process
footprint and no test output. It was stopped without changing assertions or
runtime behavior. No local normal or race pass is claimed for that attempt.

The repository's normal CI suite runs every package with JSON test output;
its critical runtime race suite runs all of `pkg/interactions`. These execute
the bounded three-repetition tests. Record the observed queue sizes, timings,
runner platform, exact tested head and successful normal/race job identities
here after those checks complete. Budgets below remain admission targets until
production enforcement is implemented.

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
