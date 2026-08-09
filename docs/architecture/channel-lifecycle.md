# Channel Lifecycle Architecture

This document records the current conservative channel lifecycle architecture
and the constraints on future lifecycle work. It supersedes the abandoned PR
88-91 refactor track without adding a generic runtime supervisor.

## Why This Exists

Historically, `channels.Manager` owned channel instances, outbound workers,
shared HTTP handlers, config reload, shutdown, and delivery lookup maps. The R6
extraction replaced those overlapping mutable fields with three explicit
owners:

- `DeliveryRuntime` owns the delivery registry, dispatcher lifetime, workers,
  queue admission, retry policy, and delivery outcomes.
- `StreamCoordinator` owns streams, final-stream metadata, placeholders,
  typing/reaction state, and tool-feedback lifecycle.
- `ChannelLifecycle` owns the channel/config registry, shared HTTP listeners and
  handlers, restart-required state, and serialized lifecycle transitions.

`Manager` retains the bus, runtime-event and durable-outbox dependencies and
adapts calls between these owners. It does not retain compatibility maps for
their state.

The failed refactor track made useful problems visible:

- replacing a channel can temporarily remove the worker while dispatch still
  consumes outbound messages
- replacing a worker can reorder messages across old and new workers
- stopping workers while holding the manager lock can deadlock with send paths
- unregistering HTTP handlers during replacement can create inbound 404 windows

The lesson is not "add a supervisor". The lesson is that delivery ownership
needs one clear owner before hot reload can be made correct.

## Design Principles

1. Prefer conservative behavior over complex hot-swap behavior.
2. Do not consume an outbound message unless there is an owned delivery path.
3. Preserve per-channel outbound ordering by construction.
4. Keep inbound HTTP registration tied to a deliverable channel state.
5. Add abstractions only when they remove a real race or simplify ownership.
6. Keep each migration PR small enough that its invariant can be reviewed.

## Non-Goals

- No changes to the separately owned durable outbound outbox in this track.
- No general runtime supervision framework unless a later PR proves the need.
- No transparent zero-drop replacement for every channel type as a first step.
- No channel-specific protocol rewrites.
- No readiness policy redesign unless it follows from a concrete runtime state.

## Current Practical Policy

Channel config changes are currently treated conservatively:

- adding a new channel may be supported live
- removing a channel drains accepted outbound delivery before stopping transport
- changing an existing channel's config uses restart-required behavior
  over in-process replacement

This is intentionally less ambitious than hot replacement. It matches the
current architecture better and avoids pretending that all channel transports
can be swapped safely under active delivery.

Runtime reconnects are a separate concern. Reconnecting the same channel
instance or transport is usually safer than replacing the logical channel
worker. That path should be improved before full config hot-swap.

## Implementation Status

R6 completed the conservative ownership baseline through PRs 595, 599, 605,
607, 609, 610, 612, 613, and 615:

- same-name active channel config changes are marked restart-required instead
  of replacing the running channel
- dispatch resolves a `DeliveryRuntime` owner; closed owners report explicit
  delivery failures instead of silently dropping consumed bus messages
- `StopAll` and `UnregisterChannel` close and drain delivery outside lifecycle
  state locks
- reload removal drains accepted delivery through `UnregisterChannel` before
  stopping the removed channel transport
- synchronous delivery helpers reject closed owners instead of bypassing the
  delivery owner during unregister
- stream and transient interaction maps live only in `StreamCoordinator`, whose
  finalization path also owns placeholder and tool-feedback cleanup
- startup, shutdown, reload, registration, and HTTP setup share one
  `ChannelLifecycle` transition lock
- repeated startup retries only channels without an active delivery owner and
  launches one dispatcher and one HTTP serve loop per lifecycle generation
- repeated/concurrent shutdown shares one drain and cannot interleave with a
  reload transition

Package tests use owner operations and fixtures rather than promoted Manager
maps. Focused tests cover queue admission/outcomes/retries, stream lifecycle,
transient cleanup, conservative reload, repeated/concurrent start and stop,
reload-versus-shutdown ordering, and shared HTTP startup. Further lifecycle
work must start from a specific unmet runtime need, not a general desire to
refactor.

## Target Invariants

### Delivery Ownership

For a channel name, outbound text and media must pass through one stable
delivery owner. Dispatch should not enqueue directly into a worker that can be
replaced independently of the queue.

Minimum invariant:

- dispatcher resolves a channel delivery owner
- the owner accepts, rejects, or drains the message
- closed/replacing owners must not silently consume and drop messages

### Ordering

For one channel name, messages accepted by the manager must be sent in the same
order they were accepted unless the caller explicitly chooses a different
priority path.

Minimum invariant:

- same-name reload must not publish a new worker that can overtake queued work
  on the old worker

### Shutdown

Shutdown may stop accepting new outbound work, but already accepted work must
either drain or be reported as not accepted. Shutdown must not wait for worker
drain while holding locks needed by send paths.

Minimum invariant:

- detach/mark stopping under lock
- drain outside lifecycle state locks
- stop transport after accepted queues are drained

