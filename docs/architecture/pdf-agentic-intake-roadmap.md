# Generic Agentic PDF Intake Mini-Roadmap

## Status

Selected follow-up program. PDF3 remains complete for its admitted `linux/amd64` protected conversational form
workflow. This mini-roadmap improves how an operator reaches and drives that workflow from an ordinary request; it does
not reopen PDF3's storage, authority, approval, writer, verification, or delivery architecture.

Only PDFI1 is admitted by the linked [implementation goal](pdf-agentic-intake-pdfi1-goal.md). PDFI2-PDFI5 are ordered
future candidates and require their own admission after the preceding exit evidence exists.

## Operator outcome

An operator can attach or authorize a supported form and say, in ordinary language, that it should be completed.
MintClaw should inspect the exact document, choose the durable protected workflow, collect only unresolved facts with
native channel controls and free text, preserve the same job across turns, show a bounded review, obtain the configured
approval, and return one verified PDF. The operator should not need to know document-tool actions, field IDs,
interaction IDs, receipt syntax, or the distinction between PDF2 and PDF3.

The behavior is document-general. No government form, tax form, agency, field naming convention, or one manual prompt
defines the workflow.

## Architectural boundary

The program reuses the completed PDF3 control plane:

- the form job and immutable source/schema identity;
- protected interactions and encrypted append-only field ledger;
- schema-bound mapping, correction, deliberative audit, and bounded review;
- approval-bound PDF2 commit, independent verification, and one outbox-owned delivery.

The reusable part of protected intake is intentionally not extracted yet. PDF field semantics remain owned by
`pkg/document`, and the model continues to see one deferred `document` capability. A future extraction is justified
only after a second accepted non-PDF workflow needs the same privacy boundary.

## Milestones

### PDFI1: Natural-language orchestration

Teach the bundled PDF skill and the compact model-facing contract to select and drive the existing PDF3 workflow from
an ordinary request.

Required behavior:

- a general request to complete a supported form selects `action=form` instead of direct `fill`;
- the agent discovers the deferred tool once, inspects the exact current source, and starts the protected workflow;
- a protected receipt resumes the same job through `form_action=continue` without replaying or echoing the value;
- `status`, natural correction intent, and cancel route to the existing job rather than creating a replacement;
- a suspended protected question is not duplicated in ordinary assistant prose;
- review readiness leads to the existing approval-bound commit and exactly one verified delivery;
- direct `fill` remains available for a caller that already has one complete, explicit, unambiguous stable-ID assignment
  map and deliberately requests the one-shot path;
- typed unsupported, stale, ambiguous, audit-unavailable, approval, verification, and delivery states remain truthful.

The [PDFI1 implementation goal](pdf-agentic-intake-pdfi1-goal.md) is the frozen admission for this milestone.

### PDFI2: Composite protected intake

Reduce question fatigue without weakening the PDF3 privacy boundary. Admit a versioned composite answer for one
coherent form section, with per-field validation, atomic acceptance semantics, deterministic partial-error reporting,
and append-only ledger events. Channel surfaces must retain a free-text escape hatch and cancellation. This milestone
must prove that raw composite values remain absent from ordinary history, traces, task state, and public job state.

### PDFI3: Fact plan, reuse, and conversational correction

Present a bounded, human-readable collection plan: known facts, missing facts, conflicts, optional blanks, and the next
coherent section. Reuse confirmed facts within the same owner-authorized job, accept ordinary correction requests
without exposing stable IDs, and invalidate derived review/approval state deterministically. Model proposals remain
non-authoritative and schema-bound.

### PDFI4: Large-form qualification

Qualify the combined workflow on deterministic synthetic small, medium, and large AcroForms plus at least one licensed
or official public large-form fixture. Prove pause/resume, restart, compaction, optional-section skipping, bounded model
context, correction, approval, source immutability, visible verification, one delivery, privacy scans, cleanup, and
rollback. No form-specific rules may enter production prompts or code.

### PDFI5: Conditional protected-input extraction

Extract a document-neutral protected-input substrate only after a second accepted non-PDF consumer exists. The generic
layer may own encrypted values, owner/revision binding, protected reply ingestion, retention, and deletion; it must not
know about `DocumentRef`, AcroForm fields, PDF2, rendering, or document delivery. Existing PDF3 envelopes and jobs must
remain readable or receive an explicit fail-closed migration. A speculative API with only the PDF adapter does not
satisfy this milestone.

## Sequence and stop gates

```text
PDF3 complete
    |
    v
PDFI1 natural orchestration
    |
    v
PDFI2 composite protected intake
    |
    v
PDFI3 fact plan and correction UX
    |
    v
PDFI4 large-form qualification

Second accepted non-PDF consumer ----> PDFI5 conditional extraction
```

- Each milestone is one independently testable user outcome and may use several focused PRs.
- A milestone cannot weaken PDF3 privacy, authority, approval, verification, recovery, or exactly-once delivery gates.
- A skill may choose the workflow but may not parse, mutate, verify, persist, or deliver a PDF itself.
- A need for a new daemon, broker, task registry, writer, delivery queue, or generic secret store triggers an architecture
  checkpoint rather than expanding the current milestone.
- XFA expansion, PDF password/decryption support, transformations, companion placement, and macOS parity remain in their
  existing roadmap lanes.

## Program completion

This mini-roadmap is complete only when PDFI1-PDFI4 have merged exit evidence and the operator can complete a qualified
large form from an ordinary request without technical prompting or protected-value leakage. PDFI5 is conditional and is
not required until a second consumer is admitted.
