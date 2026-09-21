# Fill and verify PDF forms

MintClaw can discover, fill, and verify ordinary AcroForms and a strictly admitted fixed-page hybrid
AcroForm/XFA subset on `linux/amd64`. The workflow always
keeps the source PDF unchanged, accepts an explicit typed field map, verifies structure and visible
appearances before publication, and refuses to overwrite an existing output.

Password-required, content-signed, certified, permission-blocked, dynamic or ambiguous XFA, malformed,
unsupported-field, missing-font, clipped, and stale-appearance inputs fail closed. Admitted hybrid output is
flattened and print-ready; it is not an editable XFA form.

The same service is available to an agent through the single deferred `document` tool. An attached PDF and an exact
local PDF path named in the current message follow the same immutable-snapshot contract; a local path is accepted only
by `inspect`, which returns the `media://` ref used by every later action.

## 1. Discover stable field IDs

```sh
mintclaw document fields --input form.pdf --json > fields.json
```

Select fields by the returned opaque `field_id`, not by guessing a display name. The report includes
the field kind, allowed export values, flags, and widget pages.

### Hybrid AcroForm/XFA print-ready output

On Linux AMD64, `inspect` reports bounded action, decoded operation-permission, content-signature,
usage-rights-signature, and hybrid-form facts without executing JavaScript, FormCalc, submit, launch, or navigation
actions. `fields` may return `form_eligibility.mode: hybrid_print_ready` when the PDF has a parseable XFA packet,
fixed pages controlled by an ordinary AcroForm, allowed print/form-fill permissions, and no content signature,
certification, MDP restriction, ReaderExtensions, or calculation order.

For this subset, `fill` regenerates complete text/choice appearances, verifies requested values and untouched field
semantics, removes non-executed XFA/scripts/actions and invalidated usage rights, flattens widgets into page content,
preserves the admitted encryption/permission envelope, and renders all pages with pinned Poppler and independent
Ghostscript. The derivative reports `mode: flattened_print_ready` with AcroForm, XFA, signatures, usage rights, and
actions absent. Dynamic, malformed, password-protected, content-signed, permission-blocked, or ambiguous hybrid forms
remain blocked with typed facts and blockers.

To qualify a pinned local hybrid PDF with a private typed fill map:

```sh
scripts/document-hybrid-qualification.sh \
  --input /absolute/path/to/form.pdf \
  --fields /private/path/to/fill-map.json \
  --output /private/path/to/filled.pdf \
  --expected-sha256 <lowercase-sha256> \
  --expected-pages <count> \
  --expected-fields <count> \
  --evidence-dir /new/or/empty/evidence-directory
```

The gate runs `inspect → fields → fill → verify → output inspect`, cross-checks both PDFs with Poppler, proves the
source digest is unchanged, and requires one verified flattened artifact. Reports remain value-free, but the supplied
map and output contain submitted values; keep them and the evidence directory private.

To qualify the same mapping through the deployed agent rather than trusting its reply text, put the semantic
instructions in a private `case-prompt.txt` without the PDF path, and put each distinctive sensitive literal on its
own line in `private-values.txt`. Then run:

```sh
MINTCLAW_BINARY=/path/to/deployed/mintclaw \
scripts/document-hybrid-live-qualification.sh \
  --input /absolute/path/to/form.pdf \
  --fields /private/path/to/fill-map.json \
  --case-prompt /private/path/to/case-prompt.txt \
  --private-values /private/path/to/private-values.txt \
  --output /private/path/to/live-filled.pdf \
  --config /path/to/deployed/config.json \
  --expected-sha256 <lowercase-sha256> \
  --expected-pages <count> \
  --expected-fields <count> \
  --expected-assigned-fields <count> \
  --evidence-dir /new/or/empty/live-evidence-directory
```

This runs one isolated `agent live --json` turn. It requires exactly one initial `tool_search_tool_bm25` call that
discovers and unlocks `document`, followed by `inspect → fields → fill → verify`, and rejects any
other model-visible tool or duplicate write, correlates the passive trace by the hashed session key, checks the
write journal and exactly one single-attempt media delivery, and scans persisted evidence for the input path and the
listed private literals. It independently inspects the delivered PDF and requires every Poppler-rendered page to be
byte-identical to the deterministic CLI baseline made from the private map. The destination PDF is copied only after
all checks pass; success ends with `MINTCLAW_PDF4H4_LIVE_QUALIFICATION_OK`.

