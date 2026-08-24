# Local Coding Agent P4.3 Tool, Change, and Status Surfaces

Status: implemented

Roadmap packet: P4.3

## Projection boundary

The terminal renders only the bounded P3 frontend projection. It does not read
runtime logs, parse model prose, execute Git directly, or infer file writes from
assistant text.

Tool cards consume `ToolState` and its optional tool-owned `CommandState`.
Repository cards distinguish two sources:

- **Verified writes** are successful file-kind write audits emitted by tools.
  They describe audited writes during this frontend session.
- **Repository changes** are fresh deterministic workspace observations. They
  describe the current Git branch, status paths, and diff stat and may include
  changes made outside MintClaw.

Neither card treats one source as proof of the other. A verified write can later
be reverted, and a dirty path can originate outside a tool call.

## Tool cards

Cards are collapsed by default and use textual outcome markers: `[running]`,
`[suspended]`, `[ok]`, `[failed]`, `[interrupted]`, and `[unknown]`. Meaning does
not depend on terminal color. The collapsed row includes duration, command
status, exit code, background/canceled/timed-out state, verified write paths,
and explicit truncation state.

Alt+J and Alt+K select a retained card. Ctrl+O expands or collapses the selected
card. At most one tool is expanded, so retained render state stays bounded.
Expanded command cards show bounded stdout and stderr, or the tool-owned combined
background output when separate streams are absent. Non-command cards show only
their bounded user-facing output.

Tool arguments are never rendered. The runtime adapter already projects only
argument field shape, but the TUI does not rely on that as a second redaction
boundary. Terminal controls are stripped and names, paths, and output wrap by
grapheme-aware terminal cell width.

## Repository refresh and status

The status line shows project, branch, model/provider, context usage, and current
activity. The repository card shows bounded changed paths, diff stat, Git
availability, dirty/clean state, truncation, and capture warnings.

Workspace updates after coding tools flow through the existing runtime observer.
Ctrl+R performs an explicit refresh while the controller is idle. The controller
serializes it against turns and compaction, and the native runtime refreshes the
same coding workspace observer used to construct the next prompt before publishing
the snapshot. This prevents a TUI-only Git view from diverging from agent context.
External branch or worktree changes therefore become visible without starting a
dummy turn.

## Evidence

Automated tests cover:

- every tool outcome marker, command exit/duration/background/cancel/truncation
  metadata, collapsed-output secrecy, and controlled expansion;
- deliberate secret argument values supplied directly at the projector boundary,
  proving neither collapsed nor expanded cards render them;
- verified write and current repository cards without conflating their sources;
- project, branch, model/provider, context, and activity status composition;
- explicit refresh serialization, unsupported capability behavior, native runtime
  publication, and branch/clean-state updates in the model; and
- the existing bounded projector, current-view coalescing, workspace observer,
  terminal-width, and race suites.

P4.4 owns the resume picker. P4.5 owns the full in-app command/help surface; P4.3
exposes only the direct presentation controls and Ctrl+R refresh needed by these
cards.
