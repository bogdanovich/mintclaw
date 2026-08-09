# Node Companion P5a proof matrix

Date: 2026-08-09

Status: complete

This record maps the admitted P5a durable-jobs contract to merged
implementation and exact evidence. It closes only P5a. It does not authorize
P8 remote workspace routing, remote coding workers, Docker or sandbox
executors, a scheduler, companion-restart process survival, live log
streaming, or the remainder of P5.

## Merged implementation

| PR | Merge commit | Scope |
| --- | --- | --- |
| #579 | `ce66516d` | Admission contract and the job/workspace/coding ownership split |
| #583 | `1cd47389` | Direct job domain, durable companion store, process lifecycle, logs, artifacts, cancellation, and restart reconciliation |
| #592 | `12e002a1` | Deny-by-default job profiles, typed commands, descriptor projection, and node-local execution |
| #604 | `acf88149` | Model discovery/invocation/status slice, durable approval, gateway recovery, artifact transfer, and redacted events |
| #611 | `c88baaa1` | Real-process restart/artifact/cancellation canary, public job-artifact authority correction, ordered cursor proof, operator docs, and CI registration |

PR #611 passed Tests, Integration Tests, Linter, Security Check, and Browser
Windows at head `5ecfb114`, received an owner rocket, and merged as
`c88baaa1`. One review/fix cycle strengthened ordered-log evidence without
expanding production scope.

## Requirement matrix

| Requirement | Implementation | Authoritative evidence | Deployment state |
| --- | --- | --- | --- |
| Configured direct job and stable ID | #583, #592, #604 | Manager/store/profile/tool tests plus `TestNodeJobVerticalSliceWithRestartArtifactAndCancellation` | Exact-main Linux target `p5a-canary` completed two explicitly distinct canary jobs |
| Exact target, actor, agent, routed session, execution, and tool-call authority | #592, #604 | Wrong-principal, changed-input, stale-discovery, profile-revision, continuation, and status-isolation tests | Main agent alone is granted exact target/profile; pairing approves only node info and five job commands |
| Durable approval without model self-approval | #604 | Exact retained-plan approval tests and real start/cancel `allow_once` continuations | Operator-authorized live interactions resolved exactly once; the expired first attempt failed closed before dispatch |
| At-most-once launch and no replay | #583, #604 | Duplicate/concurrent start, response-loss, disconnect, invocation recovery, and one-launch fixture | Native macOS proof and Linux CI pass; each live invocation produced one distinct durable job |
| Gateway restart and disconnect continuity | #604 and #611 | Real WSS disconnect, replacement admission/runtime, original `nodes_status`, same job ID, one launch | The first live job survived concurrent gateway restarts and was recovered by the same routed session without another start |
| Later-turn status and ordered bounded logs | #583, #592, #604, #611 | Bounded first chunk, same-cursor replay, forward continuation, exact joined stdout, and separate stderr | Live status recovered terminal `succeeded`; two stdout cursor reads and a separate 16-byte stderr read completed |
| Truthful timeout and cancellation | #583, #592, #604 | Timeout/race/process-group tests plus real approved cancel and terminal cancellation evidence | Native macOS proof and Linux CI pass |
| Declared immutable artifact retrieval | #583, #604, #611 | Anchored snapshot tests plus real list, fresh discovery, internal transfer, spool ownership, size, digest, and byte verification | Declared live artifact was listed as 18 bytes and downloaded to the gateway spool with matching SHA-256 |
| Companion restart does not relaunch | #583 | Lifecycle-boundary reconciliation tests convert nonterminal work to explicit `unknown` or `interrupted` | No process-survival claim |
| Redacted events, approvals, logs, and traces | #604 and #611 | Sentinel tests and real event/diagnostic scans exclude argv, cwd, environment, host paths, and raw log/artifact bytes | Live diagnostic scan found no stdout, stderr, or artifact-content sentinels; opaque references and digests remain visible only where deliberately present in authorized input/final previews |
| Linux and macOS support | #583 and #611 | Shared direct-exec implementation; native macOS real-process canary; native Linux CI canary | macOS proof passed three repetitions; Linux CI and exact-main live companion pass |
| Existing behavior and defaults remain healthy | All P5a PRs | Focused race tests, tagged lint, exact-head CI, and existing invocation/file-transfer E2Es | All product units active, zero recent errors, smoke response exact; reviewer unchanged |

