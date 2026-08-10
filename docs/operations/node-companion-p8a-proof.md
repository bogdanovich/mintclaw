# Node Companion P8a proof matrix

Date: 2026-08-10

Status: complete; implementation, deterministic proof, and bounded Linux and
macOS deployment evidence recorded

This record maps the admitted P8a remote-workspace contract to merged code and
authoritative evidence. P8a is a stateless placement layer for the live
AgentLoop. It does not move the model, session, memory, channel, approval
authority, or coding transcript to a companion and does not authorize P8b,
remote coding workers, browser/MCP routing, synchronization, or shell jobs.

## Merged implementation

| PR | Merge commit | Scope |
| --- | --- | --- |
| #629 | `7d432204` | Finish an approved foreground continuation before the live worker interprets its stop signal, while retaining canceled outcome truth |
| #631 | `98fd79b7` | P8a admission, boundaries, validation matrix, and mandatory stop |
| #635 | `d7daccab` | Remote workspace config/router plus bounded native read and search |
| #663 | `a70f42be` | Bounded whole-file write and structured patch over P2 confinement and durable invocation truth |
| #674 | `ae0f53d1` | `workspace_exec` foreground direct argv and composition with the existing P5a job start |
| #676 | `17ae1bde` | Deterministic Linux/macOS real-process proof and initial proof matrix |
| #679 | `c450e865` | Project the selected file profile consistently at gateway admission, companion authorization, and replay |

## Requirement matrix

| Requirement | Implementation | Authoritative test evidence | Deployment state |
| --- | --- | --- | --- |
| One configured alias binds one target and working scope | #635 | config validation, target-policy isolation, fresh catalog, profile and revision tests | `vpn-workspace` and `ab-workspace` each bind one target, one scope, and one exact file/job profile |
| Local omission and explicit remote selection | #635 | decorator, unknown-alias/no-fallback, reload, and `TestRemoteWorkspaceVerticalSliceRealProcess` local-vs-remote read | Omitted workspace read remained gateway-local; explicit Linux and macOS aliases produced remote effects |
| Bounded native read and search | #635 | traversal/symlink/special-file, pagination, ignored-file, ordering, timeout, result-bound, Linux/macOS, race, and real-process WSS tests | Live exact-file reads and searches succeeded on both deployed profiles |
| Whole-file write and structured patch | #663, #679 | exact approval, create/replace/delete, identity/hash conflict, prepared publication, committed-prefix truth, multi-profile projection, and real-process WSS tests | Linux and macOS create/read/patch/read canaries produced verified bytes and SHA-256 values |
| Foreground direct argv in the same scope | #674 | alias-only arguments, timeout/output bounds, config reload, failure/unknown, one-launch, and real-process WSS tests | `echo p8a-foreground-ok` succeeded on both platforms through `workspace_exec` |
| Existing durable P5a job composition | #674 plus P5a | workspace job grant/profile tests and `TestNodeJobVerticalSliceWithRestartArtifactAndCancellation` restart/status/log/artifact/cancel proof | Linux reconnect recovery and macOS later-turn status recovered stable job IDs as `succeeded` |
| Exact approval continuation and stop-race truth | #629, #635, #663, #674 | live-worker completion, consumed-answer cancellation, changed-input/revision/catalog, and retained-plan tests | Exact continuation is deployed; bounded workspace profiles intentionally use their existing no-approval operator policy |
| No replay after uncertainty | #635, #663, #674 | disconnected read/write/exec tests, status recovery, duplicate identity, and real-process disconnect with exactly one fixture launch | Linux patch uncertainty and macOS update disconnect were recovered by status only; no model invocation replay occurred |
| Redacted typed observations | #635, #663, #674 | tool-log, event, trace, path/content/patch/search/argv/environment sentinel tests | Operational log scan found no canary content or argv; private traces retained hashed, redacted call arguments and bounded diagnostic results |
| Linux and macOS compatibility | all implementation PRs | shared native implementation, focused platform tests, integration-tag real-process test, and CI | Healthy bounded profiles are connected on Linux `vpn` and Darwin amd64 `ab-local-test` |

## Deterministic real-process proof

`TestRemoteWorkspaceVerticalSliceRealProcess` runs on Linux and macOS with the
`integration` build tag. It builds and launches the actual `mintclaw-node`,
pairs it over the production WSS admission/session stack, registers the
ordinary agent tool surface, and performs native filesystem and process
effects beneath a temporary companion-owned root.

The canary proves that omitting `workspace` reads the gateway-local file while
the explicit alias reads the distinct remote file. It then creates remote
content, finds it with native bounded search, patches it, runs an allowlisted
foreground direct-argv fixture, and starts the same fixture through the
existing durable job manager. Finally it disconnects the real companion after
a foreground mutation has recorded its launch, reconnects the same identity,
repeats the same stable tool-call identity, receives uncertainty instead of a
fresh execution, and verifies exactly one native launch marker.

