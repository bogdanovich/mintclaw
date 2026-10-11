# Native PDF Backend Provenance

## Contract

MintClaw admits native PDF executables only on `linux/amd64` and only when the
bytes match the package revisions qualified below. The manifest in
`pkg/document/backend_manifest.go` is the single runtime source for package
revision, functional role, isolation mode, executable path, and SHA-256.
Capability discovery reads and hashes the executables without running them.
Every operation copies the admitted bytes into a sealed executable snapshot
and rejects a missing or changed executable before parsing a document.
Native operations also require the mandatory policy documented in
[Native PDF Confinement](pdf-native-confinement.md).

| Backend | Ubuntu package | Functional role | Executable SHA-256 |
| --- | --- | --- | --- |
| Poppler 24.02.0 | `poppler-utils=24.02.0-1ubuntu9.10` | independent standard-form visual verifier and primary hybrid-form visual verifier | `pdftotext`: `88263e2333fa712d6eadfc69dd2daf826792a67abb14f4d2110673d5f195b154`; `pdftoppm`: `ab7c91f0f144335ad7696f4f23aefbd564ef67c099290bd8f813cdbb47cd1e9e`; `pdfinfo`: `b81cc1b1588d7ce8e2c384130c2e5d5d8e339e738e615cbe1ef7dc9819ef2bbe` |
| Ghostscript 10.02.1 | `ghostscript=10.02.1~dfsg1-0ubuntu7.9` | independent hybrid-form visual verifier | `gs`: `eed795c04354a20cecc95a21155b560b55094330c279459b68037984b7c23667` |

The Ghostscript revision is the Ubuntu Noble security update qualified after
USN-8791-1. The Poppler security bundle supersedes the older PDF1A pin; historical
exit reports retain their original evidence. `libpoppler134=24.02.0-1ubuntu9.10` and
`libgs-common=10.02.1~dfsg1-0ubuntu7.9` are part of the corresponding host
bundle. The qualified fonts remain `fonts-dejavu-core=2.37-8` and
`fonts-liberation=1:2.1.5-3`. The confinement boundary is qualified with
`bubblewrap=0.9.0-1ubuntu0.3` and the recorded `/usr/bin/bwrap` digest.

