# Manual PDF Eval Fixtures And Oracle

## Current Status

The repository contains six PDF scenarios and four deterministic synthetic AcroForms, with exact SHA-256 checked
by offline corpus validation. The dialogue packet lets `eval run` attempt PDF conversations on a narrowly admitted
isolated profile, using protected facts and owned fixtures. Writes, native review/confirmation and exact approval,
authenticated artifact collection, operation/outbox settlement, and complete private-surface evidence remain pending.
Required PDF checks therefore remain inconclusive; a first question, caption, or oracle control is not qualification.

The cases cover loan filling, correction/Back, own-request conditional intent, empty required values, cancel/no-write,
and a bounded large-form section. Four use an adaptive user. The large case requests two Safety findings in an editable
partial draft, not completion of all 100 fields. Negative cases never grant approval.

`pdf.fields` is hidden oracle data covering every field with an expected value, private fact handle, or preserve
predicate. Models receive natural tasks/personas and handles, not golden field mappings. The public
`MC_EVAL_GENERATE_PRIVATE_V1` marker is reserved for fresh per-trial private generation by the integration packet;
it is not an answer and must never be sent literally. `{{fixture}}` is one runner-owned source substitution, not a DSL.

## Native Evidence Preparation

The integration foundation prepares a new private fixture directory per trial, rechecks pinned source bytes, and
resolves public generation markers to fresh ephemeral values. It never resumes an existing directory. Local release
retains a potentially active remote source; terminal cleanup removes only the owned source and empty directory,
not unexpected files recursively. Neither rule certifies native job/resource cleanup on its own.

Protected input requires a fresh native waiting record matching the full interaction ID, short ID, agent, session,
route/sender and expiry. The native `PromptDeliveryID` is an **outbox ID**, not the platform message ID seen on the
wire. The observer reads the exact delivered outbox intent and binds its `PlatformMessageIDs` to the observed prompt.
Pending/ambiguous delivery, stale controls and mismatched routes cannot admit private input.

Opaque protected-answer tokens are read only for a local job locator, never decrypted or treated as authority.
The native form store authenticates the public job and owner; the fixture source and job digest must match.
Explicit `/answer` ingress remains responsible for sealed binding validation and consuming stale/wrong-route
input before ordinary history. Private values appear only in an ephemeral request, not a stored event or role context.
Raw admitted registry, outbox and form-store bytes are scanned before projection; unavailable or unscannable evidence
does not establish safety. These helpers do not grant approval or settle delivery.

## Dialogue Admission

The prepared profile must declare `pdf` and `interactions`, enable the document tool with an enabled native audit
model, and allow only `document` and `request_user_input` under a deny-default tool policy. Exec, MCP, browser,
hooks, trace capture, external channels, reload/deploy, broad path permissions, and approval bypass remain refused.
Undeclared document access and unadmitted audit fallbacks are refused before sending a task.

The common conversation runner owns streaming, request correlation and follow-ups. The PDF adapter substitutes
the owned source path, admits native public or protected question prompts, and binds all protected answers to one
source-pinned, authenticated job and owner. Raw WebSocket frames, including discarded heartbeats/auxiliary frames,
are inspected before map decoding; duplicate keys cannot hide an earlier private value. A confirmed private value
is a hard failure. Truncated, opaque escaped or otherwise unscannable evidence is unknown, not proof of a leak.
Only a non-secret protected-input marker enters retained transcript evidence; the actual value travels in the
ephemeral native `/answer` request. Ordinary scripted slash commands cannot bypass this path.

No approval is granted by this packet. A public native job state is retained as a value-free observation, not as a
claim of artifact correctness, no-write safety, complete privacy coverage or remote cleanup. All attempted PDF
fixtures are conservatively retained while native settlement is unknown. PDF evidence is not yet eligible for
judge-only regrading. Later write/delivery witnesses must replace these explicit unknowns before E3 exits.

## Optional Dependencies

The oracle uses [pypdf](https://pypdf.readthedocs.io/en/stable/user/forms.html) for structure/value readback and
[PyMuPDF](https://pymupdf.readthedocs.io/en/latest/widget.html) for widget text and raster observations, independently
of production pdfcpu/PDFium. Direct and transitive versions are pinned in
[pdf-requirements.txt](../../evals/oracles/pdf-requirements.txt). Reportlab generates fixtures and negative controls.
None of these packages is linked into the Go binary or installed into the production gateway by this packet.

PyMuPDF has an [AGPL or commercial license](https://pypi.org/project/pymupdf/). Operators must admit that dependency
for their manual eval environment; this is not a claim that a subprocess boundary removes license obligations.
The environment is optional for offline catalog commands, general conversations, and ordinary CI.

```sh
uv venv /absolute/private-eval-oracle --python 3.12
uv pip install --python /absolute/private-eval-oracle/bin/python -r evals/oracles/pdf-requirements.txt
/absolute/private-eval-oracle/bin/python -B evals/oracles/test_pdf_oracle.py
MINTCLAW_EVAL_PDF_ORACLE_PYTHON=/absolute/private-eval-oracle/bin/python \
  go test -tags goolm,stdjson ./internal/eval -run '^TestPDFOracleManualQualification$' -count=1
```

The Go adapter runs the embedded script in isolated Python mode with a minimal environment. One bounded JSON
metadata line and exact-length source/artifact bytes travel over stdin, not argv, temporary files, or model context.
Bounds: 64 MiB per PDF, 32 pages, 256 unique flat text/checkbox fields, 16 million rendered page pixels, 8 KiB returned
output, one minute of child execution. The child starts no further subprocesses. This does not certify hostile-PDF
sandboxing, prove target isolation, download artifacts, or grant approval.

Errors are value-free; parser diagnostics are discarded. Results contain code/version fingerprints, source/artifact
digests, predicates and actual counts only. Missing dependencies, malformed/unsupported inputs, exhausted bounds
or process failure cannot pass. Complete observed mismatches remain explicit negative evidence.

## Coverage And Limits

- Exact field values, preserved/blank values, and checkbox state semantics.
- Editable field names/types/flags, original widget geometry, page count, and absence of XFA.
- Text extracted within widget bounds must match the expectation. The observed raster differs from an independently
  regenerated whitespace-only/unchecked appearance exactly when nonblank data is expected.
- Page rasters preserve background outside masked widget rectangles. This conservative exact comparison is for
  these synthetic fixtures, not a universal font/layout equivalence metric.
- Controls: correct independent fill, unfilled fake success, old corrected value, stale/invisible appearance,
  altered page background, wrong schema, malformed private input, and serialized empty required values.

This is not full OCR, proof of glyph shapes in arbitrary fonts, repeated/hierarchical widget or hybrid/XFA support,
native audit/approval proof, or logical delivery proof. E3 must combine the oracle with native source/job/operation/
outbox identity and the authenticated observed artifact, not merely a caption or session presence. Raw non-artifact
surfaces require their own bounded private-value scan before judging.

## Regenerate

```sh
/absolute/private-eval-oracle/bin/python -B evals/oracles/generate-pdf-fixtures.py
mintclaw eval validate --root ./evals --suite pdf --json
```

Generation reuses PDFI3 small/conditional/large layouts and adds an empty-required variant. It is not run during
ordinary evals or required CI. Intentional fixture changes require visual review, case digest updates, and renewed
oracle controls; changed fixture bytes are never silently admitted.
