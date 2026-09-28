---
name: pdf
description: Inspect, read, render, conversationally complete, directly fill, and verify an exact current-turn PDF attachment or an authorized local PDF through MintClaw's bounded document service.
---

# PDF

Use this skill for either an exact `media://` PDF ref listed in the current verified attachment metadata or a local
PDF path that the user supplied in the current message. A path is not authority by itself: the `document` tool requires
an exact current-message selector match and admits it only when the configured workspace and read-path policy permits
it. Never normalize it into another spelling or substitute a guessed filename, older attachment, browser viewer,
shell command, or provider-native PDF upload.

1. Discover the hidden `document` tool once with `tool_search_tool_bm25`.
2. Call `document` with `action: inspect` before choosing a strategy. For a current attachment, pass its exact
   `source` ref. For a local PDF, pass the exact user-supplied `path` only to this inspect call.
3. A successful local-path inspect returns a temporary turn-owned `media://` `source_ref`. Use that ref, never the
   mutable path, for every later action. Do not repeat the host path in the final answer.

## Choose the workflow after inspection

- For reading, use the reported page and text facts to choose a small, explicit, sorted, unique page list. Prefer
  `extract` when text is available; its `[page N]` sections are protected current-turn context, and answers should cite
  those page numbers. Use `render` only when visual layout or image content is necessary, and set `retain: true` only
  when the user asks to receive rendered pages. If rendering returns `vision_unavailable`, report it without guessing.
- For an ordinary request to complete, fill in, or help with a supported form, use the protected conversational
  workflow below. Do not call `fields` followed by direct `fill`, even for a small form.
- Reserve one-shot `fields` then `fill` for an explicitly requested technical operation whose complete,
  unambiguous stable-ID assignment map has already been established for this exact source. Use only reported field
  kinds and export values; never infer an ID or copy one person's value into another person or section. Submit only
  typed `text`, `boolean`, `choice`, or `choices` assignments. Values are protected current-call input and must not be
  copied into summaries or diagnostics.

## Protected conversational form workflow

1. Call `fields` for the inspected source before `form/start`. Keep its exact `field_schema_digest`; start rejects a
   missing or stale digest. From bounded document evidence and reported field facts,
   briefly explain the form, applicable sections, and a bounded collection plan in human terms. Before the first
   protected question, include that short summary and plan in the agent-authored question so the user sees why the
   requested fact is needed. Clarify goals in ordinary conversation first. Do not dump the raw field inventory.
2. Call `action: form`, `form_action: start` with the source and exact `field_schema_digest`; it prepares the job but
   asks nothing and does not nominate a next field. Deliberately choose one stable field from the semantic facts, then
   use `form_action: collect` with its `field_id`, the original `job_id`, and your concise human `question`. The first
   collect also requires separate value-free user-facing `form_summary` and `collection_plan`; write both in the user's
   language and do not include existing or newly supplied field values in either.
   Do not expose IDs or derive question order from raw field/schema order.
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
5. At `ready_for_review`, call `form_action: review`; explain blockers and collect/correct deliberately. Let the operator
   review it, then on a finish request call `commit` once. Never duplicate or self-approve its confirmation. Completion
   already means verified, single PDF delivery; never send, fill, or commit again. Report the same job during recovery.

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
