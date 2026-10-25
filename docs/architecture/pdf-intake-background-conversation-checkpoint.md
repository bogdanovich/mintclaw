# Background draft conversation qualification checkpoint

## Bounded observation — 2026-10-25

PRs [#1867](https://github.com/bogdanovich/mintclaw/pull/1867) and
[#1870](https://github.com/bogdanovich/mintclaw/pull/1870) were reviewed, normally
merged and deployed at `05e7404ed6c0318aae2b8337bb997f7638c823ff`.
Fresh authenticated gateway sessions used the unrelated synthetic equipment-loan
fixture, not historical Telegram/I-134 jobs.

Native readback established two names, then four confirmed facts after an
ownership clarification: both names, borrower-only telephone and borrower city.
The contact telephone had no user value or blank/N/A disposition; retained
ambiguity was consumed (`pending_inputs=0`, `retained_bytes=0`, ledger revision 4).
Unrelated arithmetic while a scope question was pending answered correctly;
the original request stayed `question` with the same text/media digests.

The real-model check also exposed two incomplete conversation paths:

- `background-a06-why` answered about protected form receipts after the prior
  assistant had correctly answered arithmetic. Canonical history retained that
  arithmetic answer, but the side-message live instruction incorrectly reused
  the replacement-task instruction: follow only the newly authorized request.
- `background-b05-return` continued the same form, then the model redundantly
  called `resume`. That operation selected the first retained scope question as
  runnable work, reopening the modal choice despite the successful continuation.

These observations are failures, not a completed qualification or a Telegram
pass. The [bounded goal](pdf-intake-background-draft-goal.md) stays active until
the repair is merged, deployed and qualified again.

## Small repair, unchanged authority

Give a native text-only side grant a conversational instruction, using recent
ordinary conversation for a follow-up. Preserve the strict replacement-task
instruction for an actual draft-suspending handoff. Select the instruction from
the existing atomic handoff progress, not from model prose or a new flag.
Budget the full largest instruction before consuming the once-only grant.

When resuming a draft, skip unanswered root scope questions in automatic work
selection. Keep not-yet-classified messages and committed answers runnable;
explicit question answering, native ownership, receipts and grant checks remain
unchanged. Do not delete, authorize or rewrite the pending request.

No new schema, service, scheduler, registry, store, conversation transcript or
authority path is required. Raw side-message text remains live-only, and form
values remain encrypted. This is not general task-stack work or full PDFI4.
Regressions cover active/paused resume, preserved pending authority, whole-prefix
capacity and ordinary side-message instructions; existing burst/resume/replay
tests must remain green. Repeat the real gateway/model check before recording
the outcome in the final evidence report.
