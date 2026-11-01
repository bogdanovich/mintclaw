# PDF Conversation History And Structured Draft Goal

## Approved operator outcome — 2026-10-26

Keep one normal conversation while a PDF draft exists in the background.
The operator explicitly chose to retain **both form-related and unrelated
ordinary messages in normal chat history**, with verified form data also stored
in the existing separate structured draft. Mixed or uncertain ordinary messages
are conversation too: semantic classification must not decide whether their
authored text survives in history. No per-message retention approval or special
task-switch command is required.

This intentionally replaces the former blanket live-only policy for ordinary
PDF conversation. Form details can therefore exist in ordinary session history
as well as the encrypted draft. The user has accepted this consequence; do not
recreate the previous retention-choice blocker. This is not permission to expose
dedicated secret/authentication answers, internal private interpretation data,
unrelated sessions or raw diagnostic/support payloads.

History and draft have different jobs:

- History retains the conversation, including corrections and detours, using
  existing session ownership, retention, context selection and compaction.
- The draft retains verified field values, their person/source/schema bindings
  and revisions. It remains authoritative about what was actually accepted,
  even when older conversation is summarized or no longer in the model window.

Historical text is not a fresh task grant, field assignment, output approval or
permission to execute a previously unresolved request.

## Starting point and read-only boundaries

Reuse the reviewed work from PRs #1867, #1870, #1875 and #1883. Do not reimplement
candidate exclusion or discard useful native ownership/replay tests. The
[2026-10-25 partial qualification](pdf-intake-background-draft-foundation-qualification.md)
proved native 2-to-4 fact acceptance, safe pending-request handling and draft
return, but failed an arbitrary historical reference because the earlier user
message was absent from canonical history. It is evidence, not completion of
this new goal or a desired deployment pin.

The former [background-draft goal](pdf-intake-background-draft-goal.md) was
cleared, not completed. This file supersedes its conflicting history-retention
requirements; source/person/field validation and execution-authority fences
remain required.

Do not backfill history by decrypting historical drafts, recovering erased
utterances or replaying old receipts. Existing human Telegram job
`form_job_7bfd647d2ae063c8640222a69de4b724`, its pending question and historical
I-134/intake jobs are read-only evidence. Previous disposable qualification
jobs were canceled: never reuse their sessions, questions, grants or limits.
Use fresh synthetic owners and clean up only their test state/key copies.

## Bounded implementation sequence

1. Inspect current main and make an architecture checkpoint before patching.
   Trace normal ingress/history, native intake/routing, current provider input,
   retries/recovery and diagnostic projections. Separate conversation retention
   from draft control rather than switching off the native runtime.
2. Implement prospective normal-history retention for all ordinary PDF
   conversation, plus correct current/historical provider context. Keep the
   existing native draft updater and task/interaction authority. Split into a
   small dependent PR sequence if needed; do not ship a side-chat-only subset
   as completion of the all-conversation requirement.
3. Run regressions, obtain reviewed merges, deploy merged source, qualify the
   combined behavior through authenticated gateway/real-model runs and native
   readback, then merge dated evidence and an updated manual Telegram recipe.

Compare at least: keeping only unrelated side grants (insufficient), bypassing
intake entirely (unsafe), and separating history from draft/control within the
existing ingress/session/runtime boundaries (preferred). Remove or refactor
obsolete modal/privacy plumbing where justified; do not preserve it merely
because tests encoded the superseded policy. Equally, do not remove owner,
source, schema, revision, idempotency or approval checks with that plumbing.

## Done criteria

1. **Ordinary history.** New user messages about the form, unrelated topics,
   mixed topics and unresolved ordinary facts are retained in the existing
   owner-scoped history, together with normal assistant replies. No blanket
   opaque-receipt substitution or one-turn-only loss remains in that history.
   Preserve normal attachment references and existing secret handling; do not
   dump PDF binaries, all extracted pages, internal prompts or private model
   proposals into history, or promote tool/document data to user authority.
2. **Identity, durability and context.** Executable tests prove each ordinary
   message is recorded once by durable identity, in order, through duplicate
   ingress, bursts, suspension, retry and restart. Legitimate repeated text is
   not discarded as a duplicate. Provider requests contain the current input
   once, with complete tool-call/result pairing, correct later steering order
   and no duplicate live-grant projection. Recent arbitrary references remain
   available under normal context policy; do not promise unlimited verbatim
   recall after arbitrary summarization. Do not reset token/intake budgets or
   destabilize the existing cache prefix to hide a failure.
