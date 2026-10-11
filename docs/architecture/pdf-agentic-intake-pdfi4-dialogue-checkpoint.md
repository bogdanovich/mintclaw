# PDFI4 Protected Dialogue Checkpoint

This dependent implementation builds on #1499 and the merged spool version prerequisite #1497. It is not exit evidence.

The explicit `WithDocumentFormIntake` composition option enables tests and the later integration packet. Existing
production wiring supplies only `WithDocumentFormAudit`: it does not advertise grouped input or start intake yet.
No runtime setting or separate provider is introduced by this implementation prerequisite.

## Invariants And Chosen Integration

Reuse the existing document tool, native form job, encrypted source/pass outcomes, inbound spool, runtime session
ownership and output transaction. No second queue, value store, semantic planner, daemon or approval protocol.

The native intake mode is the only authority for matching owner, route, immutable document, schema and revision.
The coordinator persists a value-free document replay guard before admitting raw text. A guarded record cannot be
cleared or rebound and cannot fall back to ordinary history after cancel, key erasure, missing tools or recovery
failure. Guarded spool records use version 2; ordinary version-1 records remain supported. #1497 is the minimum
reader that refuses unsupported guarded records instead of exposing them. It is not a safe live-intake rollback
target: an old runtime cannot protect new, not-yet-guarded messages. Before downgrading, explicitly close all active
intakes and drain guarded records, or retain a build with the same intake admission support. Do not silently disable
the composition option while protected conversations remain active.

The existing tool owns schema loading, bounded private interpretation and native application. Ordinary model
continuation sees only an opaque receipt and value-free progress. Uncertainty is retained in the existing encrypted
pass outcome and participates in its whole-proposal replay digest. Findings remain until their own field resolves;
an unrelated window cannot erase them or consume the protected input. No additional durable state machine is added.

Compared with routing standalone text as ordinary guidance, this preserves the admitted protected-input contract.
Compared with pretending every message answered a registry question, it avoids fabricated interaction ownership.
Compared with another secret subsystem, it keeps lifecycle, capacity, key erasure and authority document-owned.

Guard refinement records one immutable opaque input reference after native admission. It is carried only as
trusted in-memory turn/steering metadata and omitted from provider-message JSON. A busy turn uses the existing
steering FIFO; receipt processing is re-armed at its existing input-injection boundary. Revision races reserve
at most two attempts per candidate window, each charged to the existing 32-pass input budget. A revision-bound
reservation group replays its committed outcome without a second private call; no separate retry counter/store.
Advisory dialog intent is stored with that exact encrypted outcome, never treated as approval authority.

## Remaining Gates

This packet implements the coordinator and tool bridge, grouped natural-message intake and controls, and focused
replay, concurrency and privacy regressions. Complete/partial
output, real model, deployed Telegram qualification and exit remain separate mandatory gates. This checkpoint and
foundation tests alone do not complete PDFI4.

The dependent review-boundary prerequisite separates opaque admission and bookkeeping from assignment changes.
Pending and staged input still fences review publication, approval and materialization. Accepted field events,
context changes and lifecycle controls still invalidate authority. A control-only finish preserves the review;
only a receipt admitted at that exact ready review can request the existing final approval. An older receipt cannot
auto-finish a newly published review. Intent is derived from existing encrypted outcomes on restart, not a second
state machine or approval grant. Native and tool tests cover this boundary; full/partial output and real channel
qualification remain mandatory before enabling the dialogue.

Extraction reservations also bind the count of existing immutable admitted receipts. A new message invalidates an
in-flight interpretation without resetting assignment review; bounded retry identity includes that admission snapshot.
No staged claim may exist when reserving. Committed/consumed receipts are retained until terminal cleanup, so the
snapshot is derived from the existing ledger, not a new counter. The bridge refreshes related inputs after reservation.
Legacy committed outcomes remain replayable; unfinished unbound reservations cannot acquire fresh apply authority.
