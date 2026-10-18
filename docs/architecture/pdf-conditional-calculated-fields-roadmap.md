# Conditional And Calculated PDF Form Follow-Up

## Status And Boundary

Deferred by the operator on 2026-10-18 after a fresh I-134 test. This records a separate future backend stage,
not admission to implement it during PDFI4 intake fixes and not evidence that I-134 has passed qualification.
The current source, protected intake, accepted facts and original independent oracle remain unchanged.

The existing fixed-page hybrid backend does not execute JavaScript or FormCalc. Some forms use those languages
to enable fields after a branch choice or calculate totals. A source read-only flag can therefore describe a
conditional or calculated field, not just an intentionally immutable value. Removing the flag or suppressing a
verification failure is not support for those semantics.

The motivating source is the public I-134 edition 01/20/25, SHA-256
`e887f9dbb4660ff9eec47d740c00358e45fcfb9c04a9776d8d95cda2e7a5e0d8`.
Source inspection found two employer-related text fields enabled by XFA branch scripts and a read-only total
computed from six asset inputs. All three were in the predeclared full-output oracle. They must not be silently
removed from that oracle to turn an incomplete qualification into a pass. This example is test evidence only:
production logic must not match its agency, field names, document digest or synthetic values.

## Ordered Vertical Slices

### CF0: Feasibility And Explicit Admission

Compare maintained backends that can establish conditional/calculated semantics under the existing one-shot worker
boundary. Do not implement a bespoke JavaScript/FormCalc interpreter, another document service or a second form
transaction. A capability proposal must fix platform packaging, provenance/license, confinement, resource limits,
source permissions, identity/schema behavior and independent output verification before enabling writes.
Existing immutable source/schema bindings must fail closed across a backend/capability transition; any migration
or newly authorized qualification transaction is explicit, never represented as recovery of the old task.

Done: an architecture decision supported by a synthetic conditional fixture, a calculated fixture, the pinned
public source and negative controls. If no bounded safe backend qualifies, keep an explicit unsupported result.
This gate must not be replaced by screenshots from a browser or direct packet/dataset edits.

### CF1: One Conditional Branch End To End

Admit only a precisely classified subset. A choice can enable its dependent fields; changing the choice invalidates
stale field/review/approval authority. Do not write inactive fields or silently discard already supplied protected
facts. Preserve the distinction between truly immutable, conditionally writable and unsupported fields.

Done: CLI and actual agent transactions on two unrelated fixtures, independent structural and visible oracles,
branch-change/stale-revision negative tests, unchanged source, privacy, recovery and one logical delivery.

### CF2: One Calculated Dependency End To End

Compute only backend-proven admitted dependencies. Define number/locale/rounding semantics, input corrections,
dependency invalidation and cycle/unsupported-script refusal. A model-supplied total is not proof that the form's
calculation was performed correctly. Keep user facts separate from derived values and report provenance.

Done: deterministic sums and correction propagation on unrelated fixtures, an independent arithmetic oracle,
all-page rendered verification and negative cases for cycles, unavailable semantics, overflows and resource limits.

### CF3: Public Large-Form Requalification And Operations

Repeat complete applicable filling of the pinned I-134 with synthetic data and another form, including conditional
employment fields and the calculated asset total. Use a predeclared field/control oracle; inspect every output page.
Retain separate review, finish and approval, immutable source, one verified delivery and protected-store cleanup.

Done: reviewed merged code deployed with backup/rollback, exact runtime source attributed, real-agent and Telegram
evidence, full field/control/visual checks, guide and exit record. Earlier partial or headless evidence is not a pass.

## Non-Goals And Stop Gates

- No universal XFA engine, arbitrary embedded scripts, network/file actions, external submission or signatures.
- Editable hybrid derivatives remain a separate capability; a print-ready result must be labelled non-editable.
- No new permanent model tools, daemon, planner, store, broker or delivery path; reuse the document service.
- Never clear read-only flags globally, authorize a field from model prose, or add form-specific production rules.
- Initially qualify Linux explicitly. Record macOS/Windows behavior and preserve their refusal/portable baseline;
  advertise parity only after their own packaging, confinement and complete vertical-slice evidence passes.
- If backend evaluation grows into a new execution/security architecture, stop at CF0 and obtain explicit admission.

PDFI4's free-text intake fixes may finish independently, but official I-134 full-output qualification stays open
until this capability and the original independent oracle pass. Documentation alone completes no runtime milestone.
