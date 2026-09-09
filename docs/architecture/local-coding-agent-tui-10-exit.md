# Local Coding Agent TUI.10 Exit Record

Roadmap packet:
[TUI.10 — Semantic MCP and tool cells](local-coding-agent-codex-tui-roadmap.md#tui10--semantic-mcp-and-tool-cells).

The merge containing this record closes TUI.10. Known MCP executions now use
wrapper-owned semantic evidence from execution through the coding frontend,
worker protocol, compact history, expanded cell, and copy-safe transcript.
Unknown native and extension tools retain the existing generic fallback.

## Shipped behavior

- The native MCP wrapper emits typed start and terminal observations with the
  safe server/tool identity, purpose, exact lifecycle outcome, bounded result
  or error, duration, and truncation state. Outcomes distinguish running,
  succeeded, failed, canceled, timed out, and uncertain execution.
- The agent annotates the final typed observation when its independent
  no-progress guard halts the turn. A successful fourth identical call remains
  visibly successful while also saying `turn halted: no progress (4/4)`;
  repeated-failure halts use separate wording. The renderer therefore does not
  mislabel the protection as the execution failure that caused it.
- The adapter and projector correlate observations by exact turn and call ID.
  Repeated identical invocations remain separate, ordered, auditable cells;
  deferred MCP discovery events do not create fake executed work.
- Compact cells use `Calling`/`Called` or a distinct actionable terminal
  failure label, show purpose and argument shape, and retain at most five
  wrapped evidence rows. `Ctrl+O` expands the selected cell and `Ctrl+T`
  exposes the complete retained copy-safe plain evidence.
- The task-scoped worker's closed JSONL projection carries the same typed MCP
  state under explicit byte, count, lifecycle, and union validation. It cannot
  combine MCP evidence with command, exploration, or repository-diff evidence
  in one tool record.
- Tools without admitted typed MCP evidence still render through the compact
  generic tool cell. No renderer classifies a call by parsing an `mcp_*` name
  or result prose.

## Safety and authority

Only the native MCP wrapper may produce MCP presentation evidence. Argument
values are absent from the typed observation and the existing tool-start
projection exposes only sorted field names such as `fields: query, repo`.
Server identity, tool identity, purpose, result, error, and loop-halt metadata
are validated, redacted, UTF-8 normalized, terminal-control sanitized, and
byte-bounded before publication. Valid JSON results receive recursive
sensitive-key redaction in addition to ordinary credential-pattern redaction.

The worker repeats the canonicalization and rejects malformed outcomes,
contradictory result/error combinations, unknown loop-halt codes, missing halt
counts, and ambiguous typed unions. Compact and expanded renderers strip
terminal controls again and hard-wrap every row to the admitted terminal
width. Truncation remains explicit rather than being presented as complete
evidence.

## Codex reference and deliberate differences

The renderer was checked against OpenAI Codex commit
`6515a72db7a82e8cdebed940aad4ba1a159ce245`, especially
`codex-rs/tui/src/history_cell/mcp.rs`. MintClaw adopts its exact-call identity,
mutable `Calling` to terminal `Called` lifecycle, duration, compact display,
bounded result preview, separate transcript representation, and generic
history-cell fallback.

MintClaw deliberately does not copy Codex's full MCP argument rendering.
Values remain unavailable to the presentation model; only their shape is
shown. MintClaw also exposes cancellation, timeout, uncertain side effects,
and no-progress guard state as separate typed outcomes because its always-on
runtime must not invite an unsafe retry or hide why a live turn stopped.

## Validation

The implementation is formatted with `make fmt` and covered by:

```text
go test -count=1 ./pkg/tools/shared ./pkg/tools/integration ./pkg/agent
go test -count=1 ./pkg/coding/frontend/... ./pkg/coding/tui ./pkg/coding/worker
go test -race -count=1 ./pkg/tools/shared ./pkg/tools/integration \
  ./pkg/coding/frontend/... ./pkg/coding/tui ./pkg/coding/worker
scripts/pre-push-lint.sh --changed
```

Focused tests prove wrapper-owned start and terminal evidence, every admitted
outcome, successful and failed loop-halt annotations, exact repeated-call
identity, discovery exclusion, frontend cloning and bounds, worker JSONL
round-trip validation, recursive JSON credential redaction, terminal-control
sanitization, tiny-width wrapping, generic fallback, five-row compact previews,
`Ctrl+O` expansion, and copy-safe full output. Semantic cell goldens record the
compact, expanded, and plain MCP lifecycle presentation.

## Exit-gate decision

TUI.10's acceptance criteria are satisfied. Turn boundaries and compaction,
the status hierarchy, composer polish, unified transcript navigation, and
migration/performance closeout remain TUI.11 through TUI.15.
