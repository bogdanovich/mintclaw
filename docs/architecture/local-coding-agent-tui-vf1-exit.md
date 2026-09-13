# Local Coding Agent TUI VF.1 Exit Record

Roadmap packet:
[VF.1 — Adaptive inline terminal shell](local-coding-agent-tui-visual-followup-roadmap.md#vf1--adaptive-inline-terminal-shell).

The merge containing this record closes VF.1. Ordinary `mintclaw code` and
active-thread resume sessions now begin as compact inline surfaces instead of
unconditionally replacing the user's terminal with an alternate screen.

## Shipped behavior

- New and resumed active-thread sessions preserve prior shell scrollback and
  do not emit alternate-screen entry or exit sequences.
- An empty session renders only the composer and footer. The transcript does
  not contribute a padded blank viewport.
- As semantic transcript cells arrive, the viewport grows to their actual
  rendered line count. At the available terminal-height bound it resumes the
  existing scroll, bottom-follow, manual anchor, resize, and cache behavior.
- The resume picker remains an explicitly full-screen application.
- `/transcript` and `Ctrl+T` temporarily enter the alternate screen because
  search, selection, help, and copy use the complete terminal height. Closing
  the overlay exits once, restores the prior inline frame and semantic scroll
  anchor, and returns focus to the composer.
- Explicit alternate-screen callers retain the bounded UTF-8 final-summary
  helper. Inline sessions leave their last rendered frame in scrollback and do
  not append a duplicate final answer.

## Ownership and implementation

The CLI composition root chooses screen mode: active threads pass
`AlternateScreen: false`, while the picker still passes `true`. `tui.Run`
derives an adaptive-height model only for an inline active thread. The model
continues to own presentation geometry but not canonical transcript or runtime
state.

Adaptive height is derived from the semantic viewport document's rendered line
count and the same composer, activity, pending-input, and terminal-height
budget used by full-screen mode. The internal viewport retains a minimum height
of one for safe Bubbles navigation, but `Model.View` omits it entirely when the
inline document is empty. Command panels page against the available terminal
height rather than inheriting the compact transcript height.

The transcript overlay uses Bubble Tea's dynamic `EnterAltScreen` and
`ExitAltScreen` commands. Opening is a two-phase transition: the main-buffer
view remains compact until Bubble Tea has processed alternate-screen entry,
then a private model message exposes the overlay. Bubble Tea remains
responsible for raw mode,
bracketed paste, focus reporting, cursor visibility, signal handling, panic
recovery, and final terminal restoration.

## Codex reference and deliberate differences

The implementation was checked against OpenAI Codex commit
`a505c71490885a44979df056284badbfdd75b3fb`, especially
`codex-rs/tui/src/lib.rs`, `codex-rs/tui/src/tui.rs`, and the `--no-alt-screen`
CLI contract. Codex supports both modes and currently resolves its default from
configuration; screenshot 08 demonstrates its inline presentation.

MintClaw selects inline mode directly for active coding threads instead of
adding another user-facing flag or configuration option while there are no
compatibility obligations. It does not copy Codex's custom terminal renderer.
Consequently, committed cells remain in MintClaw's bounded semantic viewport
rather than being incrementally printed into native scrollback. Canonical
thread history and `/transcript` remain the complete durable sources.

## Validation

The implementation is formatted with `make fmt` and covered by:

```text
go test -count=1 ./pkg/coding/tui ./cmd/mintclaw/internal/coding
go test -race -count=1 ./pkg/coding/tui ./cmd/mintclaw/internal/coding
scripts/pre-push-lint.sh --changed
```

Focused model tests prove that an empty 80x24 surface has only composer/footer
rows, a short transcript uses exact content height, and a long transcript stops
at the admitted viewport bound. CLI tests prove that new and explicit resumed
threads select inline mode while the resume picker remains alternate-screen.

Unix PTY tests prove:

- ordinary empty, interrupted, SSH-shaped, tmux-shaped, narrow no-color,
  compaction, fallback, and resumed sessions never emit `CSI ?1049h/l`;
- pre-TUI shell output remains in the ordinary buffer;
- `/transcript` emits exactly one alternate-screen entry and exit;
- normal exit, idle and active `Ctrl+C`, `SIGTERM`, and an induced subscription
  panic restore bracketed paste, focus reporting, and cursor visibility; and
- a real nested tmux session, when tmux is installed, retains visible content
  and restores both terminal layers.

## Exit-gate decision

VF.1's compact-start, bounded-growth, explicit-overlay, and restoration
criteria are satisfied. The package intentionally does not add the blank
composer/footer row or change the 72-column rule; those geometry changes remain
VF.2 so their goldens are based on this final inline-height contract. Semantic
Markdown and response policy remain VF.3 and VF.4.
