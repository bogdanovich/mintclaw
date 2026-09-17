# PDF0B Inspection Backend Decision

## Decision

MintClaw uses `github.com/pdfcpu/pdfcpu` `v0.15.0` as the sole PDF0B production inspection backend on
`linux/amd64`. It is linked into the existing one-shot document worker and is never called in the gateway/core
process. Poppler `24.02.0` (`pdfinfo`, `pdftotext`, and `pdfsig`) is the independent, test-only oracle.

No production operation shells out to Poppler, Python, Node.js, a browser, or a downloaded executable. There is no
fallback parser: a missing or unsupported backend fails closed. Inspection does not authorize extraction, rendering,
password handling, signature trust validation, form writes, XFA rendering, or any PDF1A behavior.

## Why pdfcpu

- It is an Apache-2.0 Go library and CLI with an `io.ReadSeeker` API. `ReadAndValidate` accepts the already-open
  immutable descriptor, so the worker never needs the source path. See the
  [pdfcpu repository](https://github.com/pdfcpu/pdfcpu) and
  [integration API](https://github.com/pdfcpu/pdfcpu/blob/v0.15.0/pkg/api/api.go).
- The pinned [v0.15.0 release](https://github.com/pdfcpu/pdfcpu/releases/tag/v0.15.0) includes malformed-input,
  encryption, form, object, and signature hardening. MintClaw still treats the parser as untrusted and runs it only
  behind PDF0A timeout, output, process-group, environment, descriptor, and scratch limits.
- One linked backend keeps installation atomic with the MintClaw binary. No dependency is resolved or downloaded
  during an operation. `go.mod` and `go.sum` pin the module and its checksums; `go version -m <binary>` exposes the
  shipped module graph for SBOM generation.
- The backend produces the structural evidence PDF0B needs: version, page count, encryption/password disposition,
  signed-field and timestamp markers, certification and restriction dictionaries, AcroForm/XFA structure, and
  decoded page content streams.
- On the PR base, equivalently stripped `linux/amd64` binaries measured 86,098,082 bytes without PDF0B and
  89,432,226 bytes with PDF0B, a 3,334,144-byte increase. A two-page inspection on the deployed host measured 0.18
  seconds elapsed and 53,296 KiB maximum RSS. These are qualification observations, not service SLOs; the exit
  record captures the final merged binary and rollback artifact sizes.

The dependency adds `github.com/hhrutter/tiff` `v1.0.6` (BSD-3-Clause) and `go.yaml.in/yaml/v3` `v3.0.5`
(MIT/Apache-2.0 by file) to the resolved graph. Existing MintClaw versions already satisfy pdfcpu's `x/crypto`,
`x/image`, and `x/text` requirements. These pinned modules and their license files are the new PDF0B SBOM inputs.
The production code is compiled only by `inspection_backend_linux_amd64.go`; other runtime tuples compile the
unavailable implementation.

## Normalization boundary

The worker converts backend objects into a closed, parent-validated `mintclaw.document_report.v1` vocabulary. String
values, warnings, count ranges, aggregate states, and cross-field coherence are allowlisted. A worker cannot place an
arbitrary parser string, path, or document content into a successful report.

`extractable_text` is deliberately a structural signal, not extracted text. A page is `present` when its decoded
content contains a text-showing operator inside a text object, `absent` when it does not, `mixed` across different
page outcomes, and `unknown` when the backend cannot inspect it. PDF1A will own actual text extraction and provenance.
Names, strings, arrays, dictionaries, and comments are tokenized without treating their bytes as operators. A page
containing an inline image remains `unknown` unless text was already proven before the image: binary inline-image
termination is filter-dependent, so PDF0B refuses to interpret its payload as content syntax.

Each page-content stream is decoded with the remaining aggregate content budget before page assembly. The worker
rejects a single expansion or a multi-stream cumulative expansion at 8 MiB; it never calls pdfcpu's unbounded
`ExtractPageContent` path. Form, XFA, and signature-restriction facts come only from objects linked by the catalog or a
validated signed field. Unreferenced xref objects cannot assert those facts.

Signature facts classify structural evidence only. Synthetic signatures deliberately do not claim cryptographic,
certificate-chain, legal, LTV, or trust validity. A signed document may therefore report `timestamped: unknown` when
PDF0B cannot prove an explicit document-timestamp signature.

## Independent oracle

`scripts/document-inspection-oracle.sh` compares the checked-in manifest with Poppler. The executable gate is:

```sh
PDF0B_POPPLER_VERSION=24.02.0 make test-document-oracle
```

The oracle agrees on version, page count, password refusal, form class, per-page text presence, and ordinary signed
fields. Its known differences are part of the executable output:

- `pdfinfo` distinguishes XFA but does not report MintClaw's XFA stream/packet subtype or dynamic/static marker;
- `pdfinfo` does not expose the normalized AcroForm field count;
- `pdfsig` does not enumerate the catalog's usage-rights signature as an ordinary form signature;
- `pdfsig` does not provide MintClaw's structural certification normalization; and
- Poppler may repair an object/xref structure that the stricter production admission rejects.

Those differences do not become fallbacks or majority votes. The stricter production result wins, and every expected
disagreement is named by the oracle harness.

## Rejected production candidates

### ClawPDF/PDFium

[ClawPDF](https://github.com/openclaw/clawpdf) `0.3.2` is MIT-licensed, requires Node.js 22 or newer, and ships a
5,584,556-byte unpacked package including PDFium WASM. Executable probes extracted the expected text and returned exit
status 4 for the password fixture. Its CLI JSON is intentionally extraction-oriented: it does not expose the complete
signature, certification, restriction, AcroForm, or XFA fact set required by PDF0B. It remains a useful candidate for
the later PDF1A render/extract decision, but adding Node and WASM beside the existing worker would not improve PDF0B.

### pikepdf/qpdf

[pikepdf](https://pikepdf.readthedocs.io/en/stable/) `10.13.0.post1` correctly probed version, pages, XFA, and password
refusal. It is an MPL-2.0 Python wrapper over native C++ qpdf. The qualification host had the Python wheel but no qpdf
CLI. Shipping and supervising Python plus a native wheel introduces a second runtime and platform packaging surface
without a fact-quality advantage over the linked Go backend, so it is rejected for production and oracle roles.

### Poppler and browser PDF viewers

[Poppler](https://poppler.freedesktop.org/) is deliberately retained only as an independently implemented system
oracle. Shipping its native utilities inside the production worker would add process parsing and distribution
dependencies while still leaving the declared fact gaps above. A browser PDF viewer is interactive rendering UI, not
a stable structural-inspection API; it cannot provide deterministic headless facts or typed refusal semantics.

## Evidence inventory

- `pkg/document/testdata/inspection-manifest.json` records every checked-in digest, construction, license, expected
  normalized facts, terminal disposition, and evidence test.
- `pkg/document/testdata/generate/main.go` deterministically generates all non-encrypted fixtures from synthetic bytes.
  The AES-256 fixture was produced once with pdfcpu `v0.15.0`; its temporary user and owner passwords were discarded
  and are not stored in the repository.
- Backend contract tests cover text, image-only, mixed pages, name operands, inline images, AcroForm, XFA-only, hybrid
  XFA, unsigned, signed, certified, FieldMDP, timestamp, usage-rights, orphan xref objects, password-required,
  truncated, malformed-xref, invalid-length, nested, page-limit, single-stream decode limits, and cumulative
  multi-stream content limits.
- Real-process tests cover descriptor-only input, scrubbed environment, output limits, timeout, crash, descendant
  termination, concurrent isolation, and deterministic worker scratch cleanup.
- `scripts/document-worker-smoke.sh` exercises the packaged CLI success and password-required paths without retaining
  a source or scratch path. `scripts/document-deployed-smoke.sh` repeats the exact checked-in success fixture after
  merged-main deployment.

## Platform and rollback

Only `linux/amd64` advertises inspection. Linux ARM, Windows, `darwin/amd64`, `darwin/arm64`, and all other tuples
return `unsupported_platform` before opening the input. macOS remains an explicit later roadmap parity lane.

Rollback is binary-atomic: restore the preceding MintClaw binary and restart only affected MintClaw gateway units.
No schema migration, persistent document state, daemon, config key, or mutable PDF artifact is introduced by PDF0B.
