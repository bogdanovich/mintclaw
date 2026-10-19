# Native computer-input ownership

Status: Linux X11 and macOS cooperative lease primitives; no input host, public
action, companion command, model grant or production setting is enabled. Other
backends explicitly refuse this mechanism. Native qualification remains a
separate gate for each platform tuple. This is one prerequisite of the
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
not a representative computer-task or input-worker qualification. Mac native
qualification is separate from this Linux result.

## macOS: conservative per-user kernel exclusion

The Mac mechanism uses nonblocking `flock` on one empty, private per-UID protocol
file under the fixed OS-global `/private/var/tmp` directory. It does not use
`HOME`, `TMPDIR`, a profile directory, bundle cache path or Mach bootstrap name
to choose ownership. This intentionally over-excludes separate GUI sessions for
the same UID, rather than letting aliases or nested bootstrap namespaces admit
simultaneous controllers. It does not serialize other UIDs or unrelated apps.

The parent must be the original root-owned sticky local directory. The file must
be a regular, empty, single-link object owned by the current non-root real/effective
UID with mode 0600. Opened handles use no-follow and close-on-exec. The mechanism
neither follows symlinks nor repairs unsafe existing objects. It retains the
original file/directory identity and subscribes to vnode events before claiming
the kernel lock, then revalidates both the retained and named objects.

Kernel events make file rename/delete/revoke, attribute/link changes and content
changes terminal, even if restored before the next check. Directory rename,
deletion and revoke events are also terminal. Harmless directory timestamp
changes and unrelated file activity do not revoke ownership: the root's retained
and named identity, owner and mode are independently revalidated on every check.
Restored privileged root-permission changes between checks are not attested by
this cooperative mechanism; it is not a security boundary against root. An
independent native process watch supplements the original AX window observer; exit/exec, window
destruction or guard close revokes ownership without rebinding a numeric PID or
window ID. Acquisition cancellation after success does not release early. The
lease's monitor uses no AX polling, screenshot, clipboard, mouse or keyboard
operations; explicit checks still validate the original AX guard.

Release closes only the owned descriptors. It never unlinks the persistent
protocol file, clears another owner's state or closes the app. A process crash
releases the kernel lock without PID/age heuristics or a lock-file deletion
routine. File tampering by the user, root or another non-cooperating process is
an ownership-loss event, not a supported recovery/admission protocol. This is
cooperative exclusion, not a security boundary against same-UID processes.
See Apple's [flock manual](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/flock.2.html).

Desktop-free real-process and race tests repeat contention, crash, cancellation,
file/root replacement, restored attributes/links/content, unsafe files,
close-on-exec and stale cleanup, plus benign directory timestamp/file churn and
terminal root authority changes. The CI repetition count is sixty within the
unchanged two-minute budget. A separate actual-app suite is:

```bash
scripts/computer-companion-native-smoke.sh --suite ownership \
  --driver /absolute/private/pinned-cua-driver \
  --node '/Users/ab/Applications/MintClaw Companion.app/Contents/MacOS/mintclaw-node' \
  --app '/Users/ab/Applications/MintClaw Companion.app'
```

The unchanged approved app launches the private native helper under its real TCC
responsibility context. The helper does not invent a main-bundle ID or bypass the
production readiness gate: it checks its own actual non-prompting permissions
and qualifies the underlying AX/window/lease mechanism separately. Each of twenty
rounds uses only owned fixture windows and independently attests that release
preserves them, different windows/processes contend, stale cleanup preserves a
new owner, target destruction never rebinds, and process exit revokes the lease.
The controller rejects a missing/skipped round, missing permission, truncated
output or unsuccessful child. The existing semantic-input verifier retains all
seven Unicode caret/effect gates when sharing this orchestration.

The first actual-app ownership attempt refused the locked desktop with exit 75;
it is not a passing native Mac qualification. No app or OS permission was changed.

This is cooperative exclusion, not a security sandbox, model policy, complete
emergency-stop implementation or proof that an external worker dispatches on
the same admitted desktop. Before input exposure, the host must integrate authoritative
principal/target/policy/control-generation binding, host-owned fresh observations,
the existing durable invocation and protected ephemeral payload mechanism,
qualified native targeting, cancellation/held-state cleanup and independent
effect verification. In particular, it does not fix Linux AT-SPI root correlation
or grant a pixel/foreground fallback. The existing read-only in-process arbiter
is unchanged and must not be represented as cross-process input exclusion.
