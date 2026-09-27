# Portable PDF Backend Mini-Roadmap

## Status

Completed cross-platform program. PPDF0 selected and qualified the pinned
PDFium/WASM candidate under the evidence and constraints in
[PDFium WebAssembly Qualification](../pdfium-wasm-qualification.md). The first
integration phase targeted Linux and macOS. PPDF5 extends the same
contract to Windows AMD64 through inherited-handle transport and a bounded Job
Object rather than inferring support from successful compilation.

PPDF0 through PPDF4 are merged. PPDF2 activates bounded PDFium/WASM extraction
and rendering, plus portable pdfcpu inspection, on Darwin AMD64 and ARM64.
PPDF3a adds ordinary field discovery there. PPDF3b adds ordinary writing and
PDFium/WASM visible readback on Linux AMD64 and Darwin AMD64/ARM64 while
retaining Poppler as an independent Linux verifier. PPDF4 makes PDFium/WASM the
stable read/render primary on Linux and macOS, removes the old Poppler primary
path, and keeps native executables only for explicitly reported verification
or hybrid roles. PPDF5 adds the path-free inherited HANDLE, process-tree and
resource containment, and the complete portable test contract on Windows
AMD64. The same synthetic contracts and exact backend identities are enforced
across the Linux, macOS, and Windows worker matrix.

| Packet | Pull request | Merge commit |
| --- | --- | --- |
| PPDF0 candidate qualification | `#1376` | `ac6c299b2a126887748cc2dcf5d37dde2cebbd98` |
| PPDF1 backend composition | `#1377` | `8341cc79c134b2567a7a0790868079519612b1e3` |
| PPDF2 portable macOS read/render | `#1378` | `ff51b4747741e381106e125e26c2a9478437297d` |
| PPDF3a portable ordinary field discovery | `#1379` | `37a411075a0d89b01eade1f82f7700b72ce6dbe1` |
| PPDF3b portable ordinary form write | `#1380` | `7c1bac69c6a35751e36d4b94d615574d51fc18f2` |
| PPDF4 common Linux/macOS baseline | `#1381` | `63b4c2d10ad547305bad94372248bfcabb2cc3fb` |
| PPDF5 Windows admission | `#1382` | `4cbbab7c4f5025349c3aef35de8756c39fe6e6f6` |

PPDF5 merged from exact reviewed head
`af5c7c421a74b04c514e598295a5b20c4089e1a5`. All 16 required checks in
GitHub Actions run `36341422789` passed, including the real Windows document
suite. Review identified one Job Object drain race on the initial head; the
merged head retains job ownership until active process accounting reaches zero,
and re-review reported no remaining high-confidence issue.

Baseline: `origin/main` at `63b4c2d10ad547305bad94372248bfcabb2cc3fb`
on 2026-09-27. The existing
portable Linux/macOS form workflow remains authoritative while PPDF5 adds only
the Windows worker transport, containment, and qualified portable composition.
The rollback boundary for PPDF5 is the PPDF4 merge commit above. Residual
limits remain explicit: hybrid forms are Linux-only, Windows has no native
Poppler/Ghostscript tier, and the portable worker is not a general filesystem
or network sandbox.

## Objective

Make PDF inspection, extraction, rendering, field discovery, and ordinary
AcroForm filling available from the same MintClaw binary on Linux, macOS, and
Windows, without requiring Node.js, CGO, Poppler, or Ghostscript for
the portable baseline.

Linux may automatically add qualified native engines for independent
verification and hybrid-form support. Users should not have to select an
engine or understand the backend topology.

## Proposed engine and boundary

The first candidate is `github.com/klippa-app/go-pdfium` in WebAssembly mode.
It embeds a roughly 5.5 MiB PDFium module and runs it through the pure-Go
`wazero` runtime. The candidate accepts document bytes from memory and exposes
page count, text, dimensions, rendering, and form inspection without CGO or an
installed executable.

Admission is not implied by adding a module version. The candidate must prove:

- an explicitly empty WASI filesystem configuration, with no default root
  mount and no network authority;
- bounded memory, pages, pixels, output, and runtime under malformed input;
- cancellation that terminates the one-shot document worker;
- deterministic cleanup of PDFium documents, images, instances, and pools;
- license and embedded PDFium provenance, release, digest, and notices;
- compatibility with MintClaw's pinned Go toolchain and an audited transitive
  module graph that does not compile unused CGO, plugin, or experimental
  runtimes into the production binary;