### Inbound Registration

Shared HTTP handlers should only route to a channel state that can handle the
inbound request. Config presence alone is not enough.

Minimum invariant:

- no handler points at an unstarted or failed channel
- replacement must not unregister the currently valid handler until a new valid
  handler is available, unless the policy is restart-required

## Architecture Options

### Option A: Conservative Restart-Required Replacement

When an enabled channel's config hash changes, reload reports that restart is
required and leaves the current runtime untouched. Operators get a clear status
instead of a risky in-process swap.

Benefits:

- smallest implementation
- avoids silent drops and ordering regressions
- preserves current mental model
- keeps reload useful for non-channel config and simple channel additions

Costs:

- changing Telegram/Slack/etc. credentials or webhook settings requires process
  restart
- no seamless replacement

This is the implemented current policy.

### Option B: Stable Per-Channel Delivery Owner

A `deliveryOwner` now exists per channel name and is registered only through
`DeliveryRuntime`. Dispatch and synchronous sends resolve this owner instead of
looking up independently mutable worker maps.

The owner holds:

- one text queue
- one media queue
- current channel transport
- lifecycle state: accepting, draining, stopped

Replacement, if later supported, happens behind the owner. The queue does not
change, so ordering remains stable. A transport swap can be implemented as:

1. stop accepting new sends only if required
2. drain current queue through old transport
3. stop old transport
4. start new transport
5. resume accepting through the same owner

Benefits:

- real path to correct hot replacement
- dispatch never races a replaceable worker pointer
- ordering can be reasoned about locally

Costs:

- larger refactor than Option A
- must carefully preserve existing send/media/placeholder behavior
- still does not provide durable delivery across process crash

This ownership boundary is implemented. Live same-name transport replacement
is not: the current policy remains restart-required, and any future replacement
design must preserve the stable admission and ordering guarantees above.

### Option C: Full Supervisor Runtime Framework

Introduce supervisors, generations, retry state, inbound registry state, and
readiness state as separate subsystems.

Benefits:

- can model sophisticated lifecycle behavior
- useful if channels need independent long-running recovery policies

Costs:

- too much machinery for the current problems
- high review surface
- easy to recreate PR 88-91 complexity

This is not the recommended next step.

## Recommended Roadmap

### Phase 1: Make Reload Conservative

Status: implemented.

Behavior:

- detect same-name channel config changes
- keep the existing running channel untouched
- return/report "restart required for channel config changes"
- keep live addition behavior only where startup already creates a normal active
  worker
- avoid expanding live removal semantics until Phase 2 defines safe drain

Acceptance criteria:

- a same-name config change cannot remove or replace an active worker
- outbound dispatch behavior is unchanged for active channels
- status/logs make restart-required state visible
- tests cover changed-channel reload leaving the old channel active

### Phase 2: Fix Shutdown/Unregister Draining

Status: implemented for the current restart-required policy.

Behavior:

- avoid holding lifecycle state locks while waiting for worker goroutines
- make accepted-vs-rejected outbound behavior explicit during stop

Acceptance criteria:

- no worker drain happens while holding lifecycle locks needed by send paths
- tests cover in-flight send callbacks during `StopAll` and `UnregisterChannel`
- reload removal drains accepted delivery before stopping the removed channel

### Phase 3: Introduce Delivery Owner

Status: implemented as an ownership boundary, without broadening hot reload.

Scope:

- dispatch and synchronous sends resolve a stable delivery owner
- workers and queues are implementation details of that owner
- same-name active config changes remain restart-required

Behavior:

- dispatch resolves a stable per-channel delivery owner
- workers become implementation details of the owner
- replacement never swaps the queue that dispatch writes to

Acceptance criteria:

- closed/replacing worker pointers cannot drop consumed bus messages
- replacement cannot reorder messages across old/new transports
- tests exercise dispatcher-level delivery, not only direct queue enqueue

### Phase 4: Optional Runtime Supervision

Trigger:

- only start this if reconnect/retry behavior cannot be kept inside channel
  implementations or the delivery owner
- prefer channel-local reconnects before introducing manager-level supervision

Behavior:

- bounded retry state is explicit
- stale retries cannot revive old config
- readiness derives from actual accepting/deliverable state

Acceptance criteria:

- retry cancellation on reload/shutdown is covered
- readiness has one documented source of truth

## What Not To Do Next

- Do not reopen PR 88-91 as-is.
- Do not introduce generations plus worker replacement without a stable queue.
- Do not publish a replacement worker before old accepted work is drained.
- Do not make dispatch ignore closed-worker enqueue failures.
- Do not use HTTP handler registration as a proxy for delivery readiness.

## Decision

R6 is complete. The default action is to stop the channel lifecycle refactor
track and keep the current conservative policy.

If later usage shows that seamless channel config replacement matters, extend
the existing stable delivery owner rather than bypassing it. If runtime
reconnect reliability becomes the concrete issue, prefer channel-local
reconnect behavior first and only introduce Option C-style supervision if local
reconnects cannot model the failure mode.
