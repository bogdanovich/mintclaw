# PDFI4 Delayed Advisory Reconciliation

## Observed Failure

On deployed `223b9af0d`, a new unrelated test used two distinct people with the same name, an initially
unassigned telephone and a later role clarification without repeating the number. Native readback confirmed
both names, the correctly assigned contact telephone and the explicitly requested blank borrower telephone.
Nevertheless, two fully text-accounted inputs retained advisory uncertainty and the agent asked for the phones again.
Trace `trace-turn-453dc8a1f8274a8e0253335c` and an owner-authorized copied-store probe establish this state.
This is not the historical I-134 budget exhaustion, a missing fact, or proof of successful clarification.

## Boundary And Options

Current reconciliation requires a freshly accepted or matching quoted proposal in the exact pass where older
text becomes completely accounted. A valid decision from an earlier pass can therefore be unavailable when
another window later finishes accounting. Requiring the operator to repeat the accepted value violates intake's
contract; erasing findings from field presence alone would weaken it. Replacing the form transaction, adding a
planner/store or persisting another proof registry is unnecessary.

Use the existing native composite event and immutable pass journal as proof. For one currently offered field:

- require completely accounted input and complete scoped-instruction coverage;
- require advisory ambiguity/confirmation only, never native invalid/conflicting/unsupported findings;
- preserve a finding explicitly returned by the current private interpretation;
- require a current, valid, non-low-confidence user decision, not preservation, deferral or a suggestion;
- require its exact current event ID to derive from an accepted native intake pass whose admission snapshot
  includes this input. Historical acknowledgements are not accepted-event proof; a superseded event cannot match;
- record an ordinary native acknowledgement in the current outcome, without appending or changing any value.

The existing acknowledgement semantics clear the input's old advice; the same canonical proof can participate in
existing older-guidance settlement. Explicit rejected-literal resolution still uses only the original current
proposal's proof, before this reconciliation. Do not let this acknowledgement authorize a chained rejected value.
Completed replay returns its immutable outcome and never recomputes authority against later state.

## Scope And Validation

No private model wire, persisted shape, limit, source/schema/owner binding, approval policy, provider, service or
store changes. Reuse at most eight current candidates and bounded existing journals. Native code owns the event
identity comparison; neither the model nor caller supplies a reusable proof token. Do not mutate old outcomes,
reserve hidden passes or clear unaccounted text.

Reproduce the delayed two-message/two-field transition before implementation, then test restart/exact and changed
replay, atomic publication failure, stale/non-user/weak/missing event proof, unaccounted text, incomplete scope,
new advice, native errors and legacy compatibility. Run focused native/tool/agent and race suites plus portable CI.
After merge/deploy, recheck the retained generic test without resending its values. Full I-134, actual Telegram
and the final exit remain mandatory; this repair does not complete PDFI4.

The native two-input regression failed before this change with both inputs pending and passed afterward, including
restart, exact/changed replay and an injected atomic-write failure. On an isolated copy of the actual deployed
case, the changed native code then acknowledged both retained inputs with no new field event or revision change:
pending inputs went from two to zero, while job revision 5 and ledger revision 4 were unchanged. The existing
`223b9af0d` reader reopened that updated copy and independently confirmed the three text values, intentional blank
and cleared retained input. This is compatibility/transition evidence on a private copy, not a deployed fix claim.
