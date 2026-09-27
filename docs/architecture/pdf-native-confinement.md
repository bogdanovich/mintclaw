# Native PDF Confinement

## Contract

Native Poppler and Ghostscript operations are available only on `linux/amd64`
when both the admitted executable bytes and the mandatory document Bubblewrap
policy are available. The policy is part of the backend identity as
`bubblewrap_document_worker_v1`; it is not controlled by the optional global
`isolation.enabled` setting and cannot be disabled independently.
MintClaw launches only the qualified `/usr/bin/bwrap` bytes at the recorded
SHA-256; it never resolves this security boundary from ambient `PATH`.

The boundary wraps the existing one-shot `mintclaw document _worker` process.
It does not introduce a daemon, a second worker protocol, or a second sandbox
framework. The parent still owns deadlines, bounded output, process-group
cancellation, artifact adoption, and scratch cleanup.

## Operation Boundary

The worker applies the mandatory policy before these operations start:

- text extraction and page rendering through Poppler;
- ordinary AcroForm candidate generation, whose visual gate uses Poppler; and
- hybrid-form candidate generation, whose independent visual gate also uses
  Ghostscript.

Verification, structural inspection, and field discovery use the existing Go
and pdfcpu paths. They retain the one-shot process boundary but do not require
Bubblewrap or native executables. A future byte-only WASM backend can therefore
remain available when the native policy is unavailable.

## Linux Policy

`pkg/isolation` owns one dedicated document policy with an empty mount view.
The child receives only:

- the immutable PDF snapshot as inherited file descriptor 3;
- the one-shot worker executable as a read-only file;
- the exact admitted Poppler and Ghostscript executable paths as read-only
  files, which the worker revalidates and copies into sealed executable
  descriptors before use;
- required dynamic libraries, fonts, locale, color, Poppler, and Ghostscript
  data as read-only directories;
- one private worker scratch directory as the only writable host mount;
- a private `/tmp`, minimal `/proc`, and minimal `/dev`.

The policy unshares network, IPC, PID, and UTS namespaces and drops all
capabilities. PID isolation is required because an unshared mount namespace
alone could otherwise expose host paths through another process under
`/proc/<pid>/root`. Absolute command arguments do not become mounts.

Failure is closed. Missing Bubblewrap, a failed functional policy probe, an
invalid private scratch directory, or a missing immutable backend executable
makes native capabilities and operations unavailable. MintClaw never retries
the same operation outside the boundary.

## Diagnostics And Qualification

`mintclaw document capabilities --json` reports the effective isolation mode
and includes the policy failure in each unavailable native backend reason.
`mintclaw doctor` uses the same report and names Bubblewrap in remediation.
Capability discovery executes only a no-input `/bin/true` namespace probe; it
does not execute Poppler or Ghostscript or inspect a PDF.

`make qualify-document-native-backends EVIDENCE_DIR=...` records the exact
Bubblewrap package and executable digest with the native backend bundle. Its
real-process tests prove extraction, rendering, ordinary forms, and hybrid
forms inside the policy. A negative probe passes an unmounted host file and a
listening host-loopback address to a confined worker and requires both accesses
to fail.

Rollback restores the MintClaw binary and all package revisions recorded in
the qualification result. If the policy probe then fails, native document
traffic remains unavailable until the boundary is repaired; removing the
boundary is not a rollback mechanism.
