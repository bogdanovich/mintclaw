# PDFium WebAssembly Qualification

## Decision

PPDF0 admits `github.com/klippa-app/go-pdfium` `v1.20.0` as the
portable PDF read/render candidate for PPDF1 and PPDF2. Admission is narrow:
the engine may run only inside the existing one-shot document worker, after
MintClaw structural inspection, with bytes-only input and the resource and
authority limits below.

PPDF1 moved only the admitted `go-pdfium/webassembly` and Wazero runtime
packages into the production module graph. PPDF2 activates that engine for
extract and render on Darwin AMD64 and ARM64 behind the existing one-shot
worker and artifact contracts. PPDF4 makes it the same production read/render
primary on Linux AMD64 and deletes the old Poppler fallback. The exact
candidate qualification remains isolated in
the nested `internal/qualification/pdfiumwasm` module, while production
contract tests now run in the same Linux AMD64, macOS AMD64/ARM64, and Windows
AMD64 matrix.
PPDF3a additionally activates the existing pure-Go `pdfcpu` ordinary field
discovery backend on those Darwin targets. PPDF3b admits ordinary form writing
there and expands PDFium's role to mandatory visible form readback on Linux
AMD64, Darwin AMD64/ARM64, and Windows AMD64. Hybrid forms remain Linux-only by
policy. Windows passes the immutable bytes through one explicitly inherited
HANDLE and contains the suspended worker in a process- and memory-limited Job
Object before it can execute.

## Pinned provenance

| Component | Identity |
| --- | --- |
| Go wrapper | `github.com/klippa-app/go-pdfium v1.20.0`, commit `6ff9abaa8b4119050917fc6e3bfba458e88a295d` |
| Go wrapper module sum | `h1:cK4fkjUznvJRysfXp+F2f11pBLvzY6ma2zWWwnlUwpw=` |
| PDFium release claim | Chromium/PDFium `8044`, Emscripten `6.0.9` |
| PDFium branch head | `f91ca5a72358bb0b00b4da9481b21fe668157614` |
| Embedded module | 5,752,581 bytes, SHA-256 `f651270c675cac90702b762f4b95d2b34e365cdb374065e40af016f4f0f304ea` |
| WASM runtime | `github.com/tetratelabs/wazero v1.12.0` |
| Wazero module sum | `h1:DuWcpNu/FzgEXgGBDp8J1Spc+CWOvvtvVyjKlaZopYU=` |

The Go module checksum database authenticates the selected module archive and
therefore the embedded WASM bytes. The wrapper tag and commit are not signed,
and upstream does not publish a bit-reproducible recipe or attestation for its
custom WASM file. The embedded digest is consequently the release identity;
it is not a claim that MintClaw can rebuild identical bytes from PDFium source.
An upgrade must repeat PPDF0 rather than accepting a new tag by version alone.

## Notices

- The go-pdfium wrapper is MIT licensed. Its pinned `LICENSE` SHA-256 is
  `fd871478ba874c3e1736c691e3ca89a350ab769db6c7aac9818024f912afb488`.
- Wazero is Apache-2.0 licensed. Its pinned `LICENSE` SHA-256 is
  `c46f033d017a5af71a1de0105ec56c41bd47f81a0bbdf779fffe316336dc7c1f`.
- The PDFium `chromium/8044` license bundle is available from the pinned
  branch and has SHA-256
  `1fe9dea718fbd75cf149adaf4d8a22a4335604d964ddb76d1b45383dec8668c9`.

Readable, digest-pinned copies are stored in `THIRD_PARTY_NOTICES` and ship in
release archives, deb/rpm packages, and container images. GoReleaser produces
SPDX JSON SBOMs for archives and native packages, so the linked Go modules and
embedded WASM dependency remain visible in release provenance. Optional host
Poppler/Ghostscript packages are reported by document capabilities and the
native provenance record rather than being misrepresented as linked binary
dependencies. A dependency upgrade must update both the qualification pins
and distributed notices in one change.

## Authority boundary

The admitted constructor must preserve all of these settings:

- an explicitly non-nil, empty `wazero.FSConfig`; the upstream nil default
  mounts the host root and is forbidden;
- input passed only as document bytes, never as a host path;
- no arguments, environment, host clocks, or network configuration;
- discarded or independently bounded stdout and stderr;
- `WithCloseOnContextDone(true)` so a worker kill interrupts WASM execution;
- one instance, one operation, and `ReuseWorkers: false`;
- deterministic cleanup of render buffers, document, instance, pool, and the
  outer worker process.

The embedded module currently imports 44 functions from only `env` and
`wasi_snapshot_preview1`; no imported function name contains socket or network
authority. A black-box test also writes a valid PDF on the host, proves that
the WASM engine cannot open its absolute path, and then opens the same bytes
successfully.

## Frozen limits