The current Poppler bundle is the official Noble security package
[24.02.0-1ubuntu9.10](https://packages.ubuntu.com/noble/amd64/poppler-utils). Its amd64 package SHA-256 values are
`19ce4557ad89350506a0f1c33ad4c5a6aa7469e48c78aacae351a59b43ee20c6` for `poppler-utils` and
`2974536ea4ce7d75e54923b1bab0f251f407d1c291427ddc7c53da65e1c505b7` for `libpoppler134`.
Their metadata was checked against the `20261011T000000Z` snapshot: the Noble security `InRelease` verifies with
Ubuntu Archive Automatic Signing Key (2018), fingerprint `F6ECB3762474EDA9D21B7022871920D1991BC93C`;
the downloaded `Packages.xz` matches that signed release's SHA-256, and the package hashes match its entries.
The extracted utilities match the installed target bytes exactly. This establishes origin, not functional acceptance;
the qualification command and unchanged visual oracle remain mandatory before merging a manifest refresh.

Qualification also exercises form appearance generation with host PDF configuration disabled. When an AcroForm has
no form-font resource dictionary, the writer prepares only its selected widgets using the existing complete-font
policy before filling. ASCII uses the PDF core Helvetica font, retaining the default appearance's size and color;
Unicode still requires admitted glyph support. Existing form-font resources are not replaced by this fallback.
This avoids pdfcpu's implicit external Roboto lookup without loading host configuration, changing source fixtures,
or removing visual verification.

## Diagnostics

`mintclaw document capabilities --json` reports the expected and observed
digest for every native executable together with the exact package revision,
role, isolation mode, and effective state. A missing or unknown executable is
`unavailable`; its reason names the package revision needed to restore the
admitted backend. Poppler unavailability leaves portable acquisition,
inspection, extraction, rendering, and field discovery available, but
withholds Linux form operations that require independent or hybrid visual
verification. Ghostscript unavailability withholds the independent hybrid
verifier without weakening portable read/render or ordinary form discovery.

`mintclaw doctor` uses the same capability report. It emits
`document.native_backend_unavailable` as an actionable warning and remains
read-only: it does not execute a parser, install a package, or contact the
network. It does execute a no-input Bubblewrap probe before reporting a native
backend as supported.

## Qualification

PR integration CI runs on Ubuntu 24.04 and installs the exact admitted package
bundle from the Ubuntu archive snapshot `20261011T000000Z`. Signed APT metadata
and explicit package revisions make preparation reproducible when the live
archive advances. This is test-host preparation, not admission of a new native
backend or a production package rollback. The snapshot service is documented
at [Ubuntu Snapshot Service](https://snapshot.ubuntu.com/).

Run qualification from the exact MintClaw source revision on the prepared
Ubuntu host:

```sh
make qualify-document-native-backends \
  EVIDENCE_DIR=/private/path/native-backend-evidence
```

The command does not download or install anything. It requires a clean source
tree, pins the evidence to its unchanged `HEAD`, builds and tests a fresh
`git archive` of that commit, and runs Go with `GOTOOLCHAIN=local`,
`GOPROXY=off`, and `GOSUMDB=off`; ignored worktree files and a missing prepared
toolchain or module therefore fail closed. Its result validation runs Python
in isolated mode and uses explicit failures that remain active under Python
optimization. It validates all seven exact package revisions, records
executable digests, verifies the capability manifest and confinement identity,
runs ordinary-form and hybrid-form visual verification fixtures, and proves
sealed executable identity plus denial of an unmounted host file and host
network. Success ends with
`MINTCLAW_DOCUMENT_NATIVE_BACKEND_QUALIFICATION_OK`.

The capability report also contains the existing `pdfcpu` and `pdfium-wasm` portable backends. Qualification checks
the exact combined backend-name set, then applies native executable/package/isolation assertions to Poppler and
Ghostscript and records those two native backends. It neither mistakes portable identities for native executables
nor accepts an unexpected supplemental backend.

The private evidence directory contains:

- `capabilities.json`, including expected and observed backend identities;
- `packages.txt` and `executables.sha256` for the host bundle;
- `fixtures.txt` with the real-process fixture results; and
- `result.json`, which binds the successful evidence and rollback package set
  to the exact MintClaw commit.

An update is not admitted by changing a digest alone. Update the package
revision and all affected executable identities together, run this command on
the target distribution, retain the fixture evidence, and pass the normal CI
and review gates. Unknown bytes remain unavailable while qualification is in
progress. Poppler text and rendering exercised by form verification are
independent oracle evidence for the native package; production read/render do
not dispatch to those executables.

## Rollback

Before deployment, retain the previous MintClaw binaries and the exact package
set from that deployment's `result.json`. Rollback restores those binary bytes
and installs the recorded package revisions as one pair. For this qualified
bundle the native packages are:

```sh
sudo apt-get install --no-install-recommends \
  bubblewrap=0.9.0-1ubuntu0.3 \
  poppler-utils=24.02.0-1ubuntu9.10 \
  libpoppler134=24.02.0-1ubuntu9.10 \
  ghostscript=10.02.1~dfsg1-0ubuntu7.9 \
  libgs-common=10.02.1~dfsg1-0ubuntu7.9
```

After restoring the recorded MintClaw commit and packages, rerun document
capabilities and the qualification command before admitting traffic. If an
exact package is unavailable, leave the affected backend unavailable; never
substitute a different executable digest during rollback. Native package
rollback does not change the portable read/render engine; restoring the old
Poppler primary requires rolling the MintClaw binary back to the PPDF3b merge
boundary recorded in the portable roadmap.
