# Document Acquisition And Inspection

## Status

PDF acquisition, inspection, extraction, rendering, ordinary field discovery,
and standard AcroForm workflows are available through the same portable
one-shot worker on qualified `linux/amd64`, `darwin/amd64`, and
`darwin/arm64`, and `windows/amd64` builds. The
[PDF0A exit record](../architecture/pdf0a-exit-record.md) contains merged-main deployment and rollback evidence for
the acquisition foundation. The [PDF0B backend decision](../architecture/pdf0b-backend-decision.md) records parser
qualification, normalization, oracle, packaging, and residual limits.
The historical [PDF1A backend decision](../architecture/pdf1a-backend-decision.md)
records the original native baseline. The current
[PDFium/WASM qualification](../architecture/pdfium-wasm-qualification.md) pins
the production read/render engine, resource evidence, and rollback contract.
[Native PDF backend provenance](../architecture/pdf-native-backend-provenance.md)
now covers only the explicit Linux independent-verification and hybrid tiers.

The commands prove bounded local-file acquisition, immutable identity, and deterministic parsing in a short-lived
worker. The shared service also admits an inbound `media://` reference only for its immutable workspace, agent, actor,
route, and session owner. The agent uses the same service through one hidden `document` tool and one
on-demand `pdf` skill. These milestones do not accept passwords or retain a durable document job.

Other tuples return a structured `unsupported_platform` result before opening
the input.

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
decode limits are enforced before page assembly. On every qualified tuple,
real worker tests cover path-free descriptor or inherited-handle input,
scrubbed environment, malformed or oversized output, crash, timeout,
process-tree cancellation, concurrent inspection, and worker-scratch cleanup.
Windows additionally proves suspended startup inside a kill-on-close Job
Object limited to two processes and 512 MiB of aggregate committed memory.

The common PDFium/WASM coverage adds ordered page selection, character and pixel budgets, UTF-8 text, image-only and
mixed pages, crop and rotation, AcroForm appearances, XFA refusal, signed classification,
password/malformed refusal, exact backend identity, private artifact adoption, atomic CLI
publication, truncation, and no retained partial output. The integration suite additionally covers
an authority-bound Telegram attachment, hidden-tool discovery, inspect-first extraction, protected
live-only text, page provenance, retained rendering, confirmed outbox delivery, and a server-local
path whose immutable source still yields the original bytes after the path is replaced:

```sh
MINTCLAW_REQUIRE_DOCUMENT_AGENT_E2E=1 go test \
  -count=1 \
  -tags goolm,stdjson,integration \
  -run '^(TestDocumentPDFTelegramVerticalSlice|TestDocumentLocalPathToolLinuxIntegration)$' \
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

Document worker admission and deployment memory/task limits are described in
[Document Process Capacity](document-process-capacity.md).

The attachment classifier trusts the bytes, not the caption, filename, or sender MIME alone. It
projects only the exact current `media://` ref, detected content type, byte size, and untrusted
presentation filename to the model. The tool will not accept a guessed ref or an older attachment
from the same route. A claimed PDF whose bytes do not begin with an admitted PDF signature is
refused before skill activation.

A current message may instead name a PDF already on the gateway host. That path activates the same
PDF skill but is not authority by itself. Only `document inspect` accepts the path, and only when it
exactly matches a local PDF selector in that current message and passes the configured agent
workspace/read policy. Quote paths containing whitespace. An equivalent alias or another PDF in the
same permitted directory is denied unless the user actually supplied that selector. The successful
report returns a temporary, turn-owned `media://` source ref; extraction and rendering must use that
immutable ref rather than reopening the host path. The ref and its private snapshot are removed at
terminal turn cleanup.

With the default `agents.defaults.restrict_to_workspace: true`, put the PDF under the agent's
workspace or explicitly match it with `tools.allow_read_paths`. Relative paths resolve beneath the
workspace. Disabling workspace restriction preserves the common local file-tool behavior and
allows absolute host paths, but does not allow symlinks or non-regular/non-PDF inputs. The path is
omitted from tool reports, model-authored durable tool history, tool logs, and ordinary diagnostic
content; safe document digests and page evidence remain observable.

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
the existing durable outbox. Their structured render report remains available to the next model
call, but the delivery-only PNG is not added to provider context. Confirmed, definitely failed, and
ambiguous channel acceptance keep their existing meanings; an ambiguous attempt is not replayed
under a new delivery identity.

## Manual portable smoke

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

On Windows, invoke `build\mintclaw.exe` from PowerShell and choose fresh output
destinations beneath `$env:TEMP`; the capability and report requirements are
identical.

The capability report must identify the current qualified tuple and advertise
`acquire`, `inspect`, `extract`, `render`, and ordinary field discovery as
`supported`. Extraction and rendering must identify the PDFium/WASM backend in
portable mode on Linux, macOS, and Windows. On Linux, an unavailable or drifted
Poppler/Ghostscript bundle may disable standard independent verification or
hybrid form operations, but it must not disable portable acquisition,
inspection, extraction, rendering, or field discovery. The acquisition report
must contain:

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
`marker=MINTCLAW_PDF1A_AGENT_CHANNEL_OK`, `marker=MINTCLAW_PDF1A_LOCAL_PATH_OK`, and
`marker=MINTCLAW_PDF1A_DEPLOYED_OK`. It runs the real-process attachment/channel and local-path
vertical tests and fails if a report exposes the repository path, a mutable replacement changes
the admitted bytes, or protected scratch survives. Omit `--host` to try `server@oc` and then
`server@oc-ts`.

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
7. Copy `pkg/document/testdata/text.pdf` beneath the main agent workspace. In a new turn, send a
   sentence containing its absolute path and ask MintClaw to inspect it, read page 1, and cite the
   page. Confirm the answer says `MintClaw text fixture`, no approval is requested for the read, and
   the trace retains safe document lifecycle/digest evidence without the host path or marker text.

## Unsupported-platform smoke

On Linux ARM or another unadmitted tuple:

```sh
./build/mintclaw document inspect \
  --input pkg/document/testdata/text.pdf \
  --json
```

Expected: exit status `3`, terminal state `unavailable`, and failure `unsupported_platform`. MintClaw must not open the
input or create protected scratch before returning that result.

## Current boundary

The parser runs only in the worker. The worker is a subprocess containment
boundary, not a general sandbox platform. It receives no original path,
MintClaw config, credentials, or ambient environment, and it is bounded by
input, page, decoded-content, runtime, and output limits. Windows supplies only
the exact immutable input HANDLE and uses a Job Object for process-tree and
resource containment. It does not claim host-level network or
arbitrary-filesystem isolation. Future document operations may add stronger
confinement only by qualifying a ready, packaged primitive; MintClaw will not
build a custom namespace, seccomp, or container manager for this feature.

Local and agent PDF extraction, rendering, field discovery, and ordinary
AcroForm writes are available on the admitted Linux, macOS, and Windows
tuples. Hybrid-form flattening remains Linux-only and requires the exact
qualified native bundle. Password handling, OCR, provider-native PDF input,
and companion placement remain unavailable.
