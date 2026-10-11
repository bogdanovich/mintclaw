# PDFI4 Free-Text Form Intake And Large-Form Qualification Goal

## Status And Objective

Admitted on 2026-10-10. Implementation and qualification are not complete.
The predecessor is the completed [PDFI3 packet](pdf-agentic-intake-pdfi3-exit.md); the parent is the
[agentic intake roadmap](pdf-agentic-intake-roadmap.md).
Admission baseline: `96d5b9d235886f0e478e6d6444476ca0c98d6acd`.

An operator can attach a supported PDF, understand what it is, identify their role and desired outcome, provide
several facts in one free-text message or several successive messages, resolve only genuinely missing or ambiguous
facts, and receive one verified filled PDF. They can also explicitly request a partially filled, editable draft and
finish the remaining fields themselves. This must work without field IDs, `/answer` commands, one-question-per-field
fatigue, repeated accepted facts, or production rules tailored to one form.

This document is the implementation contract, not an exit report. Merging it does not complete PDFI4.

### Operator-Approved Output Scope

On 2026-10-10 the operator explicitly chose to finish free-text intake and filling first, and defer editable I-134
output to a later backend milestone. This decision supersedes the original requirement to qualify an editable partial
I-134 in PDFI4. The current Linux hybrid backend deliberately emits flattened, print-ready derivatives; see
[the form backend contract](../guides/document-forms.md#hybrid-acroformxfa-print-ready-output).

PDFI4 must still qualify complete applicable filling of the official I-134 with synthetic data, and complete plus
editable partial transactions on unrelated ordinary AcroForms. It must truthfully explain the hybrid output limitation,
and must not promise an editable draft or silently flatten a request to finish remaining fields manually. Defer a
generic editable-hybrid derivative capability, not an I-134-specific special case. No XFA/backend expansion is added
to this milestone. All other privacy, recovery, channel, validation, and completion criteria remain mandatory.

## Verified Motivating Incident

The live main gateway on revision `63804ea1f2652338861e224fbbe0b391504514c9` received a form-completion request on
2026-10-10 at approximately 20:38-20:41 UTC. Both standalone responses to the surname question were resolved as
ordinary guidance, not protected values:

- interactions `697835de` and `eaedf809` have `answer.superseded=true` and no protected receipt;
- both subsequent `form/status` results report the same job at revision 1, with 0 confirmed and 197 unresolved fields;
- the first continuation asks the same field again; the second asks a different field without having stored either
  answer; interaction `00431e98` was subsequently cancelled;
- the observed model responses use `gpt-6.1-sol`; changing model intelligence alone does not repair this ingress
  contract;
- no workspace PDF skill override is present in the main workspace.

Evidence: full, untruncated diagnostic traces `trace-turn-e8c05b17c68dd41cc1e756d9`,
`trace-turn-1638d0c16c2334374224bce5`, and `trace-turn-10fc83afcc89f0f52bcf16c2`, correlated with the interaction
registry. No answer value is reproduced here. The incident proves that these answers were not accepted; it does not
prove that a successfully accepted receipt was lost or repeated.

The current code deliberately distinguishes a verified reply or `/answer` from standalone guidance. It also exposes
single-field collection and treats every unfilled writable field as unresolved. PDFI4 must replace the unsuitable
operator contract for its admitted intake mode, not merely add a prompt telling the model to ask fewer questions.

## Operator Contract

1. Inspect the exact attachment and read bounded document evidence. Explain its identity, purpose, relevant parties,
   and supported filling strategy. If the user has already requested filling and identified their role, do not ask
   those questions again. Otherwise ask only the intent or role question that materially changes the task.
2. Present a concise, meaningful list of needed information grouped by person and subject, not raw schema order.
   Distinguish information already present, applicable missing information, ambiguous branches, and fields the
   operator may deliberately leave for later. A full inventory must remain browsable outside model context.
3. Explain once that the active form intake accepts ordinary-looking text securely. Accept multiple facts in a
   natural paragraph, not only labelled lines, JSON, a prescribed delimiter, or one structured answer per section.
   Additional messages augment the same job. The user need not know the current eight-field window.
4. Associate each fact with the intended person, section, and exact field. Preserve facts beyond the current window
   through the bounded protected intake rather than discarding them. Ask only for missing, ambiguous, conflicting,
   or invalid facts, and never silently copy a value to another person or field.
5. Confirm progress concisely without echoing personal values into ordinary history. Explain whether an answer was
   accepted, held for clarification, or rejected. Successful acceptance must not lead to another request for that
   fact unless the operator asks to correct it or a specific validation conflict is explained.
6. Support clarification, correction, back, pause/resume, and cancel in understandable language and with existing
   channel controls. A message mixing facts and a clarification must not be written wholesale into one PDF field.
7. On an explicit partial-finish request for an ordinary AcroForm, leave deferred fields unchanged and produce a
   clearly identified editable draft. For a hybrid backend that only supports flattened output, explain the limitation
   and preserve the job instead of fulfilling the request with a non-editable copy. Do not manufacture blank/
   not-applicable answers or claim the form is complete or submission-ready.
8. Before either complete or partial output, show the appropriate review and wait for a separate finish request and
   existing commit confirmation. Verify the artifact, preserve the source, and deliver exactly one logical PDF.
   Signatures, certifications, and external submission are never inferred or performed by this milestone.

## Architecture And Privacy

Reuse the existing form job, protected interaction sink, encrypted append-only ledger, schema-bound mapping,
deliberative audit, approval, PDF2 writer, verification, and outbox. Do not add a daemon, broker, generic secret store,
parallel form planner, or second document transaction.

- The agent and bundled PDF skill own interpretation, roles, applicability, semantic field selection, question order,
  and partial-completion intent. Native code owns identity, validation, revisions, persistence, and transaction gates.
- Extend the document-owned protected binding/receipt with a versioned composite intake. It binds the owner, route,
  job, immutable source/schema, selected candidates, and revision. Existing single-field bindings and persisted
  receipts remain readable; incompatible migrations must explicitly fail closed.
- While that intake is active, a same-owner message in the exact admitted route must reach protected ingestion before
  ordinary continuation/history/diagnostic content handling. Do not depend on Telegram reply metadata for every
  answer. Ambiguous target jobs, other senders/topics, stale prompts, and replayed callbacks fail closed.
- Do not globally turn every standalone chat message into a field answer, or change approval semantics. Uncertain
  input remains protected while its intent is clarified; it must not silently supersede the intake and leak into
  ordinary model history. Clarification and cancel remain distinct from supplied facts.
- Free-text extraction uses a bounded, ephemeral, tools-disabled semantic call within the existing document/agent
  integration, analogous to the current protected audit call. Only this scoped call can see the protected utterance
  and necessary bounded evidence. Its request and value-bearing response cannot become normal history, summaries,
  logs, trace previews, or public tool arguments/results. No provider-side retention guarantee is implied.
- The model proposes, never authorizes, assignments. Native validation rejects unknown IDs, wrong owners or revisions,
  ambiguous person bindings, type/choice violations, and unsupported transformations. Extract only supplied facts;
  do not invent values, normalize meaning speculatively, or infer unrelated legal/financial answers.
- Protected utterances and unresolved candidates stay in the existing job's encrypted storage and retention domain,
  with opaque receipts and value-free public progress. They are removed by the existing terminal cleanup policy.
  A new generic protected-input substrate remains PDFI5, not part of this milestone.
- Define the accepted subset before mutation and commit its field events atomically and idempotently. Invalid or
  ambiguous candidates have deterministic, value-free findings; never commit a prefix and then report the whole
  answer as failed. Unaccepted facts remain protected and explicitly accounted for, not falsely acknowledged or lost.
- A correction, scope change, or new accepted fact invalidates stale review and approval. New messages and concurrent
  retries cannot overwrite an accepted fact without an explicit correction decision.

Partial completion is a distinct transaction intent bound into review, approval, commit, verification, and recovery.
Deferred, not-applicable, intentionally blank, preserved, confirmed, and unresolved fields must remain distinguishable.
Partial mode does not weaken full-completion validation, remove source-required constraints, or conceal missing data.
If a backend can only create a flattened copy, do not promise that its remaining fields are editable.

## PR Packets And Dependencies

0. **Admission:** merge this contract and synchronize the roadmaps. No runtime or completion claim.
1. **Protected composite substrate:** extend existing binding, receipt, and ledger support with bounded composites,
   atomic acceptance, deterministic findings, idempotency, recovery, and backwards-compatible single-field fallback.
   Freeze the concrete ingestion/extraction limits and test owner/route/schema/revision isolation. A transport-only
   implementation is not yet proof of natural-language intake.
2. **Semantic intake and channel dialogue:** implement the protected ephemeral extraction boundary, same-route
   standalone ingress, multi-fact and successive-message handling, safe pending facts, bounded schema/evidence
   browsing, value-free progress, and the bundled skill's grouped dialogue. Include the actual incident regression,
   accepted-receipt no-repeat tests, mixed guidance/facts, wrong-person ambiguity, and Telegram transport coverage.
3. **Scoped partial completion:** carry explicit complete-versus-draft intent and deferred scope through the existing
   review/approval/write/verify/delivery transaction. Test that partial drafts retain editable unassigned fields and
   complete mode keeps its existing validation gates. Corrections and scope changes invalidate old authority.
4. **Qualification and exit:** qualify merged code through a real agent, the deployed runtime, and Telegram ingress;
   record exact revisions, artifact oracles, privacy scans, recovery and rollback evidence, and manual test steps.
   Merge the exit report and mark PDFI4 complete only after all gates below pass.

Each code packet has independently testable contracts and may be split further at an architecture checkpoint, without
dropping any acceptance criterion. Prefer merging a prerequisite before its dependent packet. When external CI or
review waiting prevents useful progress, the autonomous PR flow may prepare a small, explicitly stacked dependent PR.
Record its dependency, keep incomplete user-facing behavior disabled, and retarget/revalidate after the prerequisite
merges. Never enable or deploy the dependent behavior before every prerequisite is merged. This execution-order
flexibility does not waive any acceptance, privacy, authority, review, or deployment gate. Use `mintclaw-autonomous-pr`:
code needs green exact-head required CI, actual review, no actionable unresolved threads, and authorized rocket
approval; docs-only admission and exit follow the docs-only exception.

## Fixed Bounds And Stop Gates

Keep the existing eight-field planning window, explicit page/offset browsing, at most three selected planning pages,
two-page/4,000-character text evidence, one-page/1,024-pixel visual evidence, and bounded value-free summary/plan.
Do not load a large form's complete schema or the full conversation into one extraction request.

Reuse the interaction answer bound, presently 16 KiB, and the existing job lifetime, capacity, field-value, and event
limits. Packet 1 must define tested caps for composite assignments, extraction passes, and retained pending input
before Packet 2 starts. Large answers or exhausted budgets produce a truthful split/clarification request while
preserving the job; they never silently truncate facts or start an unbounded model loop. A coherent section can span
several eight-field views; neither those internal windows nor section labels may become required user syntax.

Preserve the current portable AcroForm baseline: Linux, macOS, and Windows must compile and run focused platform-neutral
intake tests. Deployed channel qualification is initially Linux. New backend or platform capabilities are not admitted.
XFA expansion, OCR, encryption, transformations, tables, new image/PDF providers, companion placement, and PDFI5 are
excluded. Unsupported document classes remain typed refusals, not alternate shell/browser paths.

Use the autonomous PR architecture checkpoint if review-driven growth stops converging. A new privacy or authority
boundary requires an explicit checkpoint before implementation, not a silent scope expansion.

## Required Acceptance Evidence

| Criterion | Required proof |
| --- | --- |
| Understand before intake | Meaningful source-grounded explanation, role/applicability clarification only when unknown, and grouped needs before requesting values |
| Actual free text | At least five facts in one unlabelled natural message map to distinct intended fields without `/answer`, JSON, or field IDs |
| Incremental input | At least three successive messages augment one job, including facts outside the first schema window, with no silently dropped facts |
| Incident regression | Standalone replies follow protected intake rather than `superseded` guidance; accepted receipts are not re-asked; source/job identity stays unchanged |
| Ambiguity and correction | Same-named roles, mixed facts/guidance, invalid values, contradictions, and natural corrections ask only targeted questions and preserve other accepted facts |
| Partial output | Operator defers a section and receives one verified editable draft; deferred fields remain unchanged, missing required information stays visible, no false completeness claim |
| Full output | Applicable facts reach review, separate confirmation, independent field/visual verification, and one PDF delivery without an external submission |
| Recovery and controls | Clarify/back/cancel, pause/resume, actual restart and compaction, duplicates, stale receipts, and lost delivery acknowledgement preserve identity and no replay |
| Privacy and authority | Protected sentinels absent from ordinary history/summaries/public projections/logs/traces; private extraction is bounded and tools-disabled; cross-owner/route/revision use fails |
| Generic behavior | Small, medium, large synthetic forms and an official/licensed large form pass without production agency names, field-name rules, or fixture-specific dialogue |
| Usable channel | Real Telegram ingress handles both reply and standalone text, several messages, typed controls, and cancellation; an agent-only mock is insufficient |

The large public fixture must include the motivating I-134 with synthetic people, addresses, and finances, subject to
its actual supported PDF classification and the operator-approved output scope above. Qualify its complete applicable
transaction; editable hybrid output is explicitly deferred. Also qualify an unrelated ordinary AcroForm with different
roles/labels in both complete and editable partial modes; fixture-specific
field oracles are permitted in tests, not in production. Record fixture provenance, edition/hash, applicable sections,
and backend classification. If the official fixture is unsupported, report the hard gate and revise admission with the
operator instead of substituting a synthetic look-alike and claiming that scenario passed.

Qualification must exercise both a complete applicable transaction and a user-requested editable partial draft on the
ordinary AcroForm lane, not merely generate a summary or ask the first question. Use no real personal data. Include
independently inspected output values, visible page verification, unchanged source hash, exactly one logical delivery,
protected-store terminal cleanup,
and bounded failure/retry tests. Distinguish actual Telegram transport evidence from headless `/answer` qualification.

## Definition Of Done

PDFI4 is done only when all mandatory code packets are merged, their exact-head CI/review/approval gates pass, merged
code is deployed with a recoverable backup, every acceptance row has executable or redacted live evidence, and the exit
report and operator test guide are merged. The exit must identify the deployed SHA, fixture hashes and artifact
oracles, exercised channels/platforms, recovery/privacy results, rollback procedure, and remaining unsupported lanes.

A goal file, admission PR, updated prompt, mock-only test, open PR, summary-only run, or first protected question is
not completion. Do not mark the milestone complete while required code, deployment, qualification, or exit evidence
is outstanding.
