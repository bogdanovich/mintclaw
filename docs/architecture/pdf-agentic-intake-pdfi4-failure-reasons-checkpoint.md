# PDFI4 Closed Processing Failure Reasons

PDFI4 is not complete. No filled artifact or Telegram qualification is claimed.

## Evidence And Scope

On deployment `bc2a40ad96a16da21b83acb3ab87e487ef5963a8`, the retained I-134 job remains at revision 42,
ledger revision 40 with two pending inputs. The last recorded failure identifies only stage `apply` and code
`intake_pending`. This cannot distinguish a rejected interpretation from a capacity, revision or storage failure.
An isolated snapshot inspected through the native owner-authorized API shows 30 and 25 reservations of 32,
and 18 and 5 accounted spans of 64. This rules out already exhausted reservation/span counts at that snapshot;
it does not establish the rejected proposal or the exact apply error. No protected values are reproduced here.

A same-job small-form retry on this deployment still retains a historical advisory finding, without a processing
failure: trace `trace-turn-e578cfce2bccaaa29c3e40f2`, sequence 8. This is a separate accounting case, not evidence
that the I-134 apply error has the same cause. No input, budget, revision, scope or ledger was reset.

## Minimal Diagnostic Change

Keep `intake_pending`, the value-free message and the existing stage. Add an optional reason from a closed
allowlist of known native/context error identities. Never serialize an error body or accept arbitrary
identifier-shaped errors as safe reasons. This changes no ingestion, assignment, authority, retry, storage,
retention or PDF backend contract. Unknown and storage errors remain `unavailable`; this is not permission
to discard input, repeat received facts, bypass exhausted budgets or approve a result.

Extend actual continue-path regressions for evidence, reservation, interpretation and apply failures. Test wrapped
private errors, unknown errors and code-shaped private strings. Preserve pending input and accepted fields.
After normal CI/review/approval and merged deployment, use fresh same-job evidence to diagnose the actual failure.
The prose-accounting PR #1645 and all artifact, privacy, recovery, Telegram and exit gates remain outstanding.
