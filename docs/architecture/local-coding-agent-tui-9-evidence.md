# Local Coding Agent TUI.9 Evidence Checkpoint

Roadmap packet:
[TUI.9 — File-change and diff cells](local-coding-agent-codex-tui-roadmap.md#tui9--file-change-and-diff-cells).

The merge containing this checkpoint delivers TUI.9's evidence foundation but
does not close the packet. The following dependent PR adds the full-width,
palette-aware GitHub-style diff renderer and its terminal-capability goldens.

## Shipped foundation

- A successful native `repository_diff` tool result owns a typed, bounded
  `RepositoryDiffObservation`. The observation is attached to that exact turn
  and provider call ID instead of being reconstructed from model-facing JSON.
- The runtime admits the observation only for a coding turn and clones it
  across hook boundaries. Ambiguous unions, unknown schema or enum values,
  negative counts, and malformed targets fail closed.
- Paths, refs, diagnostics, hunk headers, provenance details, and line content
  are redacted and byte-bounded. File, hunk, and line counts have independent
  caps, and projection loss is explicit through the typed truncation state.
- The frontend stores a deep-cloned diff on the semantic tool cell. A later
  workspace refresh may replace or invalidate the current `/diff` panel state,
  but it cannot mutate historical evidence already attached to an earlier
  cell.
- Compact cells say `Repository diff observed`, show target, totals, per-file
  stats, provenance, and binary/submodule/omission/truncation states. They do
  not say `Edited` or otherwise claim MintClaw authored externally observed
  changes.
- The full cell uses the complete bounded plain evidence, including hunks and
  line numbers, as the copy-safe transcript representation. The renderer does
  not fall back to the mutable global `/diff` snapshot.
- The closed coding-worker protocol has an independent renderer-neutral diff
  DTO with its own schema, enum, structural-text, aggregate-byte, file, hunk,
  and line validation. Paired companions therefore retain the historical diff
  rather than silently losing this new frontend state.

## Authority and lifetime

The passive repository evidence service remains the source of truth. The
model's prose, generic tool output, and the global current-diff panel are never
promoted into historical diff evidence. Per-file baseline provenance means
only `pre-existing`, `first observed during thread`, `resolved since baseline`,
or `indeterminate`; none of those classifications proves authorship.

Each cell owns an immutable presentation snapshot. The tool observation,
frontend state, consumer snapshot, TUI cell, and worker projection all deep
clone nested files, hunks, lines, and provenance paths at their respective
boundaries. This keeps replay and full-transcript output deterministic even as
the repository continues changing.

## Validation

The implementation is formatted with `make fmt` and covered by focused tests
for:

- repository tool ownership of typed evidence;
- fail-closed union/schema/target/line-kind admission;
- redaction, UTF-8 safety, independent count and byte bounds, and deep cloning;
- coding-only runtime publication and hook isolation;
- exact call-ID adapter correlation;
- immutable historical evidence across current `/diff` refreshes;
- provenance-safe compact text and complete bounded full-cell evidence; and
- worker projection bounds, control sanitization, clone isolation, and strict
  validation.

The exact test and lint commands are recorded in the pull request validation
section. TUI.9 remains open until the dependent rich renderer PR proves every
theme, color capability, wrapping, gutter, syntax-preservation, and full-row
background acceptance criterion in the roadmap.
