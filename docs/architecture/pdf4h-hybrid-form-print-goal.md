# PDF4H Hybrid Form Print-Ready Transformation Goal

## Status

Admitted as a bounded `linux/amd64` implementation goal. PDF4H is a parallel hybrid-form transformation slice; it
does not implement or depend on the pure static-XFA mutation admitted by PDF4B. Until this goal's positive admission
gate and every completion criterion pass, MintClaw continues to refuse modification of every hybrid AcroForm/XFA
document.

The official USCIS I-134 is the live qualification document because it exposed the product gap. The implementation
must remain document-general: no form number, filename, field name, field identifier, agency rule, or synthetic test
value may appear in production admission, mapping, or write logic.

## Operator outcome

Given an admitted hybrid AcroForm/XFA PDF and an explicit field map, an operator receives exactly one of:

1. one new print-ready PDF whose requested values structurally round-trip and visibly render;
2. one typed refusal naming every independently established blocking class; or
3. one existing definite failure, uncertain, or delivery-ambiguous outcome without a claimed usable artifact.

The canonical successful output is a normalized, flattened derivative, not an editable hybrid form. The report must
state that XFA, AcroForm interactivity, and any invalidated usage-rights signature are absent from the derivative. It
must not present the output as preserving certification, signatures, accessibility, or producer-specific behavior.
The source is immutable and remains the only original document.

## Qualification identity

The live qualification input is downloaded by the operator from:

`https://www.uscis.gov/sites/default/files/document/forms/i-134.pdf`

The initially qualified envelope is:

- edition `01/20/25`;
- SHA-256 `e887f9dbb4660ff9eec47d740c00358e45fcfb9c04a9776d8d95cda2e7a5e0d8`;
- 511,302 bytes and 10 pages;
- 250 AcroForm fields;
- XFA present as a packet array with rendering state currently unknown;
- encryption present without a required open password and with restricted permissions;
- one detected signature plus usage rights, without DocMDP or FieldMDP; and
- extractable text on all ten pages.

These facts are qualification evidence, not a permanent form allowlist. The official PDF is not committed. The smoke
harness accepts an exact local input path, verifies its digest before any operation, and refuses an unrecognized
revision until its facts and expected render baseline are reviewed.

## Synthetic acceptance case

The deployed live test requests only these values:

- basis for filing: `Another individual`;
- supporter family and given names: `TESTER`, `ALICE`;
- beneficiary family and given names: `SAMPLE`, `BOB`;
- mailing street: `123 Test Street`;
- city, state, and ZIP code: `Seattle`, `WA`, `98101`;
- country: `United States`; and
- mailing address equals physical address: `Yes`.

SSN, A-Number, signatures, attestations, unknown required fields, and any ambiguous field stay blank. The test values
are not business-valid form data and the result is never submitted.

## Positive admission gate

A hybrid input is admitted only when bounded worker inspection positively proves all of the following before writing:

- an ordinary AcroForm field/widget representation is complete for every requested field and affected page;
- the selected representation has fixed pages and geometry and renders without an XFA engine;
- XFA is not dynamic, foreground-authoritative, script-dependent, externally bound, repeating, page-growing, or the
  only owner of any requested value;
- there is no password requirement, content signature, certification, timestamp, DocMDP, or FieldMDP;
- the only signature-like state eligible for normalization is a positively identified usage-rights signature;
- the decoded permission set positively permits form filling and printing; a generic `restricted` state is not
  sufficient evidence;
- requested fields have supported AcroForm kinds, visible widgets, deterministic export values, and no calculations,
  validation scripts, submit actions, or unsupported dependencies; and
- the output permission policy can preserve or tighten the admitted source permissions without password bypass.

Unknown is refusal. Removing XFA, a signature, encryption, or permissions merely because a writer can open the bytes
is forbidden. A failed permission-preservation or representation-authority proof ends the positive path.

## Transformation and verification contract

The existing document service, worker, operation journal, protected form ledger, artifact transaction, and outbox own
the workflow. PDF4H adds no second service, daemon, broker, database, model-visible tool, browser editor, or desktop
automation path.

The write sequence is:

1. acquire and revalidate the immutable source identity;
2. classify the complete hybrid, security, action, permission, and field envelope in the worker;
3. bind explicit typed values to discovered stable field IDs;
4. create a new private candidate, regenerate appearances, and flatten only the admitted AcroForm widgets;
5. remove only the explicitly admitted redundant XFA and usage-rights state, preserve or tighten permissions, and
   record every normalization in the typed report;
6. reopen the candidate for structural readback and verify requested and untouched field semantics;
7. render every affected page with the production renderer and an independent renderer;
8. compare unchanged regions against the source baseline and prove each requested value is legible inside its widget;
9. atomically register and create one logical delivery intent only after every assertion passes; and
10. deterministically remove private scratch on success, refusal, cancellation, timeout, or crash.

