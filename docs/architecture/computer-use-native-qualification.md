# Computer use native qualification

MintClaw's desktop contracts, read-only host, and private Cua adapter are opt-in
infrastructure. Public computer tools and companion commands remain disabled
by default and require separate explicit grants.
Native qualification tests a real driver against disposable applications before
any desktop input route is exposed to agents.

## Reproduce the tests

The runner creates two native fixture windows: one admitted target and one
non-admitted guard. It checks effects from the application's own state events,
not from a driver's success receipt. Fixture values are synthetic; existing
applications and accounts are not captured. Closing the worker must leave the
fixture alive; final test cleanup closes only the owned fixture.

Stage the official Cua Driver 0.34.1 binary privately, without running its
installer. The runner and adapter verify the executable checksum. They do not
download, install, reset permissions, or modify production configuration.

```sh
bash scripts/computer-native-qualification.sh --driver /absolute/path/cua-driver
```

On Linux, supply the intended X11 session's `DISPLAY`, `XAUTHORITY`, and, when
needed, `XDG_RUNTIME_DIR` and `DBUS_SESSION_BUS_ADDRESS`. Python 3 with GTK 3
PyGObject is required. On macOS, `swiftc` builds the AppKit fixture; the process
launching the worker needs actual OS Accessibility and Screen Recording grants.
macOS process ancestry alone does not prove TCC attribution.

`--case ObservationLifecycle` or another named test isolates a diagnostic case.
A partial pass never qualifies omitted capabilities. An optional absolute
`MINTCLAW_COMPUTER_NATIVE_TEST_BINARY` selects a cross-built test executable;
normal runs compile it in a temporary directory. Unit tests skip native
qualification unless the explicit driver environment is supplied.

## Pinned driver

