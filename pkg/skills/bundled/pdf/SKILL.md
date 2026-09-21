---
name: pdf
description: Inspect, read, render, discover AcroForm fields in, fill, and verify an exact current-turn PDF attachment or an authorized local PDF through MintClaw's bounded document service.
---

# PDF

Use this skill for either an exact `media://` PDF ref listed in the current verified attachment metadata or a local
PDF path that the user supplied in the current message. A path is not authority by itself: the `document` tool requires
an exact current-message selector match and admits it only when the configured workspace and read-path policy permits
it. Never normalize it into another spelling or substitute a guessed filename, older attachment, browser viewer,
shell command, or provider-native PDF upload.

1. Discover the hidden `document` tool with `tool_search_tool_bm25`.
2. Call `document` with `action: inspect` first. For a current attachment, pass its exact `source` ref. For a local
   PDF, pass the exact user-supplied `path` only to this inspect call.
3. A successful local-path inspect returns a temporary turn-owned `media://` `source_ref`. Use that ref, never the
   mutable path, for every later `extract`, `render`, `fields`, `fill`, or `verify` call. Do not repeat the host path
   in the final answer.
4. Use the reported page count and text facts to choose a small, explicit, sorted, unique page list.
5. Prefer `extract` when text is available. The returned `[page N]` sections are protected current-turn context;
   cite those page numbers in the answer.
6. Use `render` only for selected pages whose visual layout or image content is necessary. If it returns
   `vision_unavailable`, report that limitation without guessing. Set `retain: true` only when the user asks to
   receive the rendered pages.
7. For an ordinary AcroForm, call `fields` before `fill`. Use only the exact opaque `field_id`, kind, and advertised
   export choices from that report. Never infer legal or business meaning from a field name and never invent a
   field ID.
8. Before `fill`, show the intended field-to-value mapping when the user's request is ambiguous and ask for the
   missing choice or confirmation. When the current request already supplies one unambiguous explicit mapping, do
   not ask again. Submit typed assignments (`text`, `boolean`, `choice`, or `choices`) only for fields the report
   admits. Field values are protected current-call input and must not be copied into later summaries or diagnostics.
9. `fill` always performs structural and visual verification before it registers and sends one PDF. Treat its
   `operation_id`, output digest, opaque artifact ref, assertion counts, and delivery state as the evidence. Do not
   use `send_file` for that artifact and do not repeat `fill` when delivery is pending or ambiguous. For an explicitly
   requested exact retry before delivery started, reuse the returned `operation_id` with the identical source and
   assignments; never substitute it into a different request.
10. Use `verify` with the returned artifact ref and exact `operation_id` only for explicit reproducible diagnosis;
    it checks the private journal and committed generation and does not replace fill's mandatory verification.
11. Propagate typed failures such as `password_required`, `form_unsupported`, `field_value_invalid`,
    `verification_visual_failed`, `delivery_ambiguous`, `unsupported_feature`, `invalid_page_selection`, and
    resource limits. Do not claim unread pages, successful delivery, form interpretation, OCR, or unsupported
    mutation.

Keep each operation bounded. Inspect again only if the source ref changes; request another explicit page selection
instead of growing one prompt beyond the tool limits.