## 2. Create a typed fill map

Keep this file private because it contains the values being entered:

```json
{
  "schema_version": "mintclaw.document_fill_map.v1",
  "assignments": [
    {
      "field_id": "field_<64 hex characters from fields.json>",
      "value": {"type": "text", "text": "Example value"}
    }
  ]
}
```

Value types are `text`, `boolean`, `choice`, and `choices`. Choices must use the exact advertised
export value. One logical repeated field has one ID and updates all of its widgets.

## 3. Fill and retain the value-free report

The destination must not already exist:

```sh
mintclaw document fill \
  --input form.pdf \
  --fields fill-map.json \
  --output filled.pdf \
  --json > fill-report.json
```

`fill-report.json` contains digests, backend identities, assertion counts, the operation ID, and an
opaque artifact identity. It does not contain submitted values or host paths. The durable private
operation state is stored below the MintClaw state directory and is required for later verification
or exact retry.

## 4. Verify the published copy

```sh
mintclaw document verify \
  --input filled.pdf \
  --expect fill-report.json \
  --json
```

Verification requires all three identities to agree: the caller-visible PDF, the successful
value-free report, and MintClaw's private durable journal plus candidate generation. A copied report
without its owner-scoped durable state cannot bless a PDF.

## Exact retry

If output publication or response delivery failed, reuse the operation ID from `fill-report.json`
with the same source and fill map. Choose a new output path:

```sh
mintclaw document fill \
  --input form.pdf \
  --fields fill-map.json \
  --operation-id document_write_<operation-id> \
  --output filled-retry.pdf \
  --json > retry-report.json
```

The retry returns the same committed generation. A conflicting source or field map is rejected, and
a partial or corrupt generation becomes `uncertain`; MintClaw does not silently start another write.

## Agent workflow and delivery

For an attachment or an authorized current-message local path, the PDF skill directs the agent through:

1. `inspect` and then `fields` to obtain exact opaque field IDs;
2. one typed `fill` call after any genuinely ambiguous mapping is clarified;
3. mandatory structural and visible verification inside the document service;
4. idempotent registration in the existing MediaStore; and
5. one recoverable outbox delivery of `filled-document.pdf`.

The agent does not call `send_file` for this result. The document operation journal records `registered`,
`delivery_pending`, and the terminal delivery outcome against one logical delivery identity. A delivered, pending, or
ambiguous operation is never blindly sent again. `verify` accepts the delivered `media://` ref and the exact
`operation_id` for diagnosis without putting the original field values back into model context.

The durable PDF3 conversational workflow collects one protected answer at a time, accepts Telegram buttons or natural
free text, supports correction/cancel/status/resume, audits a bounded mapping, and places final mutation behind the
configured approval policy. It is qualification-only until the PDF3 exit record is merged. Operators should use the
[PDF3 conversational form test](../operations/pdf3-conversational-form-test.md), which includes the synthetic fixture,
exact deployed path, automated gate, Telegram steps, restart, no-replay, privacy, and cleanup checks.

Submitted values are protected tool-call input. Ordinary history, task deliverables, traces, logs, and document
journals keep only field IDs, assignment count/hash, source/request/output digests, assertion counts, operation ID,
delivery identity, and opaque artifact ref.

## Repeatable smoke test

On a Linux AMD64 checkout with pinned Poppler installed:

```sh
make test-document-form-cli
```

The command builds MintClaw in a temporary directory and tests field discovery, fill, durable
verification, exact retry, source immutability, value/path-safe reports, scratch cleanup, and
no-overwrite publication. Success ends with `MINTCLAW_PDF2_FORM_CLI_OK`.

To run the real agent-tool and durable Telegram-channel harness:

```sh
make test-document-form-agent
```

It exercises attachment and authorized-local-path field discovery, verified fill, MediaStore registration,
exactly-once delivery, definite rejection, ambiguous acceptance without blind replay, safe retry, the explicit agent
`verify` action, and history/trace/journal redaction. Success ends with
`MINTCLAW_PDF2_FORM_AGENT_OK`; the same run emits the PDF3 protected-store, interaction, restart/compaction,
mapping/review, approval, PDF2-commit, source-unchanged, single-delivery, privacy, cleanup, and agent markers.
