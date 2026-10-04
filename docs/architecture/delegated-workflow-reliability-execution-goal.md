# Delegated Workflow Reliability Execution Goal

## Status and scope

Status: active. PR 1 is in CI/review at #1457. PR 2 implementation is complete
on a dependent branch, with full-suite validation and merge pending. PR 3 is
pending. No production deployment yet.

Fix the shared delegation contract behind the October 3 browser incident.
Do not redesign the browser driver or add site-specific workarounds. The same
contract must apply to gateway and companion execution and to every channel.

The production audit found three connected defects:

1. A conditional authentication handoff became an unconditional objective.
   Successful publication, verification, and close were consequently reported
   as partial, and finalization tried to hand off an already closed session.
2. Partial-result projection exposed internal objective instructions and raw
   bookkeeping instead of a cohesive user-facing answer.
3. Verified incomplete-outcome enforcement restored text after a user-only
   result had already been delivered; outer final delivery sent it again.

The relevant main trace is `trace-turn-e9a2552a7959e2dda308d070`, records 6
and 12. The browser trace is `trace-turn-d704169407821c170e7527b7`; record
220 confirms close before the final repair. Main service logs on October 3,
2026 show two delivered outbounds at 12:28:37 and 12:28:40 PDT. Keep private
trace content, account details, and runtime identifiers out of public evidence.

## Contract

- Required user outcomes, conditional execution steps, resource lifecycle,
  presentation, and delivery ownership are separate concerns.
- Runtime receipts and validated outputs remain the authoritative evidence.
  Natural-language presentation cannot upgrade an unverified outcome.
- Confirmed final delivery is an explicit terminal disposition, not an empty
  string convention. Preserving a deliverable for history, task status, or
  inspection must not authorize sending it again.
- Delivery uncertainty never causes blind resend. Definite pre-acceptance
  failure retains the existing bounded fallback behavior.
- An explicit request for human control requires a real durable handoff.
  Handoff needed only for authentication is conditional; its absence must not
  downgrade an otherwise completed task or reopen a closed session.
- Conditional lifecycle support must not provide a way to silently skip a
  required result or external commit. No free-form condition interpreter,
  workflow DSL, or new state machine is admitted.
- A real handoff still suspends durably, preserves the exact session, and
  restores authority before continuation. Timeout, cancellation, supersession,
  and cleanup remain receipt-backed.
- Ordinary answers are cohesive prose in the user's requested language with
  complete requested facts and public links. Internal checklist labels are
  not user-facing headings. Exact JSON is selected only by explicit intent.
- Presentation runs once on the verified terminal result and never performs
  browser actions. Supporting output must not duplicate the summary.

## Pull-request sequence

### PR 1: Authoritative final delivery

Carry the final-delivery disposition through terminal enforcement,
finalization, the turn result, and outer text/media delivery. Reuse the existing
disposition types rather than adding a parallel delivery ledger.

Acceptance criteria:

- Already delivered success, partial, and blocked results produce no second
  final outbound, with or without a retained text/media deliverable.
- The structured incomplete result survives for canonical history and task
  inspection; suppression does not erase facts or receipts.
- Runtime-owned exact safety terminals retain their intentional precedence.
- Delivery uncertainty remains non-retryable unless delivery is definitely
  known not to have been accepted.
- A regression crosses real terminal completion and outer delivery rather
  than testing each helper in isolation.

### PR 2: Required outcomes and conditional lifecycle

Make the delegation schema distinguish an explicit required handoff from a
conditional handoff. Keep required behavior as the compatibility default.
Restrict any conditional declaration to lifecycle work; required result and
external-action verification cannot be bypassed through it. Preserve the
declaration through durable suspension and continuation.

The caller declares `requirement: "if_needed"` only on a `live_handoff` item,
for example authentication recovery. Omitted requirements and `"required"`
retain mandatory semantics. The child partitions an unnecessary conditional
handoff into `not_needed_items`; a performed handoff instead claims its durable
receipt in `completed_items`, and a needed but unsuccessful handoff remains in
`missing_items`. This is a bounded declaration, not a condition interpreter.

```json
{
  "objective_items": [
    {"item": "Inspect and report both account results", "kind": "result"},
    {
      "item": "Hand over the same session only if authentication is required",
      "kind": "live_handoff",
      "requirement": "if_needed"
    },
    {"item": "Confirm cleanup", "kind": "result"}
  ]
}
```

