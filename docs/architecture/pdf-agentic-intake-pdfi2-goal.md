# PDFI2 Agent-Owned PDF Form Dialogue Goal

## Status

Complete. Merged, deployed, and live-qualification evidence is recorded in the
[PDFI2 exit report](pdf-agentic-intake-pdfi2-exit.md). This frozen packet corrected the conversation boundary
discovered by the first live large-form run without reopening PDF3 source retention, encryption, schema binding,
audit, approval, write, verification, recovery, or delivery guarantees.

## Objective

Make one durable PDF form job a capability that the agent deliberately drives instead of a backend wizard that drives
the agent. Preparing a job must not ask a question. The agent must explicitly choose a schema-valid field before a
protected question is delivered, and accepting that answer must return control to the agent without selecting the next
field. A user must be able to request clarification without that request becoming a form value.

## Fixed ownership boundary

| Concern | Owner |
| --- | --- |
| Understand document purpose, parties, sections, and user intent | Agent plus bundled PDF skill |
| Choose question order and explain why a fact is needed | Agent plus bundled PDF skill |
| Bind a question to an exact writable field and job revision | Native document tool |
| Store and normalize protected answers | Protected interaction sink and form-job ledger |
| Decide whether an inbound message is an explicit protected answer or new conversational guidance | Interaction runtime |
| Audit, review, approval, mutation, verification, and delivery | Existing PDF3/PDF2 control plane |

`FormMappingSummary` may report value-free progress and blockers. It must not choose or automatically deliver the next
question.

## In scope

1. Split form preparation from question collection:
   - `form/start` creates or resumes the exact owner-bound job and returns safe state without suspension;
   - a dedicated form action accepts `job_id` and one exposed stable `field_id`, validates both against the pinned
     schema and revision, and creates one protected question;
   - `form/continue` maps one accepted answer and returns safe state without opening another interaction.
2. Replace the ambiguous model-facing continuation contract:
   - expose one purpose-named protected answer reference;
   - do not present the generic interaction ID as an alternative document continuation token;
   - reject cross-job, stale, replayed, malformed, or wrong-authority references;
   - retain backward-compatible persisted-state recovery where required, but do not advertise legacy ambiguity.
3. Distinguish answers from guidance:
   - a button, an explicit answer command, or a transport-verified reply to the active prompt may supply a protected
     value;
   - a non-answer ordinary message supersedes only the pending question, leaves the form job and ledger intact, and
     returns the message to the agent as conversational guidance;
   - an explicit cancel still cancels the form job through the existing authority checks;
   - optional skip/not-applicable remain typed intents rather than text values.
4. Add agent-controlled navigation:
   - clarification and back leave the protected ledger unchanged;
   - correction targets the same job and invalidates derived review/approval state;
   - no path asks the operator for a field ID, interaction ID, or receipt.
5. Update the bundled PDF skill and compact document-tool description only after the native contracts exist. The skill
   must inspect and discover fields, explain the document and proposed collection plan, then deliberately request each
   protected field. It must never infer a field ID or continue a stale job.
6. Add deterministic unit, integration, agent vertical, restart/compaction, and live-agent coverage.

## Out of scope

- form-specific semantics, aliases, or field-name rules;
- automatic reuse of personal data from memory, profiles, or unrelated sessions;
- composite multi-field protected answers, which remain PDFI4 scope;
- a generic protected-input extraction, which remains conditional PDFI5 scope;
- XFA expansion, OCR, tables, redaction, transformations, macOS parity, or a new PDF backend;
- a new daemon, broker, task registry, writer, verifier, artifact store, or delivery queue.

## PR sequence

1. **Contract foundation:** make preparation non-suspending, add explicit field collection, make continuation identity
   unambiguous, and remove automatic next-field delivery. Preserve existing state compatibility and add focused
   document-tool/form-job tests.
2. **Interaction guidance handoff:** admit verified answers separately from ordinary guidance, abandon a superseded
   question without canceling its form job, and cover Telegram plus generic-channel reply semantics.
3. **Agent orchestration:** update the PDF skill, compact schemas/descriptions, and vertical agent harness so the agent
   inspects, explains, plans, chooses, resumes, reviews, commits, and delivers.
4. **Qualification and deployment:** run existing document regressions plus the focused live scenario, deploy the exact
   merged main revision, and record exit evidence. PDFI3 starts only after this packet exits.

Each code PR is independently reviewable and merges before its dependent successor starts.

## Acceptance matrix

| Invariant | Required proof |
| --- | --- |
| Prepare only | `form/start` returns an active job and safe bounded progress with no suspension |
| Agent choice | No protected question exists until the agent supplies one schema-valid field ID |
| One question | An accepted answer maps exactly that field and returns without opening another question |
| Continuation identity | The model sees one purpose-named answer reference and cannot substitute an interaction ID |
| Clarification | “What exact information do you need?” leaves field events and ledger revision unchanged and reaches the agent |
| Navigation | Clarify/back preserve the job; explicit cancel terminates it; optional blank intents remain typed |
| Authority | Wrong owner, route, source, schema, job revision, field, receipt, and replay fail closed |
| Privacy | Protected sentinels remain absent from model context, history, traces, public state, and delivery metadata |
| Recovery | Restart and compaction preserve the prepared job, active question, accepted receipt, and no-replay boundary |
| Transaction | Review, approval, source immutability, verified mutation, and exactly-one delivery remain unchanged |
| Agent UX | A short ordinary request produces a document summary and plan before any value question |
| Generality | Production code, prompts, and fixtures contain no agency-, form-, locale-, or field-name special case |

## Completion criteria

PDFI2 is complete only when all four PR packets are merged, required CI and exact-head automated reviews pass, owner
rocket approvals authorize each code merge, and the exact resulting `main` revision is deployed. The live test must
show a natural request, summary-before-question behavior, clarification without ledger mutation, a resumed protected
answer, review, approval when configured, and exactly one verified PDF artifact. Existing PDF2/PDF3/PDFI1 aggregate
tests and privacy scans must remain green. Any deferred semantic planning or grouping work must remain explicitly in
PDFI3/PDFI4 rather than being claimed by this exit.
