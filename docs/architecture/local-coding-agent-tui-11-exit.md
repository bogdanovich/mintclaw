# Local Coding Agent TUI.11 Exit Record

Roadmap packet:
[TUI.11 — Turn separators, compaction, and final response](local-coding-agent-codex-tui-roadmap.md#tui11--turn-separators-compaction-and-final-response).

The merge containing this record closes TUI.11. Coding turns now retain a
typed final answer, correlated compaction lifecycle, and truthful terminal
work boundary in the same ordered semantic timeline used by the TUI, headless
JSONL frontend, and task-scoped worker.

## Shipped behavior

- Final assistant messages use a dedicated `final_answer` presentation kind.
  Progress commentary keeps its bullet treatment, while the final response is
  rendered as unprefixed answer text and is the only assistant phase printed
  by the alternate-screen and plain headless exit summaries.
- A turn that performed concrete typed work reserves stable timeline space
  before its final answer. At terminal completion that reservation becomes a
  subtle divider; elapsed `Worked for ...` text appears only above sixty
  seconds, matching the useful-duration threshold in the Codex reference.
- Chat-only answers do not receive a work divider. Failed and interrupted work
  turns without a final answer receive a terminal boundary after their work
  with explicit `Work failed` or `Work interrupted` wording. Suspended turns
  remain resumable and do not claim terminal completion.
- Compaction is a first-class mutable semantic item keyed by attempt ID. Start
  and progress update the same item until completed, no-progress, failed, or
  interrupted. Compact cells show lifecycle, duration, and observed token
  reduction; full and copy-safe views add the trusted trigger and summary
  counts without falling back to a generic tool card.
- The closed task-worker protocol and `mintclaw code exec --json` projection
  carry explicit compaction and turn-boundary payloads with durations and
  fail-closed lifecycle/count validation. Plain `code exec` still prints only
  the final assistant response.
- Canonical transcript hydration retains message timestamps, root-turn
  identity, and concrete-work evidence. Resume deterministically rebuilds the
  same boundary identity and derives its completed duration from the durable
  user-to-final interval; loading an older page can restore a previously
  omitted turn start without duplicating the boundary.

## Ordering and authority

Presentation sequence remains projection-owned. An assistant item reserves a
single unused sequence before itself, so learning that it is the final phase
never moves the message past later work. Commentary releases its unused
reservation. Duplicate terminal observations preserve the first completed
boundary identity, sequence, timestamp, duration, and revision.

Concrete work is admitted from typed tool execution or correlated compaction,
including patch, review-related tool, and repository-evidence activity. The
renderer does not infer work from assistant prose, command text, generic
status strings, or elapsed time. Historical reconstruction likewise relies on
canonical tool-call/result structure and root-turn markers rather than text
parsing.

Compaction details originate in the agent's correlated lifecycle payload.
The frontend bounds and clones them, the worker independently bounds and
validates them, and every terminal renderer sanitizes and wraps them. Missing
attempt identity cannot create a lifecycle cell, preventing unrelated or
legacy diagnostics from appearing as executed compaction work.

## Codex reference and deliberate differences

The implementation was checked against OpenAI Codex commit
`6515a72db7a82e8cdebed940aad4ba1a159ce245`, especially
`codex-rs/tui/src/history_cell/separators.rs` and
`codex-rs/tui/src/chatwidget/compaction.rs`. MintClaw adopts the useful elapsed
threshold, compact completion metadata, explicit compaction lifecycle, and
separate final-answer treatment while keeping its existing Bubble Tea
semantic-cell architecture.

MintClaw keeps the roadmap's divider-before-final ordering rather than
depending on Codex's internal history-cell assembly. It also exposes failed,
interrupted, no-progress, and background compaction states because an
always-on runtime must make continuation safety explicit.

## Validation

The implementation is formatted with `make fmt` and covered by:

```text
go test -count=1 ./pkg/coding/frontend/... ./pkg/coding/tui \
  ./pkg/coding/worker ./cmd/mintclaw/internal/coding
go test -race -count=1 ./pkg/coding/frontend/... ./pkg/coding/tui \
  ./pkg/coding/worker
scripts/pre-push-lint.sh --changed
```

Focused tests prove chat-only omission, work-boundary ordering, duplicate
terminal idempotency, useful-duration thresholding, failed/interrupted
wording, final-answer selection, compaction identity and lifecycle, token and
summary rendering, canonical resume reconstruction, worker union validation,
headless JSONL payloads, plain output, width bounds, and deterministic compact,
full, and no-color golden output.

## Exit-gate decision

TUI.11's acceptance criteria are satisfied. Status/footer hierarchy, composer
polish, unified transcript navigation/accessibility, and migration/performance
closeout remain TUI.12 through TUI.15.
