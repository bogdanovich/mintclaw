# Natural Task Routing During Protected Form Intake

## Product Contract

An active form must not trap an ordinary conversation or require the user to
remember `/cancel_form`. Explicitly switching tasks preserves the previous
draft and accepted facts. A new ambiguous attachment prompts a meaningful
choice with buttons and a free-text answer; it must not silently cancel the
old form, overwrite its source or repeatedly ask the user to upload the file.
Ordinary clarification such as assigning a previously supplied telephone to
the borrower and adding a city remains input to that same form.

This repairs conversation routing, not the PDF backend, I-134/XFA qualification
or every task-management workflow. It admits no form-/language-specific phrase
parser, new service, second value store, approval bypass or extraction-budget
reset. Explicit cancellation remains available, but leaving a task does not
imply consent to erase its protected data.

## Implementation Sequence

1. Preserve closed failure categories from private interpretation to native
   progress. The actual Telegram trace
   `trace-turn-2770bfdd04277421f2ae55f2`, sequence 9, retained the 194-byte
   input with zero accepted values at revision 1, but collapsed the cause to
   `interpretation_unavailable`. Its precise historical cause is unknown;
   do not call that proven temporary or blame concurrent compaction.
2. Implement a bounded document-owned routing transition. Reuse encrypted
   native form state, protected interpretation, authenticated media ownership,
   existing question/answer controls and runtime continuation. Preserve the
   old draft and provide a deliberate way to return to it. Keep task-selection
   uncertainty separate from missing field data.
3. Qualify the combined deployed behavior with harmless synthetic requests,
   native readback, source/privacy checks, cleanup and a short Telegram guide.

Every implementation PR must include working code and regressions. This
contract or its merge alone does not complete the work.

## Required Invariants

- Owner, chat/topic/route, source, schema and revision fences still apply.
- An already guarded input is never replayed raw into ordinary history just
  because a form was paused, canceled, replaced, missing or expired.
- Only the newly authorized task's input/media can enter its continuation;
  never copy earlier protected form values into another task or person.
- New attachments have authenticated ownership. Choosing another task cannot
  substitute an unrelated file or a later upload for the bound pending input.
- Duplicate, stale, reordered and restart cases cannot reapply field changes,
  select another pending request or acquire new output/approval authority.
- Unknown/invalid intent and mixed private facts remain protected and ask for
  clarification. A switch is not an assignment, final PDF request or approval.
- Existing historical Telegram/I-134 jobs are not canceled, retried or repaired
  by this work. Use new isolated synthetic sessions for automated qualification.

## Done Criteria

- Generic executable RU/EN tests cover explicit task change, ambiguous media,
  buttons and free text, preserving/resuming a draft, missing/uncertain intent,
  and the role-only telephone/city clarification staying on the original job.
- Native checks prove accepted values and person isolation, source immutability,
  stale/wrong-owner/replay rejection, bounded private context and no leak into
  ordinary persisted history, diagnostics or public progress.
- Every code PR has exact-head green CI, substantive review, authorized rocket
  and confirmed normal merge. Documentation-only PRs use the standing immediate
  merge exception after their local/live-file/mergeability/thread checks.
- Merged code is deployed with verified backup/rollback, actual process/source
  identity, healthy services and an exact-marker diagnostic smoke.
- A real authenticated gateway/model synthetic sequence changes tasks without
  `/cancel_form`, handles an ambiguous attachment without losing it, and verifies
  the preserved draft through native state. Test cleanup and limitations are
  recorded. A mock, first question or prose claim is not this proof.
- The actual Telegram failure above is diagnosed to the evidence available;
  the retained input is not blindly repeated. Any fresh reproduction is clearly
  separate. Do not label unknown historical provider/format causes as proven.
- Merge dated evidence and a short manual Telegram scenario. Actual Telegram
  requalification remains operator-driven and is not claimed before its replies
  and traces are inspected. Full PDFI4/I-134 remains open.
