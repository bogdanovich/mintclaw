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

1. Start with `action: form`, `form_action: start`, and the inspected `source` ref. This workflow owns field discovery,
   protected collection, validation, mapping, review, verified mutation, and delivery.
2. When the tool suspends for a protected question, the channel has already delivered that question with its controls
   and free-text input. End the turn without paraphrasing, duplicating, or answering the question yourself.
3. After the operator answers, the ordinary model context receives only an opaque protected receipt. Resume the same
   job with `action: form`, `form_action: continue`, and that exact receipt as `event_id`. Never copy, summarize, echo,
   log, or ask the operator to repeat the raw answer. Never ask for an interaction ID or `/answer` syntax.
4. Keep the original `job_id`. Use `status` for progress, `correct` with a `field_id` already exposed by safe job or
   review state, and `cancel` when requested. Translate ordinary correction intent yourself; do not ask the operator
   for a field ID. Never start a replacement job to perform these actions.
5. When the bounded review is ready, let the operator review it. On a request to finish, call `commit` once for the
   same job. The configured approval interaction owns any confirmation; do not add a second confirmation, approve it
   yourself, or interpret silence as consent.
6. A completed commit has already run structural and visual verification and registered one outbox-owned PDF
   delivery. Do not call `send_file`, direct `fill`, or `commit` again. If delivery is pending, ambiguous, or recovering,
   report/status the same job instead of starting another write.

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
