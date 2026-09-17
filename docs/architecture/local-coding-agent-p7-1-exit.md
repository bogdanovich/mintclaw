# Local Coding Agent P7.1 Exit Record

Roadmap packet: [P7.1 — Non-interactive `code exec`](local-coding-agent-roadmap.md#p71--non-interactive-code-exec).

The merge containing this record closes P7.1. MintClaw now has a one-shot,
non-interactive frontend over the same native coding controller, durable thread
store, project authority, and single-writer lease as the terminal UI. It does
not add a daemon, another agent loop, or another session format.

## Merged implementation

[PR #1088](https://github.com/bogdanovich/mintclaw/pull/1088), merge
`623a9aa1`, added:

- `mintclaw code exec [prompt]` for a new durable project thread;
- `mintclaw code exec resume <thread-id> [prompt]` for an existing thread;
- repeatable `--attach`, persistent `--model`, plain final-response output, and
  schema-versioned JSONL lifecycle events;
- stable exit categories for model, project, tool, context, suspended, and
  interrupted outcomes; and
- SIGINT/SIGTERM handling with bounded interruption, settlement, and shutdown.

The complete event and exit contract is documented in
[P7.1 non-interactive execution](local-coding-agent-p7-1-exec.md).

## Lifecycle evidence

- A queued submit has one actor-owned admission result. Cancellation before
  readiness prevents admission; readiness remains authoritative when it races
  a fast runtime result.
- After admission, JSONL always emits `turn.started` and exactly one terminal
  `turn.completed` or `turn.failed`. Missing arguments and other pre-admission
  failures use the top-level `error` event.
- A terminal frontend projection is not treated as durable completion. The
  command waits for runtime post-turn work, metadata persistence, accumulated
  committed-write errors, and operational errors before publishing success.
- Cancellation remains active during that settlement barrier and produces an
  interrupted terminal event even if the interrupt request itself fails.
- Repeated settlement reads retain the same result, including a runtime that
  exits before signaling readiness.
- Plain and JSON modes share typed controller state; neither parses logs or
  model prose, prints the startup banner, emits terminal control sequences, or
  creates a second transcript writer.

Representative coverage includes
`TestCodeExecJSONLNewAndResumeUseSameThread`,
`TestExecuteControllerTurnWaitsForSettlementBeforeSuccess`,
`TestExecuteControllerTurnHonorsCancellationDuringSettlement`,
`TestSubmitCancellationBeforeReadinessDoesNotBlockCoordinator`,
`TestSubmitPreservesReadinessWhenFastFailureAlsoSettles`, and
`TestSubmitRetainsFailureWhenRuntimeReturnsWithoutReadiness`.

## Validation and review gates

The final implementation head passed formatting, changed-package lint, focused
unit and integration tests, race tests, Linux and Windows compile-only checks,
and a process smoke verifying clean JSONL stdout, human stderr, help output,
and exit status. GitHub CI passed frontend, lint, security, full tests, race,
Darwin and Windows compilation, macOS portability, integration, and browser
jobs.

Automated review found and drove regression coverage for settlement ordering,
interruption terminal events, JSON validation, admission races, cancellation
during settlement, deferred durability errors, coordinator responsiveness,
and retained pre-readiness failures. All findings were resolved before the
clean review and owner rocket approval. The implementation was then rebased
onto the document-acquisition CLI merge; the only conflict preserved both
independent exit-code dispatch paths, and the full CI matrix passed again.

The host's default CGO build still requires the external Matrix libolm headers.
The repository-supported `goolm,stdjson` build exercised the changed root CLI
path locally, while CI covered the normal supported matrix.

## What users can test

From a repository with a configured coding model:

```text
mintclaw code exec "Inspect this repository and summarize its structure. Do not modify files."
mintclaw code exec --json "Inspect this repository and summarize its structure. Do not modify files."
mintclaw code exec resume <thread-id> "Continue the investigation."
mintclaw resume
```

The JSON form should contain no banner or terminal escapes and should finish
with one turn terminal event. The returned thread ID should remain visible to
`mintclaw resume` in the same project, and both resume forms should append to
that thread's existing durable files.

## Next boundary

P7.2 is next, but it is an investigation rather than an implementation
commitment. First use and measure the P7.1 one-shot worker: startup latency,
memory after completion, repeated MCP setup cost, background-task needs, and
remote-attachment constraints. A daemon should be admitted only if those
measurements show a bounded benefit that cannot be met by a supervised one-shot
worker behind the same thread/task contract. Until then, the foreground TUI and
`code exec` remain the supported execution boundaries.

P7.1 does not add multi-agent worktrees, live-agent or Telegram task dispatch,
paired-companion access, workspace rewind, automatic Git publication, or
remote filesystem authority. Those remain separate later roadmap packets.
