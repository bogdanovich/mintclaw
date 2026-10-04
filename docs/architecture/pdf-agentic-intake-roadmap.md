# Generic Agentic PDF Intake Mini-Roadmap

## Status

Active follow-up program. PDF3 remains complete for its admitted `linux/amd64` protected form transaction. This
mini-roadmap improves how an agent plans and conducts the conversation that supplies that transaction; it does not
reopen PDF3's storage, authority, approval, writer, verification, or delivery architecture.

PDFI1 is complete for natural-language routing. Its admission, implementation, exact-revision deployment, and
qualification evidence are recorded in the [PDFI1 exit report](pdf-agentic-intake-pdfi1-exit.md). A later live run
showed that routing alone is not agentic intake: `form/start` immediately selected the first unresolved raw-schema
field, ordinary clarification text was accepted as that field's protected value, and the continuation exposed both an
interaction identity and a protected receipt that a model could confuse. Those are post-PDFI1 design findings, not a
rollback of its routing evidence.

PDFI2 is complete; its implementation, deployment, and live evidence are recorded in the
[PDFI2 exit report](pdf-agentic-intake-pdfi2-exit.md). PDFI3 is admitted and in progress under its
[implementation goal](pdf-agentic-intake-pdfi3-goal.md); it has no exit evidence yet. PDFI4 remains not started.
PDFI5 remains conditional on a second accepted non-PDF protected-input consumer.

## Operator outcome

An operator can attach or authorize a supported form and say, in ordinary language, that it should be completed.
MintClaw should inspect and understand the exact document, explain its purpose and applicable sections, build a bounded
fact plan, and ask only coherent missing questions. The agent owns that dialogue, including clarification and ordinary
correction. The durable document service owns exact schema facts, protected values, source identity, mutation,
verification, and delivery. The operator should not need to know document-tool actions, field IDs, interaction IDs,
receipt syntax, or the distinction between PDF2 and PDF3.

The behavior is document-general. No government form, tax form, agency, field naming convention, or one manual prompt
defines the workflow.

## Architectural boundary

The program reuses the completed PDF3 control plane:

- the form job and immutable source/schema identity;
- protected interactions and encrypted append-only field ledger;
- schema-bound value mapping, deliberative audit, and bounded review;
- approval-bound PDF2 commit, independent verification, and one outbox-owned delivery.

The backend is not the conversation planner. A form job may retain a bounded semantic plan and expose value-free
progress, but it must not select the next question merely by walking PDF field order. The agent and bundled PDF skill
own document interpretation, question order, explanations, and ordinary dialogue. `pkg/document` validates every
proposed stable field identity, stores protected values, rejects stale or ambiguous mappings, and performs the final
transaction.

Protected input is an explicit privacy mode, not an accidental trap for every conversational message. While a
protected question is active, the UI must offer unambiguous answer, clarify, back, skip/not-applicable when allowed,
and cancel paths. A clarification request must never be committed as a form value. The reusable protected-input layer
is still not extracted: a future extraction is justified only after a second accepted non-PDF workflow needs it.

## Milestones

### PDFI1: Natural-language orchestration

**Status:** Complete. See the [PDFI1 exit report](pdf-agentic-intake-pdfi1-exit.md).

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

### PDFI2: Agent-owned question handoff and continuation identity

**Status:** Complete. See the [PDFI2 exit report](pdf-agentic-intake-pdfi2-exit.md).

The focused [PDFI2 implementation goal](pdf-agentic-intake-pdfi2-goal.md) fixes the ownership boundary, PR sequence,
acceptance matrix, and completion gate for this milestone.

Separate durable job preparation from protected question delivery and remove ambiguous continuation identity.

Required behavior:

- `form/start` creates or resumes the owner-bound job and returns safe bounded state without suspending or selecting a
  field;
- an explicit agent action selects one schema-valid field and starts one protected question; the backend never falls
  through to the next raw-schema field;
- accepting the answer yields one purpose-named continuation receipt, maps that exact field, and returns control to the
  agent without opening another question;
- generic interaction identity remains available for runtime diagnostics but is not a valid or competing document
  continuation argument;
