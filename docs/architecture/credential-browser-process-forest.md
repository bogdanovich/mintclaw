# Credential Browser Process Forest

## Private Root ownership

The private `mintclaw-credential/internal/supervision` package owns one Linux
cgroup-v2 worker forest at a time. It provides a concrete termination operation
for the [private browser channel](credential-browser-private-channel.md).
No command, listener, provider startup, or agent tool currently invokes it.
It does not complete browser supervision or activate credential access.

Only real/effective Root UID and GID may open or start it. Production startup
uses the fixed parent `/sys/fs/cgroup/mintclaw-credential-browser.slice`, which
the operator must configure beforehand. The package opens protected Root
ancestors without following symlinks, refuses group/other writes and access
ACLs, and requires the real cgroup-v2 filesystem, a domain cgroup, no live
processes, and no leftover child directories. It refuses a parent containing
its own supervisor process. A restart cannot silently adopt an earlier forest.

Start generates a fresh child name, creates a private Root directory, pins
its descriptor, and uses Go's `UseCgroupFD`/`CgroupFD` to attach the initial
process atomically through clone3. It does not move a PID after an ordinary
spawn or infer ownership from a PID file. Unsupported kernels deny startup;
there is no process-group fallback. The Root caller supplies the separately
qualified fixed command and private inherited endpoint. Inherited environment,
stdin, stdout/stderr capture, caller-selected cgroup descriptors, and environment
keys outside the small runtime allowlist are denied.

## Retirement evidence

Explicit retirement, original process exit, or cancellation starts one bounded
cleanup. Kernel `cgroup.kill` stops the forest, including descendants that
changed session/process group. Cleanup requires an unambiguous
`cgroup.events` value of `populated 0`, reaping of the original process,
matching the pinned directory to its original named inode, and successful
removal and descriptor closure. Removing the group prevents later attachment
to that dying cgroup. A close acknowledgement or original process exit alone
never proves the descendants died.

Cleanup has a 1.5-second internal deadline. A caller can bound or cancel its
wait; that does not cancel the ongoing cleanup or renew admission. Any missing,
malformed, replaced, failed, or timed-out evidence permanently quarantines
the supervisor instance. It retains ownership of an uncertain forest and
admits no replacement worker. Protected operator repair/service teardown is
required before reopening an empty parent. Raw spawn and process errors are
discarded; callers receive one fixed error.

## Qualification and remaining gates

`scripts/qualify-credential-browser-forest.sh` is for a disposable Linux
qualification host. It builds a race-enabled test executable and runs it in a
temporary Root systemd unit with private child groups, bounded tasks/memory,
a runtime deadline, and an empty environment. It never accepts a token or
opens a vault. The PR integration job runs it as a mandatory step.

The actual-kernel fixture starts an unprivileged parent and a descendant that
uses `setsid` and ignores SIGTERM. It exercises explicit retirement, cancellation,
leader exit leaving that descendant, second-worker denial, idempotent cleanup,
token-environment refusal, and unsafe cleanup-control metadata that must quarantine
the supervisor and deny restart. Only known synthetic groups are repaired and removed
by the fixture. The ordinary parser tests reject ambiguous kernel evidence.
Non-Linux builds fail closed.

On 2026-10-11 at 18:18 UTC, the normal Linux fixture passed on `server@oc`
inside a disposable, resource-bounded Root systemd unit with an empty
environment. It witnessed the unprivileged parent and its setsid descendant
in the actual cgroup and passed all retirement and quarantine cases above.
The unit and temporary executable were removed. No production unit, checkout,
configuration, host encryption key, browser profile, or credential was changed.
At 18:34 UTC, the same public source and pinned `golang.org/x/sys` v0.48.0
also passed kernel-evidence parsing under the Linux race detector and the
race-enabled actual-kernel fixture (2.28 seconds). Its disposable compilation
directory and Root unit were removed. Race-enabled actual-kernel qualification
remains mandatory in CI. The host's agent admin access still excludes it from
real credential deployment.

This resource owner accepts a command constructed by trusted Root code; it
does not establish that command's browser identity, privilege drop, immutable
executable/modules, private mount/profile/IPC boundary, sandbox witness,
network confinement, or independence from agent administration. Those remain
required startup gates before connecting the browser channel, loading the
provider, or allowing credentials. Root supervision IPC and full browser
startup wiring remain later changes. Qualification on the actual selected
host remains separate from the disposable CI fixture.

See the [browser admission](credential-browser-login-admission.md),
[credential roadmap](credential-access-roadmap.md), and
[Mac VM host plan](../security/credential-mac-vm-host-plan.md).

Primary references: [Linux cgroup-v2 interface](https://docs.kernel.org/admin-guide/cgroup-v2.html)
documents whole-tree kill, live-process evidence, and non-revivable dying groups;
[Go Linux process startup](https://go.dev/src/syscall/exec_linux.go)
defines the atomic cgroup descriptor attachment.
