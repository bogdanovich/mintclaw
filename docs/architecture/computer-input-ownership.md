# Native computer-input ownership

Status: Linux X11 cooperative lease primitive; no input host, public action,
companion command, model grant or production setting is enabled. macOS and other
backends explicitly refuse this mechanism until separately implemented and
qualified. This is one prerequisite of the
[computer-use roadmap](computer-use-roadmap.md), not full input qualification.

## Native identity, not aliases

`WindowGuard.AcquireInputLease` uses the original native observer connection;
it does not resolve a profile/desktop/display alias into a second lock namespace.
On X11 the original server serializes the check-and-claim transaction for one
stable private input-control selection, shared across clients and screens.
Different windows, companion processes, profiles and screen-number suffixes on
that same server therefore contend for the same cooperative resource. Another
X server is a distinct desktop; no shared filesystem directory or caller-chosen
identifier establishes authority.

This uses an unmapped one-pixel InputOnly window as the lease's lifetime token.
It never maps/fronts/focuses an app, reads selection data or accesses the
CLIPBOARD/PRIMARY selections. The stable private selection name is protocol
namespace, not an application-name heuristic or an authorization source.
Non-cooperating applications and human input are not blocked by this lease.

The atomic admission briefly grabs the server, checks for an existing owner,
claims only when empty, verifies the original token and publishes the local
lease before ungrabbing. It never steals an occupied selection. Each ownership
transaction has a 250 ms socket deadline (or a shorter caller deadline). The
preceding retained-window validation keeps its existing two-second bound; the
complete acquisition/check call is not promised to finish within 250 ms.
Cancellation or an uncertain protocol result closes the original observer
connection, whose X11 close-down releases its own server grab, selection and
control window. It does not close the independently owned application connection.
See the [X11 protocol](https://www.x.org/releases/X11R7.7/doc/xproto/x11protocol.html)
for selection ownership, server grabs and client close-down semantics.

## Loss, cancellation and cleanup

`InputLease.Check` revalidates the retained native window/process and the exact
current owner; it never reacquires. New admission/check contention returns busy
rather than queueing unbounded input. SelectionClear, token/target destruction,
retained process exit or transport loss revokes the original lease. Restoring
the same numeric selection owner does not resurrect it. Input ownership events
are observed continuously; native process handles supplement destruction events
for applications that retain X resources after exit. Read/write failure and
explicit close are observed on the retained socket itself: XGB's nonblocking
event API does not distinguish an empty queue from an ordinary transport error.
These terminal failures close only the observer transport; no new connection,
numeric descriptor lookup or application shutdown is used to recover authority.

The acquisition context bounds acquisition, not the later lease lifetime.
Canceling that context after success does not unlock before the future executor
has stopped input and cleaned up owned held state. `Done` means ownership is
revoked, **not** that keys/buttons or already accepted effects were cleaned up.
This primitive sends no input and makes no held-state guarantee.

Explicit close revokes locally and destroys only its own lifetime token. It
never sets the selection owner to None, so stale cleanup cannot clear a newer
owner. An independently attested already-absent token is idempotent cleanup;
ambiguous cleanup fails closed. The selected app/window remains independently
owned. A new lease requires explicit admission and fresh host generation/evidence.

## Qualification and remaining integration

Deterministic tests cover atomic ordering, occupied ownership, unknown outcomes
at every transaction step, socket timeout/cancellation and late callbacks.
Opt-in native tests create only their own windows and exercise competing native
guards/processes, process crash, stolen/restored ownership, destroyed control
tokens/targets, stale cleanup and separate screens of one server. The dedicated
CI job uses its own two-screen Xvfb and repeats each native fixture twenty times;
the actual attached Linux tuple is qualified separately. Native tests
do not capture existing apps, press keys, move the pointer or touch the clipboard.

The final reviewed head of merged
[#1787](https://github.com/bogdanovich/mintclaw/pull/1787) was exercised twenty
times on the attached Linux GNOME/X11 desktop: contention/crash, stolen/restored
ownership, token/target destruction,
explicit socket close and server-side observer disconnect all passed. Existing
window-incarnation, wrong-process-owner, process-exit and event-pressure guards
also passed twenty repetitions. That server has one screen, so the cross-screen
case explicitly skipped there. The separate two-screen, race-enabled CI gate
passed twenty repetitions on that same reviewed head, as did all 15 CI checks
and automated review before authorized merge. This is lease-primitive evidence,
not a representative computer-task or input-worker qualification; macOS input
leases remain unsupported.

This is cooperative exclusion, not a security sandbox, model policy, complete
emergency-stop implementation or proof that an external worker dispatches on
this same server. Before input exposure, the host must integrate authoritative
principal/target/policy/control-generation binding, host-owned fresh observations,
the existing durable invocation and protected ephemeral payload mechanism,
qualified native targeting, cancellation/held-state cleanup and independent
effect verification. In particular, it does not fix Linux AT-SPI root correlation
or grant a pixel/foreground fallback. The existing read-only in-process arbiter
is unchanged and must not be represented as cross-process input exclusion.
