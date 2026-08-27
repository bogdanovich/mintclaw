# Architecture Simplification R1 Local Node-Identity Bridge Rollout

Date: 2026-08-25 PDT / 2026-08-26 UTC

Status: local bridge rollout complete and reverified after a later service
stop; fleet-wide R1 reset not yet authorized or complete

## Scope And Authority

The operator authorized backing up, installing, restarting, and verifying the
PR #899 bridge on the local `p5a-canary` and `p3-canary` companion services.
The operation did not authorize or change gateway binaries, active profiles,
manual node-registry state, or remote companions.

The deployed source was `origin/main` at `71ad3e53`, which contains PR #899 and
its rollout documentation. A temporary detached worktree produced these exact
artifacts:

| Artifact | SHA-256 |
| --- | --- |
| `mintclaw-node` | `8df6237a7352a04e21a962baa921464a152aec93c61d20c32ac6707b9e92c399` |
| `mintclaw-node-service-helper` | `19ddd97cceeb440161c393147e1a6ff1cca6b8b0bee271aca7ddbfafdd249268` |

The node reports
`v0.1.0-p8a.2-814-g71ad3e53`.

## Backup And Rollback Boundary

The checksum-verifiable backup is retained at:

```text
/home/server/mintclaw-node-bridge-backup-20260826T045522Z
```

It contains the effective and configured pre-rollout node binaries, the P3
helper binary, the affected unit definitions and wrappers, and the exact
configuration files without reproducing their contents here. `SHA256SUMS`
validated every recorded file. `MANIFEST.txt` and `RESULT.txt` record the
effective process revisions and the rollback sequence.

Rollback restores each affected host-side deployment unit as a pair: P5a uses
its backed-up node binary and unit configuration; P3 uses its backed-up node
and helper binaries together before starting the helper and then the node. The
failed intermediate artifacts and evidence were preserved rather than folded
into the final state.

## Rollout Result

P5a accepted the current node directly. Its user service remained active with
zero restarts, the running executable matched the intended node digest, and
the registry showed `p5a-canary` connected on the intended version.

The first P3 node-only attempt failed closed with
`load service helper snapshot: EOF`. The exact previous node was restored. The
unchanged helper also had to be restarted before the old pair recovered,
demonstrating that the P3 node and privileged helper form one same-release
deployment unit. After that recovery was verified, both current artifacts were
installed in one stopped window, the helper was started first, and the node was
started second.

Final P3 verification established:

- `mintclaw-p3-node.service`, `mintclaw-p3-service-helper.service`, and the P3
  canary supervisor were active and running;
- the node and helper had zero restarts, successful results, and zero exit
  status;
- both running executable paths resolved to the dedicated P3 installation;
- both artifact digests matched the intended build; and
- `p3-canary` was connected on the intended node version.

The final observation window had no warning-level entries for either local
node or the P3 helper. All production gateway and reviewer units remained
active, the gateway error window was empty, no failed units or legacy product
processes were present, and the gateway, profiles, and node records had not
been manually cut over.

The temporary 626 MiB build worktree was unregistered and removed after its
read-only Go module cache permissions were normalized. Free space returned to
6.9 GiB.

## Post-Rollout Reverification

A later host operation stopped the static P3 node and service-helper units
cleanly at the same time as other deployment preparation. It did not replace
their installed files. Under the original explicit restart and verification
authority, the retained artifacts were checked against the rollout digests,
the helper was started before the node, and the pair was reverified.

Both units returned active with zero restarts and successful service results.
The node reconnected as `p3-canary` on `71ad3e53` with explicit Ed25519, and
the initial post-start warning/error journal was empty. A subsequent
current-main gateway restart closed the P3 WebSocket once with an expected
EOF; the unchanged node process reconnected 15 seconds later and retained zero
service restarts. P5a remained active on the same verified node digest.

This reverification did not authorize or mutate remote companions, gateway
binaries, active profiles, or registry records. A separate stopped preflight
had already changed the latter data; the current roadmap records that evidence
and keeps its remaining actions distinct from this local rollout.

## Remaining R1 Gate

This rollout still does not permit PR #901 or the strict removal release to
merge or deploy. The later preflight converted all six records to explicit
Ed25519, upgraded `ab-local-test` to the bridge, and completed the version 4
and `AGENTS.md` data cutover. The remaining gates are now narrower:

- connected Linux `vpn` still runs pre-bridge `03b08be2` and must be upgraded
  or deliberately retired;
- the older pending Darwin and revoked Linux records need an explicit retention
  or removal decision;
- the Darwin companion browser path must pass a functional streamed-snapshot
  canary; catalogue advertisement and the separate gateway-local driver are
  already current;
- seven inert tool-policy denies must be removed from the converted configs;
- a same-time full backup of effective binaries and all durable state must be
  created; and
- PR #901 must then merge, deploy, verify, and exercise rollback before Z1 can
  begin.
