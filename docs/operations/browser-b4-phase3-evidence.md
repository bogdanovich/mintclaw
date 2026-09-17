# Browser B4 Phase 3 Deployment Evidence

## Status

Browser B4 Phase 3, **Ephemeral Profiles**, is merged, deployed, and complete.
Phase 4, **Gateway Attached Chrome**, is now the active dependency-ordered
phase.

The production gateway and configured Darwin companion run artifacts built
from merged `main` commit
`aa91bd77d908669d4584f72dd6b78f7f42037bb5`. This record closes the Phase 3
acceptance criteria in
[Browser B4 Execution Goal](../architecture/browser-b4-execution-goal.md) under
the authority and stop conditions in
[Browser Capability B4 Admission](../architecture/browser-capability-b4-admission.md).

Credential injection, password-manager integration, and secret-manager
integration are outside B4. Persistent managed-profile login through visible
handoff remains the deployed authentication mechanism.

## Merged Implementation

| Pull request | Merge commit | CI |
| --- | --- | --- |
| [#1141](https://github.com/bogdanovich/mintclaw/pull/1141) | `aa91bd77` | [10 of 10 jobs passed](https://github.com/bogdanovich/mintclaw/actions/runs/34292616776) |

The implementation adds a shared `ephemeral` profile mode to gateway and
companion configuration, discovery, browser workers, and node descriptors.
Each open creates a random owner-only `.e1-<identity>` directory and launches
Playwright with isolated browser state inside it. No managed profile state is
copied or imported. Screenshots and downloads continue to use the existing
bounded artifact stores rather than the ephemeral identity directory.

Cleanup first renames a session directory to its exact quarantine name, then
removes it through an anchored directory handle and synchronizes the parent.
Every cleanup step revalidates root identity, directory identity, ownership,
permissions, and lease authority. Missing cleanup proof produces
`cleanup_required` or a safe lost/unknown result; it is never reported as a
successful close.

Cross-process lifecycle authority uses an immutable kernel-owned namespace in
addition to the configured anchored file lock. Linux uses an abstract Unix
socket keyed by the complete configured-path digest. Other Unix systems use a
collision-aware loopback endpoint that serves and verifies the complete
identity; Windows retains a non-delete-sharing parent handle. This prevents a
writable temporary pathname or a concurrently rebound configured parent from
creating two owners for one profile.

The final review regression rebound both the historical temporary guard and
the configured parent while the lease was held. It passed 20 consecutive runs
on Darwin and 20 on the deployed Linux host. Targeted lifecycle, race,
gateway, companion, browser-host, node-routing, policy, configuration, MCP,
and real-driver tests passed, as did repository lint and Darwin, Windows,
NetBSD, and Linux compilation gates. Autonomous review completed without an
unresolved finding before merge.

## Exact Deployment

Both deployed artifacts report `v0.1.0-p8a.2-1657-gaa91bd77`:

| Artifact | SHA-256 |
| --- | --- |
| Linux gateway and CLI | `8d50dcf38f556f1316ddafb3d61f47015fc1cae8743325a407363337700a69e7` |
| Darwin amd64 companion | `890601995273dd823996a61198cd15cc5c713239ee90f290281ece8d0ad28aeb` |

The deployed configuration digests are:

| Configuration | SHA-256 |
| --- | --- |
| Main gateway | `908dc681cdee8a280755b1131779dbdc6d42b5f7d0dd18557ffff4a41908ab53` |
| Darwin companion | `76bd141e181bfc4dab458624125c47ccba45d050301ab6afcf68a5de2489bc18` |

Both placements expose `ephemeral-v1` with `mode=ephemeral`,
`network_mode=any_http`, `capability_mode=full_access`,
`approval_mode=model_requested`, real execution enabled, approved actions
enabled, and headless runtime. Existing `managed` mappings and profile data
were not modified.

The rollout restarted only the main gateway and the configured `local-test`
companion. The companion tunnel was preserved during the initial cutover, and
the unrelated `p4-isolated` companion was never restarted or modified. A
catalog refresh initially omitted the existing operator alias
`ab-local-test`; discovery correctly failed as `node_unavailable`. The alias
was restored while retaining exactly the same already approved 24 commands,
with advertised and approved catalog hashes both equal to
`7b1c71f146b49cd146d77a62b31853bed2c8e2e7854aaa4e0f0a6c9e546f7b29`.
No node authority was added.

## Identity Isolation

A host-local HTTP fixture set a cookie, local-storage marker, Cache Storage
entry, and service worker in one ephemeral session. After close, a newly
opened session checked the same origin.

Gateway request `8ddc2c8c-4f05-42cb-967b-8f86dce7f681` returned:

```json
{"gateway_profile":"gateway/ephemeral","first_open_state":"ready","seed_state":"B4P3_SEEDED cookie=true local=true cache=true service_worker=true","first_close_state":"closed","second_open_state":"ready","clean_state":"B4P3_CLEAN cookie=false local=false cache=false service_worker=false","second_close_state":"closed","safe_error":null}
```

The companion exercised the same two-session sequence in browser trace
`trace-turn-ef4dad10ca75be5e4361c05b`. Because the outer live client timed out
while the remote turn continued, a bounded follow-up request
`19f81a80-548c-49b0-8790-fe7d322b94ee` captured the exact second-session
result:

```json
{"open_state":"ready","exact_body_status":"B4P3_CLEAN cookie=false local=false cache=false service_worker=false","close_state":"closed","safe_error":null}
```

Filesystem watchers on both placements observed at most one correctly named
session directory. Normal close changed it to the exact `.quarantine` form
before deletion. The companion watcher recorded `unsafe=0`; both roots ended
empty.

## Recovery And Fault Canaries

An active gateway ephemeral session was interrupted by restarting only the
main gateway. Its original root was absent before the replacement gateway
became ready, and the live client returned `disconnected` instead of claiming
success. Durable inbound replay restarted the interrupted request with a new
ephemeral identity rather than restoring the old directory. The exact scoped
Playwright child for that fresh identity was then terminated as a driver-crash
injection. Its complete Chrome process tree and runtime root were removed.
Request `34b7c33f-42a0-4e62-a5bb-6488d0dad3f4` subsequently proved immediate
gateway reuse and returned a clean identity with a successful close.

For companion transport recovery, an ephemeral root was active while only the
local-test tunnel was restarted. The companion process and unrelated
`p4-isolated` process retained their PIDs, the tunnel received a new PID, and
the root was removed. Request `398c73aa-f8ad-454f-b94e-689a47ba5c9c`
reported `outcome_unknown` with `worker_unavailable` and did not reopen or
claim a close. After reconnection, request
`bda8b829-c387-4040-b905-3f208712fe99` opened a clean replacement and closed
it.

For companion process recovery, an ephemeral root was active while only the
local-test node was restarted. The node received a new PID; the tunnel and
`p4-isolated` PIDs remained unchanged. Startup recovery removed the root, and
the interrupted request `af83be1d-cd50-4532-9bfc-a1f2a163a4b5` reported
`driver_unavailable` without reopening. The broker briefly advertised
`recovery_required` until the interrupted turn reached its terminal cleanup,
then returned to `ready`. Request `fb0bd22e-60c2-41b4-be2e-482ee31603fe`
proved clean immediate reuse and a successful close.

Unit, integration, failure-injection, and real-driver coverage additionally
proves open failure, timeout, cancellation, expiry, configuration reload,
cleanup retry, symlink and ownership changes, permissive permissions, path
overlap, and cleanup-boundary rejection.

## Managed Regression And Final Audit

Non-mutating managed-profile smoke requests observed only the initial blank
page and closed without navigation or action:

```json
{"target":"gateway","profile":"managed","open_state":"ready","initial_url":"about:blank","close_state":"closed","safe_error":null}
{"target":"companion","profile":"managed","open_state":"ready","initial_url":"about:blank","close_state":"closed","safe_error":null}
```

The final audit found:

- both ephemeral roots owner-only, empty, and free of unrecognized entries;
- all ephemeral lock files owner-only, regular, and single-linked;
- zero browser or driver processes associated with either ephemeral root;
- no remaining fixture process, listener, directory, or source file;
- the main gateway, every product user service, the companion node, and its
  tunnel active, with zero failed user or system units;
- zero legacy processes and zero error-through-alert journal entries in the
  final ten-minute status window; and
- 28 GiB free on the gateway after cleanup.

Representative isolation, restart, disconnect, node-restart, recovery,
managed-regression, and final-smoke browser traces were checked against
diagnostic schema v1. All ten selected traces ended `completed`, used
`redacted_content`, had no truncation, remained within record bounds, and
contained no profile path, user-data-directory argument, or companion state
path.

## Rollback And Retention

The checksum-verified immediate pre-deployment recovery sets are:

```text
/home/server/mintclaw-recovery-b4p3-pre-20260909T002637Z
/Users/ab/mintclaw-recovery-b4p3-node-pre-20260909T002637Z
```

They contain only the affected binaries, configuration, units, launchd
definitions, service state, restore order, and checksums. Browser profiles,
sessions, logs, traces, caches, and media were deliberately excluded. The
immediately previous Phase 2 gateway set and the O8 companion state set remain
available. Three older server B4 sets and one older local-node set were
permanently removed only after their role was matched to and their replacement
was verified against its checksum manifest. Unrelated recovery sets with
distinct security or deployment roles were preserved.

## Phase Conclusion

Every Phase 3 acceptance criterion is satisfied without reaching a mandatory
stop condition. Gateway and companion ephemeral sessions start without prior
identity, clean up through success and recovery paths, fail closed when worker
outcome is unknown, and return to immediate clean reuse without orphan browser
processes. The existing managed identities remain operational and untouched.

Phase 4 may now add per-session attached Chrome on the gateway through the
official Playwright extension flow. Companion attached Chrome remains
dependent on successful Phase 4 deployment evidence.
