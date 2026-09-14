# Fill and verify PDF forms

MintClaw can discover, fill, and verify ordinary AcroForms on `linux/amd64`. The workflow always
keeps the source PDF unchanged, accepts an explicit typed field map, verifies structure and visible
appearances before publication, and refuses to overwrite an existing output.

Encrypted, signed, certified, restricted, XFA, malformed, unsupported-field, missing-font, clipped,
and stale-appearance inputs fail closed. Form flattening is not yet available.

The same service is available to an agent through the single deferred `document` tool. An attached PDF and an exact
local PDF path named in the current message follow the same immutable-snapshot contract; a local path is accepted only
by `inspect`, which returns the `media://` ref used by every later action.

## 1. Discover stable field IDs

```sh
mintclaw document fields --input form.pdf --json > fields.json
```

Select fields by the returned opaque `field_id`, not by guessing a display name. The report includes
the field kind, allowed export values, flags, and widget pages.

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
`MINTCLAW_PDF2_FORM_AGENT_OK`.
