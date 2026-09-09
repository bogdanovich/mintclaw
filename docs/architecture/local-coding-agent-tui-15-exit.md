# Local Coding Agent TUI.15 Exit Record

Roadmap packet:
[TUI.15 — Migration, performance, and parity closeout](local-coding-agent-codex-tui-roadmap.md#tui15--migration-performance-and-parity-closeout).

The merge containing this record closes TUI.15 and the complete TUI.6–TUI.15
initiative. The destructive
[migration checkpoint](local-coding-agent-tui-15-migration.md) removed the
compatibility projection and obsolete renderer. The
[performance checkpoint](local-coding-agent-tui-15-performance.md) established
explicit budgets, bounded caches, privacy-safe diagnostics, focused
benchmarks, and four-hour logical-session evidence. This final packet adds the
PTY/recovery matrix, current user guide, and requirement-by-requirement audit.

## Final shipped architecture

- `frontend.ThreadSnapshot.Items` is the only ordered presentation state.
  Canonical per-thread JSONL and admitted typed evidence remain authoritative;
  semantic cells, viewport documents, overlays, and search indexes are
  disposable bounded projections.
- There is one semantic live renderer and one full transcript projection over
  the same cells. The flat transcript builder, parallel message/tool/change
  arrays, compatibility synchronizer, generic selected-tool UI, and removed
  expansion keybindings no longer exist.
- Runtime, persistence, terminal control, and presentation stay separated by
  `frontend.Controller`. The TUI owns no agent loop, provider, session store,
  repository parser, or compatibility state machine.
- Presentation metrics retain only durations, counters, and high-water marks.
  They contain no prompts, paths, arguments, output, diffs, search terms,
  attachment metadata, account identity, or transcript text.

## PTY and recovery matrix

`app_terminal_unix_test.go` now drives Bubble Tea through a real pseudo-terminal
instead of calling model methods alone. The deterministic matrix covers:

| Scenario | Size/environment | Required observation |
| --- | --- | --- |
| Normal `/exit` | 80×24 xterm-256color | alternate screen, bracketed paste, and cursor restore |
| Idle `Ctrl+C` | 80×24 xterm-256color | clean exit and the same restore sequence |
| Active `Ctrl+C` | 80×24 with an owned running command | graceful interruption becomes visible, then idle exit restores the shell |
| `SIGTERM` | 80×24 xterm-256color | Bubble Tea signal shutdown restores the shell |
| Induced subscription panic | 80×24 xterm-256color | panic recovery restores the shell before controller cleanup |
| SSH-shaped fallback | 80×24 with `SSH_CONNECTION` and `SSH_TTY` | provider-fallback warning and one final answer remain visible |
| tmux-shaped compaction | 120×30 with `TMUX`, `TMUX_PANE`, and screen TERM | completed compaction and continued final answer remain visible |
| Real tmux client/server | nested 80×24 PTY when `tmux` is installed | the TUI runs inside tmux and both terminal layers restore |
| Narrow crash/resume | 40×16, no color, resumed runtime | retained pre-crash work, interrupted boundary, resumed final answer, and stacked resumed status are readable |

The SSH-shaped test deliberately validates the TTY/environment behavior without
depending on network access, an SSH daemon, credentials, or host policy. The
real tmux test is skipped only when the executable is unavailable. Clipboard
routing has separate deterministic coverage for local, SSH, tmux, and bounded
tmux-wrapped OSC 52 paths.

The PTY presentation evidence composes with existing runtime tests rather than
pretending a fabricated snapshot proves durability:

- `TestNativeCodingCommandEditsAndResumesAcrossProcessBoundary` constructs a
  fresh runtime over the same external thread root, proves prior decisions and
  tool journal reach the resumed provider, performs a second write, and asserts
  every user/final message occurs exactly once in canonical history;
- `TestNativeControllerTranscriptPageRestoresCommentaryOrderAfterRestart`
  rebuilds ordered user, commentary, evidence-only tool, and final entries from
  the canonical store;
- `TestStreamingFallbackAndVisibleFailureRemainUnambiguous` proves invisible
  pre-output fallback can produce one final answer while visible partial output
  followed by failure cannot be relabelled as success;
- adapter tests preserve retry/fallback warnings, redaction, compaction, tool
  interruption, and terminal turn ordering; and
- foreground and background compaction tests prove a completed checkpoint does
  not regress an already completed turn.

## Overall acceptance audit

### Reference-scenario parity

- **Live plan:** TUI.6 renders the authoritative current plan as a native
  checklist and restores its checkpoint without parsing model-facing text.
- **Causal progress and grouping:** TUI.7–TUI.8 retain commentary between typed
  work phases, group only compatible successful commands/exploration, keep
  failures prominent, and expose complete evidence through `Ctrl+T`.
- **Working state:** TUI.4/TUI.12 show truthful phase, elapsed time, interrupt
  binding, and a structured status card without duplicating it in the footer.
- **Repository evidence:** TUI.9 consumes P6.4 typed provenance, never assistant
  claims, and provides width-filling green/red hunks with explicit no-color
  signs and line numbers.
- **MCP/tools:** TUI.10 correlates semantic lifecycle by turn/call ID, preserves
  repeated calls, renders no-progress protection separately from tool outcome,
  and keeps an explicit fallback card for unknown tool types.
- **Compaction and final boundary:** TUI.11 shows safe compaction lifecycle,
  truthful work termination, a bounded elapsed separator, and a distinct final
  answer. The PTY matrix proves compaction and interruption remain visible.
- **Composer and complete transcript:** TUI.13–TUI.14 provide accessible input,
  paste/attachment labels, queued guidance, unified search/copy/navigation, and
  focus/position restoration.

### Correctness and durability

- Typed reducer tests cover start, delta, completion, retry, fallback,
  interruption, timeout, crash uncertainty, and orphan completion.
- Coalescing is revision-aware: skipped animation updates increment a counter,
  while committed items converge without duplication.
- Resume reconstruction is canonical-store based and deterministic. Renderer
  caches can be deleted without changing history or ownership.
- Native cross-process and fallback tests assert exactly-once user/final
  messages and no blind mutation replay.

### Bounds and performance

- The [performance checkpoint](local-coding-agent-tui-15-performance.md)
  records explicit first-paint, update, resize, hydration, viewport, overlay,
  and Unicode-search latency/allocation budgets with measured baselines.
- Live presentation retains at most 256 messages, 128 tools, 64 observation
  items, and 256 hydrated historical entries. A cell has at most four render
  documents, and closing the overlay releases its derived line/match caches.
- The four-hour logical test drives 240 streaming/command checkpoints, periodic
  plans, oversized output, overlay search, and 40/80/120-column reflow while
  enforcing category, cache, hydration, truncation, and peak-cell bounds.
- Completion and shutdown tests cover renderer, overlay, ticker, command,
  subscription, controller, and temporary rich-input cleanup boundaries.

### Terminal quality

- Semantic and golden tests cover 40, 80, and 120 columns; truecolor,
  256-color, ANSI-16, and no-color; light/dark themes; reduced/disabled motion;
  tiny terminals; and copy-safe plain rendering.
- The PTY matrix proves normal exit, idle and active interruption, SIGTERM,
  panic, SSH-shaped sessions, tmux-shaped sessions, actual tmux when installed,
  narrow no-color resume, compaction, and provider fallback.
- Multi-rune terminal input, bracketed multiline paste, combining marks, CJK,
  emoji, bidi-adjacent text, invalid UTF-8, grapheme/cell-width wrapping, and
  Unicode folded search have deterministic coverage. OS-specific IME candidate
  UI remains terminal-owned; MintClaw tests the resulting multi-rune input.
- Windows has no Unix PTY test; affected packages compile for Windows, while
  the raw PTY lifecycle runs on Linux CI and the recorded macOS validation.
  Full common-shell qualification remains in the broader coding P8.2 roadmap.

### Safety and trust

- Semantic UI state originates in typed runtime, tool, command, MCP, plan, and
  repository evidence. Renderer code never parses arbitrary prose to invent
  tool kind, lifecycle, write ownership, or patch provenance.
- All compact/full/plain paths share control sanitization, byte/row limits, and
  redaction boundaries. Tests cover escape, OSC, bidi-control, link, path,
  secret, large-output, and invalid-union inputs.
- Repository cells distinguish a current observation from a verified
  MintClaw write and retain pre-existing change provenance.
- `/status` states the autonomous full-access or read-only runtime truth. No
  approval dialog, sandbox selector, or hidden authority was introduced.

## Current user contract and deliberate differences

The [local coding agent guide](../guides/coding-agent.md) is the current source
for start/resume/exec commands, keybindings, slash commands, raw/plain modes,
attachments, compaction, recovery, SSH/tmux behavior, accessibility,
troubleshooting, storage, and trust. Historical packet records remain
point-in-time architecture evidence and may mention bindings removed by the
TUI.15 cutover.

MintClaw intentionally keeps Bubble Tea/Lip Gloss, autonomous-by-default
execution, provider-neutral auth/model support, per-thread local storage,
Seahorse context, native attachments, and future always-on/companion
delegation. It does not copy Codex branding, Rust/Ratatui implementation,
app-server transport, approval UI, sandbox selector, or storage model.

## Validation owned by the final packet

The exact pre-PR tree passed these checks on 2026-09-09:

```text
make fmt
go test -count=3 -run 'TestTerminal(LifecycleEmitsRestorationForExitSignalAndPanic|PTYMatrixCoversRemoteNarrowAndRecoveryPresentation|PTYInterruptsActiveWorkThenReturnsToUsableShell|LifecycleRunsInsideTmuxWhenAvailable)$' ./pkg/coding/tui
go test -count=1 ./pkg/coding/tui
go test -count=1 ./pkg/coding/... ./cmd/mintclaw/internal/coding
go test -race -count=1 ./pkg/coding/frontend ./pkg/coding/tui
go test -count=1 -run 'Test(AdapterProjectsMetadataRetryFallbackAndRedactedError|AdapterBackgroundCompactionPreservesCompletedTurnState|StreamingFallbackAndVisibleFailureRemainUnambiguous)$' ./pkg/coding/frontend/agentadapter
go test -count=1 -run 'Test(NativeControllerTranscriptPageRestoresCommentaryOrderAfterRestart|CodingMetadataStatePersistsOnlyCompletedCompactionCheckpoint|NativeCodingCommandEditsAndResumesAcrossProcessBoundary)$' ./cmd/mintclaw/internal/coding
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -exec=true -run '^$' ./pkg/coding/tui ./cmd/mintclaw/internal/coding
go test -exec=true -run '^$' -tags goolm,stdjson ./...
make lint-docs
scripts/pre-push-lint.sh --changed
git diff --check
```

The real tmux case ran rather than skipped on the recorded macOS host. CI then
re-runs the affected Linux tests, race checks, lint, security, integration, and
Darwin/Windows compilation before merge.
