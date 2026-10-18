# PDFI4 Newer Confirmation And Older Advisory Guidance

## Reproduced Boundary

A protected paragraph has a natively accepted fact and an advisory uncertainty about another field.
Its remaining connective prose is subsequently accounted, but that uncertainty correctly remains.
A later protected message explicitly confirms the other field, or explicitly leaves it blank. Native
validation accepts the new decision, yet the fully accounted older message remains pending forever.
Generic two-field regressions reproduce both cases, without a form name, agency or provider rule.

## Initial Repair And Multiwindow Checkpoint

The first ready head added 42 production lines in two files, 196 test lines and this 33-line checkpoint.
It passed its first substantive review with zero review/fix cycles. A subsequent ten-field native regression
reproduces a foundational limitation: nine older advisory markers cannot all be confirmed in one eight-field pass.
Both later windows accept their decisions, yet the original message remains pending. The full diff, tests, body,
history and first clean review were re-read before extending the implementation; the needed projection/reader
changes cross the doubled-file baseline. This is a lifecycle accounting defect, not a reason to raise window caps.

Continuing the all-or-nothing helper cannot converge on larger sections. Resetting extraction budgets, increasing
the planning window, mutating historical outcomes or adding per-input flags is rejected. A separate foundational
PR would duplicate the prerequisite's existing immutable settlement journal. Instead, refactor this focused PR
around that journal: a native-derived advisory-field variant on the CURRENT encrypted outcome records each
independently accepted decision. No second resolution store, model authority or extraction pass is introduced.

## Bounded Repair

Reuse the native atomic apply after current selection and validation. Consider only earlier committed
messages whose entire text and any scoped instruction are already accounted. A remaining advisory
ambiguity/confirmation may settle only for a field in THIS pass's native `CurrentValueIDs` proof subset.
Historical acknowledgement alone is not current-value authority. Record its old input, field and through-pass
snapshot on the current outcome. Accumulate those immutable records across bounded windows and restart;
consume and erase the older message only when all its remaining findings are resolved.
An arbitrary globally valid canonical field is insufficient evidence. Native invalid/conflicting
findings, unaccounted facts, unvisited scoped fields, unrelated fields and deferred choices stay pending.
Do not change a value, append an extra event, mutate an old outcome, reserve another extraction, reset
a budget, add another store, or grant finish/review/approval authority. Advisory settlements never mask
native invalid/conflicting findings, including later findings on the same field.

The existing encrypted resolution has exactly one variant: an exact accounted literal span (legacy) OR
an advisory field ID (new, no invented offsets). Advisory thresholds are distinct from literal-resolution
thresholds. Reader validation binds old identity/order/pass snapshot, current candidate and accepted/acknowledged
outcome, complete old text accounting, unique variant identity and bounded metadata. At most eight current fields
can settle each earlier pending input; the metadata bound derives from the existing eight-field/four-pending-input
limits, not a raised model, input, extraction or literal budget. New readers retain legacy span compatibility;
older strict readers fail closed on the advisory property. Deploy with a coherent protected-store/binary backup,
rebuild operational readers, and forward-repair after new writes. Never binary-only rollback or restore a stale
profile over newer protected facts.

This focused follow-up is stacked on the correction-accounting PR while its CI/review runs, because
combined finding projection must respect its immutable cross-receipt settlements. Retarget to main
only after that prerequisite merges. Preserve the whole-text and native error gates, rather than
clearing old findings whenever any field becomes valid or reclassifying rejected facts as prose.

## Validation And Exit

Carry the failing positive and explicit-blank regressions forward. Add unaccounted-fact, unrelated-field,
native-error, deferred-choice, scope-coverage, acknowledgement-only, restart and replay checks. Existing
extraction counts and accepted values must remain unchanged. The nine-marker regression must settle in two
windows with restart between them and unchanged old outcome snapshots. Cover malformed/duplicate variant links,
legacy/new reader compatibility and native/later-finding preservation. Run focused/full intake and race tests,
formatter and lint, then exact-head CI/review/rocket. Real artifact and Telegram qualification remains
mandatory; merging this repair does not complete PDFI4.
