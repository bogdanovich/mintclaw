# Architecture Soundness and Robustness Review

Status: owner admitted A1/A2 correctness fixes and A3 bounded qualification.
A4 remains conditional; A5 retains its existing owners. Snapshot: `77c79cd50e61ce50d75b8fe0e4b60a63f56b5fda`, October 19, 2026 UTC.
The prior [second-sweep fixes](reliability-code-health-20261018-sweep.md) remain
complete. This pass covers the current runtime architecture and 16 subsequent
merged PRs affecting 92 files.

MintClaw has a sound modular-monolith foundation. The domain brokers, typed
protocols and explicit lifecycle owners should be retained. Its main weakness
is the enforcement of ownership at initialization and terminal commit. Three
reproductions expose two underlying problems: final answers can be committed
before resource disposition is settled, and task registries can admit new work
before restoration finishes. These justify focused changes within existing
owners, rather than a service split or a new orchestration framework.

## Architecture assessment

| Area | Judgment | Evidence and implication |
| --- | --- | --- |
| Module boundaries | Sound foundation | Domain packages such as `pkg/computer`, `pkg/tasks` and `pkg/nodes` do not depend on the agent or gateway. Gateway and agent packages compose them. Keep the domain state machines independent. |
| Lifetimes and ownership | Mostly explicit, with two enforcement gaps | Process-wide task/interaction coordinators survive runner replacement; generation-specific wiring is replaced together. A1/A2 below show where visibility or completion precedes readiness. |
| Durability and recovery | Strong explicit contracts | Outbox recovery converts interrupted attempts to ambiguous outcomes, keeps exact identities and separates transport settlement from domain completion. Task mutations distinguish committed writes from rollback. Preserve these distinctions. |
| Authority boundaries | Appropriate complexity | Principals, session/generation bindings, native evidence and local admission are independently checked. Protected input transport does not silently enable an input host. Repeated checks at independent authority boundaries are useful. |
| Simplicity | Orchestration needs targeted reduction | Finalization spans model repair, canonical history, streaming and deferred cleanup. The reproduced inconsistency establishes a reason to consolidate this responsibility. File size alone does not establish a reason to extract other packages. |
| Robustness under contention | Needs explicit budgets | Interaction file-lock acquisition has no deadline, and notification queues can grow while observers stall. These need bounded stress qualification and an explicit contention policy. |
| Tests | Broad coverage with missing composed postconditions | Existing storage, lifecycle, reload and native-contract selections pass. New probes fail at the boundaries between those components. Add tests that compare canonical history, returned outcome and delivery after cleanup. |

The Go package inventory contains 194 packages for the inspected macOS build
with `goolm,stdjson`. Across tracked platform sources, `pkg/agent` has roughly
58,400 production lines in 153 files; `pipeline_execute.go` has 3,502 lines.
`pkg/tools/browser.go` has 3,680 lines. These identify review hotspots, not
refactoring acceptance criteria. The important finding is that finalization
policy crosses several of those hotspots and commits before its last decision.

The responsibility split is already documented in
[AgentLoop Runtime Host](agentloop-runtime.md). Source inspection confirms the
stable `turnRuntime`, `interactionCoordinator` and `taskCoordinator`, plus
replaceable `turnRunner` generations. Gateway reload quiesces turns, prepares
new services, reconciles authority and publishes a prepared generation. Its
fault-injection tests exercise rollback after each preparation stage. This is
useful lifecycle structure and should not be replaced with a generic supervisor.

## Simplification priorities

Most of the protocol and authorization complexity serves explicit requirements.
Separate transport, application, automation-control and durable-delivery state
represent different owners. Preserve their independent checks and evidence.

The turn engine has more accidental complexity. Repair budgets and lifecycle
flags in `turnExecution` participate in decisions across `pipeline_terminal.go`,
`pipeline_finalize.go` and `turn_coord.go`. A1 demonstrates the maintenance cost:
the finalization context freezes one answer before deferred cleanup can choose
another. Put the lifecycle decision into preparation of the existing terminal
snapshot, and remove the later competing mutation. Extract adjacent phase
helpers where that gives one clear owner; retain domain-specific verification.

The large browser adapters and eval finalizers are maintenance candidates for
related work. Share mechanics only where the inputs, evidence and failure rules
actually match. The already completed private-fill extraction is an example of
bounded reuse; shared site-admission policy would require a separate product
and authority decision.

