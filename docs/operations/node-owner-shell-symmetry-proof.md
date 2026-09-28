# Node Owner-Shell Cross-Platform Symmetry Proof

## Status

Complete on 2026-09-28 UTC. The four admitted executor/OS cells share one
model-facing `shell.exec.v1` contract. The two modes that are explicitly
configured in this deployment are live and healthy. The other two remain
disabled but have native platform proof; no executor was enabled implicitly.

This record closes the follow-up admitted by
[`node-owner-shell-symmetry-admission.md`](../architecture/node-owner-shell-symmetry-admission.md).
It does not admit macOS PTY or confirmed cancellation, managed-update
composition for the macOS helper, generic sudo, fleet work, or another command
surface.

## Merged Changes

| PR | Merge | Result |
| --- | --- | --- |
| [#1399](https://github.com/bogdanovich/mintclaw/pull/1399) | `623260d818dd5675a9d6da712ee9a8c67234d220` | Architecture, authority, lifecycle, validation, and mandatory-stop contract |
| [#1400](https://github.com/bogdanovich/mintclaw/pull/1400) | `56ba1ae980e896558668c225d35b7b4afd18eb9a` | Shared Unix `local_user` executor enabled on Linux with conservative outcome truth |
| [#1407](https://github.com/bogdanovich/mintclaw/pull/1407) | `8ebcd5b116d4e8ac437389901578971e2cc266a5` | Private macOS privileged helper, exact LaunchDaemon lifecycle, shared policy frames, and truthful capability projection |

The final native builds and focused validation used merged `main`
`281e84b8230c857fae5df62bea1bd116834bfd2a`, the current tree after the
concurrent browser and context work required for this canary. This closeout PR
was subsequently rebased onto `7f52994e5`; that intervening local-coding merge
does not touch the companion packages or alter the recorded deployed artifact.

## Implemented Matrix

| OS | Executor | Effective authority | Confirmed cancel | Agent PTY | Evidence and deployment |
| --- | --- | --- | --- | --- | --- |
| Linux | `local_user` | Companion service account | No | No | Native Linux process, identity, environment, output, child-survival, timeout, and ambiguity race tests; disabled in deployed VPN config |
| macOS | `local_user` | Companion service account | No | No | Native race suite plus live `ab-2` invocation as UID 501 in `/Users/ab` |
| Linux | `privileged_helper` | Exact root-owned profile | Yes | Yes | Existing broker regression/race proof plus live `vpn` invocation as UID 0 in `/` |
| macOS | `privileged_helper` | Exact root-owned profile | No | No | Native private-channel, supervised-process, identity, environment, disconnect, saturation, detached-child, path, and LaunchDaemon transaction tests; built but not installed because no root-owned operator policy is configured on this Mac |

The macOS helper artifact being present is not authority. Its node config must
select `privileged_helper`, its root-owned broker config must bind the exact
companion binary, config, account, and profile, and the system LaunchDaemon
must be installed explicitly. This deployment had no such operator policy and
non-interactive `sudo` was unavailable, so the safe result was to leave the
mode disabled rather than partially install or fall back to a public socket.

## Native Validation

On Linux amd64 at the exact deployed main commit, this focused race run passed:

```text
go test -race -tags goolm,stdjson ./pkg/nodes/companion \
  -run 'Test(LinuxAuthorityBrokerPolicyRejectsInvalidPeerBoundary|ConfigNormalizesLocalUserOwnerShell|ConfigRejectsAmbiguousOwnerShellExecutor|LocalUserShellExecRunsAsCompanionAccount|LocalUserShellCancellationIsUnknownAfterStart|LocalUserShellBoundsOutput|LocalUserShellBackgroundChildIsUnknown|ConfigNormalizesLinuxPrivilegedShellHelper|ConfigRejectsLinuxPrivilegedShellWithoutEndpoint|ConfigMigratesLinuxBrokerSocketToPrivilegedHelper)$'
ok github.com/bogdanovich/mintclaw/pkg/nodes/companion 1.843s
```

On Darwin amd64 at the same main, both focused race packages passed:

```text
go test -race -tags goolm,stdjson ./pkg/nodes/companion ./cmd/mintclaw-node
ok github.com/bogdanovich/mintclaw/pkg/nodes/companion 172.369s
ok github.com/bogdanovich/mintclaw/cmd/mintclaw-node 4.077s
```

That Darwin run includes real process execution through the private inherited
capability, exact configured identity/environment, background-child unknown
truth, disconnect unknown truth, 8+1 saturation without factory loss, root
path verification, exact companion binding, create-only LaunchDaemon
publication, readiness, failure rollback, and privileged-mode lifecycle
rejections. PR CI supplied the broad tagged repository, lint, platform-build,
and unchanged-subsystem gates; no local full suite was repeated.

## Deployment

### Linux privileged helper

The VPN node retained its existing explicit root profile and moved from the
load-time `broker_socket` alias to the current configuration:

```json
{
  "owner_shell": {
    "enabled": true,
    "privileged_helper": {
      "endpoint": "/run/mintclaw/node-authority-broker.sock"
    }
  }
}
```

Recovery sets were created before mutation:

- `/home/deploy/mintclaw-owner-shell-symmetry-backup-20260928T031631Z`;
- `/var/backups/mintclaw-owner-shell-symmetry-20260928T031631Z`; and
- `/home/server/mintclaw-owner-shell-symmetry-backup-20260928T031631Z`
  for the gateway-host build/install baseline.

Final artifacts from that exact merged-main commit are:

| Artifact | SHA-256 |
| --- | --- |
| VPN `mintclaw-node` | `4b25106db21442bb4b7d23846d5f69e5e752a411c567e0861de3c89f07e664a6` |
| VPN `mintclaw-node-broker` | `f6dbc29167342a72ddbb1735cbed01ed21ffa0215193676a711c0a63ec36329a` |

The first rollout exposed an operational ordering invariant: the Linux broker
pins the first admitted companion PID. Restarting broker and node as separate
operations can bind the fresh broker to the old node and make the replacement
fail closed with an EOF while loading its snapshot. The saved binaries and
config restored successfully. The corrected rollout restarted both units in
one systemd transaction, letting the declared `After`/`Requires` graph stop and
start the exact pair together. A second automatic rollback exercised the same
recovery set when an overly strict canary expected socket group `root` instead
of the configured companion group `deploy`; runtime itself had already
connected successfully.

The final transaction ended with both units `active/running`, `NRestarts=0`,
socket `root:deploy:0660`, one process in the exact companion cgroup, no
warning-or-higher journal entries after activation, and admission state
`connected`. The changed catalog was renewed with the previous exact command
allowlist; no new command was granted.

### macOS local user

The active `io.github.bogdanovich.mintclaw.node.local-test` LaunchAgent was
backed up under
`/Users/ab/mintclaw-owner-shell-symmetry-backup-20260928T034324Z`, then only
that companion was replaced and restarted. The reviewer, gateway, tunnel, and
unrelated LaunchAgents were not restarted.

| Artifact | SHA-256 |
| --- | --- |
| Installed Darwin `mintclaw-node` | `dbdf950d3779ce35cb1039aadf6825664aa159607e1da062e0a0138a19a8d6fa` |
| Built, disabled Darwin `mintclaw-node-broker` | `0b56de5c8eb9a43f74676f709797b83f80c3497af3d5a25103f68b11343a0cb9` |

The LaunchAgent returned `running` with the exact configured binary and config
arguments. The connected node advertised merged-main version
`v0.1.0-p8a.2-2445-g281e84b82`; its catalog already matched its approved hash.

## Model-Facing Canaries And Exactly-Once Evidence

The live gateway agent used fresh discovery followed by one `nodes_invoke`.
It did not use `system.exec.v1`, terminal, jobs, coding, browser, or file tools.
Both aliases are explicit owner targets in the deployed exact-target approval
bypass list, so these canaries did not manufacture or self-answer a human
approval. Required-approval binding and continuation remain covered by the
existing invocation/interaction suites.

### Linux `vpn`

Session `owner-shell-symmetry-vpn2-20260928` returned:

```text
OWNER_SHELL_VPN_20260928_OK
UID 0
hostname ip-172-26-15-101
cwd /
status succeeded
supports_cancel true
supports_terminal true
```

Private passive trace `trace-turn-9fd14554b0f93c5d7a201836` is complete and
untruncated. Records 7/8 contain one `nodes` call/result and records 14/15
contain one `nodes_invoke` call/result under tool-call ID
`call_hshekwg4lI32ZcI6TncCl3YO`. There is no second invocation record.

### macOS `ab-2`

Session `owner-shell-symmetry-macos-20260928` returned:

```text
OWNER_SHELL_MACOS_20260928_OK
UID 501
hostname ab-2.local
cwd /Users/ab
status succeeded
supports_cancel false
supports_terminal false
```

Private passive trace `trace-turn-086aba90594320fbe8660826` is complete and
untruncated. Records 7/8 contain one `nodes` call/result and records 13/15
contain one `nodes_invoke` call/result under tool-call ID
`call_1gC3co7c8TRhnCPK4k3NVb6z`. There is no second invocation record.

The trace files remain private deployment evidence. They are not published as
fixtures or used for replay/evaluation.

## Health And Rollback

The final stack audit reported every MintClaw product unit active, zero product
or global failed units, zero legacy processes, and zero error-level entries in
the ten-minute window. The reviewer remained active and was never restarted.

Linux rollback restores the user and root recovery sets together, restores the
previous config, and restarts node and broker in one systemd transaction. It
then verifies exact hashes, socket ownership, cgroup membership, admission,
and the prior catalog grant. macOS rollback restores the saved node binary and
kicks only `io.github.bogdanovich.mintclaw.node.local-test`; the saved config
and plist were unchanged. No privileged macOS rollback is needed because that
mode was not installed.

## Mandatory Stop

The symmetric backend matrix, truthful capability differences, current config
vocabulary, native proof, applicable live deployments, health, rollback, and
exactly-once model canaries are now evidenced. Stop here. macOS PTY, stronger
descendant containment, managed-update/helper composition, generic sudo,
fleet management, bootstrap, and unrelated companion roadmap work require a
new admission.
