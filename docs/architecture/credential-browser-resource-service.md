# Private Browser Resource Service

> Historical component record from the superseded isolated-runtime design.
> The operator-approved [trusted-runtime design](credential-trusted-runtime-design.md)
> and [current roadmap](credential-access-roadmap.md) govern new work.
> Separate-host, VM, Root-helper, service-role, and process/network isolation
> requirements below are no longer deployment or rollout gates. Reusable
> component behavior and its exact qualification evidence remain recorded here.

## Status and authority

The credential CLI's `serve-resources` command gives the protected Linux Root
launcher one private transport to the dedicated broker. It opens no token,
provider, approval store, agent tool, model endpoint or browser control API.
It does not create accounts, directories, locks, a runtime image or a systemd
deployment. Native macOS and other unsupported systems deny.

This transport carries resource controls, not approval authority. Later broker
integration must retain the actual original `LoginLease`, complete scope and
caller lifetime, attach the enforcing proxy before start, qualify the real
Node/Chromium worker and sandbox, and only then retrieve a selected secret.
An initial-exec acknowledgment is not browser readiness. The transport's
20-hour ceiling never renews or replaces an approval grant.

## Fixed endpoint and identities

The operator supplies `/run/mintclaw-credential-supervision`, owned by Root
and the canonical `mintclaw-credential` broker GID with exact mode 0750.
The directory permits broker traversal but no broker writes. Root pins every
ancestor, rejecting symlinks, writable ancestors, special modes and ACLs.
The existing protected launcher lock, runtime image, helpers and disjoint
`mintclaw-credential-browser` role remain independently required.

Root binds `worker.sock` through the retained directory, sets Root/broker
ownership and mode 0660 before listening, and enables sequenced packets and
per-message credentials. An existing socket is never removed or reused.
Shutdown removes only the socket whose device/inode matches this instance's
recorded object. A stale or replaced endpoint requires protected operator
recovery; automatic restart cannot silently claim it.

Both peers require kernel `SO_PEERPIDFD`, retaining the original process
lifetime. Root admits only the exact broker UID/GID; the production client
requires its real/effective identity to match that named broker and requires
the server's UID/GID to be Root. Each packet must also have exactly one
`SCM_CREDENTIALS` matching the original creator's PID, UID and GID. Passing
the connection to another process supplies no authority. Dead or closed
process handles deny; there is no numeric-PID fallback.

## Protocol and ownership

Packets have exactly two bytes: the fixed operation and a reserved/status byte.
Requests use zero and successful replies use one. The only operations are:

| Operation | Phase and result |
| --- | --- |
| Allocate | First request on one connection; returns exactly the private loopback TCP listener and unnamed worker-control stream |
| Start | After allocation and broker adoption; executes only the launcher's fixed program and acknowledges its initial exec |
| Retire | After start; closes the allocation permanently and acknowledges verified whole-forest retirement |

Before-start withdrawal closes the connection without starting a process.
Malformed, repeated, out-of-order or unexpected controls close the connection
and retire any allocation. There are no caller-selected commands, flags,
paths, URLs, PIDs, profiles, secrets or error strings. Failed calls are not
replayed. One connection owns one allocation; Root serves them serially and
does not admit new work before the previous cleanup finishes.

Only a successful allocation reply may transfer rights, exactly two. The
receiver installs them atomically with close-on-exec, rejects body/control
truncation and extra/foreign ancillary data, and closes every installed right
on rejection. The client checks a listening TCP socket bound to 127.0.0.1 and
an unnamed stream created by the original Root server. Root's namespace,
image, helpers and process handles are never exported.

The client retains its original context and the endpoint files; it allows
one export and one start. Original cancellation, server process death or
connection hangup closes its originals and marks `Done`. Hangup detection
polls flags without receiving or peeking rights. Explicit retirement owns
its final acknowledgment and closes the originals before requesting it.
Any copies made to construct the proxy or channel remain broker-owned and
must close with that same lifetime. Root cannot revoke another process's
descriptor copies.

Root ties its allocation to the original connection, pinned broker process,
service lifetime and resource ceiling. Lost connections, broker death,
timeout, shutdown and failed launch trigger the existing bounded forest
cleanup. Startup or cleanup failures retain launcher quarantine and terminate
the resource service with a fixed error. Raw process, socket and kernel
diagnostics never cross this transport.

## Qualification and remaining work

The disposable Root qualification uses separate synthetic broker/browser
UIDs, real Linux helper and namespace/cgroup operations, and a synthetic
in-image executable in place of Node. It exercises normal retirement,
before-start closure, original-context cancellation, broker process death,
out-of-order controls and server loss. Packet tests exercise actual kernel
rights transfer, atomic close-on-exec, malformed/truncated packets, closed
process identity, descriptor cleanup and process handoff rejection. Each
required fixture must report PASS; compilation or a skipped fixture is not
qualification.

These synthetic roles do not qualify production named accounts, systemd
policy, actual immutable vendor Node/Chromium, the protected Mac/VM boundary,
Android approval or real 1Password access. No such host changes or secret
provisioning occur here. The enforcing proxy/private channel, actual original
lease and broker session registry still need composition before provider
activation. Remote typed agent intents, phone notification delivery and the
CA6 account pilot remain subsequent roadmap work.