## Real-process proof contract

`TestNodeJobVerticalSliceWithRestartArtifactAndCancellation` runs only on
Linux or macOS with the `integration` build tag. It builds and launches the
real `mintclaw-node`, pairs it over production WSS, and uses a deterministic
model provider through the ordinary agent tools.

The test proves this sequence:

1. Discover `job.start.v1`, suspend for durable human approval, and start one
   direct argv process.
2. Disconnect and close the gateway node runtime while the companion-owned
   process continues, then release and finish that process.
3. Reopen the same durable gateway workspace, reconnect the same node, and
   recover the original start invocation through `nodes_status` without a
   second launch.
4. Query terminal job status in a later turn, replay the same stdout cursor,
   read stderr separately, and list the one declared artifact.
5. Obtain fresh artifact discovery and download the immutable snapshot through
   the existing P2 transfer spool, verifying owner-bound reference, size,
   digest, and bytes.
6. Start another real process, suspend for a second approval, cancel it through
   `job.cancel.v1`, and verify terminal `canceled` evidence and one launch.

The fixture uses an allowlisted executable script and direct argv on both
platforms. It does not use shell-text input, PTY, Docker, a fake node runtime,
or timing as the source of lifecycle truth. Durable store state, marker files,
WSS observations, invocation records, and artifact bytes provide the evidence.

The canary also exposed a fail-closed integration defect: the descriptorless
internal job-artifact transfer was checked as if pairing had to approve a
second hidden command. The correction maps only that internal transfer's
preparation authority to the already approved public `job.artifacts.v1`
command while retaining the exact internal transfer command in the immutable
execution plan. Focused tests prove that unrelated commands are unchanged.

## Configuration and rollback

Jobs remain disabled unless all of these are present:

- an enabled companion `node_job_profiles` entry;
- complete `system_exec` executable and working-scope discovery aliases;
- a gateway node target bound to the exact `job_profile`;
- agent target policy granting that target; and
- pairing approval for every intended public job command.

Start and cancel default to required human approval; reads default to no
approval. A model cannot choose an executable path, working root, profile,
executor, OS identity, shell, log path, or retention policy.

Rollback is configuration-first: disable or remove the companion job profile,
remove the target's `job_profile` binding, restart only the affected companion
and gateway profiles, and renew changed-catalog pairing approval without job
commands. Already accepted nonterminal work is not replayed. Graceful companion
shutdown performs bounded cleanup; after a companion or host restart, an
unproven prior process is reported as `unknown` or `interrupted`.

## Deployment evidence

Merged main `c88baaa1` was built in clean detached worktrees on Linux amd64 and
macOS amd64. The installed Linux binaries are:

- gateway: `d15ed99ec1ce5184b3826b6a59719d041b39151cef93c6a06487a25a2275a039`;
- companion: `a29bdfee714fd3437b539b370aee01ecebe33df3a24eb9b59bdeee1770c81f4e`;
  and
- web launcher: `a145b84cf4b27ee3573433b60f2a4380ab4d6cdc71621961f1e3a61417bad741`.

The native macOS amd64 companion build has SHA-256
`361083a351d426f73c3e415f9b1ee0807c958d6eb9eaa9661344393b28310db5`.
The exact review head passed the real-process canary three times on macOS; the
same canary passed in native Linux CI. Cross-builds also succeeded for Linux
amd64/arm64 and macOS amd64/arm64.

The bounded live Linux profile uses a separate `p5a-canary` identity, systemd
user service, state directory, executable, working scope, target binding, and
job profile. Pairing grants `node.info.v1` plus only the five public job
commands; `system.exec.v1` and `system.which.v1` remain unapproved. The main
agent alone receives the target alias. The node reports exact-main version,
Linux amd64, profile revision `p5a-canary-profile-v1`, and connected state.
Model-facing discovery returned `job.start.v1` as available with only
executable alias `canary` and working scope `canary-root`.

