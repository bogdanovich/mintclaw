---
name: pdf
description: Inspect, read, and render an admitted PDF through MintClaw's bounded document service; when the active runtime also advertises the form surface, safely complete and verify supported forms.
---

# PDF

Use this skill for either an exact `media://` PDF ref listed in verified attachment metadata or an exact local PDF path
supplied by the user. A path is not authority by itself: the `document` tool admits it only when the active runtime's
workspace and read-path policy permits it. Never normalize it into another spelling or substitute a guessed filename,
unverified historical attachment, browser viewer, shell command, or provider-native PDF upload.

The coding runtime exposes `document` directly. A gateway may defer it behind its normal first-party tool discovery;
use that discovery only when it is actually present in the current tool catalog.

1. Call `document` with `action: inspect` before choosing a strategy. For a current attachment, pass its exact
   `source` ref. For a local PDF, pass the exact user-supplied `path` only to this inspect call.
2. A successful local-path inspect returns a temporary turn-owned `media://` `source_ref`. Use that ref, never the
   mutable path, for every later action. Do not repeat the host path in the final answer.

## Choose the workflow after inspection

- For reading, use the reported page and text facts to choose a small, explicit, sorted, unique page list. Prefer
  `extract` when text is available; its `[page N]` sections are protected current-turn context, and answers should cite
  those page numbers. Use `render` only when visual layout or image content is necessary. Set `retain: true` only when
  that field exists in the admitted schema and the user asks to receive rendered pages. If rendering returns
  `vision_unavailable`, report it without guessing.
- For an ordinary request to complete, fill in, or help with a supported form, prefer the protected conversational
  workflow below when the admitted `document` action enum includes `form`. If `form` is absent but `fields`, `fill`,
  and `verify` are all present, use the direct local-artifact workflow only when the user's current request already
  supplies every required value and each mapping is unambiguous. Do not solicit missing protected values through
  ordinary chat; explain that this runtime cannot run the protected collection workflow. A direct coding fill returns
  a thread-owned artifact and never implies channel delivery.
- For the direct workflow, call `fields`, build one complete stable-ID assignment map for the exact source, and call
  `fill` once. This also covers an explicitly requested technical operation whose complete map was established by the
  user. Use only reported field kinds and export values; never infer an ID or copy one person's value into another
  person or section. Submit only typed `text`, `boolean`, `choice`, or `choices` assignments. Values are protected
  current-call input and must not be copied into summaries or diagnostics. The branch is unavailable unless all three
  direct actions occur in the admitted schema.

## Protected conversational form workflow (only when `form` is admitted)

1. Call `action: form`, `form_action: discover` with the inspected source. Do not call `fields`: discover returns an
   exact `field_schema_digest` and a small unresolved-first `candidate_fields` window while the complete inventory
   stays outside model context. Fields with a `blocker` still need input; value-free candidates without one are included
   only when space remains so existing entries can be corrected. Candidates are page-ordered within those groups. From
   the inspect evidence and candidates, briefly explain the form, applicable sections, and a bounded collection plan in
   human terms. Clarify goals in ordinary conversation first. Do not dump field IDs or the raw inventory.
2. Call `action: form`, `form_action: start` with the same source and exact `field_schema_digest`; it prepares the job
   without asking a question. After successful discovery, the runtime requires this exact tool-only transition; do not
   replace it with a plain-chat value question. Deliberately choose a candidate, then call `form_action: collect` for
   a missing value or `form_action: correct` for an existing value the user asked to replace, with its `field_id`, the
   original `job_id`, and your concise human `question`. The first protected question also requires separate value-free
   user-facing `form_summary` and `collection_plan`; write both in the user's language and do not include existing or
   newly supplied field values.
   Keep the question itself focused on one fact; do not repeat the summary or plan in it. Each section has its own
   character budget. Set `interaction_language` to the user's BCP-47 language so headers and controls match.
   Offer `blank_actions` only when the document's instructions and the user's intent justify leaving this particular
   field blank or treating it as not applicable. Omit them when uncertain. A PDF `required: false` flag is only a
   technical constraint and does not establish semantic optionality. Native required fields reject blank controls.
   For a checkbox, phrase a binary question and provide both `checked_label` and `unchecked_label` in the user's
   language. The short labels must exactly match the two meanings stated in the question; do not pair an either/or
   question with generic Yes/No choices. Do not expose IDs or treat candidate order as prescribed question order.
   Every collect and correct call must include the non-empty `question`. If the tool reports that it is missing, retry
   the same job and field with the question; never fall back to asking for the protected value in plain chat.
3. Collect delivers the question and suspends; do not duplicate it. An answer yields exactly one `protected_answer_ref`;
   pass that exact value as `answer_ref` to `continue`. Never substitute an `interaction_id`, expose the answer, or ask
   for it again. A value button, `/answer`, or verified reply to the active prompt is an answer; other messages are
   ordinary guidance. `Clarify` and `Back` are navigation, never field values. On `Clarify`, explain the requested fact
   and why it matters, then collect the same field again. On `Back`, use `status` to choose a previously answered safe
   field and `correct` it; if nothing precedes the current question, explain that instead. In either case, do not call
   `continue` until a new protected answer receipt exists. Continue returns progress without choosing or asking the
   next question.
4. Keep the job. Use `status`, `correct` with a safe field plus a new question, and `cancel` on request. `Skip` and
   `Not applicable` are typed blank-value decisions for optional fields; `Cancel` and `/stop` terminate the workflow.
   Translate ordinary correction intent yourself; never request a field ID or replace the job.
5. At `ready_for_review`, call `form_action: review`. Its counts and bounded, value-free `fields` window summarize the
   complete internal review; prioritize any returned blockers and use the returned stable `field_id` with `correct`
   when needed. A ready review is a hard human boundary: summarize it and wait for a new user message; never call
   `commit` or `cancel` in the review-producing turn. Let the operator review it. On a finish request, call `commit` once.
   Never duplicate or self-approve its confirmation. Completion already means verified, single PDF delivery;
   never send, fill, or commit again. Report
   the same job during recovery.

For expert one-shot `fill`, treat its `operation_id`, output digest, opaque artifact ref, assertion counts, and delivery
state as evidence. Do not repeat `fill` when delivery is pending or ambiguous. Use `verify` with the returned artifact
ref and exact `operation_id` only for explicit reproducible diagnosis; it does not replace mandatory fill verification.
For an explicitly requested exact retry before delivery started, reuse that `operation_id` with the identical source
and assignments; never substitute it into another request.

Propagate typed failures such as `password_required`, `form_unsupported`, `field_value_invalid`,
`verification_visual_failed`, `delivery_ambiguous`, `unsupported_feature`, `invalid_page_selection`, and resource
limits. Do not claim unread pages, successful delivery, form interpretation, OCR, or unsupported mutation.

Keep each operation bounded. Inspect again only if the source ref changes; request another explicit page selection
instead of growing one prompt beyond the tool limits.
