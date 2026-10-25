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

## Request-boundary checkpoint after the second real-model run

The follow-up PR [#1875](https://github.com/bogdanovich/mintclaw/pull/1875)
passed current-head CI/review and deployed at
`ebf8613c5d78f5ac6e4e139d69e5e0e42a7e71e4`. Its first ready baseline was three
production files (+28/-6), one test file (+71) and this document (+51), with no
review-fix cycles. The repeated live conversational invariant still failed, so
do not keep tuning the same instruction or call the goal complete.

Fresh request `background-c06-why` again answered about form handling after the
previous assistant had answered arithmetic. The canonical user message is an
opaque protected receipt; freshly authorized input is carried only inside a
tool message. Changing that tool message's instruction did not establish the
actual current user request at the provider boundary.

The native data invariants passed again: two names became four confirmed facts,
no excluded contact value/disposition, zero retained form input and the same
source/draft after return. In the second session, native scope stayed `question`
with identical payload digests after unrelated arithmetic and return. Resume no
longer reopened it. However `background-d05-return` claimed unprocessed form
input and suggested retry despite native `pending_inputs=0`; it confused a
separate pending task-scope request with unprocessed form text. One earlier
rejected interpretation recovered using the existing typed retry, with the
same job and `input_count=1`; it is not a first-attempt success claim.

### Decision and invariants

Considered: further instruction tuning, persisting a new conversation
transcript, relabeling tool results as user messages, or a bounded provider-only
projection of the existing typed `ContextInput` grant. Choose the last option.
The other approaches either repeated the failed mechanism, weakened protected
history, or broke tool-call/result ownership.

Reuse the existing live projection identity/lifetime and text allowance. Bind a
native grant to the exact live tool occurrence. Add its user message only in a
detached provider-request copy, after the complete paired tool-result block;
never relabel a tool result or rewrite the original user receipt. Leave
canonical history, snapshots, envelopes, journals and diagnostic input opaque.
Preserve pairing, later steering order, retry/rebuild behavior and
fail-closed handling for missing/ambiguous identities or truncated input. Generic
document text is not an authorized user input. No new store, registry, task
stack, schema or authority path is admitted.

Context-overflow rebuilding reserves the message cost of provider-only user
input for compaction, assembly and history trimming. It must not refill old
history using a smaller canonical-only estimate, drop the current input or
increase the provider's output-token limit. A near-window regression exercises
the real overflow/rebuild path, not just the projection helper.

Separately derive renderer guidance from native `pending_inputs`: a retained
task-scope question is not a failed form-data receipt. No field/form/language
phrase rules, inferred values, output authority or budget reset are permitted.
Executable request-boundary/privacy/cache tests and another real-model check
are required before this bounded goal can close.

The provider-only projection repairs the current request, not historical
conversation. The existing transient policy also removes earlier unrelated
user requests from ordinary history; an answer such as a bare number cannot
reconstruct that lost context. A product/privacy decision is pending on retaining
confidently unrelated conversation as normal chat history while keeping form
facts, mixed/uncertain inputs and unresolved attachments protected. Do not add a
new transcript store, silently change retention, or claim general conversational
continuity from a single arithmetic example.

## Deployed foundation and remaining decision — 2026-10-25

PR [#1883](https://github.com/bogdanovich/mintclaw/pull/1883) passed exact-head
CI/review/rocket and was normally merged, then deployed at `541e3a56…`.
The [dated partial qualification](pdf-intake-background-draft-foundation-qualification.md)
records successful native exclusion, ordinary ping, preserved pending authority
and same-draft return, plus a deliberately nontrivial historical-reference
failure. Earlier side-message text is still absent from ordinary history;
retention has not been changed without the user's decision. The goal stays
incomplete. No further phrase tuning or full PDFI4 admission follows from the
passing native checks.