- fixture parity on Linux AMD64 and macOS AMD64/ARM64;
- measured binary growth, cold startup, elapsed time, and peak RSS.

The WASM runtime stays inside the existing one-shot `mintclaw document
_worker`. WASM narrows engine authority; the process boundary still owns crash,
timeout, output, and memory reclamation.

## Backend composition

MintClaw should resolve one immutable backend set when a worker starts. Callers
request an operation, not an implementation, and there is no retry into a
different engine after an operation has begun.

| Role | Portable baseline | Qualified Linux addition |
| --- | --- | --- |
| Structural inspection | `pdfcpu` | same |
| Text extraction | PDFium/WASM | qualification-only Poppler oracle |
| Page rendering | PDFium/WASM | no runtime read/render fallback |
| Field discovery | `pdfcpu` | same |
| Standard AcroForm write | `pdfcpu` | same |
| Standard visible verification | PDFium/WASM | PDFium plus Poppler |
| Hybrid flatten verification | unavailable initially | Poppler plus Ghostscript |

This is additive capability composition, not an OS switch distributed through
the codebase. The portable engine provides consistent user-facing output;
qualified native engines add assurance or operations. If native dependencies
are missing or stale, portable operations remain available while native-only
hybrid operations fail closed.

Capabilities and reports must expose the selected backend identities and
effective isolation modes. They must not silently claim dual verification when
only the portable renderer ran.

## Management guardrails

- Keep the existing `inspectionBackend`, `readBackend`, `formFieldsBackend`,
  and `formWriteBackend` operation boundaries.
- Add one concrete backend-set resolver; do not create a generic plugin
  registry, service locator, or configuration flag per engine.
- Do not mount the host filesystem into WASM. Input arrives as bytes and output
  leaves as bounded values or worker-scratch artifacts.
- Keep one WASM instance at a time per worker. Do not introduce a persistent
  PDF daemon merely to amortize startup.
- Freeze backend selection for an operation. Capability loss produces a typed
  unavailable result, not a mid-operation semantic fallback.
- Keep protected values, durable jobs, approval, write journal, artifact
  adoption, delivery outbox, and recovery unchanged.
- Treat extraction reading-order differences as explicit backend semantics and
  qualify them; do not rewrite text heuristically to mimic Poppler.

## Delivery packets

### PPDF0: qualify the Go PDFium/WASM candidate

Scope:

- pin one `go-pdfium`, PDFium module, and `wazero` version;
- prove the selected version without an unrelated MintClaw Go toolchain bump
  and record the reachable production dependency graph;
- run the existing malformed, Unicode, reading-order, crop, rotation,
  image-only, extreme-dimension, and AcroForm fixtures through the candidate;
- prove an empty filesystem view and no network imports;
- measure binary size, cold operation time, repeated operation time, peak RSS,
  cancellation, and memory reclamation;
- freeze acceptable resource thresholds before production integration.

Completion gate:

- the candidate passes the security and fixture packet on Linux and macOS;
- unsupported behavior is enumerated with typed expected outcomes;
- provenance and notices are reproducible;
- evidence supports adoption, or this roadmap records rejection and stops
  without adding the dependency to production.

### PPDF1: add deterministic backend composition

Dependencies: PPDF0 and PRR1 from the reliability roadmap.

Scope:

- represent portable and native backend availability as one immutable backend
  set;
- report exact engine and isolation identities per operation;
- preserve current Linux behavior while the portable path is dark-launched in
  contract tests;
- add table-driven selection tests for Linux with and without qualified native
  dependencies and for supported macOS tuples.

Completion gate:

- selection logic has one owner and no caller contains OS-specific engine
  choice;
- capabilities distinguish portable, independently verified, and native-only
  operations;
- startup and operation failures remain typed and fail closed.

### PPDF2: admit portable inspect, extract, and render on macOS

Dependencies: PPDF1.

Scope:

- make the one-shot worker protocol run on Darwin AMD64 and ARM64;
- make `pdfcpu` inspection portable without weakening its limits;
- implement PDFium/WASM extraction and rendering behind the existing read
  contract;
