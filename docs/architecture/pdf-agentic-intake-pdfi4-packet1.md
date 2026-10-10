# PDFI4 Packet 1: Protected Composite Substrate

## Status

In implementation. This is a prerequisite for the [PDFI4 goal](pdf-agentic-intake-pdfi4-goal.md), not a user-facing
completion claim. Packet 1 is split into an atomic field-ledger primitive (1A) and protected utterance/binding ingress
(1B). Neither a merged 1A nor a transport-only 1B demonstrates natural-language intake.

## Packet 1A Contract

`FormJobStore.AppendComposite` is trusted protected-call input, not a model-facing tool accepting plaintext values.
It reuses the existing store lock, owner authorization, immutable source/schema/backend binding, encrypted per-field
envelopes, ledger chain, snapshot publication, and terminal cleanup.

- At most 32 distinct schema-valid assignments and 16 KiB of aggregate textual/choice values per composite. Existing
  per-field value limits (8 KiB), job byte limits, retention, and ledger event limits also apply.
- Validate the complete selected set before mutation. Invalid fields return sorted, value-free findings and accept
  none of that set. The later intake consumer owns retained unaccepted facts and any deliberately selected valid subset.
- Preserve existing values unless replacement is explicitly authorized. Corrections of ledger-owned facts require
  the exact superseded event. Blank and not-applicable decisions still use the existing typed single-value path.
- Normalize admitted field kinds using the existing deterministic normalizer; do not infer semantic assignments from
  field names. The caller must establish supplied-fact provenance before reaching this primitive.
- Publish all member events in one atomic snapshot write. Detach the working record before construction so an error
  cannot publish a partial correction even when unrelated expired jobs are pruned in the same store operation.
- Bind idempotency to the entire canonical assignment set, original revision, job, and schema. Reordering a retry is
  harmless; changing a value, member set, or original revision conflicts. Duplicate retries never overwrite later
  corrections or produce another write.
- Return one opaque `form_composite` receipt and value-free field identities/progress. Receipt resolution reads safe
  current progress only; it does not replay assignments or return values.
- Persist versioned composite membership inside each member's encrypted payload. Validate complete membership,
  unique fields/indexes, digest binding, and the contiguous revision range on protected reads, including after restart.
- New accepted facts invalidate existing review/approval/commit authority. Cancel/expiry/terminal cleanup erase the
  same protected material as the single-field path.

The snapshot/encryption envelope versions remain unchanged. Legacy single-field payloads omit the optional composite
member and remain readable. A pre-PDFI4 binary must fail closed when decrypting the new payload shape, rather than
interpreting it as a partial single-field answer. Never roll mutable job data backwards after new writes. Deployment
qualification must record a forward-compatible recovery or deliberate termination of new intake jobs before an older
binary is used.

## Packet 1B And Packet 2 Boundary

Packet 1B extends the existing protected binding/sink with encrypted, owner-bound utterance receipts and bounded pending
input. Before it merges, freeze and test caps on retained utterances, extraction passes, and presentation candidates,
with single-field fallback, restart, cancellation, and duplicate ingress coverage. Do not introduce a new secret store
or generic protected-input framework.

Packet 2 establishes protected provenance through its ephemeral semantic extraction boundary and wires same-route
standalone messages into that ingress before ordinary history or diagnostic capture. It handles facts outside a current
eight-field view and deterministic partial-error feedback. Packet 1A does not expose ordinary multi-fact chat intake,
change Telegram routing, alter approval semantics, deliver a PDF, or fix the motivating standalone-response incident.

## Validation Required For 1A

Focused tests cover field-kind normalization, one write for concurrent duplicates, whole-set replay binding, schema/
source/backend and owner/route isolation, pending single-answer fencing, assignment/value/event limits, explicit
preserved-value replacement, no prefix after validation or snapshot-write failure, no aliased correction during
unrelated expiry, correction invalidation of a real review/approval, authenticated malformed membership, restart-safe
receipt resolution, public/persisted sentinel scans, and terminal erasure. Run the existing document tests and affected
tool/agent tests, race-test the ledger, format and lint changed Go packages, and use normal exact-head PR gates.
