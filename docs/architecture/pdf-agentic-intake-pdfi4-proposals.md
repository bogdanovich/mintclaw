# PDFI4 Grounded Intake Proposal Checkpoint

## Purpose And Dependency Boundary

The merged [protected substrate](pdf-agentic-intake-pdfi4-packet1.md) admits utterances but cannot yet turn them into
accepted facts. This packet adds only native application of bounded, protected proposals. It depends on merged 1A
and 1B; it is independent of the private-provider logging prerequisite because it introduces no provider call or
public intake action. Live semantic dialogue must wait for both prerequisites to merge.

The [PDFI4 goal](pdf-agentic-intake-pdfi4-goal.md) remains incomplete. Native tests are not real-model or Telegram
qualification. No backend, final-output policy, general secret framework, store, service or second planner is added.

## Native Contract

- One proposal binds the exact authenticated owner/route, immutable source/schema, committed input, reserved pass,
  current revision and ordered candidate view. Only the reserved candidates may receive assignments or findings.
- Values are derived from exact literal references into the retained utterance, not from a model-supplied replacement
  value. Literal occurrence counts only exact, token-aligned matches, is bounded and Unicode-safe; a proposal cannot
  invent, translate or concatenate values. Substrings embedded in earlier words do not shift an eligible occurrence.
  Existing deterministic field/choice/date normalization remains the only normalization authority.
- A single source span cannot silently populate different people/fields. Ambiguous or contradictory proposals retain
  their input for clarification. Same-field, already-accepted identical facts can be acknowledged without another
  value event. A replacement needs explicit correction evidence and the exact current superseded event; source
  values are never silently replaced.
- Determine the valid accepted subset and value-free findings before mutation. Reuse the existing composite event
  construction, and publish accepted events plus input accounting in one existing atomic snapshot write. Do not call
  `AppendComposite` and then separately mark the input consumed. Structural/provenance failure publishes nothing.
- Retain the original utterance while substantive text remains unaccounted for. Bounded literal accounting records
  distinguish accepted facts from explicitly classified non-data guidance; whitespace/punctuation is not a fact.
  Facts outside the current eight-field view therefore remain protected for another view. Clearing raw text and
  marking consumed happen atomically, only after accounting covers the utterance.
- Persist an exact whole-proposal keyed replay digest and safe outcome on the existing reservation. Identical retries
  recover the original result; changed proposal bodies fail closed, even after another input advances the job.
  `AppliedRevision`, accepted/acknowledged IDs, findings and consumption describe that original pass. `Job` and
  `Progress` describe current state; a replay is not permission to reinstate old values or repeat delivery.
  No accepted value, literal, private proposal digest or raw model output appears in ordinary result JSON.
- New accepted facts and accounting/control changes invalidate stale review/approval. Staged input remains unreadable;
  pause, close, cancel, expiry and all existing final gates retain their substrate semantics.

Literal provenance and native typing do not mathematically prove a person's semantic association or a model's
classification of non-data prose. The later tools-disabled interpreter must flag uncertainty rather than guess;
independent audit, operator review and adversarial role/correction qualification remain mandatory. Do not describe
structural checks as a proof of semantic correctness.

## Bounds And Evidence

Keep 16 KiB/input, four unresolved inputs/64 KiB retained text, eight candidates/pass, 32 reserved passes/input,
32 assignments/composite, existing event/store/retention caps and 256 replay identities. Freeze an additional
64-accounted-span ceiling per input; exhaustion preserves the job and asks for splitting/clarification, never truncates.

Each proposal body also stays within 64 literal references/16 KiB total referenced text. Validate UTF-8 before
JSON-based replay hashing so invalid bytes cannot normalize into an already accepted replacement-rune literal.

Required native tests: five facts from an unlabelled paragraph; another fact outside the first view retained and
later consumed; three successive messages on one job; same-person versus two-person spans; repeated facts;
invalid types/choices, unknown IDs, invented literals and ambiguities; explicit corrections; typed clarify/back;
atomic write/construction failure; exact/changed/out-of-order replay after restart; competing revision/cancel races;
input/accounting/pass bounds; no public or persisted plaintext sentinel; unchanged source identity; legacy composite
and single-field compatibility; portable platform tests and focused races. Full UI, applicability and partial-output
qualification remain later work.
