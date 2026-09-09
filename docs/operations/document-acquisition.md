# Document Acquisition And Inspection

## Status

PDF0A acquisition, PDF0B inspection, and the PDF1A local and agent/channel read paths are available
for the qualified `linux/amd64` bundle. The
[PDF0A exit record](../architecture/pdf0a-exit-record.md) contains merged-main deployment and rollback evidence for
the acquisition foundation. The [PDF0B backend decision](../architecture/pdf0b-backend-decision.md) records parser
qualification, normalization, oracle, packaging, and residual limits.
The [PDF1A backend decision](../architecture/pdf1a-backend-decision.md) pins the production
Poppler bundle, independent ClawPDF/PDFium oracle, executable identities, resource evidence, and
rollback contract.

The commands prove bounded local-file acquisition, immutable identity, and deterministic parsing in a short-lived
worker. The shared service also admits an inbound `media://` reference only for its immutable workspace, agent, actor,
route, and session owner. The agent uses the same service through one hidden `document` tool and one
on-demand `pdf` skill. These milestones do not accept passwords or retain a durable document job.

Only `linux/amd64` is admitted. Other platforms return a structured `unsupported_platform` result before opening the
input. This remains intentional until each tuple proves the same worker, packaged backend, and fixture contracts.

## Automated checks

From the repository root:

```sh
make test-document
```

The suite covers regular files, symlinks, FIFO/devices, MIME refusal, byte limits, cancellation, replacement and
mutation races, duplicate names, concurrent operations, read-only snapshots, report redaction, and cleanup. It proves
same-owner inbound admission and refusal before snapshot creation for every workspace, agent, actor, route, or session
mismatch, invalid or released references, and replaced backing files.

Inspection coverage adds text/image/mixed pages, AcroForm, XFA, signatures, restrictions, encryption,
password-required refusal, malformed structures, deterministic limits, catalog/signature reachability, PDF name and
inline-image token handling, and strict parent-side result validation. Single-stream and cumulative multi-stream
decode limits are enforced before page assembly. On `linux/amd64`, real worker tests cover descriptor-only input,
scrubbed environment, malformed or oversized output, crash, timeout, process-group cancellation, concurrent
inspection, and worker-scratch cleanup.

PDF1A coverage adds ordered page selection, character and pixel budgets, UTF-8 text, image-only and
mixed pages, crop and rotation, AcroForm appearances, XFA refusal, signed classification,
password/malformed refusal, exact backend identity, private artifact adoption, atomic CLI
publication, truncation, and no retained partial output. The integration suite additionally covers
an authority-bound Telegram attachment, hidden-tool discovery, inspect-first extraction, protected
live-only text, page provenance, retained rendering, and confirmed outbox delivery:

```sh
MINTCLAW_REQUIRE_DOCUMENT_AGENT_E2E=1 go test \
  -count=1 \
  -tags goolm,stdjson,integration \
  -run '^TestDocumentPDFTelegramVerticalSlice$' \
  ./pkg/agent
```

The synthetic inventories and evidence mappings are in `pkg/document/testdata/acquisition-manifest.json` and
`pkg/document/testdata/inspection-manifest.json`, and `pkg/document/testdata/read-manifest.json`. No
fixture contains personal or production data. On a host with the pinned independent oracles, also
run:

```sh
PDF0B_POPPLER_VERSION=24.02.0 make test-document-oracle
PDF1A_CLAWPDF_ROOT=/tmp/clawpdf-0.3.2/package make test-document-read-oracle
```

## Inbound service contract

Inbound adapters bind ordinary turn media before agent execution, independently of node file-transfer policy. The
agent adapter uses the same service boundary as the CLI:

```go
snapshot, report := document.AcquireMedia(ctx, mediaStore, mediaRef, owner, options)
snapshot, report := document.InspectMedia(ctx, mediaStore, mediaRef, owner, options)
snapshot, report := document.ExtractMedia(ctx, mediaStore, mediaRef, owner, readOptions)
snapshot, report := document.RenderMedia(ctx, mediaStore, mediaRef, owner, readOptions)
```

`owner` contains the exact non-reversible workspace, agent, actor, route, and session correlations bound to the
reference. The binding pins size and SHA-256. Acquisition receives an authority-checked open descriptor and verifies
those bytes while creating the immutable snapshot; it never reopens a mutable backing path. Unknown, unbound, released,
altered, or cross-authority refs return `denied` with `source_not_authorized` without revealing whether a reference
exists.