## Admitted order of work

| ID | Priority | Work | Completion condition |
| --- | --- | --- | --- |
| A1 | High | Resolve terminal lifecycle obligations before committing the final outcome | Cleanup failure is represented; canonical history, returned result, terminal events and final delivery agree. Preserve suspended handoffs and no-replay behavior. |
| A2 | Medium | Publish task registries only after restoration is complete | Concurrent first access cannot create work that restoration marks lost, and only one initializer can mutate the store. |
| A3 | Medium qualification | Bound interaction lock waits and observer backlog | Contention and stalled-observer tests establish a latency/memory budget and explicit busy, timeout or resynchronization behavior. |
| A4 | Conditional | Simplify pipeline phases during A1 and related changes | Move responsibilities into clear same-package owners and reduce duplicated terminal decisions; avoid a generic workflow engine or arbitrary file splitting. |
| A5 | Existing owners | Continue behavioral qualification and measured storage/dependency follow-ups | Use the existing eval, credential and first-sweep gates. A code review does not complete those programs. |

A1 should be one coherent lifecycle correction with its regression matrix.
A2 is independently reviewable. A3 should establish the budget and failure
semantics before introducing a queue or storage abstraction. The earlier
conditional eval-finalization extraction remains conditional.

## A1 Terminal outcome is committed before cleanup settles

