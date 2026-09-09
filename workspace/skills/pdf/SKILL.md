---
name: pdf
description: Inspect and read the exact PDF attached to the current turn through MintClaw's bounded document service.
---

# PDF

Use this skill only for the exact `media://` PDF refs listed in the current verified attachment metadata.
Never substitute a filename, local path, older attachment, browser viewer, shell command, or provider-native PDF upload.

1. Discover the hidden `document` tool with `tool_search_tool_bm25`.
2. Call `document` with `action: inspect` first.
3. Use the reported page count and text facts to choose a small, explicit, sorted, unique page list.
4. Prefer `extract` when text is available. The returned `[page N]` sections are protected current-turn context;
   cite those page numbers in the answer.
5. Use `render` only for selected pages whose visual layout or image content is necessary. If it returns
   `vision_unavailable`, report that limitation without guessing. Set `retain: true` only when the user asks to
   receive the rendered pages.
6. Propagate typed failures such as `password_required`, `unsupported_feature`, `invalid_page_selection`, and
   resource limits. Do not claim unread pages, successful delivery, form interpretation, OCR, or mutation.

Keep each operation bounded. Inspect again only if the source ref changes; request another explicit page selection
instead of growing one prompt beyond the tool limits.
