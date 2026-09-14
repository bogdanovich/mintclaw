# Local Coding Agent TUI Screenshots 14-16 Roadmap

Status: in progress. This bounded follow-up addresses the compact-start,
answer hierarchy, command evidence, and history-navigation gaps shown in local
screenshots `14.png` through `16.png`.

## Reference and findings

The admission audit compares MintClaw `main` at
`a343fef798a5bc450ef665b565abfcc1d8fc968a`, screenshots
`/Users/ab/agent-screenshots-2026-08-30-1932/14.png` through `16.png`, and
OpenAI Codex `main` at `a505c71490885a44979df056284badbfdd75b3fb`.

The gaps have distinct causes:

1. A new MintClaw thread has useful model, directory, and permission metadata,
   but previously rendered only the composer and footer. Codex presents that
   context in a compact welcome card.
2. MintClaw's Markdown renderer already understands headings, tables, lists,
   links, and code, but final answers start at column zero. Codex prefixes the
   answer with `• ` and gives continuation lines a two-column inset, making the
   response easier to distinguish from the terminal edge and user prompt.
3. Single shell commands already render as `Ran <command>` with a bounded
   head/tail output preview. Consecutive successful commands are collapsed to
   only `Ran N commands`, hiding both names and results from the main surface.
   Read/list/search tools are intentionally labelled `Explored`; they are not
   shell commands and must not be misrepresented as such.
4. Completed commentary, tool calls, and answers remain in the semantic store;
   the final answer does not replace them. The bounded viewport follows the
   bottom, however, and MintClaw did not enable mouse events, so normal
   trackpad/wheel use scrolled terminal-native history instead of the active
   transcript. `Page Up` and `Ctrl+T` worked but were insufficiently obvious.

Modern Codex commits completed cells into native terminal scrollback and owns
a separate mutable tail with resize reflow. Reproducing that subsystem would
require new history insertion, tail replacement, reflow, pagination, and
rollback machinery. This follow-up keeps MintClaw's existing semantic viewport
and makes it directly scrollable by wheel/trackpad, while retaining keyboard
navigation and the complete durable transcript overlay.

## Delivery packet S14-16.1

Scope:

- show a compact, bounded startup card with version, model, directory, and
  effective permission mode;
- dismiss the card on work, an accepted slash command, or `Esc`, and omit it
  for resumed threads and constrained terminals;
- render final Markdown answers with the same leading bullet and continuation
  inset already used for assistant commentary;
- retain `Ran N commands` grouping while listing bounded command names and one
  result preview per visible member;
- enable wheel/trackpad scrolling for the main transcript, panels, and full
  transcript overlay, including older-history hydration at the top;
- advertise `PgUp history` when the main transcript is scrollable; and
- preserve complete, uncollapsed command evidence in `Ctrl+T`.

Acceptance:

- a fresh 80x24 thread shows the startup card without adding it to canonical
  transcript history, while narrow/short sessions retain the composer;
- resumed or active sessions never show a stale welcome card;
- final Markdown headings, paragraphs, tables, lists, and code are inset under
  one answer bullet at 40, 80, and 120 columns;
- a grouped command surface names each displayed command and shows a bounded
  result line, with an explicit full-transcript hint;
- wheel up moves the bounded transcript without deleting completed cells, and
  `Page Up`, `Alt+End`, and `Ctrl+T` continue to work without a mouse;
- Bubble Tea enables and restores mouse cell-motion mode on normal exit,
  interruption, signals, panic, SSH-style PTYs, and tmux;
- focused unit, golden, race, PTY, and changed-package lint checks pass; and
- merged `main` is deployed with a verified rollback backup, healthy services,
  all-profile configuration/doctor checks, a bounded live coding smoke, and a
  completed redacted, non-truncated diagnostic trace.

## Stop condition

This packet is complete only when the implementation and evidence are merged
and deployed. It does not claim native-scrollback parity with Codex, add
mouse-only controls, change command execution semantics, or label exploration
tools as shell commands.
