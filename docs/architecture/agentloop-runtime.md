# AgentLoop Runtime Host

`pkg/agent.AgentLoop` is the runtime host for an agent workspace. It owns shared
services and public APIs, but it should not contain turn-step execution logic.
One logical agent turn still starts at `runAgentLoop`; detailed turn execution is
delegated to `Pipeline`.

## Responsibility Split

| Layer | Owns | Should Not Own |
| --- | --- | --- |
| `AgentLoop` | process lifecycle, service wiring, registry/config access, public APIs, channel/bus integration | live turn registries or LLM/tool iteration mechanics |
| `turnRuntime` | turn admission, active session and route claims, pending stops, request tracking, sequencing, inbound spool settlement, current runner generation | process reload policy or LLM/tool iteration mechanics |
| `interactionCoordinator` | process-lifetime interaction registry cache, resolution callbacks, resume-flight deduplication, workspace catalog serialization, recovery admission | generation-specific delivery wiring or turn execution |
| `taskCoordinator` | process-lifetime task registry creation and reconciliation, terminal-task context, delivery state, async-completion admission | transport delivery, parent synthesis, or generation-specific wiring |
| `inboundTurnCoordinator` | inbound scheduling, same-session serialization, busy-session steering enqueue, worker goroutine lifecycle, ack/release decisions | route normalization or LLM/tool execution |
| `runtimeSessionClaim` | atomic session placeholder claim/release semantics shared by inbound workers and recovery | turn execution, routing, delivery |
| `inboundMessageTurn` | normalized inbound route/session/model/dispatch envelope for one message | command handling or pipeline execution |
| `turnRunner` | one runtime generation's turn lifecycle, config snapshot, interaction components, and pipeline wiring | process reload policy or LLM/tool iteration mechanics |
| `Pipeline` | context assembly, LLM calls, tool loops, steering injection during a turn, finalization | inbound bus scheduling or session claiming |

## Inbound Flow

1. `AgentLoop.Run` initializes runtime services and reads inbound/observed bus
   channels.
2. Inbound messages are delegated to `inboundTurnCoordinator`.
3. The coordinator resolves the message's steering target. Non-routable/system
   messages stay synchronous.
4. For routable messages, the coordinator claims the session through
   `runtimeSessionClaim`.
5. If the session is already active, the message becomes queued steering for
   that session, except `/stop`, which is handled immediately.
6. If the claim succeeds, a worker goroutine acquires the worker semaphore,
   handles pending stop state, runs `runTurnWithSteering`, and then acks or
   releases the inbound spool entry.
7. `runTurnWithSteering` calls `processMessage`, then drains queued steering
   continuations for the same session before publishing the final response.

This keeps same-session messages serialized while allowing different sessions
to run concurrently up to the configured worker limit.

`AgentLoop.Run` constructs one run-scoped `inboundTurnCoordinator` before the
receive loop and reuses it for every inbound message. The coordinator has no
reload-generation state, so retaining another copy on `AgentLoop` would only
duplicate ownership.

## Turn Launch Boundary

`processMessage` is the last host-side step before turn execution. It prepares
and logs the inbound message, handles system messages, builds an
`inboundMessageTurn`, processes commands, applies pending skill overrides, and
then calls:

```go
al.runAgentLoop(ctx, turn.Agent, opts)
```

`runAgentLoop` remains the boundary for one logical agent turn. The loop's
`turnRuntime` selects the current `turnRunner`; that runner wraps the turn with lifecycle
concerns and delegates turn progression to:

```go
pipeline.runTurnLoop(ctx, turnCtx, ts)
```

Code below that boundary should be considered turn execution and belongs in
`Pipeline` or pipeline-owned helpers.

`AgentLoop` constructs one `turnRuntime` during initialization. That owner
constructs one runner generation with one reasoning publisher, feedback
publisher, synchronous delivery component, asynchronous delivery component,
interaction runtime, and pipeline snapshot. It atomically replaces that whole
generation when config or injected runtime wiring changes. Admitted turns
retain their original runner; new turns use the new generation. This avoids
rebuilding dependency bags for every user and child turn without allowing a
reload to mutate an in-flight pipeline.

In-turn events, aborts, steering polls, sensitive-data filtering, and final
render policy use that pipeline snapshot directly. `turnRunner` uses its
concrete `turnRuntime` for admission, active-turn registration, and inbound
spool settlement. Child tools receive their registered child runner explicitly;
the turn context no longer publishes `AgentLoop` as a service locator. There is
no single-implementation callback interface between the runner and pipeline.

