# Local Coding Agent TUI.6 Exit Record

Roadmap packet:
[TUI.6 — Native plan checklist cell](local-coding-agent-codex-tui-roadmap.md#tui6--native-plan-checklist-cell).

The merge containing this record closes TUI.6. `update_plan` now has one
bounded, typed plan contract shared by tool observation, presentation, and a
replaceable thread-owned current-plan checkpoint. The visible TUI renders the
typed state directly and never parses model-facing arguments, tool output, or
canonical transcript prose to recover a checklist.

## Shipped behavior

- `Updated Plan` uses the Codex-like connector hierarchy, an optional muted
  explanation, checked and crossed-out completed steps, a bold cyan current
  step, and muted pending steps. `✔`, `→`, and `□` preserve every state without
  color.
- Plan text reflows from source state at each admitted width. Control data,
  invalid UTF-8, secrets, step count, individual fields, and total bytes are
  sanitized or bounded before reaching presentation or disk.
- Identical visible plans from later tool calls do not add transcript noise.
  Changes to explanation, scope, order, or progress remain causal historical
  plan cells.
- The generic successful tool card is hidden only after a trusted typed plan
  observation represented it. Failed, invalid, command-bearing, or
  write-bearing calls remain visible and navigable.
- `/status` reports the same latest plan and completed count as the transcript.
  Both interactive controllers and one-shot headless coding turns atomically
  replace `presentation/current-plan.json` under the selected thread lease.
- Resume restores that checkpoint as display state without inventing a tool
  call, replaying work, or adding it to model history. A missing checkpoint is
  valid; malformed, ambiguous, over-deep, unsafe, stale-authority, or
  released-lease state fails closed.

## Authority and lifecycle

Historical conversation remains canonical JSONL/Seahorse state. The current
plan checkpoint is small replaceable presentation evidence, not another
conversation log, and is stored inside the individual coding thread rather
than a global database or project checkout. Ephemeral tool call IDs are not
persisted. Thread trash owns the complete presentation directory, while fork
does not copy a plan whose validity depends on a parent transcript boundary.

## Validation

The implementation was formatted with `make fmt` and passed:

```text
go test ./pkg/coding/plan ./pkg/tools/shared ./pkg/coding/frontend/... ./pkg/coding/thread ./pkg/coding/tui ./cmd/mintclaw/internal/coding
go test -race ./pkg/coding/plan ./pkg/coding/frontend/... ./pkg/coding/thread ./pkg/coding/tui ./cmd/mintclaw/internal/coding
scripts/pre-push-lint.sh --changed
```

Focused coverage proves typed event admission, secret redaction, bounds,
duplicate suppression, meaningful progress history, ANSI capability fallback,
Unicode and one-column reflow, hidden-card navigation, `/status`, atomic
replacement, strict JSON, lease enforcement, committed-write classification,
headless persistence, and controller restore without tool replay.

## Exit-gate decision

TUI.6's acceptance criteria are satisfied. This packet does not introduce the
full command transcript, exploration grouping, verified diff rows, generic MCP
cells, turn/compaction separators, status-card redesign, composer polish, or
transcript search. Those remain owned by TUI.7 through TUI.15.