The first approval attempt, interaction `ee495408`, expired before the answer
arrived and failed closed without dispatch or a job record. A fresh
operator-authorized interaction started job
`job_7447ec5401797bbdb80226b97aa8a87f` exactly once. The job reached
`succeeded` with exit code 0, 31 bounded stdout bytes, and 16 bounded stderr
bytes. Concurrent gateway restarts interrupted live clients after dispatch;
the same routed session recovered terminal status and logs without issuing a
second start. Two forward stdout reads and one stderr read exercised the
public cursor API. Exact same-cursor replay and concatenated byte ordering are
covered by the native real-process and Linux CI proof.

That first request did not declare its output file, so `job.artifacts.v1`
correctly returned no downloadable artifact even though the process created a
file. A separate, explicitly named artifact canary started
`job_901a41dd9b9dc8038d18d05f032b29f6` with the declaration
`result -> p5a-live-result.txt`. It completed once with the same bounded logs
and retained one 18-byte artifact. Listing returned opaque reference
`jobart_b1722fa274fd11a301f1ea88126b859e` and SHA-256
`f09d1944d916e0718fbd3bd4f3038ad83639c73609ab0ffb07e8a8327642a867`.
After a fresh public `job.artifacts.v1` discovery revision, `nodes_download`
committed the bytes to gateway artifact
`transfer-artifact://a8a568d330ff6509f38421c9197b5300` with the same size
and digest and `deliver=false`. An intentionally incomplete download request
without job ID and discovery revision was denied with
`FILE_TRANSFER_DENIED`, demonstrating fail-closed argument binding.

All live approval interactions for the successful jobs and artifact download
are resolved with one accepted answer. The final diagnostic scan found no raw
stdout, stderr, or artifact-content sentinel in trace files. Opaque artifact
references and the non-secret digest occur only in authorized input and final
response previews, while typed runtime events retain bounded state and count
facts. No host path, environment value, raw artifact byte, or unrestricted log
content was added to passive event evidence.

The rollback backup is
`/home/server/mintclaw-p5a-backup-20260809T120507Z`; it contains the prior
binaries, systemd units, service states, checksums, and the main configuration
before the canary target. Rollback disables and stops only
`mintclaw-node-p5a-canary.service`, restores that main config, restarts only
`mintclaw-main`, and restores the exact binaries only if core rollback is
required. No mutable job state is restored over newer state.

Only `mintclaw-main` and `mintclaw-main-web` were restarted for the core
deployment, followed by one additional main restart for the bounded target
configuration. Later concurrent gateway-only restarts exercised durable live
recovery without restarting the companion or reviewer. The reviewer retained
PID `2300834` and its original start time. Final checks show main, web,
reviewer, and the canary companion active, with zero error-level main/web/node
journal entries in the final 15-minute window. The launcher returns the
expected HTTP 302, and stateless live smoke returned
`P5A_DEPLOY_C88BAAA1_OK`. Diagnostic
trace `trace-turn-608ac17b14ea607396653dbd` has schema
`mintclaw.diagnostic_trace.v1`, completed outcome, eight records, and no
truncation.

An initial isolated macOS deployment attempt used a separate identity rather
than the shared browser target. External WSS admission did not complete within
the handshake timeout, so that LaunchAgent was stopped and its files were
preserved at
`/Users/ab/mintclaw-p5a-external-wss-backup-20260809T122224Z`. This record does
not claim a live external-ingress macOS canary; native macOS production-WSS
real-process evidence and the live Linux exact-main canary are the scoped
platform proof.

## Completion and mandatory stop

Every admitted P5a Definition-of-Done item is evidenced above. P5a is
complete. Stop this workstream here: do not begin P8, Docker/sandbox
executors, live streaming, scheduling, companion-restart process survival, or
any other deferred P5 capability.