Config-derived turn behavior reads that runner-owned `Cfg` snapshot directly.
It does not pass through policy interfaces for model selection, retries,
streaming, media limits, prompt construction, or tool-content filtering. Keep a
seam only when the runtime has a real alternate implementation, such as the
deterministic retry sleeper used by timing tests.

Pipeline runtime wiring is also direct. Bus and event emission retain narrow
interfaces because tests and alternate buses provide real implementations.
Active-request counting and turn abort use their concrete owners; they do not
sit behind a service bag or single-implementation interface.

`NewAgentLoop` constructs the turn runtime, background-compaction runner, and
model-execution manager exactly once. The active-request counter belongs to the
turn runtime; there are no lazy accessors that create alternate owners after
construction. Event emission likewise constructs its small concrete adapter at
the wiring boundary rather than through an `AgentLoop` factory.

Pipeline context wiring keeps its semantic grouping, but not artificial
interfaces. Context assembly, steering, and media resolution retain narrow
interfaces because they have real substitutes. Background compaction, model
execution, and terminal-task context use their concrete generation owners so
the immutable pipeline snapshot remains explicit across reloads.

Pipeline interaction wiring follows the same rule. Reasoning, feedback,
synchronous delivery, hooks, and suspension retain interfaces because tests
provide real substitutes. Fallback execution and asynchronous tool completion
use their concrete generation owners; fallback-attempt observation is a direct
capability rather than an optional type assertion.

The runner constructs each concrete interaction component once and gives the
pipeline interfaces references to those same instances. Host-side recovery,
task completion, final-response cleanup, and subturn admission use the current
runner's concrete component instead of asking `AgentLoop` to reconstruct one.
Prompt delivery stays on the interaction runtime, so suspension does not call
back through `AgentLoop` merely to rediscover its owner.

Interaction durability has a different lifetime from delivery wiring. One
process-wide `interactionCoordinator` owns registry caching, resolution
callbacks, resume-flight deduplication, catalog serialization, and recovery
admission. Each runner generation receives a human-interaction component that
references that stable coordinator. Config reload therefore replaces delivery
behavior without copying or resetting mutable interaction state.

Task state follows the same process lifetime. One `taskCoordinator` owns task
registry creation, restart reconciliation against the concrete interaction
owner, terminal-task prompt context, durable delivery decisions, and
async-completion admission. The pipeline receives that coordinator directly;
it no longer receives `AgentLoop` as a terminal-task service locator. Async
delivery also uses the coordinator and the generation's concrete user-delivery
component directly instead of five host callbacks.

Reasoning publication is owned by the pipeline's concrete component. There is
no test-only `AgentLoop` component factory or reasoning pass-through façade;
component behavior is tested at its actual owner.

Synchronous tool-result delivery is generation-owned pipeline wiring. The
unused `AgentLoop` delivery factory and pass-through are absent, so there is no
second host-side route to the same component.

## Session Claiming

`runtimeSessionClaim` is the shared session ownership primitive. A claim stores
a setup placeholder in `turnRuntime.activeTurnStates` with `LoadOrStore`, so two
messages for the same session cannot both launch workers during setup.

Claims release only the exact placeholder they installed. This is intentional:
if a real `turnState` or a newer placeholder replaces the original value, stale
cleanup must not delete it.

Current claim users:

- inbound workers, including context-cancel cleanup while waiting for the worker
  semaphore
- pending-stop cleanup before draining queued steering
- unanswered-session recovery

## Recovery

Unanswered-session recovery scans durable session history for sessions ending in
an unanswered user message. When recovery launches a turn, it now claims the
session through the same `runtimeSessionClaim` primitive used by inbound
workers.

Recovery still builds its own `processOptions` because the unanswered user
message is already durable history and must not be appended again as a fresh
user message.

## Intentional Coupling

Some coupling remains by design:

- `/stop`, `/subagents`, hard abort, recovery, and runtime observability all
  use the one `turnRuntime` registry composed by `AgentLoop`; none owns a
  parallel map.
- `processMessage` still handles commands before `runAgentLoop` because commands
  need routed session/model context but should not always start an LLM turn.
- `runTurnWithSteering` still publishes the final joined response after
  continuation drains. This preserves current delivery behavior while keeping
  in-turn steering injection inside `Pipeline`.
- `inboundMessageTurn` still lives in `pkg/agent` rather than a new package
  because it depends on agent registry, routing, session allocation, and model
  binding internals. Moving it would create an artificial package boundary.

Future refactors should preserve this rule: host/runtime code may decide when a
turn starts and how it is observed or delivered, but the turn's internal LLM and
tool progression belongs to `Pipeline`.
