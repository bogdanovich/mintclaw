# Local Coding Agent TUI Screenshot 13 Polish Roadmap

Status: active. This bounded follow-up owns the workspace-status noise, bottom-surface rhythm, and
live working animation exposed by screenshot `13.png`. It does not reopen the completed
[Codex-like TUI roadmap](local-coding-agent-codex-tui-roadmap.md) or its
[08-12 visual follow-up](local-coding-agent-tui-visual-followup-roadmap.md).

## Baseline and observed gaps

The admission audit compares MintClaw `main` at
`ea39a09f4246a2bd36aa1b486bbc189060e095ca`, screenshot
`/Users/ab/agent-screenshots-2026-08-30-1932/13.png`, and OpenAI Codex `main` at
`a505c71490885a44979df056284badbfdd75b3fb`.

The screenshot reveals three separate problems:

1. MintClaw creates a `tui:workspace` transcript cell whenever a workspace snapshot exists. Even a
   clean repository therefore consumes four persistent rows after every answer, despite `/status`,
   `/diff`, and `Ctrl+R` already exposing the same current state.
2. The normal view reserves a blank row between the composer and footer, but not between the last
   transcript, working, or queued-guidance surface and the composer. The repository cell therefore
   appears attached to input.
3. The live working row already owns correct phase and elapsed-time state, but animated mode only
   alternates `•` and `◦` every 300 ms. Current Codex instead renders a smooth, grapheme-aware
   brightness sweep across the status label, updates it at frame cadence, and uses static fallbacks
   when motion or color capability is reduced.

The large answer at the top of screenshot 13 is one source-backed Markdown cell. It is not a set of
unseparated table cells. MintClaw already inserts vertical whitespace between semantic cells and a
full-width rule at a turn boundary. Adding boxes or rules between every cell would make the
transcript noisier and would not match the Codex hierarchy.

## Decisions

- Remove the ambient workspace cell from the transcript for both clean and dirty repositories.
- Keep detailed current repository evidence in `/status` and `/diff`; keep `Ctrl+R` as the explicit
  refresh action.
- Add `*` to the footer branch only when Git status is available and dirty. Clean and unknown states
  remain visually quiet.
- Reserve exactly one row between a non-empty transcript/activity surface and the composer, plus
  the existing row between composer and footer. Do not add a leading blank row to an empty compact
  startup or to the tiny-terminal fallback.
- Keep horizontal rules as turn boundaries, not generic cell borders.
- Reuse the existing single working ticker and timer. Add presentation-only shimmer spans instead
  of a second lifecycle clock or background goroutine.
- Animate only when motion and palette capability make the effect legible. Reduced motion,
  disabled motion, no-color, and low-color terminals retain explicit static phase and elapsed text.

## Delivery packets

### S13.1 - Quiet repository state and bottom-surface rhythm

Scope:

- remove `tui:workspace`, its insertion ordering, and obsolete renderer;
- retain and test `/status`, `/diff`, and `Ctrl+R` repository discovery;
- add the dirty footer marker without changing the detailed branch field;
- add the transcript/activity-to-composer gap and update height accounting; and
- add screenshot-13-style integrated goldens at narrow, standard, and wide widths.

Acceptance:

- a clean or dirty workspace never adds `Repository changes` to the transcript;
- `/status` still reports repository clean/dirty and `/diff` still lists bounded paths and stats;
- a dirty known branch is shown as `branch*` in the footer and a clean branch as `branch`;
- non-empty normal-height surfaces have one blank row above and below the composer;
- empty adaptive startup stays three rows and 1-4 row terminals retain an input path;
- separate semantic cells retain whitespace, and only turn boundaries use a full-width rule; and
- no-color and 40/80/120-column fixtures remain bounded.

### S13.2 - Codex-like working shimmer

Dependencies: S13.1.

Scope:

- render a two-second brightness sweep across whole grapheme clusters in the active phase label;
- drive the sweep and elapsed clock from the existing cancellable, focus-aware ticker;
- keep phase changes, pause/resume, interruption, background work, and compaction semantics intact;
- use terminal/theme-aware foreground and background-safe tones without blocking palette queries;
  and
- preserve static, readable output for reduced/disabled motion and limited color modes.

Acceptance:

- truecolor animated fixtures show different bounded shimmer frames with unchanged visible text;
- the highlight overlaps adjacent graphemes and never splits combining sequences;
- one frame source updates both animation and elapsed time, with no duplicate pending ticker;
- blur, hidden state, completion, cancellation, and context shutdown stop future frames;
- no-color output contains no ANSI, and reduced/disabled modes contain no time-varying styling;
- elapsed formatting remains `0s`, `1m 05s`, and `1h 01m 01s`; and
- focused unit, golden, race, and PTY lifecycle tests pass.

### S13.3 - Visual and deployed closeout

Dependencies: S13.1-S13.2.

Scope and acceptance:

- reproduce screenshot 13 with a completed Markdown answer, compaction notice, clean and dirty
  workspace states, active work, and the composer/footer;
- verify light, dark, no-color, narrow, SSH, and tmux fixtures;
- update the coding-agent guide and this roadmap with exact merged evidence;
- deploy merged `main` with a verified rollback backup; and
- require healthy services, config loading, a live smoke, and a completed non-truncated diagnostic
  trace before marking this roadmap complete.

## Dependency order and stop condition

```text
S13.1 -> S13.2 -> S13.3
```

Work stops when all acceptance criteria above are merged and deployed. This roadmap does not add a
repository sidebar, per-cell boxes, mouse-only controls, a second presentation store, terminal
palette queries that can block SSH startup, or changes to agent/tool/compaction semantics.
