# Local Coding Agent TUI.12 Exit Record

Roadmap packet:
[TUI.12 — Codex-like status card and footer hierarchy](local-coding-agent-codex-tui-roadmap.md#tui12--codex-like-status-card-and-footer-hierarchy).

The merge containing this record closes TUI.12. `/status` now renders one
structured, width-aware operational card from typed frontend state, while the
persistent footer is limited to stable glanceable facts and no longer repeats
the live working lifecycle.

## Shipped behavior

- `ThreadSnapshot` carries a bounded ephemeral `RuntimeStatus` alongside
  durable thread metadata. It records the effective MintClaw version,
  new/resumed state, reasoning effort, permission and autonomy modes, admitted
  instruction sources, loading-warning count, and a redacted provider-auth
  summary when the runtime can prove one.
- Runtime status is recomputed whenever a new or resumed native coding
  controller is constructed. Repository/status refresh also reloads
  instruction-source metadata through the same loader used for prompt
  construction and publishes it atomically with the new repository evidence.
  Existing session storage and the closed worker protocol require no migration.
- Instruction reporting follows actual precedence in each directory:
  `AGENTS.override.md`, then `AGENTS.md`, then `CLAUDE.md` only as a fallback.
  The frontend receives path, scope, label, global/truncated state, and warning
  count, but never instruction content or diagnostic text.
- The interactive `/status` surface groups session identity, model/provider and
  reasoning, project/directory and Git state, instructions, context and
  compaction, permissions/autonomy, optional provider account state, and the
  current authoritative plan. Wide terminals align labels and values; narrow
  terminals stack and hard-wrap them instead of clipping.
- `RenderStatusPlain` exposes the same fields as a borderless, copy-safe,
  control-sanitized diagnostic report. Missing required operational facts are
  labelled unavailable; optional account data is omitted when the runtime has
  no supported evidence.
- The footer now shows the model, optional reasoning effort, abbreviated
  directory, branch, context, and repository refresh notice as space permits.
  Current activity and compaction lifecycle remain in the working line and
  semantic transcript cells rather than being repeated in every frame.

## Authority, privacy, and bounds

Status fields come from runtime construction, typed repository evidence, and
the frontend projector. The renderer does not parse assistant prose or tool
output. Permission state reflects the actual read-only/full-access runtime
profile, and MintClaw's autonomous `--yolo` default is stated explicitly.

Provider construction may prove that OAuth, token, or API-key material is
configured, but it does not prove remote account health. The card therefore
reports `configured` unless a future provider-specific health source supplies
a stronger typed state. Account IDs, email addresses, tokens, API keys, and
endpoint credentials are not part of the frontend type.

Runtime strings use the projector's byte limits. Instruction sources and
warning counts have independent count bounds, with explicit source truncation.
Rendering strips terminal control data, abbreviates only paths actually inside
the configured home directory, and hard-wraps every line to the terminal width.

## Codex reference and deliberate differences

The implementation was checked against OpenAI Codex commit
`1a4096e273e80da30947e57fdfa45be92858ca91`, especially
`codex-rs/tui/src/status/card.rs` and its field formatter. MintClaw adopts the
separate structured card, aligned wide layout, stacked narrow layout,
copy-safe plain representation, truthful optional data, and compact footer
hierarchy while retaining its Bubble Tea command-panel architecture.

MintClaw deliberately omits rate-limit rows because its provider layer does
not yet expose one authoritative cross-provider source. It also omits personal
account identifiers instead of echoing an email or account ID. The current
plan remains in `/status` because it is durable MintClaw thread state and must
remain consistent across resume.

## Validation

The implementation is formatted with `make fmt` and covered by:

```text
go test -count=1 ./pkg/agent \
  -run 'TestCodingInstruction(Status|sSelect|sUseClaude)'
go test -count=1 ./pkg/coding/controller ./pkg/coding/frontend/... \
  ./pkg/coding/tui ./cmd/mintclaw/internal/coding
go test -race -count=1 ./pkg/coding/controller ./pkg/coding/frontend/... \
  ./pkg/coding/tui ./cmd/mintclaw/internal/coding
scripts/pre-push-lint.sh --changed
```

Focused tests prove runtime enum normalization and cloning, real instruction
precedence without content leakage, redacted provider-auth classification,
atomic repository/runtime refresh, home-path abbreviation, borderless plain
output, footer de-duplication, and deterministic Git, non-Git, resumed,
compacted, and limited-provider fixtures at 40, 80, and 120 columns.

The full local `pkg/agent` suite still has a pre-existing macOS-only
path-canonicalization failure in
`TestNewCodingAgentLoopReadOnlyAuthorityOmitsMutationTools`: a temporary path
under `/var` is compared with its `/private/var` resolution. The same focused
test fails from the clean pre-change `main` worktree and is outside this UI
packet; the TUI.12 instruction-loader tests above pass.

## Exit-gate decision

TUI.12's acceptance criteria are satisfied. Composer and queued-input polish,
unified transcript navigation/accessibility, and migration/performance closeout
remain TUI.13 through TUI.15.
