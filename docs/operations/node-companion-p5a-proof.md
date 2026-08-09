# Node Companion P5a proof matrix

Date: 2026-08-09

Status: implementation proof in progress; deployment evidence pending

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
| Proof PR | pending | Real-process restart/artifact/cancellation canary, public job-artifact authority correction, operator docs, and CI registration |

The proof PR must pass Tests, Integration Tests, Linter, Security Check, and
the repository's platform checks before this table can record its merge.

## Requirement matrix

| Requirement | Implementation | Authoritative evidence | Deployment state |
| --- | --- | --- | --- |
| Configured direct job and stable ID | #583, #592, #604 | Manager/store/profile/tool tests plus `TestNodeJobVerticalSliceWithRestartArtifactAndCancellation` | Pending merged-main deployment |
| Exact target, actor, agent, routed session, execution, and tool-call authority | #592, #604 | Wrong-principal, changed-input, stale-discovery, profile-revision, continuation, and status-isolation tests | Deny-by-default target/profile configuration pending |
| Durable approval without model self-approval | #604 | Exact retained-plan approval tests and real start/cancel `allow_once` continuations | Pending live canary |
| At-most-once launch and no replay | #583, #604 | Duplicate/concurrent start, response-loss, disconnect, invocation recovery, and one-launch fixture | Native proof passes on macOS; Linux CI pending |
| Gateway restart and disconnect continuity | #604 and proof PR | Real WSS disconnect, replacement admission/runtime, original `nodes_status`, same job ID, one launch | Native proof passes on macOS; Linux CI pending |
| Later-turn status and ordered bounded logs | #583, #592, #604 | Store/router/tool tests plus repeated stdout cursor and separate stderr reads | Native proof passes on macOS; Linux CI pending |
| Truthful timeout and cancellation | #583, #592, #604 | Timeout/race/process-group tests plus real approved cancel and terminal cancellation evidence | Native proof passes on macOS; Linux CI pending |
| Declared immutable artifact retrieval | #583, #604 and proof PR | Anchored snapshot tests plus real list, fresh discovery, internal transfer, spool ownership, size, digest, and byte verification | Native proof passes on macOS; Linux CI pending |
| Companion restart does not relaunch | #583 | Lifecycle-boundary reconciliation tests convert nonterminal work to explicit `unknown` or `interrupted` | No process-survival claim |
| Redacted events, approvals, logs, and traces | #604 and proof PR | Sentinel tests and real event/diagnostic scans exclude argv, cwd, environment, paths, log bytes, artifact refs, and digests | Pending merged-main scan |
| Linux and macOS support | #583 and proof PR | Shared direct-exec implementation; native macOS real-process canary; native Linux CI canary | Linux CI pending |
| Existing behavior and defaults remain healthy | All P5a PRs | Focused race tests, tagged lint, exact-head CI, and existing invocation/file-transfer E2Es | Pending deployment health check |

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

Pending after the proof PR merges. Record here:

- exact merged-main commit and binary digests;
- focused merged-main validation and native Linux/macOS evidence;
- backup locations and the exact gateway/companion profiles changed;
- deny-by-default configuration and one bounded live job canary where safe;
- node, gateway, web, and log health after restart;
- confirmation that the reviewer process was not restarted; and
- rollback verification.

## Completion and mandatory stop

P5a is not complete while this status is pending. After the proof PR, Linux CI,
merged-main deployment, real platform evidence, rollback record, and final
health checks are recorded, change this status to complete and stop. Do not
begin P8 or any deferred P5 capability from this workstream.
