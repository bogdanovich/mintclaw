# PDFI4 Guidance Reclassification Checkpoint

PDFI4 is not complete. No qualification artifact or actual Telegram result is claimed here.

## Observed Divergence

On merged deployment `10fa246de67f67fed8099d97587c32e5a9116121`, a retained finish-only message in an ordinary
AcroForm job remained pending even after its private interpretation successfully reported `intent=finish`.
Trace `trace-turn-0b3bb1cd62db480cef9fd576`, tool result sequence 8, reports the same job at revision 7,
ledger revision 2, editable-draft scope, zero unresolved mapping fields, but one retained input and a historical
`field_confirmation_required` finding. There was no processing failure and no PDF. A new native regression
reproduces this without a provider: earlier uncertainty never settles after an exact, fully accounted guidance
interpretation. Retrying the same message indefinitely is not recovery.

## Invariant And Minimal Resolution

An advisory field uncertainty is not a supplied value. The existing private interpreter can distinguish a control
message from facts, but native accounting must enforce its bounded provenance. Resolve only historical
`field_ambiguous` or `field_confirmation_required` findings in the current reserved candidate window when:

- the entire original utterance is accounted for as non-data, with no accepted field evidence or scoped instructions;
- the current successful pass has an advisory control intent, not facts or draft;
- that pass has no assignments, acknowledgements, scope changes or new findings.

Unrelated windows cannot erase a finding. Native invalid/conflicting facts remain pending. Incomplete accounting,
mixed facts, scoped dispositions and current uncertainty also remain pending. Settlement creates no field events,
does not change revisions or review scope, does not rebind the original receipt and never authorizes commit.
The existing separate-current-review and final-approval gates remain mandatory.

Review reproduced a three-pass case: a native conflicting correction, later advisory uncertainty for the same field,
then a finish-only classification of the original text. The latest advisory must not hide unresolved native history.
Project native blockers through intervening advice until accepted or acknowledged evidence resolves the field. Test
the actual apply/restart/replay sequence and every native/advisory code combination; do not discard the correction.

## Architecture Choice And Validation

Prefer this one accounting correction over a new discard-input command, replacement queue, budget reset, widened
capacity, provider retries after decoding failure, or another form job. Reuse existing authenticated encrypted
reservations, outcome intents, literal spans and whole-proposal replay. Persist a bounded native-derived list of
resolved advisory field IDs with the existing encrypted outcome; do not retroactively reinterpret earlier passes
using later accounting or resurrect a resolved finding when another window is processed. Older outcomes omit this
optional list and remain readable. The model cannot supply the list. Add no store, schema version or authority token.
This refines the existing private non-data classification boundary rather than enabling model-generated values.

Tests must reproduce failure before the fix; cover exact replay and restart, stale/changed-body and final-approval
fences, accepted/unaccounted facts, unrelated fields, native invalid/conflicting findings and current uncertainty.
Run native and portable focused suites, race coverage and scoped lint. Extend the existing Windows qualification
job to execute native `TestFormIntake` tests, not just compile them or exercise the agent/tool bridge.
Code still requires exact-head CI, actual
review and authorized merge, then merged deployment and same-job continuation. Broader clarification and I-134
qualification remain unfinished; this checkpoint does not waive their gates.

## Stateful Rollback

New readers accept old outcomes, but old readers strictly reject the new optional private outcome property after it
has been written. This is forward readability, not binary-only downgrade compatibility. Before deployment, stop the
affected gateway and back up the coherent profile, including encrypted form jobs and their profile key, together with
the previous binaries. Never restore only an old binary against newer job state or automatically overwrite post-deploy
user facts with the backup. A data rollback requires an explicit comparison and operator decision; preserve the newer
snapshot for forward recovery. Existing strict decoding fails closed rather than silently dropping resolution state.
