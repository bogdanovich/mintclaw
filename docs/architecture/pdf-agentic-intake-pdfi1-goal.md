# PDFI1 Natural-Language PDF Form Orchestration Goal

## Status

Complete. The admission, implementation, exact-revision deployment, and qualification evidence are recorded in the
[PDFI1 exit report](pdf-agentic-intake-pdfi1-exit.md). PDF3 remains the completed prerequisite.

## Objective

Make the existing protected PDF3 workflow the default agent path when an operator gives MintClaw a supported PDF form
and asks, in ordinary language, to complete it. The operator must not need to provide a technical workflow prompt,
stable field IDs, interaction IDs, `/answer` syntax, or document-tool action names.

## In scope

1. Update the bundled PDF skill with an explicit strategy split between read-only work, conversational form completion,
   and the already-supported expert one-shot stable-ID fill.
2. Keep the `document` tool deferred. The agent discovers it once and uses the exact current attachment ref or the
   authorized local-path inspect result.
3. Require inspection before strategy, then start the existing protected workflow with `action=form` and
   `form_action=start` for an ordinary form-completion request.
4. Resume a protected answer using only its opaque receipt with `form_action=continue`. Never copy, summarize, or ask the
   model to repeat the raw answer.
5. Teach the agent to use the same job for status, correction, cancel, review, approval-bound commit, recovery, and
   terminal reporting.
6. Prevent duplicate ordinary prose when the tool has suspended for a protected channel question. Do not add a second
   confirmation before the existing approval interaction.
7. Add deterministic regression coverage starting from a short natural user request and exercising tool discovery,
   inspect/start, protected answer continuation, review, approval, one verified delivery, and privacy-negative checks.
8. Add a copy-pasteable manual test that uses an ordinary request rather than a tool script or long prescriptive prompt.

## Fixed decisions

- PDF3 remains the sole durable conversational workflow and PDF2 remains the sole writer/verification transaction.
- Existing protected storage, interaction, mapping, review, approval, operation, artifact, and outbox schemas do not
  change in PDFI1.
- Direct `fill` is not removed. It is reserved for an explicitly requested one-shot operation with a complete,
  unambiguous stable-ID assignment map already established from the current source.
- An ordinary request such as “fill this form” uses PDF3 even when the form has only a few fields. Convenience does not
  bypass protected collection.
- A tool-created question owns delivery of that question. The assistant waits instead of paraphrasing it into an
  additional chat message.
- Existing approval policy decides whether commit needs operator approval. The skill does not synthesize approval or
  interpret silence as consent.
- No form-specific semantics, aliases, field rules, or sample answers enter the production skill or code.

## Out of scope

- grouped or composite protected questions;
- a generic protected-input package or durable-schema rename;
- importing personal data from memory, profiles, unrelated chats, or external systems;
- XFA expansion, encrypted-PDF mutation, OCR, tables, redaction, generation, or other PDF5 capabilities;
- new worker, broker, daemon, binary, task state machine, writer, or delivery mechanism;
- macOS capability advertising or parity qualification.

## Implementation sequence

1. Merge this admission as a docs-only PR.
2. From the new `origin/main`, update the bundled PDF skill and only the compact model-facing descriptions needed to
   make its routing unambiguous.
3. Add focused skill-contract and agent/channel vertical tests. Reuse existing PDF3 fixtures, protected answer sink,
   approval manager, document writer, outbox, and privacy scanners.
4. Add the ordinary-language manual test and update the mini-roadmap with the merged evidence.
5. Merge through normal CI, automated review, and owner rocket approval.
6. Deploy the exact merged `main` revision to `server@oc`, confirm service health, and run the existing aggregate
   document deployed smoke plus the focused natural-language scenario.

## Acceptance matrix

| Invariant | Required proof |
| --- | --- |
| Ordinary entry | A short “fill this attached form” request activates the PDF skill and discovers the deferred tool |
| Strategy | Inspect precedes `form/start`; incomplete conversational work never uses direct `fill` |
| Protected continuation | Each accepted value returns only an opaque receipt to ordinary model context and advances the same job |
| Interaction UX | The channel receives one protected question at a time with buttons/free text/cancel and no duplicate assistant question |
| Lifecycle | Status, correction, cancel, review, approval, and commit target the original owner-bound job |
| Privacy | Unique raw sentinels are absent from ordinary model context, canonical history, traces, interactions, public state, and outbox metadata |
| Correctness | Source digest is unchanged and PDF2 structural plus visual verification succeeds |
| Delivery | One operation and one outbox intent produce exactly one delivered PDF across continuation/recovery |
| Regression | Existing direct fill, PDF3 workflow, read/render, local-path, approval, and delivery tests remain green |
| Deployment | Exact merged SHA is active on `server@oc`; health and document deployed smoke pass |

## Completion criteria

PDFI1 is complete only when:

- the admission and implementation PRs are merged;
- the production PDF skill describes the complete natural-language workflow without a form-specific prompt;
- deterministic tests begin with an ordinary user request and prove inspect/start, protected continuation, review,
  approval-bound commit, source immutability, privacy, and exactly-one delivery;
- direct one-shot fill remains supported only under its explicit stable-ID contract;
- the copy-pasteable manual test is checked in and does not tell the user to call tools or provide IDs;
- the exact merged revision is deployed and the focused plus aggregate deployed checks pass; and
- the mini-roadmap records PDFI1 exit evidence while PDFI2-PDFI5 remain explicitly unstarted.
