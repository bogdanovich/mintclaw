# Document Acquisition Smoke

## Status

PDF0A acquisition and subprocess-boundary slice. The command proves bounded
local-file acquisition, immutable identity, and a real short-lived worker
handshake. The shared service additionally admits an inbound `media://`
reference only for its immutable workspace, agent, actor, route, and session
owner. It does not parse PDF objects, render pages, register an agent tool, or
retain a durable document job. PDF0A closes only after merged-main Linux
deployment evidence and its exit record are committed.

The initial admitted runtime tuple is `linux/amd64`. Other platforms return a
structured `unsupported_platform` result. This is intentional until the
mandatory subprocess boundary and the same real-process probes are admitted
there.

## Automated check

From the repository root:

```sh
make test-document
```

The focused suite covers direct regular files, final-component symlinks,
FIFO/devices, non-PDF input, byte limits, cancellation, path replacement,
mid-copy mutation, distinct digests for duplicate names, concurrent
acquisition, read-only snapshots, report redaction, and cleanup. It also proves
same-owner inbound acquisition and refusal before snapshot creation for every
workspace, agent, actor, route, or session mismatch, invalid or released refs,
and replaced backing files. On `linux/amd64` it additionally launches a real
worker process for both local and owner-bound media inputs and covers success,
scrubbed environment, malformed or oversized output, crash, timeout, complete
process-group cancellation, and worker-scratch cleanup.

The synthetic inventory and its evidence-test mapping are checked in at
`pkg/document/testdata/acquisition-manifest.json`. No fixture contains personal
or production data.

## Inbound service contract

Inbound adapters bind ordinary turn media before agent execution, independently
of whether the profile exposes node file transfer. An eventual document agent
adapter calls the same service boundary as the CLI:

```go
snapshot, report := document.AcquireMedia(ctx, mediaStore, mediaRef, owner, options)
```

`owner` must contain the exact non-reversible workspace, agent, actor, route,
and session correlations already stored with the reference. The binding also
pins size and SHA-256. Acquisition receives an authority-checked open file
descriptor and verifies those exact bytes again while creating the immutable
snapshot; it never reopens a mutable backing path. Unknown, unbound, released,
altered, or cross-authority refs all return `denied` with
`source_not_authorized`; the response deliberately does not reveal whether a
reference exists. A successful report retains the opaque source ref and safe
correlations, but never the backing path or bytes. The worker receives only
content type, size, SHA-256, and the immutable snapshot descriptor.

## Manual Linux smoke

Build the current branch and use the checked-in synthetic fixture:

```sh
make build
./build/mintclaw document capabilities --json
./build/mintclaw document acquire \
  --input pkg/document/testdata/acquisition-fixture.pdf \
  --json
```

The capability report must say `linux` / `amd64`, advertise only `acquire` as
`supported`, and keep parser-backed operations `unavailable`. The acquisition
report must have:

- `schema_version` equal to `mintclaw.document_report.v1`;
- `operation` equal to `acquire` and `state` equal to `succeeded`;
- content type `application/pdf`, the exact input byte size, and a 64-character
  SHA-256 digest;
- authority kind `local_operator`; and
- no local input or protected-scratch path.

The immutable snapshot is operation-scoped and is deleted when the CLI command
closes. Re-running the command produces a new operation ID but the same input
size and digest. A successful report also means the installed executable
started its private worker mode, passed the snapshot on an inherited file
descriptor, received a matching versioned result, and cleaned the worker
scratch. The worker command is hidden and is not an operator API.

## Deployed Linux smoke

After merged `main` has been built and installed on the configured deployment,
run from a MintClaw checkout:

```sh
scripts/document-deployed-smoke.sh --host server@oc
```

The script uses `/home/server/src/mintclaw/build/mintclaw` and the checked-in
`pkg/document/testdata/acquisition-fixture.pdf`; it never reads a live profile.
It prints the deployed repository SHA, fixture digest, `state=succeeded`,
`scratch=clean`, and `marker=MINTCLAW_DOCUMENT_ACQUIRE_OK`. It fails if the
report exposes the repository path or protected scratch survives. Omit
`--host` to try `server@oc` and then `server@oc-ts`.

## Unsupported-platform smoke

On macOS, Windows, Linux ARM, or another not-yet-admitted tuple:

```sh
./build/mintclaw document acquire \
  --input pkg/document/testdata/acquisition-fixture.pdf \
  --json
```

Expected result: exit status `3`, terminal state `unavailable`, and failure code
`unsupported_platform`. MintClaw must not open or inspect the input before that
fail-closed result.

## Current boundary

The worker is a subprocess containment boundary, not a new general sandbox
platform. It receives no original path, MintClaw configuration, credentials,
or ambient environment, and it is bounded by runtime and output limits. It does
not itself claim host-level network or arbitrary-filesystem isolation. A
future parser backend may add stronger confinement only by qualifying a ready,
packaged Linux primitive; MintClaw will not implement a custom namespace,
seccomp, or container manager for this feature.

PDF object inspection remains unavailable. macOS remains fail-closed until its
separate parity slice proves the same worker, fixture, packaging, and cleanup
contracts described in the roadmap. After the code-side tests in this slice,
the remaining PDF0A work is merged-main Linux deployment evidence and the
milestone exit record.