Detailed durable job restart, later-turn status, ordered logs, artifacts, and
cancellation remain proven by
`TestNodeJobVerticalSliceWithRestartArtifactAndCancellation`; P8a reuses that
manager and lifecycle rather than duplicating its harness. Focused P8a tests
prove workspace/job binding and stable authority. Together these tests cover
the admitted complete path without adding a second runner, protocol, store, or
test framework.

## Configuration, security, and rollback

Remote workspaces default to an empty map. Enabling one requires all of:

- one existing node target allowed by the agent's target policy;
- one exact file profile and authenticated working-scope alias for file tools;
- paired approval of only the intended `workspace.*.v1` commands;
- an executable alias plus `system.exec.v1` for foreground execution; and
- a bound P5a job profile, separate `jobs` grant, and `job.start.v1` for job
  mode.

The model cannot choose a node ID, host, root, profile, executable path, cwd,
shell, policy revision, or approval. Paths are relative and remain confined by
P2's no-follow resolver and native publication rules. Unknown aliases,
disconnected targets, stale discovery, and changed authority fail without
local fallback. Post-dispatch mutation uncertainty is retained and never
blindly replayed.

Rollback removes the remote workspace entry and its agent target grant first,
then removes workspace commands/profile bindings from the affected companion
and pairing approval. It does not restore an older invocation ledger over
newer durable state. Local file tools need no migration or rollback because
their omitted-workspace path is unchanged.

## Deployment evidence

Deployment completed on 2026-08-10. The gateway runs merged `main` at
`9bcff61c`, which contains the complete P8a implementation through `c450e865`;
the intervening merged change touches only the gateway browser package. The
`v0.1.0-p8a.2` node release points exactly to `c450e865`. Its manifest and
signature were verified offline with the configured Ed25519 public key before
activation; the manifest SHA-256 is
`728d9ce6c3bc4ae1e8b70a7672aeb8f6464b9f1508e5d0a5d94fd8b1438abbc6`.

The Linux `vpn` companion is connected with bounded root
`/home/deploy/mintclaw-node-canary`, file profile `workspace-files`, and the
existing P5a job and direct-exec profiles. Live model-facing calls created,
read, searched, and patched remote files, ran foreground direct argv, and
started durable job `job_1b2618a3f4ab886b9f4b9fb4c41fec77`. Status from the
wrong routed session was denied; the original routed session recovered the
succeeded job after reconnect. An uncertain patch was not replayed. A later
exact patch produced `p8a-patch-ok\n`, 13 bytes, SHA-256
`f1216b1ad32b3604c04705e2eea31aca2ee8e478496225d4e221ad08db4be00a`.
Trace `trace-turn-ddaa7d602ed03cf40563e844` records the uncertainty/status
path, and `trace-turn-4d918bacf20fa177e700a5b4` records the successful
merged-main read.

The macOS `ab-local-test` companion is connected under the stable launchd
coordinator on `v0.1.0-p8a.2`, with bounded root
`/Users/ab/mintclaw-node-canary` and selected profile
`p8a-workspace-files`. The update invocation
`inv_ddc3a2f63662cea23ce21aa540c42426975d7999130791f304d390194d8dc455`
crossed the activation disconnect once, was never replayed, and recovered by
`nodes_status` as `succeeded`, `healthy`, and `successor_verified`, with no
rollback. Reloading the durable pairing registry during its stable-health
window caused one bounded candidate-process relaunch, so the coordinator
records two launch attempts for that one activation transaction; no second
model invocation, staging transaction, or payload activation decision
occurred. The deterministic real-process canary remains the authoritative
exact-one-launch disconnect proof.

The macOS model-facing file sequence created, read, searched, patched, and
read `p8a-ab-live.txt`; the final content is `after-ab-p8a\n`, 13 bytes,
SHA-256
`624cca85be93d8e9d711e567311251428d493df3bb54e13099290bf03b9c3978`.
Foreground `echo` returned exit code zero. Durable job
`job_90a8af7bf0f5fea3bad9e505dd8436f0` was recovered in a later turn as
`succeeded` with exit code zero. The relevant private traces are
`trace-turn-404733c156fd7f3bfc93619f` for the file sequence and
`trace-turn-2bdca97562415be2498eb7e8` plus
`trace-turn-3fd8db72a200db4cd11365aa` for exec and later status.

Gateway, web, reviewer, Linux node, and macOS node health checks are active;
the final gateway reload preserved the reviewer process. Pairing approval
retains the existing browser commands while adding no browser workspace
routing. Operational gateway logs contained none of the file-content or argv
canary sentinels. Local-default read behavior remained gateway-local. Backups
were retained at `/home/server/mintclaw-p8a-profile-fix-20260810T203307Z`,
`/home/deploy/mintclaw-p8a-profile-fix-20260810T203439Z`, and
`/Users/ab/mintclaw-p8a-final-20260810T210500Z`.

## Completion and mandatory stop

Every admitted Definition-of-Done gate is evidenced above. P8a is complete.
Stop this workstream before P8b, remote coding workers, shell jobs,
browser/MCP workspace routing, synchronization, Docker, fleet, bootstrap, or
other deferred capability work.
