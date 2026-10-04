# Delegated Workflow Reliability Execution Goal

## Status and scope

Status: implementation and acceptance complete. PR 1 merged as #1457
(`5a58ce0fa`), PR 2 as #1461 (`203fb0ff0`), and PR 3 as #1464 (`d97157636`).
Each code PR passed current-head CI and review and received authorized merge
approval.

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

Reuse the producer's existing `result` field as the complete answer; do not
introduce another model call, renderer agent, or semantic evaluator. Retain
it as `user_summary` only after the reported status matches the verified
outcome and every output value, link, and ID survives a conservative literal
coverage check. Incomplete answers must also retain the specific blocker.
This is a data-preservation guard, not an independent natural-language fact
checker. Keep canonical outputs and receipts unchanged and persist the
selected presentation with them. If coverage fails, project retained data
without objective-instruction headings. Supporting JSON becomes field/value
lines; explicitly requested exact JSON keeps its dedicated transport path.
The same retained-data fallback applies to succeeded mixed action/result
tasks: rejected producer prose is never recycled as a success summary.
Carry the bounded root request as presentation-only evidence for ordinary
result objectives as well as handoffs, so an internally translated task does
not override the user's language. It does not grant additional authority.

The transport acceptance test also covers child feedback queued behind a
rate limit. Preserve the exact turn generation when dismissing feedback,
even when no progress message has reached admission yet. Reuse the existing
terminal-generation mechanism; do not add a second cleanup state machine.
Late progress from that completed turn must be suppressed, while progress
from a genuinely later turn in the same durable session remains available.

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

## Implementation and acceptance record

### Merged changes