| Resource | Admission limit |
| --- | ---: |
| Input bytes | 20 MiB |
| Structural pages | 2,000 |
| Extract pages / characters | 20 / 256,000 |
| Render pages | 8 |
| Render edge | 3,200 pixels |
| Pixels per page / operation | 16M / 32M |
| Artifact bytes | 32 MiB |
| WASM linear memory | 2,560 pages, 160 MiB |
| Go runtime soft memory limit | 320 MiB |
| Process peak RSS | 512 MiB |
| Cold operation | 20 seconds |
| Repeated operation | 5 seconds |
| Active-render cancellation | 5 seconds |
| Candidate binary growth | 64 MiB |

| Evidence host | Binary growth | Cold | Repeated | Peak RSS |
| --- | ---: | ---: | ---: | ---: |
| Local Darwin AMD64 | 12,984,480 bytes | 2.6-3.1 s | 2-3 ms | 301-321 MB |
| Deployed-host Linux AMD64 | 12,797,873 bytes | 12.7-16.2 s | 12-25 ms | 435,994,624 bytes |

The required CI matrix records the same evidence for Linux AMD64 and both
admitted macOS architectures. Thresholds are deliberately above observed
variance but below the process and deployment envelopes. The slower Linux
sample is retained as the conservative reference rather than inferred from a
faster hosted runner.

## Fixture outcomes

The candidate passes bytes-only extraction and bounded rendering for plain
text, Unicode, backend-specific reading order, rotation/crop, image-only,
extreme-dimension, and ordinary AcroForm fixtures. Image-only extraction is
empty, AcroForm classification is retained, and render buffers are explicitly
cleaned. Production uses pdfcpu's bounded effective CropBox preflight before
PDFium rendering because PDFium normalizes some invalid extreme page boxes;
the emitted PNG must exactly match the preflight dimensions.

Truncated and password-protected fixtures fail during open. PDFium does open
MintClaw's malformed-xref fixture, so structural inspection remains mandatory
and authoritative before any portable read, render, or form verification.
For an ordinary form candidate, PPDF3b renders each affected page with and
without form annotations, enumerates exact widget rectangles, and flattens only
PDFium's in-memory document copy so text and glyph boxes can be read back from
each widget. The candidate bytes are never replaced by that flattened copy.
Exact text, selection fill, button interior marks, clipping, stale appearance,
page, pixel, and widget bounds all fail closed. Dynamic XFA is rejected before
this backend, and portable hybrid flattening remains unadmitted.

## Resource evidence

`internal/qualification/pdfiumwasm/qualify.sh` performs all admission checks:

1. build a minimal Go test binary and candidate test binary and enforce the
   frozen growth ceiling;
2. build and test with `CGO_ENABLED=0`, then reject reachable CGO, native
   go-pdfium, HashiCorp plugin, and gRPC packages;
3. prove that production reaches the admitted WebAssembly packages while
   excluding CGO, native go-pdfium implementations, plugins, and gRPC;
4. verify that distributed notices exactly match the qualified license pins;
5. verify module identity, embedded digest, imports, filesystem denial,
   fixtures, limits, cleanup, and cancellation;
6. run cold/repeated work in two fresh child processes with the production
   worker's 320 MiB Go runtime soft limit, measure peak RSS with `getrusage`,
   and wait for both processes to exit as the reclamation proof.

No qualification step downloads code or PDF data during a document operation.
Go modules are resolved before execution, and every runtime fixture is either
synthetic MintClaw test data or a digest-pinned dependency fixture.

## Integration constraints

PPDF1 through PPDF5 do not weaken this admission. In particular, they keep
structural inspection ahead of PDFium and form discovery, freeze one backend
set per worker, preserve typed failures, and never retry an in-progress
operation in another engine. Form readback keeps the existing eight-page and
32-million-pixel operation bounds. A requirement for host mounts, worker reuse,
a daemon, runtime downloads, or a higher resource ceiling reopens PPDF0.

The production evidence is `TestPDFiumReadBackendMatchesPortableContract`,
`TestPortableProcessReaderUsesPDFiumAndAdoptsVerifiedArtifacts`, and
`TestPDFCPUFormFieldsBackendMatchesManifest`, the portable form-write matrix,
stale/clipped refusal tests, and durable form lifecycle tests on all admitted
architectures. The `TestPortableProcess*` suite additionally proves the real
worker boundary on Linux AMD64, both macOS architectures, and Windows AMD64.
Windows-specific tests prove exact inherited-handle transport, suspended Job
Object admission, aggregate memory/process limits, descendant cancellation,
and scratch cleanup. Linux's optional
independent standard-form verifier is covered by native backend qualification;
it is not a read/render fallback. The PPDF4 rollback boundary is PPDF3b merge
commit `7c1bac69c6a35751e36d4b94d615574d51fc18f2`.