A successful report retains the opaque source reference and safe correlations, but never the backing path or bytes.
The worker receives only content type, size, SHA-256, explicit limits, and the immutable snapshot descriptor.

## Agent And Channel Workflow

`tools.document.enabled` defaults to `true`, but the `document` tool is registered only when the
qualified inspection, extraction, and rendering backend is present. It remains hidden until the
model calls the existing `tool_search_tool_bm25`. A verified current PDF activates the checked-in
`pdf` skill and forces the primary model route; an unrelated turn receives neither the skill body
nor the `document` schema. Tool and skill allowlists remain authoritative and can still deny the
workflow.

The attachment classifier trusts the bytes, not the caption, filename, or sender MIME alone. It
projects only the exact current `media://` ref, detected content type, byte size, and untrusted
presentation filename to the model. The tool will not accept a local path, a guessed ref, or an
older attachment from the same route. A claimed PDF whose bytes do not begin with an admitted PDF
signature is refused before skill activation.

The skill directs the model to inspect first and then select explicit, sorted, one-based pages.
Extraction may expose at most 32 KiB of page-labelled text to the next model call. That text is not
written to canonical tool-result history or ordinary diagnostic previews; durable state retains
only safe reports, source/artifact digests, page numbers, counts, states, and opaque refs. Reading
more requires another bounded page selection.

Rendering is available only when the selected model entry explicitly declares an image-input path.
An empty vision override asserts that the same model accepts images; a named override routes the
rendered PNG through the existing vision-model path:

```json
{
  "tools": {
    "document": {
      "enabled": true
    }
  },
  "model_list": [
    {
      "model_name": "primary",
      "provider": "openai",
      "model": "gpt-5.4",
      "enabled": true,
      "capabilities": {
        "vision": {}
      }
    }
  ]
}
```

Use `"vision": {"model": "vision-model-alias"}` when a separate configured model should receive
page images. Without either declaration, `render` returns `vision_unavailable`; MintClaw does not
guess that a model can see images. PDF bytes are never sent through provider-native document input.

By default rendered pages are current-turn model context and are released at turn completion. The
model sets `retain: true` only when the user asked to receive them. Retained PNGs are registered in
the authority-bound `MediaStore`, represented by a canonical `taskresult.Deliverable`, and sent by
the existing durable outbox. Confirmed, definitely failed, and ambiguous channel acceptance keep
their existing meanings; an ambiguous attempt is not replayed under a new delivery identity.

## Manual Linux smoke

Build the current branch and use the checked-in synthetic fixture:

```sh
make build
./build/mintclaw document capabilities --json
./build/mintclaw document acquire \
  --input pkg/document/testdata/text.pdf \
  --json
./build/mintclaw document inspect \
  --input pkg/document/testdata/text.pdf \
  --json
./build/mintclaw document extract \
  --input pkg/document/testdata/unicode.pdf \
  --pages 1 \
  --output /tmp/mintclaw-text.jsonl \
  --json
./build/mintclaw document render \
  --input pkg/document/testdata/rotated-crop.pdf \
  --pages 1 \
  --output-dir /tmp/mintclaw-pages \
  --json
```

The capability report must identify `linux/amd64`, advertise `acquire`, `inspect`, `extract`, and
`render` as `supported`, and leave fields, fill, verify, and flatten unavailable. If the exact
Poppler version or executable hashes differ, extract and render remain unavailable. The acquisition
report must contain:

- schema `mintclaw.document_report.v1`;
- operation `acquire` and state `succeeded`;
- content type `application/pdf`, exact size, and a 64-character SHA-256 digest;
- authority kind `local_operator`; and
- no local input or protected-scratch path.

The inspection report is tied to the same digest. For `text.pdf`, it reports pdfcpu `v0.15.0`, PDF version `1.7`, one
page, no encryption, signatures, AcroForm, or XFA, and `extractable_text.state: present`. It contains no extracted text.
The operation-scoped snapshot is deleted when the CLI closes.

The extraction output is UTF-8 JSON Lines with an explicit one-based page on every record. The
report contains the immutable source SHA-256, selected pages, character counts, backend identity,
artifact size/digest, and no extracted content or path. The rendered directory contains only
`page-0001.png`; for the synthetic crop-and-rotation fixture at 144 DPI it is 792 by 612 pixels. The
report carries the same source digest and exact page mapping. PDF1A refuses every existing output
path and does not expose an overwrite flag: choose a fresh path, or explicitly move/remove the old
output before invoking MintClaw. This keeps publication on one atomic no-replace operation instead
of attempting an unsafe directory exchange. A failed or canceled operation publishes no output and
leaves document scratch empty.