The flattened derivative has ten pages, no live AcroForm fields, no XFA, no signature dictionary, no usage-rights
entry, and no executable PDF action introduced by the transformation. Unchanged page content must stay structurally
present and visually equivalent outside bounded changed regions. Rasterizing complete pages is not an implementation
shortcut.

## Agent-facing contract

The existing deferred `document` tool remains the sole model surface. Its safe `inspect` projection exposes bounded
PDF version, page, encryption, password, permission, signature, DocMDP, FieldMDP, usage-rights, AcroForm, XFA, and
text facts. A refused `fields` call exposes every independently established blocker plus one stable terminal failure;
it does not collapse the envelope to the first generic `form_unsupported` message.

No projection includes host paths, field values, document bytes, XFA XML, raw metadata, backend errors, credentials,
or unbounded producer data. Tool and trace tests prove those values are absent rather than best-effort redacted.

## Pull-request sequence

Use one dedicated autonomous worktree. Start each dependent PR from the latest merged `origin/main`.

1. **PDF4H0 contract:** this goal, roadmap admission, exact live envelope, safety boundary, stages, and completion
   gates. Documentation only.
2. **PDF4H1 observability:** safe inspection projection, complete blocker set, CLI/tool parity, privacy tests, and a
   local-path/live-agent regression that initially records the hybrid refusal without hardcoded presentation prose.
3. **PDF4H2 admission:** decoded permission facts, usage-rights/content-signature distinction, hybrid authority and
   action classifier, redistributable positive/negative fixtures, independent oracle, and a no-write feasibility gate
   against the pinned I-134 revision.
4. **PDF4H3 write core:** normalized fill/appearance/flatten transaction, permission preservation, structural and
   dual-render verification, refusal matrix, crash/cancellation/recovery, privacy, and cleanup.
5. **PDF4H4 live exit:** deterministic CLI and agent smokes, deployed `agent live` proof, trace assertions, one
   artifact, independent visual audit, rollback, and the merged exit record.

A failed PDF4H2 gate ends this goal as an evidence-backed typed-refusal result; PDF4H3 must not begin. Review-driven
growth across unrelated subsystems triggers the autonomous PR architecture checkpoint rather than silent expansion.

## Automated and live acceptance

One checked-in harness accepts `--input`, `--expected-sha256`, and an evidence directory. It runs the production CLI
and worker suites, then sends one bounded request through `mintclaw agent live --json`. The live prompt requires only
document tools and forbids ordinary shell, browser, Python, external PDF libraries, and manual page drawing.

Machine acceptance reads typed reports, operation state, artifact metadata, and a correlated passive trace. It never
decides success by matching conversational prose. Trace assertions require `inspect`, `fields`, the admitted write,
and verification; they reject prohibited tools, duplicate writes, duplicate delivery, missing terminal state, or an
uncorrelated artifact.

Independent acceptance verifies the source digest, output page count and security envelope, expected values, forbidden
blank fields, every changed-page render, unchanged-region tolerance, visible bounds, one delivery identity, privacy,
and scratch cleanup. The final evidence records exact source/runtime commits, fixture and output digests, backend and
oracle versions, operation and trace IDs, commands, health, backup, and rollback.

## Completion criteria

PDF4H is complete only when:

1. the live scenario that currently returns `form_unsupported` produces one verified print-ready artifact using only
   document tools;
2. all requested safe inspection facts and usable general field semantics reach the agent;
3. no production logic recognizes I-134, USCIS, the qualification digest, its field IDs, or its business rules;
4. the source is unchanged and every structural, permission, visual, privacy, recovery, cleanup, trace, and
   single-delivery assertion passes;
5. ordinary AcroForm and every existing pure/hybrid/dynamic-XFA, encryption, signature, restriction, and PDF0-PDF3
   regression remains green;
6. every code PR passes CI, automated review, actionable-thread resolution, and authorized rocket approval;
7. merged main is deployed with matching binaries, healthy services, bounded clean journals, a rollback bundle, and a
   new successful trace; and
8. an exit record and final synthetic PDF are available with one optional manual Telegram checklist.

## Stop gates and non-goals

Stop without write support if representation authority, permission compliance, safe signature/rights normalization,
field binding, appearance generation, unchanged-region preservation, or independent visible verification cannot be
proven. Do not weaken security, bypass permissions, rasterize complete pages, or claim partial output as completion.

Out of scope are form submission, legal or immigration advice, real personal data, signatures and attestations,
editable hybrid-XFA preservation, generic dynamic-XFA support, OCR, arbitrary repair, browser-authoritative editing,
desktop automation, approval bypass, commercial backends, companion placement, and macOS parity. macOS remains
fail-closed until a later parity goal repeats the complete contract on each supported architecture.
