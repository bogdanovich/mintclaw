---
name: pdf
description: Inspect and read an exact current-turn PDF attachment or an authorized local PDF through MintClaw's bounded document service.
---

# PDF

Use this skill for either an exact `media://` PDF ref listed in the current verified attachment metadata or a local
PDF path that the user supplied in the current message. A path is not authority by itself: the `document` tool admits
it only when the configured workspace and read-path policy permits it. Never substitute a guessed filename, older
attachment, browser viewer, shell command, or provider-native PDF upload.

1. Discover the hidden `document` tool with `tool_search_tool_bm25`.
2. Call `document` with `action: inspect` first. For a current attachment, pass its exact `source` ref. For a local
   PDF, pass the exact user-supplied `path` only to this inspect call.
3. A successful local-path inspect returns a temporary turn-owned `media://` `source_ref`. Use that ref, never the
   mutable path, for every later `extract` or `render` call. Do not repeat the host path in the final answer.
4. Use the reported page count and text facts to choose a small, explicit, sorted, unique page list.
5. Prefer `extract` when text is available. The returned `[page N]` sections are protected current-turn context;
   cite those page numbers in the answer.
6. Use `render` only for selected pages whose visual layout or image content is necessary. If it returns
   `vision_unavailable`, report that limitation without guessing. Set `retain: true` only when the user asks to
   receive the rendered pages.
7. Propagate typed failures such as `password_required`, `unsupported_feature`, `invalid_page_selection`, and
   resource limits. Do not claim unread pages, successful delivery, form interpretation, OCR, or mutation.

Keep each operation bounded. Inspect again only if the source ref changes; request another explicit page selection
instead of growing one prompt beyond the tool limits.