The final output path is an untrusted caller namespace, so the no-replace rename remains atomic even
when another process creates that path concurrently. The randomized staging directory is mode
`0700` and operation-private, but it is not a security boundary against a process running with
MintClaw's own effective UID. Such a process can already inspect or mutate MintClaw-owned files and
can change a staged child between any userspace validation and directory rename or unlink. Concurrent
same-UID mutation of MintClaw's private staging namespace is therefore outside this local CLI
contract. Exact-name and inode checks plus non-recursive abort cleanup remain defense in depth for
corruption and ordinary races; they do not claim indivisible validation against that excluded actor.
Within this actor model, the successful atomic rename is the commit point and every earlier failure
publishes no output.

Check the protected-input disposition separately:

```sh
set +e
./build/mintclaw document inspect \
  --input pkg/document/testdata/encrypted-password-required.pdf \
  --json
echo "$?"
set -e
```

Expected: exit status `3`, terminal state `unsupported`, failure `password_required`, and partial facts proving
encryption and password requirement. PDF0B has no password input and does not attempt decryption.

## Deployed Linux smoke

After merged `main` is built and installed on the configured deployment, run:

```sh
scripts/document-deployed-smoke.sh --host server@oc
```

The harness uses `/home/server/src/mintclaw/build/mintclaw` and checked-in `text.pdf`; it never reads a live profile. It
prints the deployed SHA, fixture digest, `state=succeeded`, `scratch=clean`,
`marker=MINTCLAW_PDF1A_AGENT_CHANNEL_OK`, and `marker=MINTCLAW_PDF1A_DEPLOYED_OK`. It runs the
real-process agent/channel vertical test and fails if a report exposes the repository path or
protected scratch survives. Omit `--host` to try `server@oc` and then `server@oc-ts`.

## Manual Telegram Checklist

Use only the checked-in synthetic fixtures; do not use a personal document as release evidence.

1. Confirm the deployed profile has `tools.document.enabled: true`. For the scan/render step,
   confirm the primary model has `capabilities.vision` as described above.
2. Send `pkg/document/testdata/text.pdf` as a Telegram file and ask: `Read the marker on page 1 and
   cite the page.` Confirm the answer says `MintClaw text fixture` and cites page 1.
3. Send `pkg/document/testdata/rotated-crop.pdf` and ask: `Render page 1 and send that rendered page
   back to me.` Confirm exactly one PNG arrives and no duplicate appears after waiting or restarting
   the gateway.
4. Send a plain-text file renamed to `.pdf`. Confirm MintClaw returns a typed unsupported/refused
   result and does not claim it read the file.
5. Make local copies of checked-in `text.pdf` and `unicode.pdf` using the same presentation filename,
   send them in separate turns, and confirm each answer remains bound to its own opaque ref and
   source digest.
6. Inspect the new completed diagnostic trace. It must show the `document` lifecycle, selected page,
   counts, digests, state, and delivery outcome, but no extracted marker text, image bytes, protected
   path, or raw filename. Follow [Debugging MintClaw](debug.md) for the trace location and commands.

## Unsupported-platform smoke

On macOS, Windows, Linux ARM, or another unadmitted tuple:

```sh
./build/mintclaw document inspect \
  --input pkg/document/testdata/text.pdf \
  --json
```

Expected: exit status `3`, terminal state `unavailable`, and failure `unsupported_platform`. MintClaw must not open the
input or create protected scratch before returning that result.

## Current boundary

The parser runs only in the worker. The worker is a subprocess containment boundary, not a general sandbox platform.
It receives no original path, MintClaw config, credentials, or ambient environment, and it is bounded by input, page,
decoded-content, runtime, and output limits. It does not claim host-level network or arbitrary-filesystem isolation.
Future document operations may add stronger confinement only by qualifying a ready, packaged primitive; MintClaw will
not build a custom namespace, seccomp, or container manager for this feature.

Local and agent PDF extraction and rendering are available only for the admitted bundle. Form
writes, password handling, OCR, provider-native PDF input, and companion placement remain
unavailable. macOS stays fail-closed until its later roadmap parity slice proves worker, fixture,
packaging, signing, update, rollback, privacy, cancellation, and cleanup contracts on both
architectures.
