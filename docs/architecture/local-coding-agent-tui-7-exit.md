# Local Coding Agent TUI.7 Exit Record

Roadmap packet:
[TUI.7 — Command lifecycle and full transcript](local-coding-agent-codex-tui-roadmap.md#tui7--command-lifecycle-and-full-transcript).

The merge containing this record closes TUI.7. Native command execution now
publishes a bounded, typed start/delta/completion lifecycle that the coding
frontend correlates by provider call ID and background-process identity. The
visible TUI renders that state directly; it does not parse model prose,
model-facing tool output, or arbitrary tool arguments.

## Shipped behavior

- Native `exec` runs publish their admitted command, working directory,
  foreground/background ownership, ordered stdout/stderr/terminal/input
  fragments, duration, exit code, and terminal outcome. This presentation sink
  is request-scoped and installed only for coding turns, so chat-agent behavior
  is unchanged.
- Start, progress, terminal interaction, and completion updates mutate only the
  matching turn/call cell. Output before a delayed start remains an explicit
  orphan until that start arrives; output after a terminal edge cannot regress
  the command or attach to another call.
- Background runs remain `Running` after the spawning tool call and after the
  foreground turn ends. Their original cell becomes succeeded, failed, or
  interrupted only when that owned process actually completes. Poll/read/write/
  send-key/kill calls describe the target process without claiming ownership of
  its lifecycle.
- The main transcript distinguishes `Running`, `Ran`, `Command failed`,
  `Command interrupted`, and unknown outcomes without depending on color. It
  includes useful command/CWD/process metadata and a deterministic five-row
  head-tail output preview.
- `Ctrl+T` opens a scrollable, copy-safe, no-color transcript panel. It renders
  each semantic cell's full representation, exposes `$ command`, causal stream
  labels, exit status, duration, explicit truncation, and routes to older/newer
  transcript hydration when the model-owned window is incomplete. `Esc` or a
  second `Ctrl+T` closes it.
- Command evidence is bounded to 64 KiB and 128 retained fragments. Oversized
  output retains the beginning and rolling tail around an explicit omission
  marker; after the live bound is reached, later output is coalesced into the
  terminal snapshot instead of generating unbounded event/log pressure.
- Non-PTY background stdout and stderr are drained concurrently, avoiding the
  prior serial-pipe deadlock risk and preserving the best observable stream
  order. PTY completion waits for its reader so trailing output cannot revive a
  terminal command.

## Safety and authority

Command observations are cloned, redacted, valid-UTF-8, byte/item bounded, and
terminal-control-sanitized before presentation. Persistent runtime logs strip
the start/progress/end observation bodies, and passive diagnostic traces do not
record progress content. Generic tools cannot opt into command rendering by
returning command-shaped prose; only the native typed provider contract is
accepted.

Canonical JSONL/Seahorse history remains the conversation authority. Command
cells and the full-transcript panel are a bounded live presentation projection,
not a second conversation log. Durable cross-restart command evidence and the
unified overlay/search work remain owned by later roadmap packets.

## Validation

The implementation was formatted with `make fmt` and passed:

```text
go test ./pkg/tools ./pkg/tools/shared ./pkg/coding/frontend/... ./pkg/coding/tui ./pkg/events ./pkg/agent
go test -race ./pkg/tools/shared ./pkg/coding/frontend/... ./pkg/events
go test -race ./pkg/coding/tui -run 'Test(Command|FullTranscript|SemanticCell|ToolCards)'
go test -race ./pkg/tools -run '<TUI.7 focused lifecycle tests>'
go test -race ./pkg/agent -run '<TUI.7 focused lifecycle tests>'
scripts/pre-push-lint.sh --changed
```

Focused coverage proves exact start/progress/end correlation, early and late
lifecycle edges, orphan completion, 32 sequential command calls, process versus
interaction ownership, background completion, write-caused exit terminal
ordering, concurrent output/input admission order, partial and short input
writes, startup failure, terminal duration under either event order, head-tail
bounds, capped progress pressure, ANSI/control injection, lifecycle labels,
five-row previews, full/plain transcript rendering, keyboard discovery, and
log-safe payload projection.

## Exit-gate decision

TUI.7's acceptance criteria are satisfied. This packet intentionally does not
group successful commands or classify read/list/search operations. That work
is TUI.8; file diffs, generic MCP cells, turn separators, status/composer
polish, the unified evidence overlay, and performance gates remain TUI.9
through TUI.15.