- [PR 1457](https://github.com/bogdanovich/mintclaw/pull/1457) preserves
  already-handled terminal delivery ownership through retained outcomes and
  outer text/media finalization.
- [PR 1461](https://github.com/bogdanovich/mintclaw/pull/1461) makes
  authentication-only handoff conditional and binds durable handoff evidence
  to the correct required objective. One substantive review-fix cycle covered
  mixed conditional and required handoffs.
- [PR 1464](https://github.com/bogdanovich/mintclaw/pull/1464) selects a verified
  complete answer, preserves exact JSON, and seals late queued feedback with
  the existing terminal generation. One substantive review-fix cycle removed
  the legacy success renderer so rejected producer prose cannot reappear in
  mixed action/result fallback.

Scope stayed within the declared delegation, presentation, and delivery
contracts. The review fixes converged by removing a duplicate rendering path;
they did not add a workflow engine, model pass, browser driver, access
restriction, or site exception.

### Deterministic verification

The final reviewed code passed the full agent package (318.719 seconds),
affected channel/protocol/task/interaction packages, tagged lint, formatting,
and documentation lint. Focused race regressions include:

- Twelve real parent/delegate/child/manager cases: Telegram and Discord
  recording adapters, direct and durable delivery, and succeeded, partial,
  and blocked outcomes. Each produced exactly one cohesive final adapter
  send; canonical outcomes and summaries remained intact.
- Six already-handled text/media cases: zero additional outer sends.
- Four gateway/companion by Telegram/Discord first-party lifecycle cases:
  browser close completed, conditional handoff was unnecessary, no post-close
  handoff repair occurred, one final send, feedback created, and zero active
  feedback carriers after completion.
- Mixed required/conditional receipt ownership, durable suspension/restart,
  timeout followed by fresh retry, authority loss, and no repeated completed
  handoff or external mutation.
- Conservative presentation coverage for every retained value, nested JSON,
  URLs, prices, IDs, blockers, and runtime downgrades. Incidental JSON is not
  promoted; explicitly declared exact JSON remains one document.

These exercise the actual manager and recording adapters, not live Telegram
or Discord network APIs. Production CLI smokes below use the running gateway's
native MintClaw channel, which intentionally suppresses Working feedback.
Therefore those smokes are not evidence of a live Telegram deletion request.

After another agent deployed merged main `1299057e4`, the contract/feedback
race suite passed again (agent 25.911 seconds; channels 1.799 seconds). The
handoff/timeout/restart suite also passed again (agent 15.616 seconds; tools
2.979 seconds; browser 1.681 seconds). Both revisions contain all three fixes.

### Live deployment and smoke matrix

The reviewed merge `d97157636` was built, validated, installed, and rolled
through the five gateways and launcher on October 4, 2026. A stateless main
response and a new diagnostic trace confirmed the runtime pipeline. A
concurrent deployment later advanced production to `1299057e4`; effective
gateway and launcher executable digests and embedded revisions were verified
against that deployment. No unrelated console, desktop, or companion service
was restarted by this work.

All smokes use first-party tools, the managed profile, public fixture pages,
and exactly one parent `delegate` call with `delivery_mode=user_only`. Ordinary
multi-page cases retain multiple independently blockable results; exact JSON
and the single cleanup probe keep their declared aggregate result. No account mutation,
purchase, listing publication, logout, or cookie clearing is used as a test.

| Live scenario | Gateway | Configured companion |
| --- | --- | --- |
| Two pages plus cleanup; conditional authentication | Succeeded; handoff not needed | Succeeded; handoff not needed |
| Explicit human handoff, same-session resume, fresh observation, second navigation | Succeeded; one prompt and one final answer | Blocked truthfully: this headless target advertises no handoff capability; session closed |
| First page succeeds; reserved `.invalid` page unavailable | Genuine partial; first findings and blocker retained | Genuine partial; first findings and blocker retained |
| Requested exact JSON | One valid object, no appended prose/footer | One valid object, no appended prose/footer |
| Reopen profile, observe blank page, close | Succeeded | Succeeded |
| Plain request without prescribed objective fields | Succeeded without forced handoff | Not separately run |
| Prose from independently accepted structured facts | Verified summary selected | Verified summary selected |

The companion's negative handoff case is not a successful live resume test.
Headed capability is required to test human control on that target; this fix
does not change deployment display preferences. Supported handoff mechanics
remain covered by the broker/tool/runtime tests, and real gateway handoff
verifies the durable continuation path.

For every accepted run, the authoritative task record and delivered response
are compared. Successful and genuinely partial runs retain their distinct
verified statuses. Unnecessary authentication is recorded as `not_needed`
when declared; an explicitly required unsupported handoff remains missing.
Normal runs have one positive final send. The gateway handoff has one prompt
and one final send, one resolved interaction, one final-delivery ID, and the
same browser resource ID for handoff, resume, and close. Empty control/progress
events are not counted as final answers.

Thirteen live cases completed their applicable positive or negative capability
gates. Each has one verified delegation, one retained canonical task, matching
delivered content, a terminal cleanup receipt, and zero active case interactions.
Across them there are thirteen final sends and one explicit handoff prompt;
all selected diagnostic envelopes are valid and untruncated.

The invalid-origin cases intentionally reach terminal browser state `lost`
with `close_state=already_closed`: network policy has already released the
worker. Cleanup receipts and subsequent profile reuse confirm release; this
is not reported as successful inspection of the unavailable page. One
companion observation needed a fresh retry; the retained outcome comes from
the successful observation, not the failed attempt.

Presentation coverage deliberately rejects paraphrased standalone prose when
it cannot preserve that entire retained output literally. These smokes cover
both admitted summaries and the compact retained-facts fallback. That fallback
can remain a factual list; it no longer exports objective instructions,
Completed/Not completed bookkeeping, or supporting JSON. This guard preserves
data; it is not a semantic fact checker or a guarantee of one fixed writing
style.

Unaccepted setup attempts are retained separately: one invalid model-generated
acceptance declaration, one concurrency-slot timeout while another browser run
was active, and one companion run interrupted by the concurrent deployment.
They are not counted as passes. Browser work is serialized for the accepted
retries; no historical task or diagnostic trace was edited.

### Operator retest

Use the deployed CLI, not a standalone agent process:

```sh
mintclaw agent live --json --trace-evidence-agent browser --timeout 420s \
  --config /home/server/.mintclaw/main/config.json --message 'PROMPT'
```

A safe ordinary prompt is:

```text
Call delegate exactly once for agent_id=browser, delivery_mode=user_only.
Use only first-party browser tools on gateway/managed.
Check https://example.com/ and https://example.org/ in the same session.
Report both actual final URLs and titles, and close the session.
Hand over control only if authentication is required; otherwise do not interrupt.
```

Repeat with `companion/managed` for that boundary. In explicit declaration
tests, acceptance belongs only to result items; `min_items` and
`required_fields` belong only to records acceptance. Use `requirement=if_needed`
only for conditional `live_handoff`, never for required data or commits.

For a harmless explicit gateway handoff test, require a unique marker in the
handoff question and use matching `--auto-answer-question continue` and
`--auto-answer-question-match MARKER` options. Never use automatic answers for
credential entry, purchases, publication, or business approvals. Confirm
`execution_evidence.status=verified`, one parent delegation, fresh post-resume
observation, actual close state, and authoritative task/interaction records.
CLI transport `outcome=success` alone does not mean every task objective
succeeded.

Private, bounded acceptance evidence and checksum-verified rollback artifacts
are retained on the deployment host. Rich diagnostic traces stay in their
existing private stores and are not attached to this report. Live profile
configs, browser access modes, cookies, credentials, workspaces, and harness
source are preserved. Doctor load errors are zero for all five profiles;
pre-existing policy findings are reported separately, not silently changed.
All twelve expected services are active, failed-unit and legacy-process counts
are zero, and the final ten-minute error-level journal window is empty.
Operation-owned remote staging and the isolated local package cache are
removed; the reusable agent worktree, private acceptance evidence, and verified
pre-change rollback set are retained. Other agents' recovery artifacts and
shared runtime/build caches are not treated as disposable operation-owned data.
