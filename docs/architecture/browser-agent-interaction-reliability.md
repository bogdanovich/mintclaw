# Browser Agent Interaction Reliability

## Status

This document records the implementation scope for the browser-agent reliability
incident observed on 2026-08-18 in the deployed `main` profile. The work is
split into focused pull requests and is complete only when every invariant and
end-to-end gate below passes.

The previously deployed durable-interaction reply-target fix remains valid and
is not reverted by this work. In the affected run, approval records resolved
normally and retained their Telegram response message IDs. The failures below
occurred after that lifecycle fix.

## User-visible symptoms

1. Plain-language guidance sent while an approval was waiting was parsed as an
   invalid `allow_once` or `deny` answer instead of steering the active browser
   task.
2. A later main-agent reply claimed that the guidance had been forwarded even
   though no forwarding action occurred.
3. The browser agent repeatedly searched the active-postings view instead of
   widening the collection to all or old postings. It requested six approvals
   for the same search action without useful progress.
4. A recoverable stale browser authority ended the workflow instead of causing
   one bounded, safe observation refresh and replanning step.
5. A two-item task was represented as completed even though only one item was
   published and verified.
6. `Working...` tool-feedback messages were recreated around every approval
   continuation. Some old carriers remained in Telegram as partial fragments.
7. The browser specialist did not use visual fallback. Every observation in
   the failed search loop used `screenshot:false`; deployed specialist guidance
   also incorrectly described screenshots as unsupported even though B2
   screenshot capture is available.

## Evidence and first divergences

The browser task used one durable task session across browser turns 30 through
62. Traces `trace-turn-c301a30a6e275ea0255b5cc3` through
`trace-turn-4cfbbabd567a46db4807f453` show repeated observe/action/suspend cycles,
and `trace-turn-22b7f81e4f38967606cd7989` records the terminal partial result.
The durable interaction registry recorded each approval as resolved; there was
no recurrence of a stuck `resuming` interaction.

The first task-level divergence was choosing and then repeating account search
after returning from the successful Yakima publication instead of inspecting a
broader posting collection. The runtime-level divergence was routing ordinary
text through approval-answer parsing while the interaction was waiting.

The tool-feedback lifecycle has an additional deterministic cause:

- its coordinator key includes a turn trace scope;
- every durable approval continuation creates a new turn;
- suspension dismisses the current turn's carrier;
- the approval prompt is also treated as terminal content for tool feedback;
- deletion is best-effort, cleanup state is in memory, failures are not logged,
  and pending cleanup expires after 30 seconds.

This design guarantees message churn across approval continuations. A failed or
late Telegram deletion can therefore leave an orphan, but the historical logs
do not retain the exact Telegram deletion error. That last failure mode is
consistent with the observed fragments but is not proven for a particular
message ID.

## Required invariants

### Approval-aware steering

- Only an interaction button, an explicit `/answer`, or an exact accepted
  token is parsed as an approval answer.
- Other authorized text safely supersedes the exact pending action, does not
  grant authority, and is durably steered to the interaction's originating
  continuation session and task.
- The superseded action is never dispatched.
- The originating agent sees the correction before choosing its next tool.
- Runtime-generated acknowledgement text reports only a routing transition
  that actually committed.

### Tool-feedback continuity

- One logical session/task owns at most one editable progress carrier even when
  it crosses many approval turns.
- Suspension pauses the carrier and its animation; it does not delete and
  recreate the carrier.
- Approval and question prompts do not finalize the carrier.
- Resume edits the same carrier in place.
- Final success, failure, cancellation, expiry, or task termination cleans the
  carrier exactly once.
- A send/edit/delete racing with pause or finalization cannot orphan a newly
  created carrier.
- Retryable Telegram cleanup failures retain ownership until success or a
  visible terminal diagnostic; idempotent "message not found" is success.
- Cleanup exhaustion is logged with the scoped key and safe message identity.