[Pipeline.Finalize](https://github.com/bogdanovich/mintclaw/blob/77c79cd50e61ce50d75b8fe0e4b60a63f56b5fda/pkg/agent/pipeline_finalize.go#L150) persists the
assistant message, marks final content on the turn and finalizes streaming.
Only after the pipeline returns does
[turnRunner.run](https://github.com/bogdanovich/mintclaw/blob/77c79cd50e61ce50d75b8fe0e4b60a63f56b5fda/pkg/agent/turn_coord.go#L107) perform default resource
cleanup. That defer can change the returned result after canonical history
has been committed. Streaming finalization also precedes this defer; its stale
final-answer risk follows from the same source ordering.

Two synthetic probes reproduce distinct consequences:

- A model opens a browser through the real `BrowserSessionTool` and tool
  registry, then returns `Task complete; browser closed.` The injected broker
  still owns that session and cannot inspect or clean it. The actual lifecycle
  methods are called, but inspection and cleanup errors produce warnings only.
  The returned turn is completed with no error and retains that final claim.
- A successful fallback browser-cleanup receipt changes the returned result to
  blocked, but canonical history still contains the earlier assistant answer.
  The probe observes different canonical and returned final content.

The first path also crosses
[the finalization requirement check](https://github.com/bogdanovich/mintclaw/blob/77c79cd50e61ce50d75b8fe0e4b60a63f56b5fda/pkg/agent/pipeline_terminal.go#L162),
which logs an inspection error and continues. The second crosses
[attachTurnCleanupReceipts](https://github.com/bogdanovich/mintclaw/blob/77c79cd50e61ce50d75b8fe0e4b60a63f56b5fda/pkg/agent/turn_coord.go#L172), which deliberately
corrects the returned result after the earlier commit. This is a sequencing
problem across owners, rather than a missing native close-error check. The
previous native cleanup fix remains necessary and correct.

Make resource disposition part of preparing the terminal outcome. Settle or
explicitly represent required unresolved cleanup, freeze that outcome, and use
it for canonical history, final delivery and terminal events. Keep a deferred
cleanup safety net for abnormal exits, but do not let it silently change an
already committed outcome. Preserve verified work and report unresolved
resource disposition explicitly; do not replay effects to repair presentation.
An unavailable optional subsystem with no owned resources should not
indiscriminately fail an unrelated conversation.

Acceptance should cover successful explicit close/release, fallback cleanup,
failed/uncertain cleanup, inspection failure after a resource was opened,
cancellation, hard abort, suspended handoff, streamed final delivery and
canonical append failure. Assert one consistent terminal outcome across those
surfaces, rather than checking each helper in isolation.

## A2 Task registry visibility precedes restoration readiness

[taskCoordinator.loadRegistry](https://github.com/bogdanovich/mintclaw/blob/77c79cd50e61ce50d75b8fe0e4b60a63f56b5fda/pkg/agent/task_registry.go#L113) puts its
candidate in the shared map before reconciling restored active tasks and stale
delivery state. A concurrent `configuredRegistry` call immediately receives that
same registry. Restoration subsequently calls
[MarkActiveLost](https://github.com/bogdanovich/mintclaw/blob/77c79cd50e61ce50d75b8fe0e4b60a63f56b5fda/pkg/tasks/recovery.go#L78) over the registry's then-current
active records, including new work created after publication.

The deterministic probe seeds an old active task and holds the real interaction
store lock while restoration resolves protected ownership. A second caller gets
the published registry and creates `new-live-task`. Releasing the lock lets
restoration finish; both the old and new task become lost, with the explanation
that their previous runtime owner is no longer alive.

This is a logical ordering race. Memory-race detection does not establish
initialization readiness. Exposure is limited to concurrent first access:
[normal shared-tool initialization](https://github.com/bogdanovich/mintclaw/blob/77c79cd50e61ce50d75b8fe0e4b60a63f56b5fda/pkg/agent/agent_init.go#L414) warms
configured workspaces, which reduces the common startup path's exposure. The
lazy coordinator APIs still lack their own readiness guarantee.

Initialize once per workspace, finish restoration, then expose the ready
registry. Serialize side-effecting construction as well: the task registry
constructor may prune and persist its loaded snapshot. A ready promise or
per-workspace initialization lock in the existing coordinator is sufficient;
there is no demonstrated need for a new registry service.

## A3 Contention and notification budgets

[Interaction store locking](https://github.com/bogdanovich/mintclaw/blob/77c79cd50e61ce50d75b8fe0e4b60a63f56b5fda/pkg/interactions/filelock_unix.go#L11) uses
blocking `Flock(LOCK_EX)` without a deadline. Persistence acquires that lock
while holding the registry mutex. A slow or hung live peer can therefore stall
registry access and startup/recovery work. This concerns an actively held lock;
an abandoned lock-file pathname by itself does not retain an advisory lock.
The outbox already has an explicit nonblocking ownership refusal.

[Interaction notifications](https://github.com/bogdanovich/mintclaw/blob/77c79cd50e61ce50d75b8fe0e4b60a63f56b5fda/pkg/interactions/events.go#L134) append cloned
records/events to a separate queue while one observer drains synchronously.
[Observer delivery](https://github.com/bogdanovich/mintclaw/blob/77c79cd50e61ce50d75b8fe0e4b60a63f56b5fda/pkg/interactions/observer.go#L18) also has a pending
queue. Snapshot record/event budgets do not bound these queues. A stalled
observer can delay callbacks and let subsequent commits accumulate notifications.

These are source-backed availability and memory-growth risks, not reproduced
production incidents or measured exhaustion. Add tests for a live lock holder,
a stalled observer, concurrent commits and shutdown. Choose bounded refusal,
backpressure or explicit snapshot resynchronization while preserving committed
lifecycle notifications. Do not silently drop authoritative events or reuse a
lossy diagnostic-event policy for state ownership.

## Validation and limits

The [reproduction record](../operations/architecture-robustness-20261019-reproductions.md)
retains exact temporary probes and commands against the frozen snapshot. They
exercise the real coordinator, pipeline, session store and public browser tool
with synthetic broker/clock/lock conditions. They use no real browser account,
OS desktop application, production service or paid model.

Existing suites passed for `pkg/tasks`, `pkg/interactions`, `pkg/outbox`,
`pkg/fileutil`, `pkg/runtimecap` and `pkg/computer`. Targeted agent lifecycle,
terminal, task restoration, disposition and external-wait tests passed, as did
gateway startup/reload rollback tests and native companion/computer-host/desktop
contract selections. A final combined race repeat produced no test output and
was stopped after a local execution stall; it is inconclusive, not a pass. The
three archived invariants were each reproduced in completed normal runs. Native
platform qualification and full frontend/whole-repo matrices were outside this pass.

Documentation lint, local-link checks and the reproduction extraction script
syntax check passed.

The owner admitted this follow-up on October 19, 2026 UTC. This assessment
publication changes no runtime implementation and does not reopen the completed
first goal or claim deployed robustness. A1/A2 will be separate code PRs; A3
first establishes contention budgets and an explicit disposition.