3. **Independent verified draft.** Use unrelated schemas and RU/EN regressions
   to prove two names, initially unowned telephone, then borrower-only
   telephone and city. Four correct facts are accepted; the other person's
   telephone remains genuinely missing, not guessed, blanked, deferred or N/A.
   Accepted facts and real conflicts survive detours/restart/context changes.
   Ordinary conversation about an uncertain value is retained without
   automatically assigning it. Never repeat the already-settled ownership
   question or ask for accepted values again.
4. **Non-modal conversation.** Greetings/ping/test, unrelated questions,
   multi-turn follow-ups and ordinary draft return work without a forced
   task/form choice. A mixed message can provide draft facts and ask a harmless
   ordinary question without losing its text or that question. Ask only about
   genuinely missing/ambiguous draft facts or genuinely unclear task scope.
   A draft-update failure must not erase/substitute the ordinary message in
   history; report its actual native status without silently applying data,
   losing accepted facts or replaying uncertain effects.
5. **History is not authority.** While a real new-attachment/request question
   is pending, unrelated conversation and draft return do not grant, execute
   or implicitly approve that original request. Prove this from native
   identities, states, unchanged payload digests and effect records, not just
   model prose. Re-check this with the original text visible in normal history:
   safety must not rely solely on withholding its content. Keep field/person
   isolation, source/schema/revision checks and output/signature/submission
   approval. No generic task stack or approval bypass is admitted.
6. **Privacy outside the chosen history.** Existing explicitly secret/password/
   authentication input channels stay protected. Do not label every form reply
   secret merely because a draft is active. Journal, diagnostics, private
   worker/tool projections and support exports retain their established
   redaction/disclosure boundaries. Update obsolete tests deliberately:
   authored form values are now expected in the owner's normal history, not
   in other owners, logs, private implementation dumps or unapproved exports.
   Native draft values remain protected separately. Verify both positive
   history retention and forbidden-disclosure cases.
7. **Reviewed code.** Appropriate normal/race, concurrency, recovery, restart,
   replay, privacy and provider-request regressions pass. Changed Go files use
   full repository formatting and actual affected-package lint. Code PRs go
   through `mintclaw-autonomous-pr`: exact-head CI, substantive review,
   authorized rocket, no unresolved actionable feedback, confirmed normal
   merge. Docs-only PRs use standing immediate-merge policy. A document, first
   reply, green unit suite or merged prerequisite is not completion.
8. **Deployment.** Use `mintclaw-deployed-ops` from freshly inspected current
   deployment, not stale PIDs/SHAs or old helpers. Preserve live configs/data,
   intentional component/profile pins and verified rollback material. Validate
   capacity before mutation, restart only affected services, verify actual
   executable paths/hashes/source, config loads, service health and error
   journals. Require an exact-marker smoke plus a fresh correctly scoped trace.
   No automatic mutable-data downgrade after new writes or unrelated cleanup.
9. **Fresh real qualification.** Use authenticated gateway/real-model input on
   new synthetic sessions. A primary fixture must prove form facts in ordinary
   history and native draft, exact current-input handling, a detour with fresh
   arbitrary labels acknowledged without echo, correct later reference, a
   mixed harmless question, genuine pending-question safety and same-draft
   return. Repeat a smaller history/draft check on an independent form; do not
   use form-specific rules. `2+2` followed by “Why?” is insufficient evidence.
   Verify native values/dispositions, history/context, source immutability,
   bounded privacy and owned cleanup. Merge dated evidence and clear remaining
   limits. Actual Telegram upload/callback acceptance stays operator-driven and
   unclaimed until observed; it does not block this bounded automated goal.

## Stop scope

No new conversation store, planner/service/registry/queue framework, field-name
or language regex rules, form-specific production exceptions, new PDF backend,
full PDFI4/I-134/XFA admission, general provider/compaction redesign or unrelated
infrastructure work. Existing normal session history plus the existing native
draft is the storage design. Dedicated sensitive inputs are not included in the
ordinary-history policy. Historical protected content is not migrated.

Stop only after implementation, reviewed merges, deployed fresh qualification
and evidence satisfy this contract, or a genuine audited blocker remains.
Perform an autonomous-flow architecture checkpoint before repeated fixes turn
into another cross-cutting state machine; keep this goal intact rather than
redefining success around an easier subset.