### Browser recovery and progress

- A stale top-level context observation may perform one read-only refresh
  without replaying a browser mutation.
- Frame-specific stale context requires a fresh context listing.
- `browser_act` is never automatically replayed after stale authority.
- Repeated semantic actions ignore ephemeral snapshot identifiers when
  detecting no progress.
- A third equivalent action on unchanged page state is rejected before another
  approval is created and returns a structured replanning instruction.
- Standard, provable GET navigation may be classified as read/navigation;
  POST, script-driven, or ambiguous submissions remain approval-bound.

### Browser perception and planning

- The specialist first checks whether the current collection scope can contain
  the requested object. An inactive, expired, deleted, archived, or historical
  target must cause inspection of all/old/history/archive navigation before a
  repeated search in an active-only view.
- One empty or mismatched search triggers scope inspection and replanning.
- When accessibility data is truncated, ambiguous, or has produced repeated
  no-progress actions, the specialist captures one bounded screenshot.
- Screenshot media is available to the next model iteration as protected,
  current-turn visual input. User delivery remains explicit and idempotent.
- Deployed specialist guidance describes the current B2 capability instead of
  the obsolete B1 limitation.

### Honest task outcomes

- Runtime execution completion is distinct from objective completion.
- Child results expose `succeeded`, `partial`, or `blocked`, completed items,
  missing items, and supporting receipts.
- A task with an uncompleted required item cannot appear as objectively
  succeeded.
- Parent replies derive forwarding and external-action claims from committed
  runtime receipts rather than unsupported prose.

## Pull-request sequence

1. `fix(interactions): route steering across pending approvals`
   - classify explicit answers separately from ordinary text;
   - safely supersede the pending action;
   - enqueue steering for the originating continuation;
   - add durable resume and no-dispatch regression coverage.
2. `fix(channels): preserve tool feedback across approvals`
   - use stable logical-session ownership;
   - add pause/resume semantics;
   - exclude interaction prompts from terminal cleanup;
   - classify, retry, and log Telegram cleanup outcomes;
   - add suspension/resume/finalization race tests.
3. `fix(browser): recover stale observations safely`
   - add bounded read-only top-level recovery;
   - retain fail-closed mutation behavior;
   - add context and authority regression tests.
4. `fix(browser): replan stalled collection searches`
   - add semantic no-progress detection across approval continuations;
   - expose page-progress signals;
   - admit only provable GET navigation without approval;
   - update specialist planning guidance.
5. `feat(browser): expose protected screenshots to the model`
   - attach retained screenshot media to the current tool-result turn;
   - preserve owner, route, size, and durability boundaries;
   - update deployed B2 specialist guidance and add multimodal tests.
6. `fix(tasks): distinguish partial objective outcomes`
   - add a structured child objective result;
   - project it through task status and async delivery;
   - ground parent-facing action claims in receipts.

Dependent pull requests start from the merged predecessor or a fresh current
`origin/main` as appropriate. Each code PR requires targeted tests, formatting,
changed-package lint, required CI, review, and repository-owner rocket approval
before merge.

## End-to-end completion gate

A fixture must exercise one logical browser task across multiple approval
continuations:

1. publish and verify one item;
2. reach an approval for an unproductive active-only search;
3. receive plain-language steering to use all/old postings;
4. cancel the unapproved search without dispatch;
5. deliver the steering to the browser continuation;
6. reuse one paused tool-feedback carrier;
7. navigate to the broader collection and find the inactive item;
8. recover once from a stale read authority if injected;
9. use protected visual fallback when the accessibility fixture is ambiguous;
10. request approval only for the final external commit;
11. report `succeeded` only when both requested items are verified;
12. remove the single feedback carrier at terminal delivery with no orphan.

The initiative is not complete when only prompts are changed. The routing,
authority, progress, cleanup, and task-status guarantees must be enforced by
runtime state and covered by deterministic tests.
