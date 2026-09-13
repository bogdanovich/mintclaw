# Local Coding Agent TUI VF.2 Exit Record

Roadmap packet:
[VF.2 — Layout rhythm and full-width rules](local-coding-agent-tui-visual-followup-roadmap.md#vf2--layout-rhythm-and-full-width-rules).

The merge containing this record closes VF.2. The inline coding surface now
keeps the composer visually distinct from its persistent footer, and a
completed turn's rule spans the semantic viewport instead of stopping at an
arbitrary desktop-era width.

## Shipped behavior

- Terminals five rows tall or taller reserve exactly one blank row between the
  composer and footer.
- One- through four-row terminals omit that decorative row and retain the
  existing compact input, status, and interrupt behavior.
- Working state and queued guidance share the same row budget; neither removes
  nor duplicates the composer/footer gap.
- Turn boundaries fill the complete admitted semantic viewport at 40, 80, 120,
  and other widths. They no longer stop at 72 columns.
- Color-disabled output keeps the same whitespace and separator hierarchy.

## Geometry ownership

`Model.View` owns the normal-height separation row. The viewport height budget
reserves that row alongside composer, working, pending-guidance, footer, and
terminal-safety rows. `pendingGuidanceRows` uses the same reservation before it
admits optional queue details. The tiny-terminal renderer remains the sole
exception and never emits the decorative gap.

Turn-boundary cells receive an already admitted render width, so the cell
repeats the single-column rule glyph for that exact width. The existing
cell-width and golden assertions reject horizontal overflow. No box, fixed
desktop width, or terminal-dependent tab was added.

## Regression evidence

Model tests cover an empty adaptive surface, normal heights of 5, 8, and 24
rows with active work and queued guidance, and tiny heights 1 through 4. They
assert the gap's placement, the row bound, and preservation of the interrupt
path.

Semantic cell tests assert exact 40-, 80-, and 120-column rule widths. The
120-column dark/no-color golden records the visible hierarchy for compact,
full, and plain transcript modes. The Unix PTY transcript-overlay test waits
for the two-phase alternate-screen surface before closing it, removing a race
between mode entry and its first render while preserving the VF.1 lifecycle
assertion.

## Validation

The implementation is formatted with `make fmt` and covered by:

```text
go test -count=1 ./pkg/coding/tui
go test -race -count=1 ./pkg/coding/tui
scripts/pre-push-lint.sh --changed
make lint-docs
git diff --check
```

## Exit-gate decision

VF.2's normal/tiny spacing, optional-surface budgeting, full-width rule,
no-color, and width-regression criteria are satisfied. Markdown remains
literal by design at this boundary; source-backed semantic Markdown is VF.3.
Coding-only response concision and truthful reasoning status remain VF.4.
