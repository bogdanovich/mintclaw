# Codex-like coding TUI roadmap

Status: proposed implementation roadmap

This roadmap defines how to evolve `mintclaw code` from its current functional
terminal shell into a coding interface with the clarity, density, and live
feedback demonstrated by OpenAI Codex. It supplements the main
[local coding-agent roadmap](local-coding-agent-roadmap.md); it does not replace
the runtime, storage, compaction, attachment, or Git-review contracts already
admitted there.

The target is interaction parity where it makes coding work easier to follow,
not a pixel-for-pixel clone. MintClaw keeps its own identity, Bubble Tea stack,
always-on agent runtime, provider model, and default autonomous execution
policy.

## Analysis baselines

This analysis used these exact revisions:

- MintClaw `origin/main` at
  [`0cfa3a76f593670961708228bb17ff021ec0639c`](https://github.com/bogdanovich/mintclaw/commit/0cfa3a76f593670961708228bb17ff021ec0639c).
- OpenAI Codex `main` at
  [`a9519cbcdd2d664530edb2469224ee03c1056799`](https://github.com/openai/codex/commit/a9519cbcdd2d664530edb2469224ee03c1056799),
  fetched directly from GitHub on 2026-08-30.
- The seven reference screenshots in
  `/Users/ab/agent-screenshots-2026-08-30-1932`, covering command activity,
  background terminals, plans, file edits, status, compaction, commentary, and
  the live working indicator.
- The official [Codex CLI overview](https://developers.openai.com/codex/cli/),
  which describes the local interactive loop, repository-aware work, visual
  input, and resumable sessions.

Codex source references below are pinned to the analyzed commit. They are
design references only; this roadmap does not propose copying Rust code into
MintClaw.

## Executive recommendation

The change is feasible and fits the existing architecture, but it is a large
presentation-system upgrade rather than a styling pass. The current Bubble Tea,
Bubbles, and Lip Gloss stack is sufficient. Rewriting the TUI in Rust or
embedding Codex would increase integration and maintenance cost without fixing
the actual problem: MintClaw does not yet retain enough ordered, typed
presentation state to render what Codex renders.

The work should proceed in four layers:

1. admit an ordered semantic presentation model and typed safe observations;
2. preserve live commentary and implement mutable active cells;
3. add Codex-like plan, command, diff, status, and transcript renderers; and
4. close out reflow, accessibility, security, performance, and resume parity.

Do not start by making the existing generic tool cards look like Codex. That
would leave plan steps unavailable, tool activity out of transcript order,
intermediate commentary discarded, command groups uncorrelated, and diffs
unverified. The first three foundation packets are therefore release-blocking
for the rest of this roadmap.

Estimated complexity is high but bounded: 16 focused packets, predominantly in
the coding frontend and TUI. Runtime/tool changes are limited to typed
observations and preservation of already-produced assistant commentary. The
always-on chat agent remains unaffected unless it explicitly adopts the same
presentation contracts later.

## What the screenshots are communicating

The reference interface works because it presents a semantic work journal,
not a chronological dump of raw callbacks.

| Reference | Visible behavior | Why it matters |
| --- | --- | --- |
| 01 | Successful commands collapse into `Ran N commands`; background-terminal waits have distinct labels; commentary separates work phases; the final response follows `Worked for …`. | A long turn remains readable while full evidence stays available. |
| 02 | `/status` is a structured card with model, directory, permissions, instructions, account, session, context, and rate-limit information. | Operational state is inspectable without crowding every frame. |
| 03–04 | A persistent `Working (13s • esc to interrupt)` line animates while work is active. Failures remain expanded and visually distinct. | The user always knows whether the process is alive and how to stop it. |
| 05 | Commentary, exploration, compaction, command groups, and an `Edited 2 files (+13 -1)` preview appear in causal order. | The transcript explains the agent's current intent and the observed effect of its work. |
| 06–07 | `Updated Plan` renders as a checklist: pending boxes, a highlighted current step, and dimmed checked completed steps. | Plan progress is live state, not opaque tool output. |

The visual treatment is intentionally restrained. Most meaning comes from
ordering, grouping, verbs, indentation, bounded previews, and state-specific
renderers. Color reinforces those distinctions but is not their only carrier.

## How Codex implements it

### Semantic history cells

Codex commits transcript content as implementations of a `HistoryCell`
interface in
[`history_cell/mod.rs`](https://github.com/openai/codex/blob/a9519cbcdd2d664530edb2469224ee03c1056799/codex-rs/tui/src/history_cell/mod.rs).
Each cell can provide a compact display, raw output, and a full transcript
representation. The main viewport therefore does not have to choose between
showing everything and losing evidence.

The transcript owns one mutable active cell in
[`chatwidget/transcript.rs`](https://github.com/openai/codex/blob/a9519cbcdd2d664530edb2469224ee03c1056799/codex-rs/tui/src/chatwidget/transcript.rs).
Its revision invalidates cached layout while a command, tool, or stream is
updated. Completed cells are then committed. Rendering anchors the active area
above the bottom pane and bounds overflow in
[`chatwidget/rendering.rs`](https://github.com/openai/codex/blob/a9519cbcdd2d664530edb2469224ee03c1056799/codex-rs/tui/src/chatwidget/rendering.rs).

This committed-cells-plus-active-cell model is the central design to adopt.

### Typed thread items and lifecycle events

Codex does not infer all UI meaning from tool names or prose. Its v2 protocol
has explicit thread items for user and agent messages, reasoning, command
execution, file changes, MCP calls, dynamic tools, and other activity in
[`protocol/v2/item.rs`](https://github.com/openai/codex/blob/a9519cbcdd2d664530edb2469224ee03c1056799/codex-rs/app-server-protocol/src/protocol/v2/item.rs).
It also exposes typed plan, diff, command-output, compaction, turn, and item
lifecycle notifications.

MintClaw does not need Codex's app-server protocol. It does need the same
separation between canonical runtime facts and renderer-owned presentation.

### Plans

[`history_cell/plans.rs`](https://github.com/openai/codex/blob/a9519cbcdd2d664530edb2469224ee03c1056799/codex-rs/tui/src/history_cell/plans.rs)
receives typed plan steps. It renders a bold `Updated Plan` heading, an optional
dimmed explanation, checked and crossed-out completed steps, a bold cyan current
step, and dimmed pending steps. The runtime records completed/total progress
when the plan update arrives.

This is notably not a parser over the text returned to the model by the plan
tool.

### Commands and exploration

[`exec_cell/model.rs`](https://github.com/openai/codex/blob/a9519cbcdd2d664530edb2469224ee03c1056799/codex-rs/tui/src/exec_cell/model.rs)
correlates multiple calls by ID, retains active start times, classifies parsed
commands, and applies explicit grouping policy. Successful agent commands can
collapse; failures and user shell commands remain visible.

[`exec_cell/render.rs`](https://github.com/openai/codex/blob/a9519cbcdd2d664530edb2469224ee03c1056799/codex-rs/tui/src/exec_cell/render.rs)
renders `Running` versus `Ran`, bounds inline output with head/tail truncation,
and offers the complete `$ command` transcript separately. Read, list, and
search commands become deduplicated `Exploring`/`Explored` summaries instead of
generic shell invocations.

[`chatwidget/command_lifecycle.rs`](https://github.com/openai/codex/blob/a9519cbcdd2d664530edb2469224ee03c1056799/codex-rs/tui/src/chatwidget/command_lifecycle.rs)
correlates start, output, and completion, mutates the active cell, flushes at
semantic boundaries, handles background terminals, and avoids combining an
orphan completion with unrelated work.

### File edits and diffs

[`history_cell/patches.rs`](https://github.com/openai/codex/blob/a9519cbcdd2d664530edb2469224ee03c1056799/codex-rs/tui/src/history_cell/patches.rs)
and
[`diff_render.rs`](https://github.com/openai/codex/blob/a9519cbcdd2d664530edb2469224ee03c1056799/codex-rs/tui/src/diff_render.rs)
produce file counts, addition/deletion totals, bounded syntax-highlighted
previews, line numbers, and a full transcript.

The renderer treats each diff row as four visual layers: line-number gutter,
`+`/`-` sign, syntax-highlighted content, and a full-width row background. The
last layer extends the addition or deletion tint through the right-side padding
rather than coloring only the text spans. On a light terminal, the fallback
palette uses GitHub-style addition and deletion backgrounds (`#dafbe1` and
`#ffebe9`) plus more saturated gutter backgrounds (`#aceebb` and `#ffcecb`).
Dark terminals use muted tints (`#213a2b` and `#4a221d`) so syntax colors remain
legible. An active syntax theme may override the rich-color backgrounds through
its inserted/deleted scopes.

Truecolor values are quantized deliberately for 256-color terminals. ANSI-16
does not use colored backgrounds because its saturated background entries can
overpower the content; it falls back to green/red foregrounds plus explicit
`+`/`-` signs. Wrapped continuation rows retain the row background, while
unchanged context rows retain the terminal's default background. These are
useful behavioral contracts for MintClaw, not Codex-specific implementation
details.

MintClaw's P6.4 repository-evidence contract is stricter about provenance than
the visual reference. The new TUI should render that typed evidence rather than
weaken it to match Codex.

### Working status and turn completion

[`status_indicator_widget.rs`](https://github.com/openai/codex/blob/a9519cbcdd2d664530edb2469224ee03c1056799/codex-rs/tui/src/status_indicator_widget.rs)
owns the status header, details, interrupt hint, and elapsed duration. It
pauses/resumes elapsed time and schedules frames while animation is enabled.
[`motion.rs`](https://github.com/openai/codex/blob/a9519cbcdd2d664530edb2469224ee03c1056799/codex-rs/tui/src/motion.rs)
centralizes animated and reduced-motion behavior.

[`history_cell/separators.rs`](https://github.com/openai/codex/blob/a9519cbcdd2d664530edb2469224ee03c1056799/codex-rs/tui/src/history_cell/separators.rs)
adds the final `Worked for …` separator only when a turn performed concrete
work. The turn runtime flushes streams and active commands before inserting it.

### Tests are part of the architecture

Codex has snapshot and state-transition coverage for narrow and wide terminals,
command grouping, overlapping calls, failures, full transcripts, plan states,
status animation, reduced motion, remapped interrupt keys, reflow, and rich
messages. MintClaw should adopt the testing pattern, while keeping semantic
state assertions alongside snapshots so harmless style changes do not rewrite
the entire suite.

## Current MintClaw architecture and gaps

The existing implementation has good boundaries to build on:

- `pkg/coding/frontend` publishes a bounded `ThreadSnapshot` through a
  coalescing subscription.
- `pkg/coding/tui` already owns Bubble Tea lifecycle, composer, viewport,
  status, commands, resize, and interruption.
- command observations and verified write audits already avoid displaying
  untrusted model claims as repository facts.
- canonical JSONL, Seahorse-derived state, resume, compaction, and attachments
  are independent from the renderer.

The following gaps explain the visible difference.

| Area | Current behavior | Required behavior |
| --- | --- | --- |
| Ordering | Transcript entries and tools are separate collections; tools are appended around turn boundaries. | One causally ordered semantic timeline with stable identity and sequence. |
| Tool meaning | `ToolState` mostly carries name, argument shape, status, duration, and a command observation. | Typed observations for plans, exec, repository changes, MCP, compaction, and other admitted surfaces. |
| Plans | `update_plan` returns silent JSON for the model; the TUI sees `Tool [ok] update_plan` and cannot access the steps. | A safe typed `PlanObservation` projected directly to a checklist cell. |
| Commentary | The projector has one assistant entry per turn. Tool-call streaming is cancelled/replaced, so inter-round commentary can disappear. | Each commentary message remains a distinct ordered item before the work it introduces. |
| Active work | Snapshot replacement rebuilds flat strings; there is no mutable active semantic cell. | An active cell updates by identity and becomes immutable when completed. |
| Commands | Generic selectable tool cards require manual expansion. | Semantic command/exploration grouping, bounded live output, prominent failures, and a full transcript overlay. |
| Diffs | Session-level changed paths and stats exist; rich hunks depend on P6.4. | Typed per-turn edit summary and bounded hunk renderer based on verified evidence. |
| Status | A static footer reports activity/project/branch/model/context. | A separate live status surface with elapsed time, interrupt hint, details, and reduced motion. |
| Final boundary | The final answer is another transcript entry. | Flush active work, optionally show `Worked for …`, then render the final response. |
| Raw evidence | Tool expansion is card-oriented and local. | One consistent full-transcript overlay for commands, diffs, tools, and truncated cells. |

The coalescing subscription is not itself a problem. A slow TUI should still
converge to the newest bounded snapshot. The snapshot must, however, contain
the complete bounded semantic aggregate. Coalescing must not erase committed
items that existed only in an intermediate snapshot.

## Target interaction model

A representative turn should read like this:

```text
› Fix the failing session-resume test.

• I’ll trace the resume path and reproduce the failure before editing it.

• Updated Plan
  └ Reproduce the failure, isolate the state mismatch, patch it, and verify it.
    □ Reproduce the failing test
    □ Trace resume-state reconstruction
    □ Implement and verify the fix

• Explored
  └ Read pkg/coding/thread/store.go
    Search ResumeThread in pkg/coding

• Ran 3 commands · ctrl+t to view transcript

• The mismatch is in derived-state rebuild; I’m narrowing the patch to that path.

• Edited 2 files (+18 -4)
  └ pkg/coding/thread/store.go +12 -3
    pkg/coding/thread/store_test.go +6 -1

────────────────────────────────────────────────────────────────────
• Worked for 4m 18s

The resume-state mismatch is fixed and the focused tests pass.

• Working (19s • esc to interrupt)
› Ask MintClaw to do anything
  gpt-5.6-sol · medium · ~/src/mintclaw
```

The status line is present only while appropriate. Commentary is assistant
output, not hidden reasoning. Reasoning remains independently configurable.
The exact key hint should reflect MintClaw's admitted bindings.

## Presentation contracts and invariants

These contracts should be agreed before implementation.

### Ordered semantic timeline

The frontend snapshot exposes a bounded ordered list of semantic items. Every
item has at least:

```text
item ID
thread ID
turn ID
monotonic presentation sequence
kind and typed payload
lifecycle: active, completed, failed, interrupted, or unknown
created/start time and optional completed duration
preview bounds and truncation metadata
```

Recommended initial kinds are user message, assistant commentary, final answer,
reasoning summary, plan update, command group, exploration group, file change,
MCP/tool call, warning/error, compaction marker, and turn separator. A generic
fallback remains for admitted tools without a renderer, but it is not the
default representation of known coding tools.

The sequence is assigned at the projection boundary, not in the renderer.
Updates retain identity and sequence. Completion never moves an item around a
later message.

### Committed and active cells

The TUI maps semantic items to cells. Completed cells are immutable. At most
one foreground cell is mutable at a time in the first implementation; a cell
may internally contain multiple correlated commands. Background processes are
represented as explicit children or status details rather than silently
claiming the only active slot.

Every cell supports:

- compact lines for the main viewport;
- full transcript lines for inspection/copy;
- width-dependent wrapping without changing semantic state;
- a stable revision for render-cache invalidation; and
- a plain/no-color representation.

### Trusted typed observations

The TUI must never parse model prose, `ForLLM` text, arbitrary tool arguments,
or shell output to invent trusted state. Tool packages publish bounded,
redacted observations through admitted types. The first additions should be:

- `PlanObservation` with explanation and ordered step/status values;
- richer command lifecycle/output metadata around the existing command
  observation;
- repository status/diff observations from P6.4; and
- explicit background-process state where the executor can prove it.

Arguments and output omitted for confidentiality do not become visible merely
because a rich renderer exists.

### Commentary and final answers

Each assistant message emitted before a tool round is retained as a distinct
commentary item. Final response content is explicitly marked final. Provider
retry and fallback may replace an uncommitted streaming attempt but cannot
erase already-committed commentary from an earlier successful round.

The coding prompt should request short, concrete progress updates before a new
work phase and after material discoveries. Prompting is a supporting measure,
not a substitute for preserving messages. The TUI never synthesizes
commentary that the model did not produce.

### Compact versus full evidence

Main-view previews are deterministic and bounded by rows, bytes, and item
counts. Truncation always has an explicit indicator and route to the full
transcript. Full transcript content may be lazily hydrated from canonical or
thread-owned evidence; it is still bounded against hostile or accidentally
huge output and must sanitize terminal control sequences.

`Ctrl+T` is the preferred parity binding if it has no conflicting MintClaw
contract. Help and command discovery must expose the actual binding instead of
hard-coding the label in rendered text.

### Resume and coalescing

Restart reconstructs the same committed semantic order from canonical JSONL
and admitted thread-owned evidence. Runtime-only active work interrupted by a
crash is rendered as interrupted or unknown, never successful. A slow
subscriber may miss animations and intermediate output chunks, but the newest
snapshot contains every retained committed item plus the latest active state.

Derived presentation indexes may be rebuilt or discarded. They do not become
a new conversation authority beside canonical JSONL and Seahorse.

### Timing and motion

Elapsed working time uses a monotonic process clock while live and a stored or
event-provided duration once completed. Waiting, paused, and interrupted states
must be explicit. Animation has one centralized ticker and a reduced-motion or
disabled mode. Hidden/off-screen cells do not schedule independent ticks.

### Theme, safety, and accessibility

All views work without color. Success, current, failure, interruption, and
unknown states differ by text or glyph as well as color. Renderers sanitize
control characters, unsafe escape sequences, paths, URLs, command output, and
user content. Palette selection supports dark/light backgrounds and degrades
to 256, 16, and no-color terminals.

## Implementation roadmap

Each packet should normally be one focused PR. Packet IDs are `TUI.*` so they
do not conflict with the main roadmap's already-completed P3/P4 foundation or
the open P6.4 Git-review work.

### TUI.0 — Golden UX fixtures and renderer seam

Status: implemented after TUI.1 and TUI.2. PR #1010's speculative adapter was
removed by the architecture-simplification O4 packet because it wrapped the
temporary flat transcript through a `legacy` cell. The replacement consumes
authoritative `PresentationItem` values, reconciles them into a disposable
model-owned semantic-cell store by stable ID/revision, and keeps the renderer
boundary inactive in the shipped viewport until TUI.4 completes the cutover.

Dependencies: none

Effort: medium

Scope:

- Convert the seven reference scenarios into repository-owned, synthetic,
  secret-free event fixtures and expected semantic assertions.
- Introduce a cell-renderer boundary with compact/full/plain behavior only
  alongside the first real semantic cells and their owning store.
- Add deterministic width, theme, and terminal-capability inputs.
- Add snapshot support for selected 40-, 80-, and 120-column views.

Done when:

- fixtures cover plan progression, grouped success, visible failure,
  background waiting, file edits, compaction, commentary, live working state,
  and final separation;
- snapshots contain no timestamps, paths, animation phase, or other unstable
  host state unless explicitly normalized; and
- semantic reducer assertions accompany visual snapshots.

Non-goals: changing runtime events or shipping a new look.

### TUI.1 — Ordered presentation item contract

Dependencies: TUI.0

Effort: large

Scope:

- Replace the transcript-plus-separate-tools rendering assumption with one
  ordered semantic item contract in `pkg/coding/frontend`.
- Add stable IDs, sequence, lifecycle, timestamps/duration, revisions, and
  typed payloads.
- Preserve the bounded/coalescing current-view architecture.
- Define deterministic orphan completion, duplicate event, retry, fallback,
  cancellation, and compaction behavior.
- Add a compatibility projection for the old TUI during migration.

Done when:

- tools and messages remain in causal order under success, retry, failure,
  cancellation, and resume;
- a slow subscriber converges without losing a committed item;
- reducer tests prove idempotency and bounded memory; and
- the frontend package contains no TUI styling or Bubble Tea types.

Non-goals: app-server IPC, revision replay, or replacing canonical JSONL.

### TUI.2 — Typed plan and tool observations

Dependencies: TUI.1

Effort: medium

Scope:

- Extend the safe tool observation union with `PlanObservation`.
- Publish the validated explanation and ordered steps/statuses from
  `update_plan` without exposing arbitrary model-facing JSON.
- Define extensible typed observations for repository evidence and background
  execution while retaining a safe generic fallback.
- Apply explicit byte/item bounds and redaction at production time.

Done when:

- frontend fixtures receive exact plan steps without parsing tool arguments or
  `ForLLM` output;
- invalid plans never enter presentation state;
- generic chat-agent behavior and silent tool-result semantics are unchanged;
  and
- secret/redaction tests cover typed and fallback observations.

### TUI.3 — Preserve inter-round commentary

Dependencies: TUI.1

Effort: large

Scope:

- Give every provider-produced assistant message a distinct presentation
  identity and phase: commentary or final.
- Commit successful commentary before its corresponding tool calls.
- Limit stream rollback to the failed/uncommitted provider attempt.
- Reconstruct commentary order on resume from canonical message/tool-call
  history or an admitted durable phase marker.
- Add a coding-only prompt contract for concise progress updates at meaningful
  phase changes.

Done when:

- the commentary visible before a tool round remains visible after later
  rounds, finalization, compaction, and restart;
- retry/fallback produces neither duplicates nor erased committed text;
- an empty commentary does not add blank cells; and
- reasoning text cannot be mislabeled as commentary.

### TUI.4 — Committed-cell and active-cell store

Dependencies: TUI.1

Effort: large

Scope:

- Extend the renderer-neutral committed and active cell state admitted by
  TUI.0 into the visible TUI rendering path.
- Update active cells by stable item ID/revision instead of rebuilding one flat
  transcript string for every output chunk.
- Cache width-sensitive layout by cell revision, width, theme, and mode.
- Preserve scroll position when cells above or within the viewport reflow.
- Flush active state deterministically at turn completion and shutdown.

Done when:

- high-frequency output updates do not re-render the complete transcript;
- completion converts exactly one active cell into one committed cell;
- resize and resume preserve ordering and composer state; and
- race, leak, and long-output benchmarks stay within declared limits.

### TUI.5 — Live working indicator and motion system

Dependencies: TUI.4

Effort: medium

Scope:

- Add `Working (elapsed • key to interrupt)` above the composer.
- Support phase text such as exploring, running, waiting for a background
  terminal, compacting, and reviewing without exposing raw internal tool names.
- Centralize animation/ticks and add animated, reduced, and disabled modes.
- Keep the existing footer for stable project/model/context metadata.

Done when:

- elapsed time advances only while the foreground turn is active;
- interruption, pause, background wait, completion, and terminal blur have
  deterministic behavior;
- the displayed key follows configured bindings; and
- no-color and reduced-motion snapshots communicate equivalent state.

### TUI.6 — Native plan checklist cell

Dependencies: TUI.2, TUI.4

Effort: small

Scope:

- Render `Updated Plan`, optional explanation, completed/current/pending steps,
  wrapping, and progress.
- Mutate the active/latest plan cell as tool updates arrive while retaining
  meaningful historical plan changes.
- Use symbols and text styles that work in limited terminals.

Done when:

- pending, current, completed, and all-complete plans match the semantic
  fixtures at narrow and wide widths;
- completed steps are distinguishable without color;
- repeated identical updates do not create noise; and
- `/status` and resume report the same authoritative current plan.

### TUI.7 — Command lifecycle and full transcript

Dependencies: TUI.4

Effort: large

Scope:

- Correlate command start, bounded output deltas, terminal interaction, and
  completion by call/process identity.
- Render running/succeeded/failed/interrupted/unknown outcomes with command,
  cwd where useful, exit code, duration, and bounded output.
- Add the full transcript overlay and copy-safe plain rendering.
- Keep failures and user-initiated shell commands expanded.
- Make orphan completion and background-process state explicit.

Done when:

- output arriving before/after lifecycle edges cannot attach to another
  command;
- five-line preview/head-tail truncation and full transcript are both tested;
- ANSI/control-sequence injection cannot alter the terminal; and
- 32 concurrent or sequential calls stay bounded and correctly correlated.

### TUI.8 — Exploration classification and command grouping

Dependencies: TUI.7

Effort: medium

Scope:

- Classify MintClaw's native read/list/search operations as semantic
  exploration without brittle parsing of arbitrary shell strings.
- Deduplicate adjacent exploration items and summarize successful command
  batches as `Ran N commands`.
- Flush groups before failures, commentary, user-attention requests, patches,
  and final answers.
- Display the discoverable full-transcript binding on collapsed groups.

Done when:

- successful groups collapse without hiding failures or skipped commands;
- read/list/search labels include bounded useful paths/patterns;
- overlapping active commands remain visible; and
- the full transcript preserves exact causal order.

### TUI.9 — File-change and diff cells

Dependencies: TUI.4, P6.4 packet 3

Effort: large

Scope:

- Render typed `Edited N files (+A -D)` summaries, per-file stats, and bounded
  hunks from the P6.4 evidence service.
- Distinguish verified thread write audits, pre-existing changes,
  first-observed changes, and indeterminate provenance.
- Add line numbers, rename/binary/submodule states, truncation, and full
  transcript navigation.
- Give every inserted render row a full-width green-tinted background and every
  deleted render row a full-width red-tinted background, including wrapped
  continuation rows and right-side padding. Keep context rows on the terminal
  default background.
- Use GitHub-like pastels and a stronger line-number gutter on light terminals,
  muted tints on dark terminals, and palette-aware syntax highlighting that
  preserves the enclosing diff background.
- Detect truecolor, 256-color, 16-color, and no-color capabilities. Quantize
  rich colors deliberately; degrade 16-color and no-color modes to explicit
  `+`/`-` signs and readable foreground styles instead of saturated backgrounds.

Done when:

- no renderer claim implies MintClaw authored externally observed changes;
- large/binary/changing files have explicit bounded states;
- golden tests prove full-row green/red backgrounds, distinct light-theme
  gutters, syntax-highlight preservation, wrapped-row backgrounds, and
  unstyled context rows;
- light, dark, 16-, 256-, truecolor, and no-color fixtures remain legible and
  distinguish insertions from deletions without relying on color alone; and
- diff refresh cannot silently replace the historical evidence for an earlier
  cell.

### TUI.10 — Semantic MCP and tool cells

Dependencies: TUI.2, TUI.4

Effort: medium

Scope:

- Replace known generic `Tool [ok]` cards with semantic MCP/tool renderers.
- Show safe server/tool identity, purpose, lifecycle, duration, bounded result,
  and actionable errors.
- Keep a compact generic fallback for unknown tools.
- Preserve current argument-shape and redaction guarantees.

Done when:

- success, failure, cancellation, timeout, and no-progress halt are distinct;
- deferred MCP discovery does not render as executed work;
- repeated identical calls remain separately auditable or intentionally
  grouped with a count; and
- sensitive values never appear through compact or full views.

### TUI.11 — Turn separators, compaction, and final response

Dependencies: TUI.3, TUI.4, TUI.5

Effort: medium

Scope:

- Flush active cells before the final answer.
- Add a subtle separator and `Worked for …` only for turns with concrete work
  and useful elapsed duration.
- Render compaction start/completion/failure as lifecycle state and a compact
  transcript marker.
- Keep final answers visually distinct from progress commentary.

Done when:

- chat-only answers do not receive a misleading work separator;
- tool, patch, review, and compaction turns do;
- interrupted/failed turns use truthful wording; and
- resume reconstructs the same completed duration and boundary.

### TUI.12 — Codex-like status card and footer hierarchy

Dependencies: TUI.5

Effort: medium

Scope:

- Redesign `/status` as a structured card with identity, model/reasoning,
  project/directory, branch, instructions source, session/thread, context,
  compaction, permissions/autonomy, and provider/account state when available.
- Keep volatile/rate-limit fields optional and provider-specific.
- Reduce the persistent footer to the highest-value stable facts and avoid
  duplicating the live working line.
- Provide plain output for noninteractive/status diagnostics.

Done when:

- missing optional data is omitted or labelled unavailable rather than
  invented;
- narrow terminals reflow instead of horizontal clipping;
- paths and account identifiers obey privacy/redaction policy; and
- status has deterministic fixtures for Git/non-Git, resumed, compacted, and
  limited-provider sessions.

### TUI.13 — Composer, queued input, and visual hierarchy polish

Dependencies: TUI.5, TUI.11

Effort: medium

Scope:

- Harmonize composer, attachment chips, long-paste labels, queued/steering
  input, placeholder, working line, and footer spacing.
- Preserve `Ask MintClaw to do anything` as the idle placeholder.
- Make multiline input, attachments, and queued messages visually clear
  without adding heavy boxes or banners.
- Review every glyph, color, label, and blank line against the reference
  scenarios and MintClaw branding.

Done when:

- the typed text has sufficient contrast on detected light and dark terminals;
- long paste/image/file labels retain their structured payload behavior;
- queued input cannot be mistaken for submitted history; and
- tiny terminals always leave a usable composer row and interrupt path.

### TUI.14 — Transcript navigation, search, copy, and accessibility

Dependencies: TUI.7, TUI.9, TUI.10

Effort: medium

Scope:

- Make full transcript a consistent overlay across commands, diffs, and tools.
- Add search, next/previous match, copy-safe plain output, and clear exit/help.
- Retain hyperlinks only when the terminal supports safe OSC-8 rendering.
- Audit keyboard-only navigation, screen-reader-friendly plain mode, Unicode,
  bidi-adjacent text, grapheme width, and IME behavior.

Done when:

- a user can find and copy any retained command/error/diff line without
  expanding generic cards one at a time;
- focus and scroll return to their prior locations after closing the overlay;
- unsafe links/control data are inert; and
- every action is discoverable without color or a mouse.

### TUI.15 — Migration, performance, and parity closeout

Dependencies: TUI.6 through TUI.14

Effort: large

Scope:

- Remove the old flat transcript builder and generic tool-selection UX after
  semantic parity is proven.
- Benchmark first paint, output update, resize/reflow, long transcript, and
  transcript hydration.
- Run PTY scenarios through SSH, tmux, narrow terminals, interruption,
  compaction, crash/restart, and provider fallback.
- Document rich/raw modes, bindings, status, accessibility, and known
  differences from Codex.
- Add privacy-safe counters for presentation latency, coalescing, render cost,
  truncation, and hydration failures.

Done when:

- every overall acceptance criterion below passes;
- no compatibility projection or dual renderer remains;
- the TUI remains bounded during a multi-hour synthetic session; and
- the main coding roadmap links a merged exit record with exact validation
  evidence.

## Dependency graph and safe parallelism

The recommended order is:

```text
TUI.0 -> TUI.1 -> TUI.2 -> TUI.3 -> TUI.4
                    |                 |
                    |                 +-> TUI.5 -> TUI.12
                    |                 |       |
                    +-> TUI.6         |       +-> TUI.13
                                      |
                                      +-> TUI.7 -> TUI.8
                                      |
P6.4 packet 3 ------------------------+-> TUI.9
                                      |
                    TUI.2 ------------+-> TUI.10
                                      |
                    TUI.3 + TUI.5 ----+-> TUI.11

TUI.7 + TUI.9 + TUI.10 -> TUI.14
TUI.6 ... TUI.14       -> TUI.15
```

After TUI.4 merges, TUI.5, TUI.6, TUI.7, and TUI.10 can proceed in parallel if
they do not change the shared cell protocol. TUI.9 should follow the relevant
P6.4 typed evidence packet rather than inventing an interim Git parser. TUI.13
should wait for the working/status hierarchy so it does not polish a layout
that will immediately change.

## Overall done criteria

The Codex-like TUI initiative is complete only when all of the following are
true.

### Reference-scenario parity

- Plan updates render a live checklist with pending, current, and completed
  states.
- Assistant progress commentary remains visible in causal order between tool
  phases and after resume.
- Adjacent successful commands and exploration calls collapse into meaningful
  summaries; failures remain prominent.
- `Ctrl+T` or the documented equivalent opens the complete ordered evidence.
- Active work shows phase, elapsed time, and the correct interrupt key.
- File edits render verified file/stat/hunk evidence with truthful provenance,
  full-width green backgrounds for additions, and full-width red backgrounds
  for removals on capable terminals.
- Compaction is visible without exposing or replaying its full internal prompt.
- Concrete work ends with a bounded elapsed separator before a distinct final
  answer.
- `/status` provides a legible structured operational summary.

### Correctness and durability

- Start, delta, completion, retry, fallback, interruption, timeout, crash, and
  orphan events reduce deterministically.
- Resume reconstructs the same committed semantic order and truthful terminal
  lifecycle states.
- A slow coalescing subscriber loses animation frames, not committed items.
- No duplicated commentary, command, patch, plan, or final answer appears after
  provider fallback or restart.
- Canonical JSONL and admitted thread-owned evidence remain the authorities;
  renderer caches can be deleted and rebuilt.

### Bounds and performance

- Long commands, large diffs, many tools, huge pastes, and long sessions remain
  within declared item, byte, row, and memory bounds.
- Main-view updates are proportional to the changed active cell and visible
  viewport, not total transcript length.
- First paint, 10 Hz output, resize, overlay open, and transcript search meet
  budgets established in TUI.0 before closeout.
- No ticker, command stream, renderer cache, or overlay leaks after turn/thread
  completion.

### Terminal quality

- Golden scenarios pass at 40, 80, and 120 columns.
- Light, dark, truecolor, 256-color, 16-color, and no-color modes remain
  readable.
- Reduced-motion mode has no shimmer or rapid animation and retains explicit
  activity text.
- SSH, tmux, normal exit, interrupt, SIGTERM, and induced panic restore the
  terminal.
- IME, combining characters, CJK, emoji, pasted text, and rich attachment
  labels preserve cursor and wrapping correctness.

### Safety and trust

- UI state is derived from typed runtime/tool/repository evidence, never model
  claims or renderer parsing of arbitrary tool text.
- Arguments, output, paths, links, and user text cannot inject terminal control
  sequences.
- Compact and full views enforce the same redaction boundary.
- Repository cells distinguish observed change from verified MintClaw write
  actions and never claim ownership of pre-existing edits.
- No new approval dialog or sandbox selector is introduced; MintClaw's
  autonomous-by-default policy remains explicit in status/help.

## Deliberate differences from Codex

MintClaw should preserve these differences unless a later roadmap explicitly
changes them:

- Bubble Tea/Lip Gloss instead of a Rust/Ratatui port.
- Autonomous execution by default, without Codex's complex approval UI.
- Provider-neutral model and authentication support.
- Native integration with MintClaw coding attachments, Seahorse-derived state,
  always-on agents, paired companions, and future remote coding delegation.
- Stronger explicit provenance for pre-existing versus observed-during-thread
  repository changes through P6.4.
- One bounded in-process current view rather than adopting Codex's app-server
  transport solely for the local TUI.

## Explicit non-goals

- pixel-perfect copying of Codex branding, wording, or every slash command;
- importing Codex's Rust TUI, app server, sandbox, approval model, or storage;
- replacing Bubble Tea before evidence demonstrates a framework limitation;
- exposing hidden chain-of-thought as progress commentary;
- parsing arbitrary shell commands to assert trusted filesystem effects;
- coupling local TUI completion to Telegram/always-on delegation or remote
  companion execution; and
- completing P6.4 review semantics inside this presentation roadmap.

## First implementation goal

The first goal should combine TUI.0 through TUI.2 only:

> Admit the Codex-like presentation foundation: repository-owned golden UX
> fixtures, a compact/full semantic cell seam, one bounded ordered presentation
> item contract, and a typed safe plan observation, while preserving the
> existing visible TUI through a temporary compatibility projection.

Its done criteria are the union of TUI.0, TUI.1, and TUI.2. It should not add
animation or restyle generic tool cards. Once it merges, TUI.3 and TUI.4 form
the next sequential goal and unlock visible UX work without building on a
temporary data model.
