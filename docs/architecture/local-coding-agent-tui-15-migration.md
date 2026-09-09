# Local Coding Agent TUI.15 Migration Checkpoint

Status: first TUI.15 implementation packet; the performance, diagnostics, PTY,
documentation, and final parity gates remain open.

This checkpoint implements the destructive migration portion of
[TUI.15](local-coding-agent-codex-tui-roadmap.md#tui15--migration-performance-and-parity-closeout).
It is intentionally not the TUI.15 exit record.

## Authoritative state after the cutover

`frontend.ThreadSnapshot.Items` is now the only stored ordered presentation
state. Message and tool payloads are read from those items on demand; snapshots
no longer store or serialize parallel `entries`, `tools`, or `changed_files`
arrays. The canonical per-thread transcript and its hydration boundary are not
changed by this presentation-only migration.

Successful file writes remain attached to the causal tool item as typed
`WriteAudit` evidence. The current workspace, status, and diff snapshots remain
separate because they describe repository state at observation time rather
than duplicating historical transcript content.

The cutover removes:

- the `syncCompatibilityProjection` mutation path and its duplicate clone and
  bounding helpers;
- the standalone changed-file projection and `Verified writes` compatibility
  cell;
- the flat `buildTranscriptView`/`renderTranscript` renderer; and
- generic tool-card selection and inline expansion state.

The bounded semantic cell store now owns the live viewport. Compact command,
diff, and MCP cells keep useful summaries in causal order; `Ctrl+T` is the one
consistent route to complete retained, searchable, copy-safe evidence. The
removed `Alt+J`, `Alt+K`, and `Ctrl+O` controls are no longer advertised by
in-app help. Historical packet exit records retain their original bindings as
point-in-time evidence and are not current user documentation.

## Migration invariants

- A frontend snapshot contains one ordered item for each retained semantic
  message, tool, plan, compaction, or turn boundary.
- Public snapshots and subscriber updates deep-copy mutable typed payloads.
- Serialization exposes `items`, never the removed top-level compatibility
  arrays.
- Full transcript rendering uses the same semantic items as the compact live
  viewport; it does not rebuild a second interleaved transcript.
- Generic tool output and arguments retain the existing redaction and bounded
  evidence rules.
- Current repository observations are not presented as filesystem effects
  caused by the agent unless a successful typed write audit supplies that
  provenance.

## Validation owned by this packet

The exact pre-PR tree passed these checks on 2026-09-09:

```text
make fmt
go test -count=1 ./pkg/coding/... ./cmd/mintclaw/internal/coding
go test -race -count=1 ./pkg/coding/frontend ./pkg/coding/tui
golangci-lint fmt --config .golangci-format.yaml --diff \
  ./cmd/mintclaw/internal/coding ./pkg/coding/controller \
  ./pkg/coding/frontend ./pkg/coding/frontend/agentadapter ./pkg/coding/tui
CGO_ENABLED=0 golangci-lint run --allow-serial-runners --concurrency 4 \
  --build-tags=goolm,stdjson ./cmd/mintclaw/internal/coding \
  ./pkg/coding/controller ./pkg/coding/frontend \
  ./pkg/coding/frontend/agentadapter ./pkg/coding/tui
git diff --check
```

The lint run reported `0 issues`. The test suite includes JSON regression
coverage proving the legacy snapshot fields are absent, semantic
compact/full/plain goldens, complete-overlay evidence tests, and a regression
that keeps current repository state before the turn separator and final
response. A production-source audit over the affected frontend, TUI, and
coding CLI packages found none of the compatibility synchronizer,
changed-file projection, flat renderer, generic tool-selection state, or
removed keybindings.

## Work still required for TUI.15 exit

Later focused packets remain responsible for privacy-safe presentation
diagnostics; first-paint, update, resize/reflow, long-transcript, hydration, and
search benchmarks; bounded multi-hour synthetic-session evidence; and the SSH,
tmux, narrow-terminal, interruption, compaction, crash/resume, and provider-
fallback PTY matrix. The last packet will publish current rich/raw-mode and
binding documentation, link the final exit record from the main coding-agent
roadmap, and perform the requirement-by-requirement parity audit.
