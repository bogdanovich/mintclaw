# Local Coding Agent TUI.8 Exit Record

Roadmap packet:
[TUI.8 — Exploration classification and command grouping](local-coding-agent-codex-tui-roadmap.md#tui8--exploration-classification-and-command-grouping).

The merge containing this record closes TUI.8. Native filesystem tools now
publish bounded semantic read/list/search observations, and the live terminal
derives disposable exploration and command groups from the authoritative
ordered presentation cells. Canonical conversation history and the copy-safe
full transcript continue to retain every individual call.

## Shipped behavior

- Native `read_file`, line-range reads, `list_dir`, and `search_files` publish
  typed operation, path, and search-pattern metadata at tool start. A remote
  workspace wrapper delegates that native contract and adds only its admitted
  workspace alias.
- The agent runtime admits those observations only for coding turns. The
  frontend re-sanitizes and correlates them by turn and provider call ID; it
  never classifies arbitrary tool names, shell strings, argument previews, or
  model-facing results.
- The task-scoped coding-worker wire projection preserves the same typed
  exploration state with an independent closed enum and byte bounds. A
  companion or worker consumer therefore does not silently lose semantic
  exploration while receiving otherwise valid frontend items.
- Contiguous successful or active exploration calls render as `Exploring` or
  `Explored`. Adjacent identical labels use an explicit `×N` count, while
  distinct and non-adjacent operations remain individually legible.
- Contiguous successful foreground commands owned by the agent render as
  `Ran N commands`. Running/background commands, user shell commands, orphan
  completions, failures, interruptions, and skipped calls remain individual
  evidence.
- Failures, commentary, final answers, user-attention tools, write/patch
  evidence, selected tool cards, and turn boundaries flush a group. Explicit
  tool navigation restores the selected call's individual card.
- Every collapsed group shows `Ctrl+T to view full transcript`. That panel
  renders the original cells in exact causal order, including command text and
  bounded output, instead of persisting or replaying a synthetic group.
- The working indicator's `Exploring repository` phase now depends on typed
  exploration observations rather than a hard-coded tool-name list.

## Safety and authority

Exploration metadata is a fail-closed union variant alongside command and plan
observations. Values are cloned, redacted, valid-UTF-8, terminal-safe at
rendering, and independently bounded at both the shared runtime and frontend
projection boundaries. The worker wire validates the closed operation enum,
metadata bounds, and mutual exclusion from command state. An ambiguous
observation with multiple variants, or an unknown exploration operation, is
discarded.

Grouping is only a bounded live-render projection. It neither mutates frontend
items nor changes JSONL/Seahorse history, tool execution, process lifecycle, or
resume semantics. The full transcript therefore remains a faithful view of the
underlying individual evidence even when the main transcript is compact.

## Codex reference and deliberate differences

The design was checked against OpenAI Codex at commit
`6515a72db7a82e8cdebed940aad4ba1a159ce245`, especially
`codex-rs/tui/src/exec_cell/model.rs`, `exec_cell/render.rs`, and
`chatwidget/command_lifecycle.rs`. MintClaw adopts Codex's typed
read/list/search grouping, adjacent deduplication, active `Exploring` state,
and call-ID lifecycle routing.

MintClaw deliberately does not port Codex's shell-command parser in this
packet. Only trusted native observations opt into exploration, which keeps MCP
tools and arbitrary commands from being mislabelled. MintClaw also keeps its
existing full-transcript overlay as the explicit evidence escape hatch instead
of making a collapsed group authoritative.

## Validation

The implementation was formatted with `make fmt` and passed:

```text
go test ./pkg/tools/shared ./pkg/tools/fs ./pkg/tools
go test ./pkg/coding/frontend/... ./pkg/coding/tui ./pkg/agent
go test ./pkg/coding/worker
go test -race ./pkg/tools/shared ./pkg/coding/frontend/...
go test -race ./pkg/coding/tui -run '<TUI.8 focused grouping tests>'
go test -race ./pkg/agent -run '<TUI.8 focused observation tests>'
go test -race ./pkg/coding/worker -run '<TUI.8 focused wire tests>'
scripts/pre-push-lint.sh --changed
```

Focused coverage proves native and remote typed observation admission,
fail-closed union validation, bounds/redaction/cloning, exact call-ID
projection, skipped-call failure retention, adjacent-only deduplication,
active exploration, successful command batches, every required group barrier,
explicit selection behavior, semantic working phases, bounded worker-wire
round trips, and exact individual call order in the full transcript.

## Exit-gate decision

TUI.8's acceptance criteria are satisfied. File-change/diff rendering and
generic MCP result cells intentionally remain in TUI.9 and TUI.10; turn
hierarchy, status/composer polish, evidence search, and performance gates remain
TUI.11 through TUI.15.
