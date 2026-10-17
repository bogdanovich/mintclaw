# Computer use native qualification

MintClaw's desktop contracts, read-only host, and private Cua adapter are opt-in
infrastructure. Public computer tools and companion commands remain disabled.
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

## Native results

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

## Integration gates

- Qualify the Mac companion's real launch identity and permission lifecycle.
- Implement OS-backed window-incarnation admission and loss detection.
- Requalify Unicode insertion on a corrected pinned driver or another backend.
- Add input contracts with fresh evidence, bounded actions, and independent
  effect verification before registering public tools or companion commands.
- Qualify additional environments separately, including Wayland and managed
  Linux desktops; attached and managed operation remain independent choices.
