# Architecture Simplification O7/O8 Cutover

Date: 2026-09-01

Outcome: passed. The combined O7/O8 release removes the browser-policy legacy
mode and makes one immutable repository baseline mandatory for every retained
coding thread. The deployed cutover deliberately discarded expired browser
records that lacked the current policy fields and seeded four coding baselines
outside the running product. No historical browser reader, resume backfill,
dual baseline shape, or deployment converter remains in steady-state source.

## Release and review

O7 source is PR #1026 after PR #1027 removed the historical browser-catalogue
reader that conflicted with the strict policy change. O8 is PR #1028, merged as
`d5d3e63e66072dd32bfa31f86788dc60bafc196a` from exact reviewed head
`6958a98b4ce1bdba2e00c0f05c70c7ce32c03b17`. All nine required CI jobs passed,
the exact-head automated review reported no finding, no review thread remained
open, and the owner rocket authorized merge.

Core, launcher, Linux node, and Darwin/amd64 node artifacts were built from the
merge commit. The final deployed core reports
`v0.1.0-p8a.2-1212-gd5d3e63e`. The installed SHA-256 digests are:

| Artifact | SHA-256 |
| --- | --- |
| Linux core and CLI | `059038281f37e0a9d955d4cec1814c7e450e53ea0fc7cef45ce8e0b1e675d840` |
| Linux launcher | `bf06a597c8939356be5bada74f0ed98f9e9e8d9e2f5e89ee5a944de3df6b8d3d` |
| Linux node | `27d4109444c4ab06d8f16b8257056fec88b1b3ec224da8c3d5e6017c7f4a5941` |
| Darwin/amd64 node | `ab5a22bc3ef8bbba5a34624fd49d813f501231175766d6898cf4514ecca3a846` |

## Compact recovery boundary

The capacity gate found more than 140 GiB free and estimated less than 5 GiB
of peak additional writes. The private server recovery set is:

```text
/home/server/mintclaw-recovery-o8-pre-20260901T115415Z
```

The checksum-verified 132 MiB set contains one copy of each affected binary,
five profiles' recovery-critical configuration and run files, affected units,
the small P5a state domain, the main node registry, the 3.6 MiB coding state
root, and the two affected browser ledgers. It excludes unrelated sessions,
traces, logs, media, repositories, worktrees, caches, and previous backups.

The matching Darwin recovery set is:

```text
/Users/ab/mintclaw-recovery-o8-pre-20260901T115415Z
```

That checksum-verified 17 MiB set contains only the previous Darwin node,
configuration, launchd job, identity, and small invocation/file-transfer state.
It excludes the 446 MiB browser and runtime tree, nested backups, logs, and
jobs. The retained monthly full-state baseline remains
`/home/server/mintclaw-r1-backup-20260828T053340Z`.

## Stopped-state data cutover

The coding preflight found four active threads, no repository baseline, four
available leases, and four Seahorse databases with `PRAGMA quick_check=ok`.
The one interactive coding process holding a lease exited cleanly after an
exact `SIGTERM`. A dry run acquired every lease before capturing any baseline
and proved that all four repositories were available, untruncated, and had
complete path and index evidence.

The first strict browser start exposed a separate deployed-data gate. The
source no longer supplied omitted capability or approval modes, but the main
browser ledger contained 90 terminal sessions, 131 prepared actions, and 135
invocations; 93 prepared actions lacked the current explicit modes. The spouse
ledger contained two terminal sessions, one prepared action lacking the modes,
and one succeeded invocation. Every session and prepared action was already
expired, so no live authority or unfinished effect had to be preserved.

Both affected gateways were stopped. Their exact ledgers were added to the
compact recovery manifest, then replaced atomically with an empty version-2
current document. This was a deliberate external discard of obsolete terminal
state, not a conversion or a runtime compatibility reader. The gateways and
companions then started normally.

With all coding leases available, the stopped-state helper captured all four
baselines before publishing the first one, then published and reloaded each
under its existing thread lease. Every final baseline uses schema
`mintclaw.repository_baseline.v1`, merge head `d5d3e63e`, branch `main`, complete
path and index evidence, and no `origin` member. The helper is deployment-only
and was removed with its focused worktree after verification.

## Matched rollback and reapply

