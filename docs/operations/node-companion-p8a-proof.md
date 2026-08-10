# Node Companion P8a proof matrix

Date: 2026-08-10

Status: implementation and deterministic proof complete; bounded deployment
evidence pending

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

## Requirement matrix

| Requirement | Implementation | Authoritative test evidence | Deployment state |
| --- | --- | --- | --- |
| One configured alias binds one target and working scope | #635 | config validation, target-policy isolation, fresh catalog, profile and revision tests | Pending bounded `vpn` and `ab-local-test` profiles |
| Local omission and explicit remote selection | #635 | decorator, unknown-alias/no-fallback, reload, and `TestRemoteWorkspaceVerticalSliceRealProcess` local-vs-remote read | Pending merged-main canary |
| Bounded native read and search | #635 | traversal/symlink/special-file, pagination, ignored-file, ordering, timeout, result-bound, Linux/macOS, race, and real-process WSS tests | Pending merged-main canary |
| Whole-file write and structured patch | #663 | exact approval, create/replace/delete, identity/hash conflict, prepared publication, committed-prefix truth, concurrent/duplicate, cancellation, and real-process WSS tests | Pending merged-main canary |
| Foreground direct argv in the same scope | #674 | alias-only arguments, timeout/output bounds, config reload, failure/unknown, one-launch, and real-process WSS tests | Pending merged-main canary |
| Existing durable P5a job composition | #674 plus P5a | workspace job grant/profile tests and `TestNodeJobVerticalSliceWithRestartArtifactAndCancellation` restart/status/log/artifact/cancel proof | Pending merged-main canary |
| Exact approval continuation and stop-race truth | #629, #635, #663, #674 | live-worker completion, consumed-answer cancellation, changed-input/revision/catalog, and retained-plan tests | Pending live approval canary where policy requires it |
| No replay after uncertainty | #635, #663, #674 | disconnected read/write/exec tests, status recovery, duplicate identity, and real-process disconnect with exactly one fixture launch | Pending merged-main disconnect canary |
| Redacted typed observations | #635, #663, #674 | tool-log, event, trace, path/content/patch/search/argv/environment sentinel tests | Pending deployed trace/log scan |
| Linux and macOS compatibility | all implementation PRs | shared native implementation, focused platform tests, integration-tag real-process test, and CI | Pending exact-main live profiles on both platforms |

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

Pending. This section is updated only after the proof PR merges, exact merged
`main` is built, bounded profiles are enabled on `vpn` and `ab-local-test`, and
health, discovery, local-default behavior, redaction, reconnect/status, and
no-duplicate-effect canaries pass without restarting unrelated reviewer or
browser workspaces.

## Completion and mandatory stop

P8a is not declared complete until the deployment section records both live
platform profiles and every admission Definition-of-Done gate. Once that
evidence is merged, stop this workstream before P8b or any deferred capability.
