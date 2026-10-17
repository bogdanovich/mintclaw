# Read-only computer companion boundary

This boundary adds opt-in, first-party computer commands to the existing node
runtime and authenticated connection. It does not enable desktop control in the
normal companion configuration, install a driver, select an ambient desktop, or
register public computer tools. A local host must be explicitly injected with
`WithComputerHost`; the native configuration path below provides that host only
for enabled operator profiles. The separate opt-in gateway tools and their
additional grants are described in [computer gateway tools](computer-gateway-tools.md).

## Authority and lifecycle

The complete internal command family provides target discovery, session open and
status, observation, handoff, request-resume, resume, and release. There is no
input, arbitrary event, application-close, shell, URL, PID, native-window selector
or desktop-address argument. Operator profile metadata selects eligible targets;
the native host supplies the exact guarded window incarnation.

Commands have no generic model contract and are excluded from `nodes_invoke`
discovery. Unknown `computer.*` commands are rejected, rather than admitted as
generic escape hatches. The node's command allowlist, risk limits and current
policy still apply. A browser capability does not grant computer authority.

The actor, agent and routed session come from the authenticated execution plan.
The trusted first-party router supplies the runtime kind and stable execution
identity; all five fields form the original turn's principal. A different turn
cannot acquire the original session merely by knowing its ID. Control requests
also carry the expected generation. Host results must preserve that owner, node,
session, policy and advertised target profile.

Handoff and resume are typed ownership transitions. The existing live-resource
interaction mechanism can continue the same turn; natural-language finish
phrases are not interpreted by these commands. Release terminates automation
authority only. It is not evidence that an application was closed or remains
open. An unconfirmed control result is retained as unknown and is not replayed
as another ownership-changing call.

## Protected observations

Accessibility text is live-only. The companion ledger retains a fixed
`protected_result` receipt, not the observation, and repeated invocation IDs
return that receipt without silently recapturing. A fresh observation needs a new
invocation identity. Private error details and rejected observation values are
not copied into ordinary errors, durable failure records or disconnect logs.

Pixels are never embedded as base64 in invocation JSON. An immutable image output
descriptor binds the original principal, session, target incarnation, worker,
policy, control generation, observation, invocation, workspace, digest, byte
limit and expiry. A native host must supply its scoped implementation of the
existing binary file-transfer protocol; descriptors contain no local path.
Transferred image bytes must be checked before they become an observation.

`observed_at`, observation timestamps and lease expiry are in the companion's
clock domain. Their validation establishes internal consistency, not gateway
freshness. A gateway adapter must anchor the request to its own clock, bound the
round trip and remaining lease, and reject expired or mismatched evidence. A
remote wall-clock timestamp must not refresh an old observation.

## Validation and remaining gates

Synthetic host tests exercise real read-only host transitions and the companion
invocation runtime, including an on-disk ledger. They verify a new observation
after same-session/worker/window handoff-resume, protected replay, full-turn
ownership, node policy, schema isolation and generic-tool exclusion. These are
fixture-backed boundary tests, not native desktop qualification.

The native implementation below integrates the incarnation guard, pinned
capture worker, bounded image transfer and explicit configuration. Qualify
the actual companion process on Linux and macOS with disposable native windows,
including capture, handoff, fresh same-target resume, loss and release without
closing the application. macOS needs the actual companion app's OS grants.
Public clicking, typing and scrolling remain a separate capability program.

## Native host configuration

The companion now constructs the attached-window host only when an explicit
`computer_profiles` entry is enabled. Absent/disabled profiles do not probe a
desktop, start a capture worker or advertise computer commands. No browser or
shell permission implies computer authority.

Merge a profile like this into the existing companion configuration, preserving
its gateway endpoint, TLS identity, state directory and other command policy:

```json
{
  "computer_profiles": {
    "selected-window": {
      "enabled": true,
      "executable": "/private/pinned/cua-driver",
      "sha256": "fd922e708099aae311873a6667beb48eaabb8c15c3abec744492db8a96b544ee",
      "pid": 12345,
      "window_id": 6789,
      "environment": {
        "DISPLAY": ":1",
        "XAUTHORITY": "/run/user/1000/gdm/Xauthority",
        "XDG_RUNTIME_DIR": "/run/user/1000",
        "DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus"
      },
      "allowed_actors": ["telegram:owner"],
      "allowed_agents": ["browser"],
      "allowed_runtimes": ["gateway"],
      "lease_seconds": 300,
      "observation_seconds": 15
    }
  }
}
```

Replace the native IDs with the operator-selected live window's coordinates;
they are not model arguments. On macOS use the pinned macOS checksum from the
native qualification document and omit the Linux `environment` object. Launch
the actual companion app with its own Accessibility/Screen Recording grants.
The profile revision is derived from the alias and complete normalized policy,
including owner/runtime constraints, driver, GUI selection and time limits.

Extend the existing node policy/approval with all eight `computer.*.v1` commands
listed above; control transitions require a `write` risk ceiling. That risk
classification concerns automation ownership, not input to the application.
Profile actor, agent and runtime allowlists remain an additional exact-match
local gate. Both gateway and coding principals must be explicitly admitted when
both are needed.

Each open performs fresh native admission. Handoff retains that admission and
its worker; resume never reconnects a replacement under the old binding. Native
loss during human control, capture/worker failure, expiry and connection loss
revoke the original authority. Disconnect fences old calls before cleanup, even
when cleanup cannot finish in its caller's budget. An uncertain release cleanup
is retained as unknown in the invocation ledger, not reexecuted on replay.

Images are PNG-only, kept in a bounded private in-memory cache, transferred over
the existing authenticated binary channel and discarded on commit, expiry, new
observation, control transition or disconnect. Every chunk checks the original
owner, native scope, worker, policy and control generation. No generic file
commands or arbitrary companion-local paths are introduced.

## Real companion smoke

`scripts/computer-companion-native-smoke.sh` launches only disposable native
fixtures and a private authenticated WSS gateway, using the actual companion
`run` process and normal config/runtime/transfer code. It never enables a
production profile, installs drivers or grants OS permissions.

```sh
bash scripts/computer-companion-native-smoke.sh \
  --driver /absolute/pinned/cua-driver \
  --node /absolute/mintclaw-node
```

Linux requires the explicit selected X11 GUI environment. On macOS also pass
`--app /absolute/MintClaw-Companion.app`; the app's executable must match `--node`.
`--test-binary` permits a cross-built integration test executable. Optional
`--capture-dir` retains only owned fixture PNGs in an existing private directory
for visual QA; by default none are retained.

The smoke verifies native pixels and transferred digest/dimensions, a visible
fixture change during human control followed by different fresh resumed pixels,
unchanged session/worker/native-window binding, release without closing either
window, native target destruction while the fixture process/guard window remain
alive, companion exit preserving remaining windows, and protected on-disk
observation receipts. The fixture's nonce-correlated own-state responses prove
window lifetime independently of a driver's success report.

The October 17 Linux GNOME X11 real-companion runs pass these cases. The macOS
app's own preflight confirms the correct bundle/GUI identity but reports both OS
grants missing; positive macOS qualification is still pending. Development
ad-hoc signatures do not establish permission persistence across rebuilds.
Public gateway computer tools and their live-resource interaction integration
require the [separate gateway grant](computer-gateway-tools.md); enabling this
native node host does not register them.
