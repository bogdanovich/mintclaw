# Private Linux window-key prerequisite

Status: explicit, default-off **library-test candidate**, not an admitted input
driver, computer tool or production capability. This is part of the
[computer-use roadmap](computer-use-roadmap.md). The existing pinned Unicode
driver build and its `artifact.py` provenance/admission format are unchanged.

## Why a separate primitive

The pinned Cua AT-SPI implementation correlates accessibility frames to X11
windows using geometry (and accepts a single frame without correlation). That
is not an exact native-window identity proof. Its generic input routes can also
fall back to different delivery mechanisms. Neither behavior is admitted here.

`window_keys.rs` is overlaid onto the exact Cua source commit
`0c69d9a2c8abbb5051a2df4a23f0fa166d5385dc` by `window-keys.patch` and the
private probe builder. Only the explicit `mintclaw-window-keys` Cargo feature
exports it. Default and portal feature sets and ordinary tool registration do
not change. No build/install is performed on a production host.

The primitive sends one chord of at most eight distinct, existing X11 keycodes
to the supplied window with `SendEvent(propagate=false)`. It rejects root
windows and the special PointerWindow/InputFocus destinations. It does not use
AT-SPI, name/geometry matching, activation, XTest, the clipboard, keyboard-map
changes, pointer delivery, a replacement connection or a fallback route.
The existing modifier mapping is read, not changed; event masks carry only
modifiers from this chord, not whatever keys the human currently holds.

## Ownership and honest outcomes

The caller must already own the original native-window/process guard, desktop
input lease and exact server/connection binding. A numeric XID is not a native
incarnation or authority. This low-level primitive does not mint that authority
or close the independently owned app. Reuse of an XID and guard/dispatch atomicity
are not qualified by these tests and still block production admission.

A nonblocking stop callback is checked before admission, before each press and
after the last press. Once observed, stop prevents subsequent presses. Every
attempted press incurs a reverse-order release attempt, including an uncertain
press. Cleanup continues after an error. A failed press/release produces
`Unknown`, never an automatic retry or a claimed rollback.

Counters acknowledge checked X-server requests only. `Completed` does **not**
prove that an application accepted synthetic input. `Stopped` does **not** mean
no earlier effect occurred. Release acknowledgements do **not** replace the
host's independent held-state/effect attestation. Stop is cooperative between
requests; the primitive does not preempt a blocked native request, impose socket
deadlines or guarantee cleanup after a panic/transport loss. Those remain
separate native gates before any input exposure. The existing durable invocation
ledger remains the future no-replay owner; this candidate adds no new ledger.

## Reproducible qualification, not a task score

`build-linux-key-probe.sh` accepts only a dedicated clean checkout at the exact
pin and a new private output directory. It builds a library-test executable with
Rust 1.97.1 and the explicit feature. Its checksum receipt is build metadata,
**not** an `artifact.py` manifest or an authorization/qualification token.

The separate CI job builds on an ephemeral runner and uses its own Xvfb server.
Six deterministic tests cover validation, stop boundaries, modifier state,
partial/uncertain presses and every combination of release failures. The builder
checks the exact unit/native test counts to reject empty-filter success. Four opt-in native
tests repeat twenty times using only newly created, unmapped X11 windows and an
independent fixture connection. They verify exact event destinations despite
identical geometry, unchanged focus/guard, a stop after an independently observed
press followed only by its release, no parent propagation and no fallback after
target destruction. No screenshot or existing window content is read.

These are protocol-fixture tests, not GTK/Chromium/Unicode support, an attached
Linux qualification, a macOS test or the twenty representative model tasks.
The retained native host integration, dispatch-incarnation fence, bounded
transport stop, independent cleanup/effect proof, other input kinds and supported
application qualification must land separately before an opt-in input pilot.
