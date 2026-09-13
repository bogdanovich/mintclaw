# Local Coding Agent TUI Visual Follow-up Roadmap

Status: admitted on 2026-09-13; VF.1-VF.5 remain to be implemented.

This roadmap follows the completed
[Codex-like coding TUI roadmap](local-coding-agent-codex-tui-roadmap.md). The
semantic presentation migration succeeded, but a direct visual comparison with
current Codex exposed five user-visible gaps that the earlier exit criteria did
not catch: MintClaw unnecessarily occupies a full alternate screen when an
empty session opens, assistant Markdown is printed literally, the composer and
footer touch, turn rules stop at 72 columns, and ordinary repository summaries
are much longer than the equivalent Codex answer.

The work is a focused follow-up, not a second presentation rewrite. The current
frontend projection, semantic cell store, bounded viewport, thread persistence,
and Bubble Tea implementation remain the foundation.

## Analysis baselines

The admission audit used these exact inputs:

- MintClaw `origin/main` at
  [`27916010b479b21ccd509c59bc1313baf10d3044`](https://github.com/bogdanovich/mintclaw/commit/27916010b479b21ccd509c59bc1313baf10d3044),
  fetched on 2026-09-13.
- OpenAI Codex `main` at
  [`a505c71490885a44979df056284badbfdd75b3fb`](https://github.com/openai/codex/commit/a505c71490885a44979df056284badbfdd75b3fb),
  fetched on 2026-09-13.
- The ordered screenshots `08.png` through `12.png` in the local reference set
  `/Users/ab/agent-screenshots-2026-08-30-1932`.
- MintClaw's shipped interactive `code`, `code resume`, and project-scoped
  resume paths, semantic transcript cells, composer, status/footer, prompt
  stack, terminal tests, and coding-agent guide.

Codex remains a behavioral reference. MintClaw will not import its Rust TUI,
app-server protocol, storage, sandbox, or approval system.

## What the second screenshot set shows

| Screenshot | Reference behavior | MintClaw gap |
| --- | --- | --- |
| 08 | Codex begins as a compact inline surface below the invoking shell command. | MintClaw has no equivalent ordinary inline mode. |
| 09 | An idle surface uses only the rows needed for identity, composer, and footer. | MintClaw clears the terminal and pads an empty transcript to almost the full height. |
| 10 | Commentary, grouped exploration, a full-width rule, and a compact Markdown response are visually distinct. | The desired hierarchy is readable without mentally parsing syntax. |
| 11 | — | MintClaw exposes literal headings, emphasis delimiters, backticks, and table pipes; its rule ends at 72 columns. |
| 12 | — | The oversized literal response continues for another screen and the composer touches its footer. |

The screenshots reveal product contracts, not just colors. Compact startup must
preserve terminal scrollback; semantic Markdown must reflow from authoritative
source; geometry must remain valid at tiny widths; and response length must be
addressed in the model instructions rather than hidden by truncation.

## Root-cause audit

### Full-screen empty startup

All ordinary interactive entry points currently pass `AlternateScreen: true`.
`tui.Run` translates that directly to `tea.WithAltScreen()`. Independently,
`Model.updateSurfaceDimensions` assigns nearly every remaining terminal row to
the transcript viewport, and `semanticViewport.View` pads even an empty
document to that height. Turning off alternate screen alone would therefore
preserve scrollback but still print a terminal-height blank surface.

Current Codex supports both terminal modes. Its `--no-alt-screen` option and
`tui.alternate_screen = "never"` select an inline viewport that preserves
scrollback. The reference screenshots demonstrate that interaction model; they
do not prove that every current Codex installation defaults to it.

MintClaw should make an adaptive, content-height inline surface the ordinary
coding-thread default. A genuinely full-screen picker or transcript overlay may
still use an alternate screen when its lifecycle is explicit and restoration
is proven.

### Literal Markdown

Assistant and final messages currently pass through `messageDocument`, which
sanitizes the text and splits it into ordinary logical lines. No Markdown parse
occurs. The subsequent wrapper flattens each logical line to plain text and
reapplies only its first style role, so adding bold spans before that wrapper
would still lose mixed styles.

Codex retains the original assistant Markdown in a source-backed message cell
and renders it at the current width. MintClaw should adopt that ownership rule:
raw sanitized source remains canonical for resize, resume, search, and copy;
the viewport receives a derived semantic document. This is safe because the
TUI is interpreting assistant message formatting, not inferring runtime facts
from prose. Plans, tools, commands, diffs, and lifecycle state continue to use
typed observations only.

### Composer/footer and separator geometry

Normal `Model.View` joins the composer and status line with one newline, leaving
no blank row between them. Its height budget likewise reserves no such row.
Tiny-terminal mode must remain a deliberate exception so the input and
interrupt path survive at four rows or fewer.

Turn boundaries explicitly calculate `min(width, 72)`. The cap is why the rule
in screenshots 11–12 ends in the middle of a wide terminal. It should consume
the semantic cell's full available width after any admitted inset.

### Response length is not rendering

MintClaw's coding base instructions ask the model to limit repository evidence
collection and provide progress updates, but they do not define concise final
answers, proportional depth, or sparse terminal-friendly formatting. Current
Codex base instructions do. Hiding, clipping, or collapsing a verbose final
answer would lose user content and is not an acceptable fix.

The screenshot comparison is also not a controlled model experiment: Codex
shows `gpt-5.6-sol` with `xhigh` reasoning, while MintClaw shows the same model
name with `off`. In MintClaw, `off` can currently mean either explicitly off or
that no reasoning value was configured and the provider default will apply.
The footer should not present those two states as equivalent.

Response policy will therefore be a separate prompt packet with deterministic
prompt tests and an optional controlled live comparison using the same model,
provider, reasoning setting, project instructions, and user request.

## Architecture decisions

1. Keep the semantic presentation store and its width-bounded render cache.
2. Keep sanitized raw assistant Markdown as the source of truth; do not persist
   ANSI or a renderer-specific tree.
3. Add a span-aware Markdown renderer/wrapper rather than styling the current
   flattened text after wrapping.
4. Keep plain transcript copy/search deterministic and free of ANSI/OSC
   controls. Link labels may be readable, but destination handling must remain
   explicit and safe.
5. Use content height for the ordinary inline surface until the transcript
   reaches the available viewport height; then preserve bounded scrolling and
   bottom-follow behavior.
6. Keep alternate-screen ownership explicit for surfaces that truly require
   it. Do not switch terminal modes merely because output becomes long.
7. Improve model concision through a small coding-only output contract. Do not
   copy Codex's full prompt or alter always-on chat-agent behavior.
8. Preserve autonomous execution by default; this roadmap adds no approval UI.

## Implementation packets

Each packet is a focused PR. Packet exit records must cite the exact merge,
tests, manual evidence, and any retained difference from the reference.

### VF.1 — Adaptive inline terminal shell

Dependencies: completed TUI.15

Effort: large; highest lifecycle risk

Scope:

- make ordinary `mintclaw code` and active-thread resume sessions inline by
  default so the invoking command and prior terminal scrollback remain visible;
- replace the empty terminal-height viewport with adaptive content height;
- grow the transcript surface only as content arrives, up to the available
  terminal height, then retain bounded scrolling and bottom-follow behavior;
- keep full-screen picker/overlay ownership explicit and restore the prior
  terminal mode, cursor, focus reporting, and viewport anchor on every exit;
- reconcile the prior alternate-screen final-summary behavior with inline
  history so final answers are neither lost nor printed twice; and
- update the coding-agent guide and earlier terminal-shell decision where the
  shipped contract changes.

Acceptance:

- an idle session does not emit `CSI ?1049h` and does not blank or pad a
  24-row terminal;
- shell content above the command remains in scrollback;
- a growing/streaming transcript uses content height before becoming a bounded
  viewport;
- resize, manual scroll, resume hydration, interruption, SIGTERM, panic
  recovery, SSH, and tmux restore a usable terminal;
- a full-screen picker or overlay enters and leaves alternate mode exactly once
  if it still needs that mode; and
- PTY tests cover empty, active, long, interrupted, and resumed sessions.

### VF.2 — Layout rhythm and full-width rules

Dependencies: VF.1

Effort: small

Scope:

- reserve one visible blank row between the composer and persistent footer in
  normal-height terminals;
- omit that decorative row only in the admitted tiny-terminal fallback;
- render turn boundaries across the full semantic viewport width instead of
  capping them at 72 columns;
- audit inter-cell blank rows and insets against screenshots 08–12 without
  adding heavy boxes; and
- keep width arithmetic Unicode-safe and free of horizontal overflow.

Acceptance:

- 40-, 80-, and 120-column fixtures show a full-width turn rule;
- 5-row and taller views include one composer/footer gap, while 1–4-row views
  retain input and interrupt access;
- working and queued-guidance rows do not steal or duplicate the gap;
- no-color output preserves the same hierarchy; and
- layout goldens fail on accidental row or width regressions.

### VF.3 — Source-backed semantic Markdown cells

Dependencies: VF.2

Effort: large

Scope:

- render assistant commentary and final answers as semantic Markdown while
  retaining sanitized source for reflow, resume, search, and copy;
- support paragraphs, headings, unordered and ordered lists, nested lists,
  emphasis, strong text, strikethrough, inline code, fenced code, blockquotes,
  thematic rules, links, and tables;
- adapt tables at narrow widths to a readable stacked/key-value form rather
  than clipping or forcing horizontal terminal scroll;
- preserve mixed styles while wrapping Unicode text at the active viewport
  width;
- define stable partial-stream behavior so an unfinished fence/table cannot
  erase or wildly reflow committed history; and
- reject terminal control injection, unsafe link destinations, raw HTML
  effects, and unbounded Markdown structures.

Acceptance:

- representative answers contain no visible Markdown delimiters unless they
  are escaped or inside code;
- headings, emphasis, lists, code, links, and tables remain readable at 40,
  80, and 120 columns in light, dark, 16-color, 256-color, truecolor, and
  no-color modes;
- resize and resume reconstruct equivalent semantic output from source;
- transcript search/copy remains deterministic, complete, and control-free;
- streaming finalization does not duplicate blocks or reorder later cells;
- cache and long-message benchmarks remain within the TUI.15 budgets; and
- fuzz/security tests cover malformed Markdown, ANSI/OSC payloads, Unicode,
  deep nesting, and oversized tables.

### VF.4 — Concise coding response policy and reasoning truthfulness

Dependencies: completed P2 coding prompt isolation; independent of VF.3 after
VF.1 establishes the visible surface

Effort: small

Scope:

- add coding-only instructions to lead with the outcome, default to concise
  factual answers, scale detail to the request, avoid exhaustive repository
  inventories unless asked, and use only the Markdown structure that improves
  scanability;
- keep progress updates short, phase-oriented, and non-repetitive;
- preserve detailed findings for reviews, investigations, requested audits,
  and genuinely complex handoffs;
- distinguish an explicit `off` reasoning choice from an unset/provider-default
  setting in the footer; and
- document a controlled live A/B procedure rather than treating one screenshot
  pair as quantitative evidence.

Acceptance:

- prompt assembly tests prove the new rules exist only in the isolated coding
  profile and coexist with project instructions;
- ordinary chat/always-on agent prompts are unchanged;
- footer fixtures never label an unknown/provider-default reasoning setting as
  explicitly off;
- a same-model/provider/effort repository-summary smoke leads with a compact
  explanation and does not enumerate unrelated paths; and
- the final response remains complete when the user explicitly requests an
  extensive analysis.

### VF.5 — Visual parity and lifecycle closeout

Dependencies: VF.1-VF.4

Effort: medium

Scope:

- reproduce the 08–12 scenarios with current MintClaw and record visual/PTy
  evidence;
- run the representative matrix in wide/narrow terminals, light/dark/no-color,
  local shell, SSH, and tmux;
- verify empty start, tool progress, compaction, final Markdown, resize, resume,
  interruption, normal exit, and signal restoration;
- update user documentation and known differences from Codex; and
- publish one exit record linking every merged packet and exact verification
  command.

Acceptance:

- the empty screen is compact and preserves prior shell output;
- final answers render semantic Markdown with no unintended raw delimiters;
- composer/footer spacing and full-width separators match the admitted
  geometry;
- normal and abnormal exits restore the shell with no duplicate final answer;
- no obsolete literal assistant-message renderer or unconditional active-thread
  alternate-screen path remains; and
- a deployed `mintclaw code` production smoke demonstrates the merged behavior.

## Dependency order

```text
completed TUI.15 -> VF.1 -> VF.2 -> VF.3 -> VF.5
                         \-> VF.4 -------/
```

VF.1 is intentionally first because terminal mode and adaptive height redefine
the geometry against which later goldens are written. VF.4 may proceed after
VF.1 without touching the renderer. VF.3 follows VF.2 so Markdown tables and
wrapping use the final content-width contract.

## Validation policy

Every implementation PR must include:

- focused semantic/golden tests for the changed contract;
- relevant race tests for `pkg/coding/tui`, frontend/controller, or prompt
  state touched by the packet;
- `make fmt` for changed Go code and the changed-file lint gate;
- Unix PTY lifecycle coverage for any screen-mode or terminal-restoration
  change, plus platform compilation for terminal-specific files;
- no-color and narrow-width evidence where presentation changes;
- a ready-for-review PR, green required checks, resolved actionable review
  threads, and the repository's trusted PR-level approval signal; and
- an exit-record update that distinguishes automated evidence from manual
  visual observation.

## Overall done criteria

This follow-up is complete only when all VF packets are merged and the VF.5
exit record proves:

- ordinary coding sessions start compactly without replacing terminal
  scrollback;
- the transcript grows and scrolls without losing order, resume fidelity, or
  bounded-memory guarantees;
- assistant Markdown is semantically rendered from safe authoritative source;
- the composer/footer gap and separators remain correct across admitted sizes;
- repository-summary answers are concise by default because of an explicit
  coding response policy, not hidden content;
- reasoning status is truthful about explicit versus provider-default choices;
- accessibility, Unicode, no-color, SSH/tmux, interruption, and terminal
  restoration contracts pass; and
- merged `main` is deployed and verified through a real `mintclaw code` smoke.

## Explicit non-goals

- pixel-perfect Codex branding or wording;
- copying Codex's Rust Markdown renderer or prompt wholesale;
- changing thread storage, compaction, tool semantics, or the approval model;
- adding a second persisted rendered transcript beside authoritative source;
- clipping final answers to manufacture apparent concision;
- exposing hidden reasoning or chain-of-thought; and
- changing always-on chat-agent response style as a side effect.