- protected question controls include clarify and cancel, plus back and optional blank intents when applicable;
- clarify/back/cancel do not append a value event, and arbitrary clarification text is not silently treated as a value;
- restart, compaction, duplicate ingress, stale receipt, correction, and cancellation preserve the original PDF3
  authority and no-replay guarantees.

Completion gate:

- the agent, not `FormMappingSummary`, chooses every new question;
- no tool result presents two plausible continuation identifiers;
- a regression reproducing “what exact information?” leaves the ledger unchanged and returns the conversation to the
  agent;
- existing PDF3 privacy, immutable-source, approval, commit, recovery, and exactly-once delivery tests remain green.

### PDFI3: Bounded semantic fact plan and natural dialogue

**Status:** Admitted; implementation and qualification in progress. The
[PDFI3 implementation goal](pdf-agentic-intake-pdfi3-goal.md) freezes ownership, packets, budgets, and stop criteria.

Teach the bundled PDF skill to interpret the form before collecting values and add only the compact native projections
needed to keep that plan bounded.

Required behavior:

- inspect and field discovery precede collection, and the agent receives enough bounded text/render/schema evidence to
  identify the document purpose, parties, sections, conditional branches, and required versus optional facts;
- before the first value question, the agent gives a short form summary and collection plan, asking about the user's
  goal only when it materially changes applicable sections;
- the safe job projection distinguishes known fields, missing fields, conflicts, optional blanks, and a coherent next
  section without exposing values;
- the agent selects ordinary versus protected input according to the fact being requested and explains protected mode
  before entering it;
- confirmed facts may be reused only inside the same owner-authorized job; model proposals remain non-authoritative,
  stable-ID-bound, and deterministically validated;
- ordinary correction intent is resolved by the agent without asking for field IDs and invalidates stale review and
  approval state.

Completion gate:

- the production skill contains no form, agency, locale, or field-name-specific rules;
- a small and a large synthetic form begin with a meaningful summary rather than the first writable field;
- the model-visible plan stays within a fixed field/page/text budget and reports truncation truthfully;
- missing or ambiguous evidence causes a focused question, not guessed field assignments.

### PDFI4: Coherent sections and large-form qualification

**Status:** Not started.

Reduce question fatigue only after agent-owned planning works. Admit a versioned composite protected answer for one
coherent section when the channel can represent it safely, with per-field validation, atomic acceptance, deterministic
partial-error reporting, and append-only ledger events. A single-value protected question remains the fallback.

Qualify the combined workflow on deterministic synthetic small, medium, and large AcroForms plus at least one licensed
or official public large-form fixture. Prove clarification, back, free text, composite and single-value collection,
pause/resume, restart, compaction, conditional and optional-section skipping, bounded model context, correction,
approval, source immutability, visible verification, one delivery, privacy scans, cleanup, and rollback. No
form-specific rules may enter production prompts or code.

### PDFI5: Conditional protected-input extraction

**Status:** Not started and remains conditional on a second accepted non-PDF consumer.

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
PDFI2 agent-owned question handoff
    |
    v
PDFI3 semantic fact plan and dialogue
    |
    v
PDFI4 coherent sections and large-form qualification

Second accepted non-PDF consumer ----> PDFI5 conditional extraction
```

- Each milestone is one independently testable user outcome and may use several focused PRs.
- A milestone cannot weaken PDF3 privacy, authority, approval, verification, recovery, or exactly-once delivery gates.
- A skill owns conversational strategy but may not parse, mutate, verify, persist, or deliver a PDF itself.
- A native tool may enforce authority and validate an agent decision, but may not silently replace that decision with
  schema-order question selection.
- A need for a new daemon, broker, task registry, writer, delivery queue, or generic secret store triggers an architecture
  checkpoint rather than expanding the current milestone.
- XFA expansion, PDF password/decryption support, transformations, companion placement, and macOS parity remain in their
  existing roadmap lanes.

## Program completion

This mini-roadmap is complete only when PDFI1-PDFI4 have merged exit evidence and the operator can complete a qualified
large form from an ordinary request through an agent-led conversation without technical prompting, raw-schema question
ordering, ambiguous continuation identity, or protected-value leakage. PDFI5 is conditional and is not required until
a second consumer is admitted.
