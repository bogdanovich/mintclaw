# Read-only computer companion boundary

This boundary adds opt-in, first-party computer commands to the existing node
runtime and authenticated connection. It does not enable desktop control in the
normal companion configuration, install a driver, select an ambient desktop, or
register public computer tools. A local host must be explicitly injected with
`WithComputerHost`; native host configuration and gateway tools are later gates.

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

Before enabling a real target, integrate its native incarnation guard, pinned
capture worker, bounded image transfer and explicit configuration. Then qualify
the actual companion process on Linux and macOS with disposable native windows,
including capture, handoff, fresh same-target resume, loss and release without
closing the application. macOS needs the actual companion app's OS grants.
Public clicking, typing and scrolling remain a separate capability program.