The rehearsal stopped the five gateways, launcher, local P5a node, and Darwin
launchd node. It restored the exact previous core/CLI, launcher, Linux node,
Darwin node, pre-publication coding archive, and the two pre-reset browser
ledgers. The old core reported `c1570f6e`; all expected services became active,
all five configurations loaded, all three registered protocol-1 nodes
connected, and the stateless live smoke returned exactly
`O8-ROLLBACK-OLD-OK`. This proves that rollback restores the matching binary
and state together; the current runtime does not need an old-state reader.

The same units were stopped again. The exact O8 binaries were reinstalled, the
pre-publication coding archive was verified unchanged, four fresh baselines
were captured and published, and the two browser ledgers were reset to the
empty current document. The target gateways, launcher, Linux node, and Darwin
node then restarted on the exact digests above. `p5a-canary`, `ab-local-test`,
and the additive older `vpn` protocol-1 peer all reconnected. The VPN peer does
not use an O7/O8 authority-bearing surface; its continued connectivity is
natural additive compatibility, not a guaranteed historical implementation.

## Final canaries and passive traces

The final live request returned exactly `O8-FINAL-LIVE-OK`. The existing
coding thread `65bd0122-f5d5-445d-9011-7eacdb99f866` resumed strictly from its
published baseline and returned exactly `O8-FINAL-CODING-OK`.

The final browser request delegated to the browser specialist and returned
exactly `O8-FINAL-BROWSER-OK`. Parent trace
`trace-turn-301d58176c13998a74edbcaf` completed with 16 records and no
truncation. Its structured child result records title `Example Domain` and an
explicit closure confirmation. Child trace
`trace-turn-04d2dfe440b9a937ffd73f8e` completed with 31 records and no
truncation; records 10-11 opened a managed gateway session, 15-16 observed it,
20-21 navigated, and 25-26 closed the same session with final state `closed`.

The final personal-workspace audit found two singular `AGENT.md` files left
beside the standard `AGENTS.md` files in the main and spouse browser
workspaces. Each singular file was byte-identical to its current plural file,
production source reads only `AGENTS.md`, and both duplicates were deleted.
All 20 configured personal workspaces now contain root `AGENTS.md` and contain
neither root `AGENT.md` nor `IDENTITY.md`. A post-cleanup live request returned
exactly `O8-PERSONAL-CLEANUP-OK`.

## Observation, cleanup, and final audit

The final observation ran from the target service start at 12:15:55Z through
12:26:08Z. All 12 expected product and system services remained active. The
five gateways, launcher, and P5a node each reported `NRestarts=0`; all three
registered nodes remained connected. The launcher, reviewer webhook, and
public HTML probes returned their expected 302, 404, and 401 statuses. The
ten-minute error-priority journal was empty, no unit had failed, and no legacy
product process existed.

All five deployed configurations load under the target core; doctor exit 2
contains existing policy findings and no schema or load error. The final main
browser ledger contains one current closed session, one current prepared
action, and one succeeded invocation from the canary. No prepared action omits
`capability_mode` or `approval_mode`, and no browser state is nonterminal. The
spouse ledger is an empty current version-2 document. All four coding baselines
still bind `d5d3e63e`, contain no `origin`, and their Seahorse databases still
report `quick_check=ok`.

Retention cleanup kept the checksum-verified monthly baseline, the newest O8
compact recovery set, and the immediately previous O7 compact set. Five older
superseded compact sets and the 3.4 MiB rehearsal coding tree were permanently
deleted; their rollback roles are covered by the retained artifacts. The
deployment-only baseline helper and empty-ledger fixture were deleted, the
focused 2.3 GiB build worktree was unregistered and removed, and worktree
metadata was pruned. Free space rose from about 140 GiB to 143 GiB. Shared Go
build, module, and lint caches total about 2.2 GiB, below the 5 GiB cleanup
threshold. The Darwin host retains its newest O8 compact set and the immediately
previous node recovery set.

The final source audit classifies remaining `legacy`, `migration`, `alias`, and
`fallback` matches as the memory benchmark, explicit OpenClaw import command,
Kagi upstream response, provider/platform behavior, current product aliases,
or genuine provider/delivery resilience. Current persisted stores reject a
wrong version and do not select an older implementation. No production match
reads historical MintClaw state, dual-writes a representation, keeps a
deprecated MintClaw API callable, reconstructs an old browser schema, or
backfills a missing repository baseline during resume.
