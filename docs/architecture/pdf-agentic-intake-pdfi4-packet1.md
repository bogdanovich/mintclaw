# PDFI4 Packet 1: Protected Composite Substrate

## Status

In implementation; 1A is merged in [PR #1484](https://github.com/bogdanovich/mintclaw/pull/1484). This is a prerequisite
for the [PDFI4 goal](pdf-agentic-intake-pdfi4-goal.md), not a user-facing
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
- Reject noncanonical job IDs before lookup. Derive composite identity and snapshot publication from the authorized
  record's canonical ID; malformed caller input must never create a second map key or corrupt unrelated jobs.
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

## Packet 1B Architecture Checkpoint

Before introducing protected utterances, the chosen boundary is the existing document-owned job and sink, not a
generic multi-answer interaction registry or a second persistence system. Extend the encrypted source-secret payload
with optional, versioned intake state. Preserve its immutable source reference and the existing atomic snapshot,
locking, authentication, retention and terminal key erasure. Legacy single-field jobs omit this state and remain
readable; an older binary must fail closed when decrypting a new intake payload. Do not roll mutable state backwards.
The only interaction/agent substrate addition is ephemeral stable transport identity beside the legacy prompt-scoped
idempotency key. The old key algorithm and old single-field receipts remain unchanged.

The concrete bounds are 16 KiB per UTF-8 utterance, four retained unresolved inputs, 64 KiB total retained text,
32 extraction passes per input, eight candidates per pass, and 256 value-free input/replay identities per job. The
existing 32-assignment atomic batch, per-field, event, store-byte and job-lifetime limits remain unchanged. Thirty-two
eight-field passes cover the 250-field public fixture without an all-schema request; Packet 2 can stop earlier when
facts are accounted for. Exhaustion must preserve unresolved input and return a split/clarification finding, not reset
the budget or silently truncate. Retired input can release its text only after explicit, atomic accounting by the
later consumer; protected transport admission alone is not semantic acceptance.

The first question uses the existing protected sink's stage/registry-claim/commit/discard recovery. Successive messages
use a document-owned atomic admission bound to the authenticated owner/route, exact active mode, immutable schema and
transport identity. They must not acquire synthetic registry claims or become ordinary steering. Whole-input keyed
digests fence changed-body retries; opaque receipts and current safe progress are the only public results. A stale
binding cannot admit new input, but an exact stored retry remains recoverable. Only committed input is available to
the later private consumer. Admission invalidates stale review and commit authority and pending input fences final
materialization. Controls do not become literal field values.

Packet 1B has no model call, public free-text tool argument, channel interception or partial-output capability. Packet 2
must remove the Telegram pre-agent input preview and protect provider error logging: a provider error message may echo
private request content even when the agent returns a generic failure. Its private calls must disable ordinary tools,
native search, stream/trace callbacks and ordinary history. This is a scoped request boundary, not PDFI5's generic
protected-input framework. The existing raw inbound spool remains transient transport storage; no claim of encryption
for every pre-admission transport byte is made.

## Validation Required For 1A

Focused tests cover field-kind normalization, one write for concurrent duplicates, whole-set replay binding, schema/
source/backend and owner/route isolation, pending single-answer fencing, assignment/value/event limits, explicit
preserved-value replacement, no prefix after validation or snapshot-write failure, no aliased correction during
unrelated expiry, correction invalidation of a real review/approval, authenticated malformed membership, restart-safe
receipt resolution, public/persisted sentinel scans, and terminal erasure. Run the existing document tests and affected
tool/agent tests, race-test the ledger, format and lint changed Go packages, and use normal exact-head PR gates.

## Validation Required For 1B

Native tests establish three successive standalone messages in the same mode, exact owner/workspace/account/topic/
session isolation, encrypted raw retention and value-free JSON, restart-safe discovery, stable transport replay across
question claims and standalone admission, changed-body conflicts, and no receipt after failed atomic publication.
Question ingress stages input until the existing registry claim commits; its discard grace protects in-flight claims,
forced discard never removes committed input, and typed back/clarify never becomes a field value. Old prompts reject
new messages while exact stored retries recover. Native review preparation/publication, approval, and materialization
must all fence both staged and committed unresolved input, including a stage-versus-audit-publication race.

Tests freeze the per-input, pending-text and extraction/candidate bounds; exhausting them retains previous unresolved
input. Pass reservations are durable and keyed, so retry or restart cannot reset the extraction budget.
Retain all reserved-key bindings in one bounded list (at most 32 per input); the list length is the budget,
not a second mutable counter. Interleaved A/B/A retries, including after restart and at the full budget, reuse their
original exact views without another charge. A reused key cannot change candidates, ordering or revision, and an old
view cannot become authority at a newer job revision. Pausing retains private text, resuming preserves the same job,
closing cannot discard unresolved input, and cancel/expiry erases the
existing source/key material. Source-reference preservation, old-reader fail-closed behavior, legacy single-field
tests, race tests and Linux/macOS/Windows CI qualify compatibility. These are substrate tests, not real Telegram or
natural-language filling evidence; those remain mandatory in later packets before the PDFI4 goal can be complete.