- preserve immutable acquisition, descriptor-only input, private scratch,
  parent artifact validation, and cancellation;
- run the same operation manifests on Linux and macOS.

Completion gate:

- `acquire`, `inspect`, `extract`, and `render` advertise supported and pass
  real-process tests on both macOS architectures and Linux AMD64;
- malformed input cannot crash the gateway or access ambient files;
- output limits and artifact identities match the existing public contract;
- Linux rollback can select the preceding native-only release.

### PPDF3: admit portable standard AcroForm workflows

Dependencies: PPDF2.

Delivery split:

- PPDF3a admits ordinary `pdfcpu` field discovery on Darwin AMD64 and ARM64,
  preserves exact backend identity validation, and rejects every hybrid form
  outside Linux before the form backend runs. Existing in-flight jobs retain a
  narrow compatibility path for the prior inaccurate isolation revision, while
  every new worker result must carry the corrected exact identity;
- PPDF3b admits portable standard writing and PDFium/WASM visible verification,
  then proves the existing recovery, approval, and exactly-once delivery flow
  on Linux and macOS. Fill remained unavailable on macOS until that packet was
  complete.

Scope:

- make `pdfcpu` field discovery and standard AcroForm writing portable;
- use PDFium/WASM for visible readback on macOS and as the baseline renderer on
  Linux;
- retain source/schema/request digest binding, structural verification,
  protected values, approval, journaling, and exactly-once delivery;
- keep hybrid XFA/AcroForm flattening Linux-only until a second independent
  portable verifier is qualified.

Completion gate:

- ordinary AcroForm discovery, fill, visual verification, restart recovery,
  and delivery pass on Linux and macOS;
- Linux with admitted Poppler records additional independent verification;
- macOS never reports hybrid support or equivalent dual-render evidence;
- no raw protected value enters ordinary history, logs, traces, or artifacts.

### PPDF4: make the portable engine the stable read/render baseline

Dependencies: PPDF3 and PRR2 native provenance.

Scope:

- switch qualified Linux read/render output to the same portable engine used
  on macOS;
- retain Poppler and Ghostscript only for explicitly reported independent or
  native-only roles;
- remove Linux production branches that no longer own an admitted role;
- update deployment, doctor, SBOM, capability, and rollback documentation.

Completion gate:

- Linux and macOS produce the admitted portable contract over the same fixture
  manifests;
- native dependency absence does not disable portable operations;
- native dependency drift disables only the operation or verification tier
  that actually requires it;
- the old primary Poppler path is deleted rather than retained as an implicit
  fallback.

### PPDF5: admit Windows

Dependencies: stable PPDF4 evidence and a focused Windows process-boundary
admission.

Scope:

- define a path-free inherited-handle or pipe transport for the immutable
  snapshot because Go `ExtraFiles` is not portable to Windows;
- provide process-tree cancellation and a Windows job-object memory/process
  boundary;
- run the full portable fixture, acquisition, artifact, form, recovery, and
  delivery suites on Windows AMD64;
- keep native Linux engines absent from the Windows capability set.

Completion gate:

- Windows advertises only operations proved by real-process tests;
- worker termination closes descendants and private scratch;
- no local path or broader filesystem authority crosses the worker protocol;
- Linux and macOS behavior remains unchanged.

## Sequence and stop gates

```text
PPDF0 candidate proof
  |
  v
PPDF1 backend composition
  |
  v
PPDF2 portable read/render on macOS
  |
  v
PPDF3 portable standard forms
  |
  v
PPDF4 common Linux/macOS baseline
  |
  v
PPDF5 Windows admission
```

Each packet is an independently reviewable user outcome. Stop at PPDF0 if the
candidate cannot prove bounded execution or acceptable resource cost. Stop at
PPDF2 if portable reading is sound but form verification is not; do not weaken
PDF2/PDF3 guarantees to claim parity. A requirement for a daemon, Node.js,
download-at-runtime, host-root WASM mount, or engine-specific public API
triggers an architecture checkpoint.

Linux/macOS phase completion requires PPDF0-PPDF4. Full cross-platform
completion additionally requires PPDF5. Hybrid-form parity outside Linux is
not part of either completion claim until an independent verifier is separately
admitted.
