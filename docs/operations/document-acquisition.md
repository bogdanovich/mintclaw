# Document Acquisition Smoke

## Status

PDF0A acquisition and subprocess-boundary slice. The command proves bounded
local-file acquisition, immutable identity, and a real short-lived worker
handshake. It does not parse PDF objects, render pages, register an agent tool,
retain a durable document job, or claim that PDF0A is complete.

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
non-PDF input, byte limits, cancellation, mid-copy mutation, distinct digests
for duplicate names, read-only snapshots, report redaction, and cleanup. On
`linux/amd64` it additionally launches a real worker process and covers success,
scrubbed environment, malformed or oversized output, crash, timeout, complete
process-group cancellation, and worker-scratch cleanup.

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

PDF object inspection remains unavailable. The remaining PDF0A work is inbound
owner binding, the complete fixture manifest/concurrency set, deployed Linux
evidence, and the milestone exit record.
