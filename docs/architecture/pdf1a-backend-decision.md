# PDF1A Read and Render Backend Decision

## Decision

PDF1A uses the Ubuntu `poppler-utils` `24.02.0-1ubuntu9.9` executables as its sole production text
and page-render backend on `linux/amd64`:

- `/usr/bin/pdftotext` for bounded per-page UTF-8 extraction;
- `/usr/bin/pdfinfo` for crop-box, rotation, and pixel-budget preflight; and
- `/usr/bin/pdftoppm` for one PNG per selected page.

The backend is available only when all three executables match the admitted SHA-256 identities for
the qualified upstream version `24.02.0`:

| Executable | SHA-256 |
| --- | --- |
| `pdftotext` | `0fb98ea179e19154a90202608c164f2a319b79f16576fa6534b2d601033565e7` |
| `pdftoppm` | `207dcabcaeea0ce572aefc498d07d44d56a9ca06a85b3ae1fecd050476a34bf8` |
| `pdfinfo` | `3293dda06d80e1e38dab859aa47368c2876aedc41cbc2e24e8fb9a4e66392078` |

ClawPDF `0.3.2`, using bundled PDFium release `7902`, is the independent test oracle. Its npm
tarball SHA-1 is `adf056702e1c64aa8fdc70a50760cd2075898c64`; the observed tarball SHA-256 is
`e0623fd3a083e331b25c3f6e36c87876dfb6858318446b6d93b42842c3cca4a1`; and its bundled
`pdfium.esm.wasm` SHA-256 is
`f3fe52ae7f150e912a8379ec4478cac9c11b4135dc56fdc039b0ff885f1c0981`. ClawPDF is never a
production fallback and is not installed in the MintClaw runtime.

## Why Poppler is production

Both candidates passed text-marker and rendered-pixel comparison over the checked-in synthetic
fixtures. The executable oracle harness reports `MINTCLAW_PDF1A_ORACLE_OK`; at 144 DPI its mean
normalized pixel differences were `0.000108` for ordinary text, `0.001599` for crop plus rotation,
and `0.000385` for the AcroForm appearance fixture, all below the admitted `0.08` tolerance.

Poppler is selected because the deployed Ubuntu host already carries the exact supported package,
it accepts PDF bytes through standard input, and it can run as a short-lived descendant of the
existing document worker without Node.js, WASM initialization, a vendored 5.5 MiB package, or a new
runtime installation path. A one-page end-to-end CLI sample on that host measured:

| Operation | Elapsed | Maximum RSS |
| --- | ---: | ---: |
| extract | 0.32 s | 54,608 KiB |
| render | 0.44 s | 54,924 KiB |

ClawPDF remains a strong independent implementation: it is MIT-licensed, has no runtime dependency
or post-install script, requires Node.js 22 or later, and bundles its own PDFium WASM. Its unpacked
package measured 5,584 KiB and its tarball 2,440 KiB. Those properties make it useful for detecting
Poppler-specific extraction or raster behavior without sharing production code.

The deliberately ambiguous reading-order fixture demonstrates a real semantic difference: Poppler
`-layout` orders the two positioned strings visually, while PDFium reports content-stream order.
PDF1A therefore requires both page markers and records that order as backend-specific; it does not
declare either ordering universally correct. Unicode, ordinary text, page identity, crop, rotation,
dimensions, scan rendering, and AcroForm appearance remain comparable.

`pdfcpu` `v0.15.0` remains the sole structural inspection authority. It is not used as a general
text or page-raster renderer. Browser automation, provider-native PDF input, `pypdf`, `pikepdf`,
runtime `npx`, and model-authored shell pipelines remain outside the production path.

## Isolation and artifact boundary

The gateway never invokes Poppler directly, including during capability discovery. Runtime
admission reads each executable without following a symlink and checks its exact digest without
executing it. Inside the one-shot worker, executable bytes are copied and hashed in one stream into
a sealed Linux `memfd`; each Poppler child is launched only from that immutable snapshot at
`/proc/self/fd/3`. Path replacement and in-place mutation therefore cannot change the admitted bytes
after verification. PDF0A first copies and hashes the source into one
read-only operation snapshot. The existing private `mintclaw document _worker` child receives that
snapshot through inherited descriptor 3, verifies its size and digest, and launches one absolute
Poppler executable with a scrubbed environment, empty `PATH`, private working directory, 30-second
deadline, bounded output, process-group cancellation, and parent-death signal.

