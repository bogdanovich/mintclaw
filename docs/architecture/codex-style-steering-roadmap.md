# Codex-Style Steering Roadmap

Status: active implementation roadmap.

This roadmap replaces MintClaw's pending-tool cancellation classification with
Codex-style same-turn steering. The cutover is intentionally incompatible: the
old classification, deferred-result, and reconciliation-prompt contracts are
removed rather than preserved behind compatibility flags.

## Objective

When user input arrives during a regular active turn, MintClaw must preserve the
current model response as one execution unit:

1. Let every tool call already emitted by that response reach its normal result.
2. Preserve one source-ordered result for every emitted tool call.
3. Record the new input as a genuine user message in the same active turn.
4. Start another model iteration with the original objective, completed tool
   results, and new user input visible together.
5. Let the model decide whether to continue, amend completed work, change
   direction, explain progress, or finish.

Steering is not cancellation. Explicit stop and graceful-interrupt paths remain
separate operations with their existing cancellation semantics.

## Why Replace The Current Model

The existing runtime classifies each pending call as `read_only`,
`non_cancellable`, `cancellable`, or `unknown`. New user input causes selected
calls to be replaced with synthetic deferred results, after which a prompt asks
the model to reconstruct the skipped intent.

That design has three structural problems:

- ordinary additive input is treated like cancellation;
- correctness depends on the model noticing and reissuing synthetic calls;
- the policy leaks into every tool implementation, the registry, runtime events,
  diagnostics, prompts, tests, and MCP trust policy.

The failure mode is worse than executing amendable work: a required mutation can
be skipped while the model still reports it as completed. Codex-style steering
keeps history factual and gives the next model iteration real results to amend.

## Runtime Contract

### Input admission

- Input is scoped to the active session and active turn.
- Arrival order is preserved by the existing steering queue.
- The configured drain mode still controls whether one message or all queued
  messages are admitted at a model boundary.
- Media follows the same resolution and persistence path as ordinary input.

### Model and tool boundary

- A model response and the tool-call batch it emitted form one execution unit.
- Steering may be captured while a tool is running, but it does not skip later
  calls in that batch.
- Every emitted call receives its real success, error, suspension, or explicit
  interrupt result. Steering never creates a synthetic skipped result.
- After the batch reaches a normal boundary, queued input is appended and the
  coordinator performs another model iteration.

### Finalization race

Before returning a final response, the coordinator performs the existing final
steering poll. If input arrived after the last tool result or while the model was
producing a direct answer, finalization is deferred and another model iteration
runs in the same turn.

### Explicit interruption

Explicit stop, hard abort, and graceful interrupt are not steering. They may
still prevent unstarted work according to their dedicated lifecycle contracts.
No steering classification API is retained for those paths.

### Human interaction and long waits

- A durable `request_user_input` suspension remains owned by the interaction
  subsystem. Steering that arrives before suspension admission is handled as
  same-turn input without fabricating deferred tool results.
- Future cooperative wake-up support may let selected wait-like tools return
  early when new input arrives. That is a tool-specific wait contract, not a
  generic mutation classification.

## Removal Scope

The implementation removes:

- `SteeringSafety` and `SteeringSafetyProvider`;
- `ToolRegistry.SteeringSafety`;
- all `ToolSteeringSafety` implementations and their dedicated files/tests;
- pending-call steering decision events and diagnostic records;
- synthetic queued-steering deferred tool results;
- prompt instructions requiring the model to reconcile deferred calls;
- tests asserting classification-based finish/skip behavior.

Existing generic skipped-tool support remains where required by explicit abort,
policy rejection, suspension, or other non-steering control flow.

## Delivery Plan

1. Add regression tests that prove a steering message arriving during a batch
   does not suppress later calls and is visible on the next model iteration.
2. Remove steering from pending-tool interruption decisions while preserving
   explicit interrupt behavior.
3. Remove the obsolete classification API, events, prompts, documentation, and
   implementation surface.
4. Validate agent, tool registry, diagnostics, and full repository tests and
   lint.
5. Deploy only after the focused PR is reviewed, approved, and merged.

## Acceptance Criteria

- A two-call batch executes both calls when steering arrives after the first.
- The next model request contains both real tool results followed by the steered
  user message.
- A direct model answer cannot finalize while queued steering remains.
- Explicit interruption tests continue to pass.
- No production reference to steering safety classifications, steering decision
  events, or deferred steering reconciliation remains.
- Session history contains no steering-generated synthetic tool result.
- Documentation describes the deployed behavior without compatibility modes.

## Future Work: Followup And Collect

MintClaw may later add queue policies similar to other agent runtimes:

- `followup`: finish the active turn and process each queued message as a new
  turn;
- `collect`: coalesce a burst and process it as one later turn after a quiet
  window.

Those modes are intentionally outside this roadmap. They require explicit queue
ownership, debounce, capacity, overflow, delivery, and cancellation contracts.
This cutover implements only Codex-style same-turn steering.
