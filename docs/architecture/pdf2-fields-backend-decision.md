# PDF2 Field Discovery Backend Decision

## Decision

PDF2 field discovery uses the already pinned Go module `pdfcpu` `v0.15.0` as its sole production
backend on `linux/amd64`. The backend runs only inside the existing one-shot document worker after
PDF0A immutable acquisition and PDF0B structural inspection. It is not a second parser process, a
daemon, or a model-authored shell pipeline.

`pypdf` `6.1.1` is the independent test oracle. It is BSD-3-Clause licensed, is installed only in an
ephemeral qualification environment, and is never a production dependency or fallback. Production
continues to ship the Apache-2.0 `pdfcpu` module already present in MintClaw's Go dependency graph.

The `fields` operation is unavailable on other operating systems and architectures. macOS support is
retained as a separate roadmap lane rather than advertised without executable evidence.

## Normalized contract

The backend returns a versioned, bounded report containing:

- an opaque source-bound `field_id`, fully qualified name, alternate name, normalized kind, flags,
  default/value presence, and kind-specific constraints;
- export/display pairs for choices, without guessing from labels;
- one opaque widget identity and one-based page number for every page annotation, including repeated
  widgets and multiple radio widgets on one page; and
- fixed field, widget, option, per-string, and aggregate report-byte limits published by
  `document capabilities`.

Text values already stored in the PDF are not returned. The report exposes only `has_default` and
`has_value`. Field and widget identifiers are SHA-256-derived capabilities bound to the immutable
source digest and normalized object identity; raw PDF object numbers are not public identifiers.

The admitted kinds are ordinary text, static-format date text, checkbox, radio, combo, and list
fields. Read-only and required flags are reported for later policy enforcement. Signature and
pushbutton fields, ambiguous identities, unsupported actions or formats, widgets without a page,
empty fixed choices, XFA, encrypted forms, signed or restricted documents, malformed structures,
and over-limit forms fail closed with typed errors. Discovery never executes PDF JavaScript.

## Qualification evidence

The deterministic `acroform-fields.pdf` fixture covers eight logical fields and ten widgets:
ordinary and multiline text, required/max-length/default-value metadata, checkbox, radio, editable
combo, multi-select list, static date format, export/display choice pairs, and one logical field
repeated on pages 1 and 2. The refusal set covers no form, hybrid XFA, signed content, a signature-only
field, and password-required encryption. Every fixture is synthetic and recorded with its SHA-256,
provenance, license, expected result, and evidence test in
`pkg/document/testdata/form-fields-manifest.json`.

The Linux backend tests compare every fixture to that manifest. A separate oracle script compares
the successful production JSON with `pypdf.PdfReader.get_fields()`, checks normalized kinds and
choice values, proves opaque identities and repeated widget pages, and runs every refusal fixture.
Qualification on the target Ubuntu host produced:

```text
production=pdfcpu version=v0.15.0
oracle=pypdf version=6.1.1
marker=MINTCLAW_PDF2_FIELDS_ORACLE_OK
```

For the comprehensive fixture, a cold one-shot CLI run on the target host completed in 0.19 seconds,
used 63,492 KiB maximum RSS, emitted a 7,670-byte bounded JSON report, and returned eight fields.
The worker inherits the existing 5-second deadline, descriptor-only immutable input, scrubbed
environment, process-group cancellation, parent-death handling, input/content/object/recursion
limits, bounded response, and safe backend-error normalization. No output artifact is produced by
this read-only operation.

Reproduce the independent oracle on `linux/amd64` with an isolated Python environment:

```sh
uv venv /tmp/mintclaw-pdf2-oracle
uv pip install --python /tmp/mintclaw-pdf2-oracle/bin/python pypdf==6.1.1
PDF2_PYTHON=/tmp/mintclaw-pdf2-oracle/bin/python make test-document-fields-oracle
```

The script rejects any other pypdf version. Installing the oracle is an explicit qualification step,
not part of normal document processing.

## Update and rollback

An update to pdfcpu, its normalized field mapping, or the oracle version requires one focused change
that updates this record, fixture digests, manifests, backend tests, oracle proof, dependency notices,
and deployed evidence together. The new version stays unavailable until the full matrix passes.

Rollback restores the preceding MintClaw binary and module lock, verifies `document capabilities`,
and reruns the fields oracle. If the exact qualified implementation cannot be restored, `fields`
must report unavailable while PDF0A, PDF0B, and PDF1A remain independently usable.
