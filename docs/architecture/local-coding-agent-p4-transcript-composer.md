# Local Coding Agent P4.2 Transcript and Composer

Status: implemented

Roadmap packet: P4.2

## Frontend boundary

The terminal keeps two bounded views of a coding thread:

- the live authoritative snapshot and revisioned deltas owned by the frontend
  projector; and
- an optional, read-only historical window hydrated from the canonical session
  transcript.

Historical paging crosses optional interfaces at the memory, session, runtime,
and frontend-controller boundaries. The TUI never reads JSONL files or imports
agent internals. A native controller records the canonical history length when
it opens and uses that fixed watermark for every page, so prompts admitted by
the live controller cannot overlap or reorder the hydrated prefix.

The JSONL reader scans under the per-session lock and retains only the requested
window. Page size is capped at 256 canonical messages. The TUI requests 64 at a
time and independently caps historical display state at 256 entries; the live
projector retains its existing bound. Tool and system messages are not converted
to historical display text, preventing persisted tool results or system content
from leaking into the transcript surface. Hydrated user, assistant, and reasoning
entries are individually bounded to 32 KiB on a valid UTF-8 boundary.

## Transcript behavior

The viewport renders semantic user, assistant, reasoning, tool, warning, and
error entries. Control sequences are stripped before display, and wrapping uses
terminal cell width rather than byte or rune count. P4.2 tool rows deliberately
show only the tool name and lifecycle status; arguments, output, duration, and
expansion belong to P4.3.

The viewport follows streaming output only while it is already at the bottom.
After manual scrolling, refreshes anchor to the visible entity ID and line offset.
The same anchor is restored after snapshot resynchronization when that entity is
still retained. Page Up at the hydrated boundary requests an older window, and
Alt+End replaces it with the latest historical window when newer hydrated state
was dropped to enforce the bound.

## Composer behavior

Enter submits the exact composer value; Ctrl+J or Shift+Enter inserts a newline.
Bubble Tea owns bracketed-paste decoding, focus, and cursor behavior. The composer
accepts a paste as one input event, preserves Unicode grapheme content, and uses
the terminal library's cell-aware cursor layout for CJK, combining marks, emoji,
and adjacent right-to-left text.

Submission runs as a command so the update loop remains responsive. A successful
admission clears the draft and records it in a bounded 100-item in-memory history.
Alt+Up and Alt+Down navigate that history while preserving the draft that was
present before navigation. Admission failure keeps the exact draft and surfaces
the error. Composer history is view-local and is not another canonical store.

## Evidence

Automated tests cover:

- multiline Unicode submission, large bracketed paste, admission failure, and
  draft-preserving history navigation;
- cell-bounded CJK, combining-mark, emoji, right-to-left-adjacent, and composed
  input plus control-sequence sanitization;
- manual-scroll anchoring during streaming, bottom-follow behavior, and snapshot
  resynchronization without composer loss;
- bounded historical paging across JSONL logical truncation and controller/runtime
  boundaries; and
- exclusion of tool/system secrets and valid-UTF-8 truncation during hydration.

P4.3 owns richer tool cards, command output, changed-file/diff presentation, and
the project/model/context status surface.