PDF input reaches Poppler only through standard input. Poppler never receives the original path.
Extraction writes one allowlisted JSON Lines name and rendering writes one allowlisted PNG name per
selected page inside worker scratch. After the entire descendant process group has stopped, the
parent rejects undeclared, missing, symlinked, non-regular, wrong-size, wrong-digest, wrong-MIME,
wrong-page, excessive-byte, excessive-dimension, and excessive-pixel results. Only a fully validated
set is atomically adopted under operation ownership and exposed by opaque artifact ref. Worker JSON
never includes content bytes or a local path.

Rendering uses `pdfinfo -box` to preflight the effective CropBox and rotation before allocation,
then `pdftoppm -cropbox -r <dpi>`. The returned dimensions must exactly match preflight. XFA render
is refused. Text extraction uses `pdftotext -layout -nopgbrk -enc UTF-8`, one selected page at a
time, and returns `text_unavailable` when the selected set has no text.

## Packages, fonts, and notices

The qualified Ubuntu 24.04 bundle is:

```text
poppler-utils       24.02.0-1ubuntu9.9  718 KiB installed
libpoppler134       24.02.0-1ubuntu9.9  3,618 KiB installed
fonts-dejavu-core   2.37-8              2,292 KiB installed
fonts-liberation    1:2.1.5-3          4,285 KiB installed
```

Poppler links the distribution's FreeType, Fontconfig, LCMS, JPEG, OpenJPEG, PNG, TIFF, NSS, and
other normal shared libraries. The distribution copyright file is
`/usr/share/doc/poppler-utils/copyright`; its aggregate licensing includes GPL-2-or-GPL-3 and named
Apache/GPL component notices. ClawPDF carries `LICENSE` and `THIRD_PARTY_NOTICES.md` in its package;
its PDFium payload includes BSD-style and Apache-2.0 notices. These packages, font packages, exact
versions, executable hashes, and notice paths are the PDF1A SBOM inputs.

No backend or font is downloaded while processing a document. Reproducible host preparation is:

```sh
sudo apt-get update
sudo apt-get install --no-install-recommends \
  poppler-utils=24.02.0-1ubuntu9.9 \
  libpoppler134=24.02.0-1ubuntu9.9 \
  fonts-dejavu-core=2.37-8 \
  fonts-liberation=1:2.1.5-3
mintclaw document capabilities --json
```

The capability check must show `extract` and `render` as supported before traffic is admitted. A
package revision or executable change intentionally disables both operations until a focused update
requalifies its fixtures, oracle comparison, hashes, deployment, and rollback.

## Reproduce the oracle

Fetch and unpack the oracle during qualification, not during document processing:

```sh
npm pack clawpdf@0.3.2
shasum -a 256 clawpdf-0.3.2.tgz
mkdir /tmp/clawpdf-0.3.2
tar -xzf clawpdf-0.3.2.tgz -C /tmp/clawpdf-0.3.2
PDF1A_CLAWPDF_ROOT=/tmp/clawpdf-0.3.2/package make test-document-read-oracle
```

The harness checks Node.js 22+, the exact package version, normalized page markers, fixed 144 DPI,
dimensions, and mean decoded RGB pixel difference. It does not compare compressed PNG bytes. The
read manifest records every fixture digest, selected pages, supported operation, expected state,
failure, truncation, tolerance, and evidence test.

## Update and rollback

An update changes the package revision, executable hashes, decision record, manifest, oracle proof,
and deployed evidence in one focused reviewed change. Rollback restores the preceding MintClaw
binary and package versions together, verifies all three executable hashes, checks `document
capabilities`, and runs the read smoke. If the exact backend cannot be restored, leave `extract` and
`render` unavailable; inspection and acquisition remain independent.

macOS amd64/arm64, Linux architectures other than amd64, XFA, passwords, OCR, and provider-native
PDF transport remain unavailable and are retained as later roadmap work.
