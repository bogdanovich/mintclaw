# PDF Intake: Explicit Clarification And Background Draft Goal

This goal was cleared, not completed. The operator approved a different history
policy on 2026-10-26: all ordinary form and unrelated conversation belongs in
normal chat history, with verified values also kept in the structured draft.
Continue from [the replacement goal](pdf-chat-history-and-draft-goal.md).
The stricter live-only requirements below are historical where they conflict
with that explicit decision; native field and execution-authority checks remain.

## Operator Outcome

A form is a retained draft, not a modal chat session. Ordinary conversation,
greetings, pings and unrelated questions can proceed without a special task-switch
command or mandatory “task or form?” choice. Returning in ordinary language
resumes the same draft/source and accepted facts. A clarification question is
allowed for genuine uncertainty, but must not capture every subsequent message
as its answer or confer authority over a different pending request.

An explicit clarification can reject an earlier candidate association without
assigning a replacement value. “That number belongs only to the borrower; the
contact's number is not supplied” settles the earlier ownership ambiguity while
leaving the contact field genuinely missing, not blanked, N/A or inferred.
This is a semantic example, never a production phrase/field rule.

## Evidence And Boundaries

Actual Telegram trace `trace-turn-14e8a0af40516fc11c87b8c5`, sequence 9,
accepted the borrower telephone and city at revision/ledger 5/4 but retained
`field_ambiguous` for the other person's telephone. The preceding trace
`trace-turn-cc11099b393edd6b1787ba75` had accepted both names. Both are complete,
untruncated traces from `cf8bf5dc5beff1a0ef92487077f981e3ca3072ee`.
The explicit negative clarification did not resolve the old candidate.

The later ordinary “test” message produced a mandatory task-scope interaction,
including attachment wording despite the absence of a new attachment. The
operator explicitly requests background-draft conversation rather than this
modal behavior. Preserve privacy instead of simply disabling protected intake.

The actual Telegram job `form_job_7bfd647d2ae063c8640222a69de4b724`, its pending
question, historical intake/I-134 jobs and their limits/retention are read-only
evidence. Do not replay, cancel, answer, reset or repair them for qualification.
Use fresh owned synthetic sessions and clean up only those test jobs/copies.

## Implementation Sequence

1. Repair semantic candidate exclusion through the existing private interpreter
   and encrypted native intake. Require exact evidence and stable field/input
   identity; reject stale, wrong-owner and unsupported exclusions. Do not hide
   invalid/conflicting facts or mutate an excluded field's value/disposition.
2. Repair non-modal routing through the existing coordinator, native task routes
   and live-context boundary. Confident unrelated intent can continue without an
   explicit “new task” phrase. Preserve original attachment/request bindings and
   let unrelated conversation bypass an outstanding form-scope clarification
   safely; an unrelated reply is not consent to execute its original request.
3. Merge, deploy, qualify the combined behavior with native readback and a real
   model, document actual evidence/remaining limits and provide a short manual
   Telegram follow-up. Documentation alone is not completion.

Each code PR is a focused reviewed merge unit. Reassess architecture before
adding another planner, task framework, registry, queue, service or value store.
Reuse the existing transaction and immutable identities; do not replace them
with heuristic correlation, language-specific rules or a growing prompt-only fix.

## Done Criteria

- Reproducing tests on independent schemas and RU/EN evidence show both names,
  borrower-only telephone and city accepted, the excluded field still missing
  with no user assignment/intentional blank/N/A, and the old association no
  longer presented as ambiguous. Other pending facts and real conflicts survive.
- Explicit exclusion is bounded, exact-evidence- and input/revision-bound,
  atomic, restart/replay-safe and absent from ordinary persisted context.
  It never resets extraction limits, accepts an arbitrary model field label or
  manufactures output/review/approval authority.
- Executable coordinator/native tests cover casual pings, unrelated questions
  without switch keywords, ordinary draft return, retained facts, follow-up
  clarification while a scope question is pending, ambiguous attachments,
  mixed private facts, invalid/uncertain classification, stale consent and replay.
  No unrelated message is automatically copied into a form field or into the
  wrong pending task; genuinely ambiguous private content stays protected.
- All in-scope code heads pass local formatting/lint and appropriate normal/race
  tests, exact-head CI, substantive review and authorized rocket before normal
  merge through `mintclaw-autonomous-pr`. Docs-only merges use standing policy.
- Merged source is deployed through `mintclaw-deployed-ops`, preserving live
  configs/data, with verified backup/rollback, actual executable/source identity,
  healthy services and an exact-marker response with a new diagnostic trace.
- Fresh authenticated gateway/model qualification proves native values/person
  isolation, corrected ambiguity state, ordinary unrelated conversation without
  modal task selection, safe pending-question behavior, same-draft return,
  source immutability, bounded privacy scans and owned synthetic cleanup.
- Merge dated evidence and a short actual-Telegram scenario. Automated gateway
  qualification is not a Telegram upload/callback pass; operator acceptance stays
  explicitly pending until actual replies/traces are inspected and does not block
  this bounded automated implementation goal.

## Stop Scope

Do not admit full PDFI4/I-134/XFA completion, a general task stack, backend work,
provider/compaction repairs, unrelated infrastructure cleanup or approval bypass.
If preserving the outcome requires a materially different architecture, stop
patching and perform the autonomous-flow architecture checkpoint first. Do not
declare completion from an admission document, first question or plausible prose.
