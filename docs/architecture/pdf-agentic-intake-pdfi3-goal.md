# PDFI3 Semantic Form Planning And Natural Dialogue Goal

## Status

Admitted on 2026-10-03. Implementation and qualification are in progress; this document does not claim an exit.
The predecessor is the completed [PDFI2 packet](pdf-agentic-intake-pdfi2-exit.md). The active program is the
[agentic intake roadmap](pdf-agentic-intake-roadmap.md).

## Operator outcome

From an ordinary form-completion request, the agent understands the exact form before collecting values, explains a
short collection plan, asks coherent questions about applicable facts, and handles clarification and corrections
without asking the operator for technical identifiers. Missing evidence causes a focused clarification, not a guessed
assignment. A short form and a large synthetic form both start with a meaningful summary instead of raw field order.

## Current gaps

The admission baseline is `176c2c04bc45835ca0f92f2e02867138ce7889ea`.

- Discovery presents eight unresolved-first candidates but offers no way to browse a later page or another window.
- Its truncation is implicit. An agent cannot reliably distinguish a complete view from a sample.
- Mapping counts combine preserved, newly confirmed, and intentionally blank fields, obscuring value-free progress.
- Discovery immediately requires preparation, and preparation requires a protected question. Semantic reading and
  ordinary intent clarification therefore need an explicit place before that transition.
- Localized checkbox labels and protected receipt consumption already exist. A model-authored checkbox question can
  still omit its labels and fall back to generic Yes/No, recreating a question/control mismatch.

The earlier I-134 channel incident is a motivating symptom, not a form-specific implementation requirement. Existing
synthetic evidence is not proof that the original Telegram exchange is fixed.

## Ownership And Design Constraints

| Concern | Owner |
| --- | --- |
| Purpose, parties, sections, applicability, question order, and ordinary intent clarification | Agent and bundled PDF skill |
| Immutable source, complete schema, stable field identities, and value-free progress | Existing document service and form job |
| Protected answers, typed navigation, encrypted values, and deterministic normalization | Existing interaction sink and ledger |
| Correction invalidation, audit, approval, write, verification, recovery, and delivery | Existing PDF3/PDF2 transaction |

Use bounded views of the existing schema and job, not a second planner or persistent plan registry. The agent's
value-free collection plan belongs in conversation context; semantic labels and conditional branches are proposals,
never authority. Native code may filter a field view by explicit page selection and paginate it; it must not interpret
field names, decide which section applies, or automatically choose a question. Do not copy values between fields or
people. Protected answers may only be consumed through their existing job-bound receipts.

Ordinary input is for goals, explanations, and ambiguity resolution, not a bypass for personal form values. Those
values remain protected. Source text and page images use the existing current-call read/render privacy contract;
they must not become persistent semantic-plan text or acquire write authority.

## PR Packets

1. **Bounded schema and progress views:** extend the existing form discovery/status projection with explicit window
   completeness, bounded page/field browsing, and value-free counts and statuses for preserved, confirmed, missing,
   conflicting, and intentionally blank fields. Keep default behavior compatible and preserve source/schema binding.
   Test deterministic browsing, budgets, owner isolation, and restart-safe state.
2. **Semantic dialogue:** teach the bundled skill to read bounded document evidence and clarify material intent before
   entering collection, then choose questions by evidence and applicability. Allow explicit browsing transitions where
   the tool-only contract otherwise prevents them without weakening the protected-question fence. Require matching
   checkbox labels for agent-authored questions. Cover small/large synthetic forms, ambiguous evidence, localized
   controls, continuation, correction, and privacy with focused workflow and agent tests.
3. **Qualification and exit:** qualify the merged behavior through a real agent and deployed smoke, including summary
   before collection, clarification, correction, review, separate confirmation, verified single delivery, immutable
   source, and privacy. Record merged revisions, test and trace evidence, rollback, residual limits, and the explicit
   PDFI4 boundary. Update the roadmap only after the exit evidence exists.

Merge each dependent packet before beginning its successor, using the autonomous PR skill. Code merges require
actual exact-head review, green required CI, no actionable unresolved feedback, and authorized PR-level rocket
approval. Docs-only PRs follow the documented exception.

## Fixed Budgets

- Keep the existing eight-field model-visible window; permit explicit later windows instead of growing it.
- A planning view may select at most three explicit sorted unique pages; no hidden semantic section detection.
- Reuse the existing field label, option, and question bounds. Report omitted fields and label truncation truthfully.
- The skill's initial text evidence selects at most two pages and at most 4,000 characters; visual evidence selects one
  page with a bounded render size. Further reading is another explicit bounded request, never an unbounded inventory.
- Keep the initial value-free summary at 512 characters and collection plan at 768 characters, as in PDFI2. These
  budgets do not authorize copying extracted source text or protected values into the summary.

## Acceptance Matrix

| Invariant | Required evidence |
| --- | --- |
| Evidence before collection | Small and large synthetic forms receive a meaningful short summary and plan before the first value question |
| Browse, not guess | A later page/window is reachable with stable IDs; omitted evidence is explicit; ambiguity triggers clarification |
| Fixed context budget | Field/page/text/label bounds and truthful truncation have deterministic tests |
| Agent ownership | No backend semantic inference, schema-order question selection, or silently copied assignments |
| Safe progress | Preserved, confirmed, missing, conflicting, and optional blank states are distinguishable without values |
| Coherent controls | Agent-authored checkbox choices match the question and bind to the correct boolean; missing labels fail safely |
| Continuation | Free text, clarify/back/cancel, correction, restart, and compaction preserve the same owner-bound job |
| Privacy and authority | Protected sentinels never enter model history, public job projections, logs, or traces; cross-owner use fails |
| Transaction | Correction invalidates stale review/approval; source stays immutable; separate confirmation yields one verified delivery |
| Actual qualification | Real-agent and deployed evidence is redacted and identifies the tested revision; unverified channel cases are explicit |

## Completion And Stop Criteria

Complete only after the mandatory packets and an exit report are merged, required exact-head CI/review/approval gates
pass, and qualification demonstrates the operator outcome. An open PR or deterministic mock alone is not completion.
Apply the autonomous skill's architecture checkpoint if review-driven scope grows or repeated fixes stop converging.

Do not add a daemon, broker, secret store, delivery queue, generic planning framework, new backend, or form-, agency-,
locale-, or field-name-specific production rule. PDFI4 composite answers and comprehensive large-form qualification,
PDFI5 extraction, XFA expansion, OCR, crypto, transformations, provider-native adapters, and new companion/platform
parity are excluded. A necessary authority/privacy redesign requires a checkpoint, not a silent scope expansion.
