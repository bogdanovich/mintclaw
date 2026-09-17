# Local Coding Agent TUI VF.5 Exit Record

Roadmap packet:
[VF.5 — Visual parity and lifecycle closeout](local-coding-agent-tui-visual-followup-roadmap.md#vf5--visual-parity-and-lifecycle-closeout).

VF.5 is complete. Its implementation merge adds one integrated visual fixture
that exercises the compact inline transcript, a completed tool, a full-width
turn boundary, semantic Markdown, the composer gap, and truthful footer state
together. The exact merged `main` commit is deployed, and real production TUI
and live-agent smokes prove the installed behavior.

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
- [VF.5 PR #1200](https://github.com/bogdanovich/mintclaw/pull/1200)
- [VF.5 merge `cfd6790bf`](https://github.com/bogdanovich/mintclaw/commit/cfd6790bf2ce0d03a8e921ade232157f08a17c19)

The roadmap admission and all five implementation packets are merged.

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
dependency closes its kqueue reader. This is the same lifecycle area covered by
[upstream Bubble Tea panic-cleanup work](https://github.com/charmbracelet/bubbletea/pull/1770).
The unchanged normal PTY suite continues to verify panic restoration, while
the targeted race command above covers the VF.5 renderer, integrated layout,
Markdown PTY, interruption, and tmux paths. No test is skipped or weakened to
conceal that dependency result.

## Deployment and production evidence

Deployment completed on `server@oc` on 2026-09-13 PDT. The clean core checkout
fast-forwarded from `290f1cb797b0c8415ac9707bb87530ddcca4e078` to the VF.5
merge `cfd6790bf2ce0d03a8e921ade232157f08a17c19`. The deployed binary reports:

```text
mintclaw v0.1.0-p8a.2-1819-gcfd6790b (git: cfd6790b)
Build: 2026-09-13T17:39:35-0700
Go: go1.26.6
```

Core, node, and launcher were built with `make build`, `make build-node`, and
`make build-launcher`, installed under `/home/server/.local/bin`, and matched
their source build artifacts byte-for-byte. The five gateway profiles,
`mintclaw-main-web.service`, and `mintclaw-node-p5a-canary.service` were the
only restarted units. Every expected unit returned active afterward; all seven
affected process executables point to the intended core, launcher, or node
binary, and no old product process remains.

The post-restart status and bounded journal audit reported zero failed units,
zero legacy processes, and zero error-level entries for every affected unit.
All five active profile configs loaded under the installed `mintclaw doctor`.
They returned exit 2 only for existing policy findings such as deliberately
write-capable remote execution, permissive channels, disabled process
isolation, external skill registries, and recent task/delivery history; no
schema or load error occurred.

### Real coding TUI smoke

A real SSH PTY launched the installed binary in
`/home/server/src/mintclaw` with the deployed `gpt-5.6-sol/openai` model and
`reasoning default`. The initial prompt asked for a concise Markdown heading,
bold word, two-column table, and bullet without tools or file changes.

The session stayed inline instead of entering the alternate screen, retained a
compact content-height surface, rendered `TUI smoke` without a literal `##`,
rendered `renderer` without `**`, presented the table without Markdown pipes,
and showed the requested bullet. The repository status remained clean. The
composer/footer gap was visible, and `/exit` disabled bracketed paste, focus,
and mouse modes, restored the cursor, and returned a successful SSH close.

### Live-agent and trace smoke

The bounded main-gateway request returned `outcome=success` and exactly
`MINTCLAW_DEPLOY_SMOKE_OK` for request
`62ac75a1-a86a-4bdb-af2e-bf1c44a6a61f` on `main-turn-4`. It produced new
trace `trace-turn-9d0e163b442a40b7b7fc5377` at
`2026-09-14T00:44:57.097406384Z`. The trace reports schema
`mintclaw.diagnostic_trace.v1`, completed status, eight records, configured
redaction policy, and no truncation.

A separate direct `agent --stateless` probe against the legacy default CLI
workspace was not used as evidence: it stopped before the model because old
session metadata contains the unknown field `aliases`. Active profile config
loading, the main live-gateway request, and the coding TUI path are unaffected.
That default-workspace data-compatibility cleanup is outside VF.5.

## Backup and rollback

The verified pre-deploy backup is
`/home/server/mintclaw-deploy-backup-20260914T003920Z`. It contains the prior
core, node, CLI, and launcher binaries; all user service units and drop-ins;
the pre-deploy unit states; and a verified SHA-256 manifest. No mutable runtime
data was changed or copied back.

If rollback is required, stop only the five gateway profiles,
`mintclaw-main-web.service`, and `mintclaw-node-p5a-canary.service`; restore the
four exact binaries from the backup; restart the same units; then rerun the
status, config-load, bounded-journal, TUI, live-agent, and trace checks. Keep
the source checkout and mutable data intact unless a separate forward or data
migration is explicitly approved.

The deployed SHA, TUI presentation, shell restoration, runtime response,
diagnostic trace, health state, and rollback assets satisfy the final VF.5 and
overall visual-follow-up gates.
