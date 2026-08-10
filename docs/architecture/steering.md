# Steering

Steering injects genuine user input into an already-running regular agent turn.
MintClaw uses a Codex-style model boundary: the current model response and its
emitted tool batch finish first, then the new input is included in another model
iteration within the same turn.

## Runtime Contract

```text
User request
  -> model response
  -> emitted tool batch
       tool 1 -> real result
       tool 2 -> real result
       steering arrives and is queued
       tool 3 -> real result
  -> steering is appended as user input
  -> next model iteration decides what to do
```

Steering does not classify, cancel, or replace pending tool calls. The model sees
the completed work and decides whether to continue, amend it, change direction,
reply, or finish. Every emitted tool call keeps exactly one source-ordered result
in history.

Explicit hard abort, graceful interrupt, and stop requests are separate control
paths. They may stop unstarted work according to their own lifecycle contracts;
ordinary inbound user messages do not acquire cancellation semantics merely by
arriving during a turn.

## Model Boundaries

Steering is checked at these boundaries:

1. At loop start, before the first model request.
2. Before tool execution begins, without suppressing the emitted batch.
3. After each tool result, so input is captured promptly while the remaining
   batch continues.
4. After the complete tool batch, before the next model request.
5. After a direct model response and immediately before finalization, so late
   input cannot be orphaned behind a completed turn.

The model never receives steering in the middle of an in-flight provider
request. Instead, the current response reaches a normal boundary and the queued
input is included in the next request.

## Scoped Queues

Steering queues are isolated by resolved session scope, usually the routed
session key such as `agent:<agent_id>:...`.

- An active turn reads only its own scope.
- Input from another chat, topic, DM peer, or routed agent cannot enter it.
- `Steer()` outside an active turn uses the process-level fallback queue exposed
  by the public Go API.
- `Continue()` checks scoped input first and then the fallback queue.

## Queue Drain Configuration

`agents.defaults.steering_mode` controls how many already queued messages become
visible at one model boundary. It does not control tool execution.

```json
{
  "agents": {
    "defaults": {
      "steering_mode": "one-at-a-time"
    }
  }
}
```

| Value | Behavior |
|---|---|
| `one-at-a-time` | Dequeue one message per model boundary. Later messages remain ordered for later iterations. |
| `all` | Drain all currently queued messages into the next model iteration in FIFO order. |

The environment variable
`MINTCLAW_AGENTS_DEFAULTS_STEERING_MODE` provides the same setting.

## Go API

### Steer

```go
err := agentLoop.Steer(providers.Message{
    Role:    "user",
    Content: "also include the attached correction",
})
```

`Steer` enqueues input and returns an error if the queue is full or unavailable.
Input accepted for an active turn remains part of that turn.

### Continue

```go
response, err := agentLoop.Continue(ctx, sessionKey, channel, chatID)
```

`Continue` starts work for queued input when the agent is idle. It resolves the
agent from the session key and avoids dequeuing the same message twice.

## Inbound Scheduling

The shared message bus preserves per-session serialization:

1. A message for an idle session starts a worker turn.
2. A message for the same active session enters that turn's steering queue.
3. Messages for different sessions may run concurrently up to
   `max_parallel_turns`.
4. Non-routable system messages retain their dedicated synchronous handling.

This makes rapid same-chat input one coherent turn while preserving concurrency
between unrelated conversations.

## Steering With Media

Steering messages retain their `media://` references in canonical history. At
the next provider boundary they use the same media-resolution path as initial
input:

- image references become provider-compatible multimodal inputs;
- non-image media is resolved through the normal attachment pipeline;
- missing durable media is represented honestly as unavailable;
- mixed text and media preserve their original order and session ownership.

## Human Interaction Boundary

`request_user_input` owns a durable suspension lifecycle. If normal input arrives
before suspension admission, MintClaw does not create a stale second question:
the call receives a normal error result explaining that input arrived first, the
dependent tail is paired as skipped, and the new user message is processed at
the next model boundary. Once a suspension is durable, its interaction routing
rules determine how an answer resumes the turn.

## Observability

Runtime events and diagnostic traces record:

- steering enqueue/injection;
- real tool start, result, error, suspension, or explicit skip;
- the next model request and response;
- final turn outcome.

There is no pending-tool steering-decision event because steering no longer
makes per-tool execution decisions.

## Future Queue Policies

Potential `followup` and `collect` modes are documented but not implemented in
[Codex-Style Steering Roadmap](codex-style-steering-roadmap.md). Current
MintClaw behavior is same-turn steering only.
