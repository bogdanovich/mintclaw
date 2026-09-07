# Document Acquisition Smoke

## Status

PDF0A first vertical slice. This command proves bounded local-file acquisition
and identity only. It does not parse PDF objects, render pages, register an
agent tool, retain a durable document job, or claim that PDF0A is complete.

The initial admitted runtime tuple is `linux/amd64`. Other platforms return a
structured `unsupported_platform` result. This is intentional until the
mandatory isolated worker and the same real-process probes are admitted there.

## Automated check

From the repository root:

```sh
make test-document
```

The focused suite covers direct regular files, final-component symlinks,
non-PDF input, byte limits, cancellation, mid-copy mutation, distinct digests
for duplicate names, read-only snapshots, report redaction, and cleanup.

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
size and digest.

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

The next PDF0A slice adds the mandatory isolated worker on `linux/amd64` and
real-process probes for network, external-file, CPU, memory, process-count,
runtime, output, cancellation, crash, and scratch-cleanup enforcement. PDF
object inspection remains unavailable until that boundary passes.
