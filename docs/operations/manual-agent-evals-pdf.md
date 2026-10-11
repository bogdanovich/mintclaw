# Manual PDF Eval Fixtures And Oracle

## Current Status

The repository contains six PDF scenarios and four deterministic synthetic AcroForms, with exact SHA-256 checked
by offline corpus validation. Domain execution remains unavailable until E3 admits document tools, protected facts,
native review/confirmation and exact approval, authenticated media, operation/outbox delivery, and private-surface
evidence. Listing a case or passing oracle controls does not qualify a real agent task.

The cases cover loan filling, correction/Back, own-request conditional intent, empty required values, cancel/no-write,
and a bounded large-form section. Four use an adaptive user. The large case requests two Safety findings in an editable
partial draft, not completion of all 100 fields. Negative cases never grant approval.

`pdf.fields` is hidden oracle data covering every field with an expected value, private fact handle, or preserve
predicate. Models receive natural tasks/personas and handles, not golden field mappings. The public
`MC_EVAL_GENERATE_PRIVATE_V1` marker is reserved for fresh per-trial private generation by the integration packet;
it is not an answer and must never be sent literally. `{{fixture}}` is one runner-owned source substitution, not a DSL.

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
