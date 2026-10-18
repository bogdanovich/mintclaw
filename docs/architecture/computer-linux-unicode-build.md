# Private Linux Unicode driver qualification

Status: implementation in progress; no patched native result is claimed yet.
This is an explicit private build/fixture path, not production installation or
an input grant. The existing read-only artifact pins and default configuration
are unchanged.

## Exact correction

The pinned Cua Driver 0.34.1 source is
`0c69d9a2c8abbb5051a2df4a23f0fa166d5385dc`. Its two AT-SPI EditableText insertion
calls pass a Unicode character count as the `length` argument. GNOME documents
that argument as a [UTF-8 byte count](https://gnome.pages.gitlab.gnome.org/at-spi2-core/libatspi/method.EditableText.insert_text.html),
while the caret offset is in characters. The previous native fixture independently
observed truncated mixed-language text even though the driver returned success.

`scripts/internal/computer-driver/atspi-utf8-length.patch` changes only those two
byte counts, adds checked conversion to the API's signed 32-bit length and tests
ASCII, Cyrillic, CJK, combining characters, emoji and Arabic. It does not alter
root selection, authorization, fallbacks, clipboard behavior or driver version
negotiation. Existing insertion/replacement fallbacks are not newly qualified by
this patch.

The newer upstream 0.36.0 source still has the same character-count calls, so a
version upgrade alone is not the correction. The patched artifact is explicitly
identified as `mintclaw-atspi-utf8-v1`, not represented as an unmodified upstream
release or selected silently as a replacement backend.

## Private build and provenance

The `Private Computer Driver` workflow uses an ephemeral Ubuntu 24.04 runner,
the exact upstream source commit, its locked Cargo dependencies, Rust 1.97.1 and
the upstream `cua-driver/portal-input` feature. No Rust toolchain or large build
tree is installed on the deployed Linux host. The initial build tuple is Linux
x86_64 with glibc 2.39; it is not a general portable-Linux release claim.

For an explicitly prepared clean private source checkout and toolchain:

```sh
bash scripts/internal/computer-driver/build-linux.sh \
  --source /absolute/private/cua-source \
  --output /absolute/new/private-artifact
```

The builder refuses an unexpected commit, dirty source or an existing output
directory. It applies the exact patch, runs the Rust length test, builds the
worker, checks the patched source remained unchanged and emits the binary plus
private metadata. Metadata records upstream commit/version, patch identity and
digest, toolchain, target, features, Cargo.lock, patched source and binary SHA256.
These are repeatable pinned build inputs and exact artifact receipts, not a
promise of bit-identical binaries across arbitrary build hosts or a cryptographic
attestation of an untrusted builder. Select artifacts only from the approved
exact-head build; metadata never grants desktop authority.

## Native fixture gate

The runner accepts the patched Linux artifact only through explicit private
metadata selection:

```sh
bash scripts/computer-native-qualification.sh \
  --driver /absolute/private-artifact/cua-driver \
  --driver-manifest /absolute/private-artifact/manifest.json \
  --case UnicodeInsertion
```

The verifier requires the exact source, patch, feature and toolchain values and
binary digest; it rejects duplicate/unknown keys, unsafe files, symlinks and
oversized metadata. An explicit selected X11/DBus environment and the supplied
disposable fixture are still required. This path neither grants permissions nor
discovers personal applications. All worker telemetry and accessibility
advertisement remain disabled.

The original exact Unicode insertion assertion remains. Added GTK cases prepare
nonempty synthetic prefix/suffix text and a character-based caret through the
fixture's own nonce-correlated IPC, then insert ASCII, Cyrillic, CJK, accents,
combining text, a ZWJ emoji and Arabic. The fixture independently reports the full
expected prefix + inserted text + suffix, with the guard window unchanged.
Full-value replacement cannot satisfy these assertions. Preparation is owned
test setup, not a public control or clipboard operation.

The CI fixture runs on its own Xvfb and DBus session; staged GNOME X11 testing
must still qualify the actual selected Linux desktop. Neither qualifies general
AT-SPI/native-window root identity, pixel input, cancellation, macOS input or a
public input surface. Those remain gates in the
[computer-use roadmap](computer-use-roadmap.md).
