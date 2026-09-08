# Document Acquisition And Inspection

## Status

PDF0A acquisition, PDF0B inspection, and PDF1A local extraction/rendering are available for the
qualified `linux/amd64` bundle. The
[PDF0A exit record](../architecture/pdf0a-exit-record.md) contains merged-main deployment and rollback evidence for
the acquisition foundation. The [PDF0B backend decision](../architecture/pdf0b-backend-decision.md) records parser
qualification, normalization, oracle, packaging, and residual limits.
The [PDF1A backend decision](../architecture/pdf1a-backend-decision.md) pins the production
Poppler bundle, independent ClawPDF/PDFium oracle, executable identities, resource evidence, and
rollback contract.

The commands prove bounded local-file acquisition, immutable identity, and deterministic parsing in a short-lived
worker. The shared service also admits an inbound `media://` reference only for its immutable workspace, agent, actor,
route, and session owner. PDF0B does not render or return document content. PDF1A core adds local
operator read commands but does not yet register the deferred agent tool; that arrives in the
dependent PDF1A agent/channel PR. These milestones do not accept passwords or retain a durable
document job. Those behaviors remain outside this operator-only milestone.

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
publication, truncation, and no retained partial output.

The synthetic inventories and evidence mappings are in `pkg/document/testdata/acquisition-manifest.json` and
`pkg/document/testdata/inspection-manifest.json`, and `pkg/document/testdata/read-manifest.json`. No
fixture contains personal or production data. On a host with the pinned independent oracles, also
run:

```sh
PDF0B_POPPLER_VERSION=24.02.0 make test-document-oracle
PDF1A_CLAWPDF_ROOT=/tmp/clawpdf-0.3.2/package make test-document-read-oracle
```

## Inbound service contract

Inbound adapters bind ordinary turn media before agent execution, independently of node file-transfer policy. A future
agent adapter will use the same service boundary as the CLI:

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
prints the deployed SHA, fixture digest, `state=succeeded`, `scratch=clean`, and
`marker=MINTCLAW_DOCUMENT_INSPECT_OK`. It fails if either report exposes the repository path or protected scratch
survives. Omit `--host` to try `server@oc` and then `server@oc-ts`.

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

Local PDF extraction and rendering are available only for the admitted bundle. Form writes,
password handling, OCR, provider-native PDF input, and agent/channel exposure remain unavailable in
the core slice. macOS stays fail-closed until its later roadmap parity slice proves worker, fixture,
packaging, signing, update, rollback, privacy, cancellation, and cleanup contracts on both
architectures.