The release is
[Cua Driver 0.34.1](https://github.com/trycua/cua/releases/tag/cua-driver-rs-v0.34.1),
at source commit `0c69d9a2c8abbb5051a2df4a23f0fa166d5385dc`.

| Platform | Executable SHA256 |
| --- | --- |
| Linux x86_64 | `fd922e708099aae311873a6667beb48eaabb8c15c3abec744492db8a96b544ee` |
| macOS universal | `56bc881744bfb956d44386e6f6ccb2f6fc7f5cef47be25cc056e08265cdf4c7d` |

The adapter uses embedded mode, exact PID/window capabilities, a minimal
environment, bounded framing, and protected stderr draining. Desktop-wide
accessibility advertisement and telemetry are explicitly disabled. A production
attached-window caller must supply a real window-incarnation guard, not merely a
PID-alive test. Fixture liveness suffices only because the test owns both
immutable window incarnations until process exit.

## Strict attached-window capture

The general read-only adapter requests pixels only and discards unsolicited
accessibility text. The pinned Linux AT-SPI bridge correlates native windows
with accessibility roots by geometry, which is not exact root identity. The
earlier fixture accessibility pass below therefore does not qualify general
attached-window accessibility capture. A future scoped accessibility provider
needs independent native root attestation; title or geometry matching is not a
substitute.

The pinned Linux driver can also capture a screen-region composite when a popup
or transient dialog overlaps the selected window. The adapter rejects the typed
`screenshot_composited` result before exposing any pixels or text. A same-process
popup or transient relationship does not implicitly admit another native window.
This is capture-scope validation, not an application-name or page-content rule.

Relevant pinned source: the
[AT-SPI root correlation](https://github.com/trycua/cua/blob/0c69d9a2c8abbb5051a2df4a23f0fa166d5385dc/libs/cua-driver/rust/crates/platform-linux/src/atspi/native.rs)
and the
[window capture implementation](https://github.com/trycua/cua/blob/0c69d9a2c8abbb5051a2df4a23f0fa166d5385dc/libs/cua-driver/rust/crates/platform-linux/src/tools/impl_.rs).
Private input qualification below remains a fixture-only path, not an enabled
agent capability.

On October 17, three native Linux GNOME X11 runs each passed the pixels-only
observation lifecycle (440 by 240 pixels, zero forwarded accessibility bytes)
and the negative `ScopeProjection` case. The negative fixture first verifies
that an owned overlapping transient dialog actually produces a valid composited
vendor frame, then requires the public adapter to return no pixels or text.
The fixture application remains alive after refusal. Run that case explicitly
with `scripts/computer-native-qualification.sh --driver /absolute/pinned/cua-driver --case ScopeProjection`.

## Historical private-driver results

These are capability-specific observations from October 10, 2026, not a general
qualification of every desktop environment or application.

| Capability | Linux GNOME X11 GTK | macOS AppKit |
| --- | --- | --- |
| Exact-window screenshot and accessibility capture | Passed at 440 by 240 pixels | Blocked by OS permissions |
| Handoff and fresh capture after resume | Passed | Blocked by OS permissions |
| Release and worker close preserve application lifetime | Passed | Not qualified |
| Mixed-language Unicode insertion | Failed with truncated text | Blocked by OS permissions |
| Unicode value replacement | Passed with exact independent verification | Blocked by OS permissions |
| Native button click | Passed with target count and unchanged guard | Blocked by OS permissions |
| Non-admitted window rejection | Passed while admitted worker remained usable | Permission denial prevents full qualification |

Linux insertion of `MintClaw Привет 世界 café 😀` produced
`MintClaw Привет 世`. The pinned AT-SPI implementation passes a character count
to an insertion API that expects a
[UTF-8 byte length](https://docs.gtk.org/atk/method.EditableText.insert_text.html).
The native assertion remains
strict and fails; replacement is a separate capability, not a silent workaround.
See the pinned
[AT-SPI implementation](https://github.com/trycua/cua/blob/0c69d9a2c8abbb5051a2df4a23f0fa166d5385dc/libs/cua-driver/rust/crates/platform-linux/src/atspi/native.rs).

Initial Linux startup also enabled Orca through automatic accessibility
advertisement. The adapter now explicitly disables that vendor behavior. After
the screen reader was stopped, live before/after checks confirmed unchanged
`ScreenReaderEnabled`, `IsEnabled`, and the GNOME screen-reader setting.

The October 10 Mac qualification launch was attributed by TCC to
`sshd-keygen-wrapper`, despite a Herdr process ancestor. The worker reported
Accessibility and Screen Recording as denied. Adding Herdr alone did not grant
this launch chain permission; a stable companion-owned, user-approved process
identity is a prerequisite for meaningful Mac qualification.

## Mac companion app launch

Package the actual companion executable in a new app bundle to give its native
permission checks a companion-owned process identity. Existing CLI and service
invocations are unchanged. The packager neither installs nor launches the app,
replaces an existing app, changes a service, or changes permissions.

```sh
go build -o build/mintclaw-node ./cmd/mintclaw-node
bash scripts/build-macos-companion.sh \
  --binary "$PWD/build/mintclaw-node" \
  --config /absolute/operator-config.json \
  --output '/absolute/new/MintClaw Companion.app' \
  --sign-identity 'Your configured code signing identity'
```

For private development without a certificate, use `--development-ad-hoc`
instead of `--sign-identity`. Ad-hoc grants must be rechecked after rebuilding;
they are not a stable signing identity across updates. The bundle contains only
an absolute reference to the external node configuration, not a credential copy.

Launch the app through Finder or `open`. A launch without arguments runs the
existing companion using the referenced configuration. Do not run a second
companion against an already-owned configuration or state directory. For a
permission-only check without starting a gateway connection:

```sh
readiness_dir=$(mktemp -d)
open -n -W --stdout "$readiness_dir/report.json" \
  '/absolute/new/MintClaw Companion.app' --args desktop-readiness
```

`desktop-readiness` checks the process's main bundle, current GUI session,
Accessibility and Screen Recording without prompting, capturing, enumerating
windows, or changing OS settings. A missing grant produces `ready: false` with
specific blocker codes. Grant permissions to the actual MintClaw Companion app
in System Settings, then relaunch and check again. Executing its embedded binary
over SSH is not the supported GUI launch path. Readiness grants no MintClaw
authority and does not prove that a desktop is unlocked or a window is admitted.

On October 17, a native app launch on macOS 26.6.2 reported the companion bundle
identity and an active GUI session. TCC attributed its permission checks to
`io.github.bogdanovich.mintclaw.companion`, rather than the SSH wrapper. Both
permissions remained denied; capture and input qualification still require the
corresponding grants.

## Linux window incarnation admission

The opt-in native window guard requires an explicitly selected local X11 socket
and a private, current-user Xauthority file. It never tries ambient display
settings, unauthenticated access, a forwarded TCP desktop, or another display.
X-Resource 1.2 reports the native window's actual client PID; an application-set
`_NET_WM_PID` property is not ownership evidence. A retained Linux pidfd identifies
the original process even after a numeric PID could be reused.

The guard subscribes to native destruction before admission and checks the
selected X screen. Destruction, process exit or observer transport loss makes it
permanently invalid. A newly admitted window receives a fresh generation. Closing
the observer releases only its connection and process handle, not the application
window. These checks remain mechanisms behind trusted host policy, not agent
commands or a replacement for companion authority and capture-scope validation.

On October 17, selected GNOME X11 native fixtures passed three detailed and
twenty additional runs covering exact XID reuse by the same process, explicit
replacement admission, wrong native owner rejection, owner-process exit and
observer transport loss. Both observer close and transport loss preserved the
fixture application window. Three further runs included 12,000 native window
events while the observer was idle; observation and teardown remained bounded,
and fresh admission still detected subsequent destruction. The screen reader
remained disabled. macOS window
incarnation admission is a separate prerequisite; this guard refuses unsupported
platforms rather than accepting a PID-only substitute.

For an explicitly selected X11 session, run the opt-in native tests with the
desktop package's Linux test binary (or `go test` on the Linux host):

```sh
MINTCLAW_NATIVE_X11_GUARD_TEST=1 \
  DISPLAY=:1 XAUTHORITY=/absolute/private/Xauthority \
  go test ./pkg/computer/desktop -run '^TestNativeX11' -count=20
```

The test creates only disposable native windows and verifies lifetime effects
through native X11 resources and kernel handles. It does not capture existing
window contents, use the clipboard or change accessibility settings.

Longer integration stress found that an interrupted pidfd poll (`EINTR`) could
mistakenly invalidate a live window. The liveness check now retries only signal
interruption on the same retained process handle within its existing two-second
budget. Process-exit readiness and all other syscall errors still invalidate the
guard; no reconnect, replacement admission or deadline extension is introduced.
Deterministic interruption/cancellation tests and native syscall fault injection
cover this boundary separately from window destruction and transport loss.
After correction, the complete desktop suite passed 1,000 native repetitions
alongside 100 race-enabled native repetitions, with all lifetime checks unchanged.

## macOS window incarnation admission

The macOS guard requires the companion's actual GUI launch identity and both
native permission grants before admitting a window. It retains the exact selected
`AXUIElementRef`, registers `AXUIElementDestroyed` before admission, and processes
that observer on a dedicated native thread and run loop. It never rediscovers a
replacement by title, position or list order. Native destruction or a failed
identity check permanently invalidates the admission.

The exact accessibility-element/CG-window mapping uses the non-public
`_AXUIElementGetWindow` symbol. Missing mapping or destruction-notification support
is explicitly unsupported; there is no approximate fallback. This compatibility
dependency must be requalified on new macOS versions. A retained kernel `kqueue`
process watch separately invalidates process exit and `exec`, with start-time and
current-user checks around registration. Numeric PID liveness is not sufficient.

Observer teardown releases only its own native references and handles. Callback
registration uses one process-lifetime trampoline and reclaimable numeric tokens,
not retained Go pointers or a new permanent trampoline for each session. Event
drains are bounded; a stopped loop or notification flood invalidates admission.

On October 17, macOS 26.6.2 native tests passed exact-symbol and AX-handle/timeout
ABI checks, kernel process exit and same-PID `exec` detection, callback cleanup,
and bounded run-loop tests. These tests neither capture nor enumerate personal
windows. They do not independently establish GUI-window destruction, capture or
handoff/resume qualification; the actual-app integration below supplies that
separate evidence.

## Read-only integration results

On October 18, the real companion and public gateway smoke suites pass on macOS
26.6.2 using an unlocked current-user GUI and the installed companion app's own
manually approved Accessibility and Screen Recording permissions. The current
GUI launcher runs the disposable fixture in the same GUI context and checks the
actual test child's exit receipt; launch success alone cannot report a pass.

The companion suite passes in 18.31 seconds with 880 by 544 native pixels,
same-session/worker/window handoff and fresh resume, original-window destruction
during human control, and release/companion exit preserving independently
attested application windows. The public gateway suite passes in 27.44 seconds
with protected native PNG transfer, localized handoff, refusal to observe during
human control, fresh changed resumed pixels, the same original binding and
release/exit preserving both fixture windows. The same pinned-runner public
gateway path passes on Linux GNOME X11 in 56.76 seconds.

The app executable remains unchanged throughout permission approval and these
runs (SHA256 `42c4e05b7aaaffb961f1697ee7252a31210cf0743fc577e1aae1ba03442cd510`).
Earlier permission-denied, post-login stale-admission and locked-GUI attempts
remain failed or refused attempts, not partial qualification. These positive
runs qualify only this unlocked read-only path; they do not qualify locked
desktops, input, general accessibility-root capture or arbitrary applications.
See the [companion boundary](computer-companion-boundary.md) and
[public gateway tools](computer-gateway-tools.md) for reproduction and grants.

## Integration gates

- Requalify the Mac companion's real launch identity and grants after app
  replacement, signing changes or macOS upgrades; ad-hoc development grants do
  not establish persistence across rebuilds.
- Requalify exact native window admission, capture and same-resource lifecycle
  for each additional GUI environment or application family.
- Requalify Unicode insertion on a corrected pinned driver or another backend.
- Add input contracts with fresh evidence, bounded actions, and independent
  effect verification before exposing public input tools or companion input
  commands.
- Qualify additional environments separately, including Wayland and managed
  Linux desktops; attached and managed operation remain independent choices.
