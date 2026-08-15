# Local Coding Agent P4.1 Terminal Shell

Status: implemented

Roadmap packet: P4.1

## Boundary

The coding terminal application is a Bubble Tea shell in `pkg/coding/tui`.
It consumes only the transport-neutral `frontend.Controller`: the shell takes
an initial authoritative snapshot, follows revisioned deltas, and requests a
replacement snapshot when the retained delta window has advanced. It does not
import the agent loop, persistence implementation, or runtime event payloads.

`mintclaw code <prompt>` selects the shell only when both standard input and
standard output are terminal files and `TERM` is not `dumb`. Redirected,
piped, test, and JSON invocations retain the existing plain renderer. This is
the supported non-TTY fallback until the stable `code exec` protocol in P7.1.

## Terminal ownership

Bubble Tea owns raw mode, bracketed paste, focus reporting, resize delivery,
the cursor, and alternate-screen entry/restoration. The MintClaw application
owns the surrounding controller lifecycle:

- a dedicated cancellable watch context stops frontend watches on every exit;
- normal idle `Ctrl+C` closes the controller and then leaves the screen;
- the first and repeated `Ctrl+C` during work preserve the admitted graceful
  interrupt and hard-cancel behavior;
- SIGINT and SIGTERM use Bubble Tea's signal path, which restores the terminal
  before `Run` returns;
- Bubble Tea's panic recovery restores the terminal before MintClaw closes the
  controller and releases the thread lease; and
- controller close has a fresh bounded timeout even when the application
  context was canceled or a long-running session exceeded an earlier deadline.

The shell asks for terminal focus reports and keeps the composer focus state
frontend-local. Width and height retain their actual clamped terminal values;
one- and two-row terminals degrade to a clipped status line instead of
pretending that more rows exist.

## Screen and scrollback behavior

Two modes are supported:

| Invocation | Screen behavior | Native scrollback after exit |
| --- | --- | --- |
| Interactive capable TTY | Alternate screen | A bounded thread/status summary and at most 2,000 bytes of the latest assistant answer |
| Non-TTY, `TERM=dumb`, `--json`, or replaced command streams | Existing plain renderer | The ordinary bounded command result; no terminal control sequences |

The alternate-screen path never replays the canonical transcript. The final
summary is derived from the already bounded frontend snapshot and preserves a
valid UTF-8 boundary. Signal and panic exits prioritize restoration and error
reporting; they do not promise a final summary.

`--no-color` and `NO_COLOR` remain compatible with the interactive shell.
They disable terminal color capability without disabling the TUI itself;
`TERM=dumb` selects the plain fallback because it cannot safely host the
interactive screen.

## Evidence

Automated tests cover:

- selection of the interactive path only after terminal admission and release
  of the coding-thread lease when the shell exits;
- non-TTY capability rejection and preservation of the plain command path;
- revisioned watch delivery and snapshot-based gap recovery;
- focus/blur and tiny/resized terminal state transitions;
- bounded valid-UTF-8 final scrollback summaries; and
- pseudo-terminal normal `Ctrl+C`, SIGTERM, and induced Bubble Tea panic paths,
  including alternate-screen, bracketed-paste, and cursor restoration escape
  sequences.

P4.2 owns the polished transcript viewport and multiline composer. P4.3 owns
tool, diff, and status presentation. P4.4 owns the resume picker, and P4.5 owns
the complete in-app command surface.
