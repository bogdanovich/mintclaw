# Local Coding Agent TUI VF.5 Exit Record

Roadmap packet:
[VF.5 — Visual parity and lifecycle closeout](local-coding-agent-tui-visual-followup-roadmap.md#vf5--visual-parity-and-lifecycle-closeout).

The merge containing this record closes VF.5 implementation and local
verification. It adds one integrated visual fixture that exercises the compact
inline transcript, a completed tool, a full-width turn boundary, semantic
Markdown, the composer gap, and truthful footer state together. The overall
follow-up remains open until this merge is deployed from `main` and a real
`mintclaw code` production smoke is recorded in a final documentation update.

## Screenshot 08–12 comparison

The ordered reference set established five visible contracts:

- an ordinary coding thread starts below the invoking shell command without
  clearing or padding the whole terminal;
- assistant prose presents headings, emphasis, tables, and lists as readable
  terminal content instead of literal Markdown punctuation;
- a turn boundary spans the complete available width;
- one blank row separates the composer from the persistent footer; and
- a routine repository summary stays proportional because of the coding
  response policy, not because the renderer hides or truncates text.

The integrated golden fixture reproduces those contracts at 40, 80, and 120
columns. The narrow fixture uses no color, the standard fixture uses a light
theme and ANSI-16 capability, and the wide fixture uses a dark theme and
truecolor capability. All three preserve the same semantic hierarchy and fit
in fewer rows than the supplied 32-row terminal, proving that the integrated
completed surface does not expand to a blank full screen. The existing empty
PTY case independently proves compact idle startup.

The fixture deliberately uses stable synthetic model output. Concision itself
is owned by the VF.4 coding prompt and its prompt-isolation tests; a visual
golden must not depend on nondeterministic provider wording.

## PTY and terminal-lifecycle evidence

The Unix PTY matrix now includes a completed Markdown answer in a local light
terminal and asserts both readable semantic content and absence of its raw
heading, emphasis, table, and fence delimiters. Existing matrix cases cover an
SSH-shaped 80x24 environment, a tmux-shaped 120x30 environment with compaction,
a 40x16 no-color resumed thread, and crash recovery.

Additional PTY coverage verifies:

- normal exit, Ctrl-C, SIGTERM, and panic restoration;
- interruption of active work followed by a usable shell;
- resize and resumed transcript presentation without duplicate final output;
- temporary alternate-screen entry and exit for the transcript overlay; and
- execution inside a real tmux session when tmux is available.

The normal active-thread command remains inline. A full-screen interactive
resume picker and the explicitly opened transcript overlay intentionally retain
alternate-screen ownership because they are bounded modal surfaces and their
restoration lifecycle is covered independently.

## Single-renderer and screen-mode audit

`presentationCell.Render` routes assistant commentary and final answers only
through `markdownMessageDocument`. The unreachable assistant/final cases have
been removed from the literal `messageDocument` fallback, so a later refactor
cannot accidentally revive a second renderer by calling the fallback switch.
User messages, reasoning, warnings, errors, and typed runtime observations
remain literal or semantic according to their existing ownership.

The screen-mode audit finds no unconditional alternate-screen option on active
coding threads. The only admitted paths are the resume picker and transcript
overlay described above. The overlay emits paired enter/exit commands and the
picker owns alternate mode for its complete invocation.

## Deliberate differences from Codex

MintClaw uses its existing Go/Bubble Tea frontend and durable semantic
projection rather than copying Codex's Rust TUI or app-server protocol. The
viewport remains bounded and source-backed, and MintClaw retains its own tool,
thread, autonomous-execution, and status vocabulary. Full-screen modal surfaces
remain available where they materially help navigation. Pixel-perfect Codex
branding, wording, animation timing, and internal widget structure are not
compatibility goals.

## Packet trail

- [Roadmap admission PR #1179](https://github.com/bogdanovich/mintclaw/pull/1179)
- [VF.1 PR #1181](https://github.com/bogdanovich/mintclaw/pull/1181)
- [VF.2 PR #1182](https://github.com/bogdanovich/mintclaw/pull/1182)
- [VF.3 PR #1186](https://github.com/bogdanovich/mintclaw/pull/1186)
- [VF.4 PR #1193](https://github.com/bogdanovich/mintclaw/pull/1193)

The VF.5 PR and its merge commit will be added to the final deployed closeout.

## Local validation

The implementation is formatted with `make fmt` and covered by:

```text
go test -count=1 ./pkg/coding/tui ./cmd/mintclaw/internal/coding
go test -count=3 -run \
  'TestTerminal(LifecycleEmitsRestorationForExitSignalAndPanic|PTYMatrixCoversRemoteNarrowAndRecoveryPresentation|PTYInterruptsActiveWorkThenReturnsToUsableShell|LifecycleRunsInsideTmuxWhenAvailable)$' \
  ./pkg/coding/tui
go test -race -count=1 -run \
  'Test(VisualFollowupIntegratedSurfaceGoldens|OnlyAssistantMessagesUseMarkdownProjection|TerminalPTYMatrixCoversRemoteNarrowAndRecoveryPresentation|TerminalPTYInterruptsActiveWorkThenReturnsToUsableShell|TerminalLifecycleRunsInsideTmuxWhenAvailable)$' \
  ./pkg/coding/tui
scripts/pre-push-lint.sh --changed
make lint-docs
git diff --check
```

The integrated visual fixtures additionally fail if a line exceeds its target
width, the full-width rule is missing, the composer/footer gap disappears, an
idle surface fills all 32 rows, raw Markdown leaks into display, or no-color
output emits ANSI styling.

A full-package macOS race run also exercised the suite but is not claimed as a
passing exit gate: the pre-existing artificial goroutine-panic PTY case can
trigger a race inside Bubble Tea v1.3.10 and `muesli/cancelreader` while the
dependency closes its kqueue reader. The unchanged normal PTY suite continues
to verify panic restoration, while the targeted race command above covers the
VF.5 renderer, integrated layout, Markdown PTY, interruption, and tmux paths.
No test is skipped or weakened to conceal that dependency result.

## Deployment boundary

Local visual and lifecycle evidence does not prove the installed binary. After
this implementation is merged, the exact merged `main` commit must be deployed
through the documented production procedure. The final closeout will record
the installed version, host, real PTY command, observed compact startup and
semantic Markdown response, restoration result, and the documentation-only PR
that changes this packet and the roadmap to fully complete.
