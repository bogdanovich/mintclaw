# Browser B4 Phase 2 Deployment Evidence

## Status

Browser B4 Phase 2, **Managed Alias And Revocation Parity**, is merged,
deployed, and complete. Phase 3, **Ephemeral Profiles**, is now the active
dependency-ordered phase.

The production gateway and configured Darwin companion run artifacts built
from merged `main` commit
`28e426d7b62f6feed85066b03afcbcb6f775299e`. This record closes the Phase 2
acceptance criteria in
[Browser B4 Execution Goal](../architecture/browser-b4-execution-goal.md) under
the authority and stop conditions in
[Browser Capability B4 Admission](../architecture/browser-capability-b4-admission.md).

Credential injection, password-manager integration, and secret-manager
integration remain outside B4. Persistent managed-profile login through
visible handoff remains the deployed authentication mechanism.

## Merged Implementation

| Slice | Pull request | Merge commit |
| --- | --- | --- |
| Managed aliases, isolation, and capacity | [#1129](https://github.com/bogdanovich/mintclaw/pull/1129) | `4fc16435` |
| Exact profile revision authority | [#1131](https://github.com/bogdanovich/mintclaw/pull/1131) | `e232db93` |
| Revision-bound revocation and conformance | [#1133](https://github.com/bogdanovich/mintclaw/pull/1133) | `28e426d7` |

The first slice added multiple arbitrary aliases, per-alias workers, storage
and lock isolation, and bounded global capacity. The second made an exact
profile revision part of session and dispatch authority. The third applied
revocation to status, handoff, resume, catalog replacement, and recovery, and
added the admitted gateway and companion conformance matrices. Every code pull
request passed required CI and autonomous review before merge.

## Exact Deployment

Both deployed artifacts report `v0.1.0-p8a.2-1612-g28e426d7`:

| Artifact | SHA-256 |
| --- | --- |
| Linux gateway and CLI | `b0bfd7d62c06d767d166ab1460e8ee48c4e1bb6f98f536ad24ef1756e36e204e` |
| Darwin amd64 companion | `70e89b25be71b757657c9cb3d82f52d1b968de745b0329da1a976339fa6daff5` |

Before restart, old and staged `doctor` output was byte-identical for both
gateway configurations, with zero configuration load errors and the same
expected policy-finding exit status. The rollout restarted only the main and
spouse gateway units and the configured Darwin companion LaunchAgent. The
companion tunnel remained continuously running.

After the canaries, the exact original production configurations were restored.
Their SHA-256 digests are:

| Configuration | SHA-256 |
| --- | --- |
| Main gateway | `9f8d5fa21e347af00b158fe6beb83ac4253a564e4d57713b13c0e4277f096b88` |
| Spouse gateway | `db6aa38b8bbc8323c3b61d50c0bc9fb13215abd7c92e438583a398cafa259b8e` |
| Darwin companion | `a66d0f44e59d69e3c26d4a649ddd98ba5a9e8576892fff5d837d044690d14515` |

Production discovery again exposes only the `managed` profile on each target.
The companion reconnected on the exact deployed version with its restored
catalog explicitly approved and its advertised and approved hashes equal.

## Managed-Alias Isolation

Temporary `phase2-alpha` and `phase2-beta` aliases used separate empty private
runtime roots and locks on each placement. A live request first confirmed that
both aliases were discoverable on gateway and companion.

The gateway isolation canary, request
`19cd97d8-b7f5-4369-adad-e7735636c496`, wrote a different host-local fixture
marker through each alias, closed both sessions, and reopened both aliases. It
returned:

```json
{"target":"gateway","alpha_first_marker":"alpha-28e426d7","beta_initial_marker":"empty","beta_first_marker":"beta-28e426d7","alpha_reopen_marker":"alpha-28e426d7","beta_reopen_marker":"beta-28e426d7","every_close_state":["phase2-alpha:first=closed","phase2-beta:first=closed","phase2-alpha:reopen=closed","phase2-beta:reopen=closed"],"safe_error":null}
```

The companion exercised the same contract in bounded requests:

- `662fb3f2-114e-4dcf-869c-0e76e944ecd0` stored the alpha marker and closed;
- `eaf43bc0-49aa-481f-8548-55cc4327fda0` observed an empty beta identity,
  stored its distinct marker, and closed;
- `8ff63405-24c7-4f2d-b8ed-cc0868427ac8` reopened alpha and observed only the
  alpha marker; and
- `31355e23-3656-42e3-88e2-253b797c97c6` reopened beta and observed only the
  beta marker.

A catalog-freshness check safely returned `context_catalog_stale` after one
navigation. The specialist refreshed contexts before continuing; no stale
reference was replayed.

## Lease And Capacity Conformance

Gateway request `3d2f7581-7694-469a-adc1-d668d979af25` and companion request
`c9aab741-2978-4460-acf0-f645f3e3a9a7` produced the same sequence:

1. the first alpha session became `ready`;
2. a duplicate alpha open returned `profile_busy`;
3. a beta open while global capacity was occupied returned `session_capacity`;
4. alpha closed; and
5. beta immediately opened as `ready` and then closed.

This distinguishes per-profile exclusion from global capacity exhaustion and
proves that either failure does not poison another alias after capacity is
released.

## Revision-Bound Revocation

For the gateway drill, a visible handoff retained a ready `phase2-alpha`
session at revision v1. Advancing the configured alias to v2 and restarting the
restart-only context manager closed the v1 session before replacement authority
was published. The retained record still identifies configured revision v2 and
session revision v1, no browser process remained, and request
`09267978-df07-451e-af3f-4f4e2adbd6d2` then opened and closed a clean v2
replacement.

For the companion cleanup-failure drill, request
`6f914a17-a841-4dc6-8176-27943aeed13c` held an active v1 session while the
companion alias advanced to v2 and its process restarted. Observe and close
failed safely as `driver_unavailable`; the session entered `closing` quarantine
instead of being falsely marked closed. Gateway catalog alignment and startup
recovery then made the retained v1 session terminal `lost` with
`gateway_restarted`, with no resurrection. After explicit v2 catalog approval,
request `3da14cb4-df58-46a9-837b-fceea0a309dd` opened and closed the v2
replacement.

The old gateway process reported the deliberately quarantined worker cleanup
failure while exiting. Its replacement became ready and completed recovery;
the final systemd audit contained no failed units or error-level entries. This
is expected evidence for the admitted cleanup-failure path, not a hidden
successful close.

## Final Production Smoke

After exact restoration, request
`ab8ea847-24f1-4dc1-8d8e-c45c5f5a11ae` exercised the normal production aliases
through the running main gateway and returned:

```json
{"gateway_profiles":["managed"],"companion_profiles":["managed"],"gateway_open_state":"ready","gateway_close_state":"closed","companion_open_state":"ready","companion_close_state":"closed","safe_error":null}
```

The corresponding parent trace is
`trace-turn-31dd151b1c7219345494eaf8`; the browser child trace is
`trace-turn-60384b4e86461277d9c413a2`. The child uses diagnostic schema v1,
contains 31 ordered records, has redacted-content policy, completed without
truncation, and records terminal completion.

Representative isolation, capacity, handoff, revocation, replacement, and
final-smoke browser traces were also audited. Successful traces all ended
`completed`, were redacted, and had no truncation. The intentional handoff
trace `trace-turn-abb957a341a20a7848887bdb` ended `suspended`, preserving its
real interaction state rather than claiming completion.

## Lifecycle, Privacy, And Rollback

The final retained browser ledger contains 44 closed sessions and three
historical terminal lost sessions, with zero nonterminal sessions. All 50
invocations are terminal `succeeded`. Interaction records contain one
cancelled, one failed, and ten resolved records, with zero nonterminal
interactions.

Both gateway units and the companion are active and ready. The final audit
found zero failed user units, zero Phase 2 fixture processes, zero first-party
browser processes, no listening fixture endpoint, and no error-through-alert
journal entries after restoration. Temporary alias runtime roots and lock files
were removed from active locations only after exact reference, holder, and
process checks; recoverable copies are in the corresponding system Trash.
Profile data was never included in deployment backups.

The checksum-verified immediate pre-deployment recovery sets are:

```text
/home/server/mintclaw-recovery-browser-b4-phase2-pre-20260908T132156Z
/Users/ab/.mintclaw-deploy-backups/browser-b4-phase2-pre-20260908T132156Z
```

They retain the prior binaries and service configuration. Mutable browser
records and persistent profile data are deliberately outside rollback.

## Phase Conclusion

Every Phase 2 acceptance criterion is satisfied without reaching a mandatory
stop condition. Multiple managed aliases are independently isolated, profile
leases and global capacity are distinguishable, exact revision changes revoke
active authority on gateway and companion, cleanup failure is quarantined, and
normal production behavior remains intact after exact restoration.

Phase 3 may now add session-only ephemeral identities with deletion and
quarantine guarantees on both placements. Attached-user Chrome remains
dependent on completion of the ephemeral-profile phase.
