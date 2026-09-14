# Local Coding Agent TUI Screenshots 14-16 Roadmap

Status: complete. [PR #1214](https://github.com/bogdanovich/mintclaw/pull/1214)
merged as `e53eb32e8f9ff28dcf4283d6d1995f4401f8c682` and was deployed with the
evidence below. This bounded follow-up addresses the compact-start, answer
hierarchy, command evidence, and history-navigation gaps shown in local
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

## Completion evidence

The final PR head `9e58328b50e0753462c0a0e0b5a9c0f37f0ef7d7` passed all ten
exact-head CI jobs, including Tests, Race, Linter, Integration Tests, macOS
Portability, and both cross-compiles. The automated reviewer reported no
high-confidence issue, no review thread remained, and the owner supplied the
PR-level rocket approval before merge.

Local validation included `make fmt`, the complete `pkg/coding/tui` and coding
CLI package tests, focused changed-contract race coverage, Unix PTY lifecycle
coverage, changed-package lint, and docs lint. A package-wide local race run
reached the existing 11-minute timeout inside the multi-megabyte synthetic
`TestFourHourPresentationSessionRemainsStructurallyBounded` fixture; the
focused race suite completed in 20 seconds and the exact-head GitHub Race job
passed.

The merged SHA was built on the deployment host with `make build`, `make
build-node`, and `make build-launcher`. The installed core, node, and launcher
checksums match their build outputs. The verified rollback backup is
`/home/server/mintclaw-core-backup-20260914T055751Z`; it includes the previous
binaries, user units and drop-ins, affected-unit states, and a checked SHA-256
manifest. An earlier `20260914T055700Z` attempt preserved the gateway symlink
instead of its target and is retained only as an incomplete artifact, not as a
rollback source.

All ten expected production units are active, no failed or legacy product
process exists, launcher HTTP returns `302`, the reviewer endpoint returns its
expected `404`, and the ten-minute error journal count is zero. All five active
profiles load under the installed doctor; exit `2` represents existing policy
findings, with no schema or load error. The source checkout is clean, and the
gateway and web process executables point at the checksum-matched core and
launcher.

A real SSH PTY showed the fresh-session card with deployed version, model,
directory, and `YOLO mode`. The successful coding turn rendered a Markdown
heading as `• TUI smoke`, indented its nested bullet, changed no repository
file, and `/exit` restored bracketed paste, focus reporting, mouse modes, and
the cursor. The first shell attempt intentionally lacked the systemd-only auth
path and is not acceptance evidence; its 24 KiB diagnostic state was moved,
not deleted, into the verified backup's `smoke-artifacts` directory.

The main-gateway smoke returned `outcome=success` and
`MINTCLAW_TUI_S14_16_OK_E53EB32E` on `main-turn-8`. Correlated trace
`trace-turn-4017342f8e609e4d3d9c9222` uses schema
`mintclaw.diagnostic_trace.v1`, completed with eight records under
`redacted_content` and `mintclaw.config_filter.v1`, and has no truncation.

## Stop condition

This packet is complete only when the implementation and evidence are merged
and deployed. It does not claim native-scrollback parity with Codex, add
mouse-only controls, change command execution semantics, or label exploration
tools as shell commands.