Acceptance criteria:

- An already-authenticated multi-objective task succeeds without a handoff
  receipt and never schedules a handoff repair after close.
- A user-requested handoff remains required and cannot complete through prose.
- A conditional handoff that actually occurs retains its durable receipt
  across same-session resume and close.
- Missing required data, commits, or verification remain partial/blocked.
- Timeout followed by a new same-route retry cannot reuse expired authority
  or inherit the previous attempt's incomplete state incorrectly.
- Existing declarations and durable records keep their required semantics.

### PR 3: Cohesive verified presentation

Separate validated task data from the ordinary user-facing answer. Preserve
the verified status while allowing normal conversational presentation. Keep
one explicit exact-JSON output path and a concise truthful fallback.

Acceptance criteria:

- Success, partial, and blocked answers do not export objective instructions,
  producer sentinels, internal envelopes, or incidental JSON fragments.
- Independently blocked work is explained naturally without losing successful
  findings, requested fields, public links, or known commit/cleanup facts.
- Requested exact JSON remains one valid document, not mixed with prose.
- An unavailable presentation pass cannot erase the verified result or claim
  unsupported success.
- The final presentation is delivered once through the disposition from PR 1.

## Validation matrix

Use ordinary deterministic unit, integration, race, and restart tests. Do not
introduce trace replay or a general evaluator.

| Scenario | Required observations |
| --- | --- |
| Already authenticated | Multiple required results complete; conditional handoff is unnecessary; session closes; one final answer |
| Authentication required | One prompt; exact-session durable handoff; answer; fresh observation; continuation; close; one final answer |
| Handoff timeout and retry | Truthful terminal timeout; cleanup confirmed; fresh retry and authority; no stale prompt or feedback carrier |
| Two independent results, one blocked | Successful findings retained; actual blocker explained; no false success or leaked checklist |
| Commit outcome unknown | No blind replay; known/unknown effect accurately reported; verification is read-only |
| Final already delivered | Success/partial/blocked and text/media matrices; zero additional sends from outer finalization |
| Definite versus uncertain delivery failure | Correct fallback only before acceptance; no duplicate on ambiguous outcome |
| Requested exact JSON | One valid JSON document with complete requested data |
| Restart or cancellation | Durable disposition and lifecycle remain coherent; no orphaned sessions, prompts, or progress carriers |

The integration boundary must include main, the real delegate tool, child
outcome extraction, terminal completion, outer final delivery, and channel
manager output. Count actual adapter sends and inspect their content; do not
substitute assertions on metadata or helper return values.

Existing browser-capability smoke tests intentionally prescribe one result
objective. Retain those capability checks, but add multi-objective and
conditional-handoff coverage instead of treating them as complete orchestration
acceptance.

## Autonomous workflow and production gate

- Use this agent's isolated `browser-reliability-20260926` worktree.
- Start each dependent PR from latest merged `origin/main`.
- Run focused tests first, broader affected-package tests, formatting, changed
  package lint, and diff/privacy inspection.
- Publish ready PRs and follow the autonomous CI/review/rocket merge workflow.
  Record the initial architecture baseline and use its drift checkpoint.
- Deploy only merged main using the deployed-ops backup, capacity, service,
  health, and rollback gates. Preserve live configuration and active user data.
- Run non-destructive production-equivalent browser/delegation/channel smokes
  and gateway/companion lifecycle checks. Do not create, republish, purchase,
  delete, or edit third-party resources as a test.
- Record exact merged/deployed revisions, test commands, actual send counts,
  lifecycle/feedback cleanup, and safe errors without private page contents.

## Completion and stop rule

Complete the goal only after all three fixes are reviewed, CI-green, merged,
deployed, and accepted with the matrix above and concrete channel output.
There must be no unresolved in-scope defect, duplicate final delivery, orphaned
feedback, or contradictory session claim. A passing isolated smoke or merged
PR alone is not completion.

If fixes require a workflow engine, site/language heuristics, duplicated
delivery ownership, relaxed commit evidence, or repeated cross-layer flags,
perform the autonomous architecture checkpoint before adding more patches.
New browser features and the separate context-compaction warning are outside
this goal.
