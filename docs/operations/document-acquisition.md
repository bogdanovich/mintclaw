# Document Acquisition And Inspection

## Status

PDF0A acquisition and PDF0B inspection are available for `linux/amd64`. The
[PDF0A exit record](../architecture/pdf0a-exit-record.md) contains merged-main deployment and rollback evidence for
the acquisition foundation. The [PDF0B backend decision](../architecture/pdf0b-backend-decision.md) records parser
qualification, normalization, oracle, packaging, and residual limits.

The commands prove bounded local-file acquisition, immutable identity, and deterministic parsing in a short-lived
worker. The shared service also admits an inbound `media://` reference only for its immutable workspace, agent, actor,
route, and session owner. PDF0B does not render or return document content, register an agent tool, accept passwords,
or retain a durable document job. Those behaviors remain outside this operator-only milestone.

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

The synthetic inventories and evidence mappings are in `pkg/document/testdata/acquisition-manifest.json` and
`pkg/document/testdata/inspection-manifest.json`. No fixture contains personal or production data. On a host with the
pinned independent oracle, also run:

```sh
PDF0B_POPPLER_VERSION=24.02.0 make test-document-oracle
```

## Inbound service contract

Inbound adapters bind ordinary turn media before agent execution, independently of node file-transfer policy. A future
agent adapter will use the same service boundary as the CLI:

```go
snapshot, report := document.AcquireMedia(ctx, mediaStore, mediaRef, owner, options)
snapshot, report := document.InspectMedia(ctx, mediaStore, mediaRef, owner, options)
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
```

The capability report must identify `linux/amd64`, advertise `acquire` and `inspect` as `supported`, and leave extract,
render, fields, fill, verify, and flatten unavailable. The acquisition report must contain:

- schema `mintclaw.document_report.v1`;
- operation `acquire` and state `succeeded`;
- content type `application/pdf`, exact size, and a 64-character SHA-256 digest;
- authority kind `local_operator`; and
- no local input or protected-scratch path.

The inspection report is tied to the same digest. For `text.pdf`, it reports pdfcpu `v0.15.0`, PDF version `1.7`, one
page, no encryption, signatures, AcroForm, or XFA, and `extractable_text.state: present`. It contains no extracted text.
The operation-scoped snapshot is deleted when the CLI closes.

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

PDF extraction, rendering, form writes, password handling, and agent/channel exposure remain unavailable. macOS stays
fail-closed until its later roadmap parity slice proves worker, fixture, packaging, signing, update, rollback, privacy,
cancellation, and cleanup contracts on both architectures. PDF1A requires a separate goal after the PDF0B exit record.
