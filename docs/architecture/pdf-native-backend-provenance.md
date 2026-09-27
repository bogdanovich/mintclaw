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
| Poppler 24.02.0 | `poppler-utils=24.02.0-1ubuntu9.9` | production extraction, rendering, and form visual verification | `pdftotext`: `0fb98ea179e19154a90202608c164f2a319b79f16576fa6534b2d601033565e7`; `pdftoppm`: `207dcabcaeea0ce572aefc498d07d44d56a9ca06a85b3ae1fecd050476a34bf8`; `pdfinfo`: `3293dda06d80e1e38dab859aa47368c2876aedc41cbc2e24e8fb9a4e66392078` |
| Ghostscript 10.02.1 | `ghostscript=10.02.1~dfsg1-0ubuntu7.9` | independent hybrid-form visual verifier | `gs`: `eed795c04354a20cecc95a21155b560b55094330c279459b68037984b7c23667` |

The Ghostscript revision is the Ubuntu Noble security update qualified after
USN-8791-1. The Poppler revision and executable identities are unchanged from
PDF1A. `libpoppler134=24.02.0-1ubuntu9.9` and
`libgs-common=10.02.1~dfsg1-0ubuntu7.9` are part of the corresponding host
bundle. The qualified fonts remain `fonts-dejavu-core=2.37-8` and
`fonts-liberation=1:2.1.5-3`. The confinement boundary is qualified with
`bubblewrap=0.9.0-1ubuntu0.3` and the recorded `/usr/bin/bwrap` digest.

## Diagnostics

`mintclaw document capabilities --json` reports the expected and observed
digest for every native executable together with the exact package revision,
role, isolation mode, and effective state. A missing or unknown executable is
`unavailable`; its reason names the package revision needed to restore the
admitted backend. Poppler unavailability withholds operations that require its
read or visual-verification role. Ghostscript unavailability withholds the
independent hybrid verifier without weakening ordinary form admission.

`mintclaw doctor` uses the same capability report. It emits
`document.native_backend_unavailable` as an actionable warning and remains
read-only: it does not execute a parser, install a package, or contact the
network. It does execute a no-input Bubblewrap probe before reporting a native
backend as supported.

## Qualification

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
runs extraction, rendering, ordinary-form, and hybrid-form real-process
fixtures, and proves denial of an unmounted host file and host network. Success ends with
`MINTCLAW_DOCUMENT_NATIVE_BACKEND_QUALIFICATION_OK`.

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
progress.

## Rollback

Before deployment, retain the previous MintClaw binaries and the exact package
set from that deployment's `result.json`. Rollback restores those binary bytes
and installs the recorded package revisions as one pair. For this qualified
bundle the native packages are:

```sh
sudo apt-get install --no-install-recommends \
  bubblewrap=0.9.0-1ubuntu0.3 \
  poppler-utils=24.02.0-1ubuntu9.9 \
  libpoppler134=24.02.0-1ubuntu9.9 \
  ghostscript=10.02.1~dfsg1-0ubuntu7.9 \
  libgs-common=10.02.1~dfsg1-0ubuntu7.9
```

After restoring the recorded MintClaw commit and packages, rerun document
capabilities and the qualification command before admitting traffic. If an
exact package is unavailable, leave the affected backend unavailable; never
substitute a different executable digest during rollback.
