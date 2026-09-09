# Local Coding Agent TUI.14 Exit Record

Roadmap packet:
[TUI.14 — Transcript navigation, search, copy, and accessibility](local-coding-agent-codex-tui-roadmap.md#tui14--transcript-navigation-search-copy-and-accessibility).

The merge containing this record closes TUI.14. `Ctrl+T` now opens one
full-screen, copy-safe transcript surface across semantic messages, commands,
repository diffs, and MCP/tool cells instead of reusing the generic command
panel.

## Shipped behavior

- `Ctrl+T` and `/transcript` open the same full-screen overlay. The composer and
  generic command panels do not compete for rows while it is active. `Esc`,
  `q`, or `Ctrl+T` closes it.
- Arrow keys or `j/k` select a plain-text row; Page Up/Page Down move by a
  visible page; Home/End or `g/G` jump to the first or last retained row.
  Page Up at the beginning hydrates older canonical transcript entries, and
  End reloads the latest page after bounded older-history navigation.
- Stable semantic cell/row keys retain the selected evidence across older-page
  prepends, live snapshot updates, and terminal resize. Closing restores the
  previous command panel, panel offset, tool selection, composer focus, and
  semantic transcript position.
- `/` opens a dedicated search editor. Search is Unicode-aware,
  case-insensitive, bounded to 256 runes, and accepts multi-rune terminal/IME
  input. `n` and `N` select the next or previous match relative to the current
  row and wrap; `Ctrl+L` clears the query.
- `c` copies the selected plain row and `C` copies all currently retained plain
  rows. A request identity prevents a delayed clipboard result from changing a
  closed or newly reopened overlay.
- Local copy tries the native system clipboard first. SSH skips the remote
  native clipboard; tmux sessions prefer checked `load-buffer -w` forwarding;
  and a bounded OSC-52 sequence is the final terminal fallback. OSC-52 payloads
  are base64 encoded, tmux-wrapped when required, and limited to 100,000 raw
  bytes.
- Search, line selection, match highlighting, copy state, loading notices, and
  help remain understandable with no color. The overlay's built-in help lists
  every binding and explicitly requires neither a mouse nor color. Even a
  one-row view presents a close key before less important text.

## Text and terminal safety

The overlay renders the existing full semantic cell evidence with
`cellColorNone` and strips ANSI, OSC, C0/C1 controls, carriage returns, and
direction-changing bidi controls before search, display, or copy. Safe visible
text inside an untrusted OSC-8 link remains visible, but the target and terminal
sequence do not. Ordinary right-to-left text, combining characters, CJK, and
emoji grapheme clusters remain intact and are width-clipped with the shared
ANSI/grapheme-aware primitives.

MintClaw does not yet have an admitted semantic hyperlink type or a reliable
OSC-8 capability handshake. TUI.14 therefore does not preserve embedded
hyperlinks supplied by model or tool text; it chooses inert readable labels
instead of guessing that an escape sequence or target is trustworthy. This
satisfies the roadmap's rule that hyperlinks may survive only after safe
terminal support is established.

For screen readers and noninteractive use, `mintclaw code exec` remains the
distinct non-alternate-screen plain path. Within the interactive overlay,
selection markers and binding names are literal text, not color-only state.

## Codex reference and deliberate differences

The implementation was checked against OpenAI Codex commit
`1a4096e273e80da30947e57fdfa45be92858ca91`, particularly
`codex-rs/tui/src/pager_overlay.rs` and
`codex-rs/tui/src/clipboard_copy.rs`. MintClaw adopts the dedicated transcript
surface, explicit navigation footer, bounded native/tmux/OSC-52 clipboard
routing, and copy-safe plain evidence while retaining Bubble Tea and MintClaw's
semantic-cell authority.

Codex has additional selection and markdown-copy facilities. MintClaw TUI.14
copies a selected rendered row or the full retained plain transcript because no
rich clipboard contract has been admitted. It never derives links or markup by
re-parsing arbitrary transcript output.

## Validation

The implementation is formatted with `make fmt` and covered by:

```text
go test -count=1 ./pkg/coding/...
go test -race -count=1 ./pkg/coding/tui
scripts/pre-push-lint.sh --changed
```

Focused tests cover command, error, and full repository-diff search; relative
and wrapping next/previous matches; multi-rune Unicode input; exact
panel/focus/tool/scroll restoration; line and complete-transcript copy; stale
and failed copy results; native, SSH, tmux, and OSC-52 backend selection;
payload injection and byte limits; older/latest hydration; stable selection
after prepend and resize; natural RTL text and unsafe bidi/OSC controls;
keyboard-only help; and one- through eight-row bounded layouts.

## Exit-gate decision

TUI.14's acceptance criteria are satisfied. TUI.15 remains responsible for
removing the final legacy transcript/compatibility paths, performance budgets,
PTY scenario parity, diagnostics, and the initiative-wide completion audit.
