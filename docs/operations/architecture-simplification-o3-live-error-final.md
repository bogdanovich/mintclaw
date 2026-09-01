# Architecture Simplification O3 Live Error-Final Deployment

Date: 2026-09-01

Outcome: passed. PR #1019 makes successful and failed agent turns publish the
same canonical terminal metadata while preserving the inbound request
identity. The one-shot live client still rejects uncorrelated traffic, but now
returns as soon as it receives either correlated success or correlated error
final instead of waiting for the outer timeout.

## Ownership change

The error path previously published without the inbound context. The gateway
therefore delivered a durable final whose MintClaw payload lacked the
`request_id` expected by the live client. The client correctly ignored that
uncorrelated message and waited until its timeout.

Both error call sites now pass their existing inbound context to the existing
response publisher. Error and success terminals both carry canonical
`message_kind=final_reply` and `outbound=final` metadata. The change adds no
second delivery owner, retry loop, wire version, historical reader, or client
fallback.

## Review, release, and recovery

The merged source is merge commit `6fc47a3e`. Its exact PR head `4e1de021`
passed all nine CI jobs, the exact-head automated review reported no
high-confidence finding, no review thread remained open, and the owner rocket
authorized merge.

Core, node, and launcher artifacts were compiled from the merge commit. Only
the affected core runtime and the incompatible Darwin/amd64 first-party
companion were installed. The final deployed core reports
`v0.1.0-p8a.2-1187-g6fc47a3e`, built with Go 1.26.6, and has SHA-256 digest
`303c2734142a785e5f407ddcaeba6a5033616e7406be72d82227e4107b6c2e45`.

Before the first build, the server capacity gate recorded 151,735,504,896
bytes free. Estimated peak writes were less than 1 GiB, leaving more than the
required additional 5 GiB headroom. The server compact recovery set is:

```text
/home/server/mintclaw-recovery-o3-pre-20260901T055100Z
```

The private 82 MiB set contains 45 files: one copy of the exact pre-deploy
core digest, five profiles' affected configuration and security files, user
units and drop-ins, restore metadata, and the one node registry later included
for the coordinated reset. It excludes all other runtime state, sessions,
traces, logs, repositories, worktrees, caches, media, browser profiles, and
nested backups. Its final `SHA256SUMS` passes.

The Darwin companion capacity gate found approximately 1.2 TiB free. Its
private 17 MiB, seven-file recovery set is:

```text
/Users/ab/mintclaw-recovery-node-local-test-pre-20260901T060100Z
```

It contains only the previous companion binary, `config.json`, identity,
launchd plist, service metadata, and checksums. It excludes the 446 MiB
`local-test` runtime tree, including logs, transfer and invocation state,
nested backups, and test rollback fixtures. Its `SHA256SUMS` also passes.

## Strict rollout, rollback, and compatibility reset

The first core restart exposed a separate browser catalogue transition already
merged through PR #1017. The main gateway rejected one persisted
`ab-local-test` record because its browser input schema predated the current
restricted-policy typed contract. The gateway entered its configured restart
loop. The other profiles started normally.

The health gate failed, so the five gateways were restored immediately to the
checksum-verified pre-deploy core digest
`944584cb688d21ab9a71926746a42cf26070ec45a63e590c00edf535e2ec6247`.
All five services returned active before the reset continued.

Read-only inspection tied the incompatible record to the active launchd job
`io.github.bogdanovich.mintclaw.node.local-test` on `ab-2.local`. It was using
Darwin node `v0.1.0-p8a.2-1160-gb7bf8b25` and had refreshed the stale record
immediately before the restart. The unrelated local P5a canary, VPN companion,
and isolated P4 coordinator were not the source.

The coordinated reset then:

1. built and verified the Darwin/amd64 node from `6fc47a3e`;
2. unloaded only the `local-test` launchd job;
3. stopped only `mintclaw-main.service` and archived its exact registry;
4. removed only the incompatible `ab-local-test` record under its file lock;
5. installed and started the current core, which held a stable PID with
   `NRestarts=0` through an isolated startup gate;
6. installed the current companion, restored its launchd job, and admitted the
   same cryptographic node identity as a new pending pairing; and
7. restored alias `ab-local-test` with its previous bounded browser/file
   authority plus the new `browser.policy.evaluate.v1` command.

The approved surface contains 13 commands. It does not grant terminal,
`system.exec`, job, or workspace authority. The companion reconnected as the
same node ID on `v0.1.0-p8a.2-1187-g6fc47a3e`; its binary digest is
`3b7b9bde3c87d3dcbac923164b23560673eadfda3c8a7a05181fbef8268d7485`.
The remaining four gateways were then restarted onto the current core.

This reset deletes obsolete persisted catalogue state and upgrades the
first-party peer. It does not add a runtime adapter or reconstruct the older
schema. Older P5a and VPN protocol-1 peers remained connected through their
still-additive non-browser capability surfaces.

## Regression and live evidence

The merged server regression
`TestRunTurnAndDrainSteeringPreservesInitialRequestCorrelationOnError` proves
that an error final retains the inbound actor, agent, channel, chat, session,
message, and request identities and carries canonical terminal metadata.

The merged client regression
`TestRunLiveReturnsCorrelatedErrorFinalWithoutWaitingForConnectionClose`
serves a correlated error final and deliberately keeps the WebSocket open. The
client returns the error response and outcome immediately, proving it does not
wait for connection close or the outer timeout. Both exact regressions passed
again on the merge commit before installation. Production error injection was
not attempted because it would have required mutating provider or policy
configuration.

A deployed success request used protocol session
`o3-live-success-20260901`. It returned exactly `O3-LIVE-OK` after 4.332 s with
request ID `816251e5-137b-445b-9c39-c6e357808540` and turn ID `main-turn-2`.
The resulting Seahorse conversation contains exactly two messages with roles
`user,assistant`; the database reports `journal_mode=wal` and
`integrity_check=ok`.

The matching redacted passive trace is
`trace-turn-28395fe50592841dde6bad5c`. It uses schema
`mintclaw.diagnostic_trace.v1`, completed with eight records and no truncation,
binds agent `main` to `main-turn-2`, and contains one delivery attempt and one
delivery outcome.

## Observation result

The final observation ran from 06:07:32Z through 06:17:14Z. All 30 samples
reported the five gateway units active, the main gateway at `NRestarts=0`, the
Darwin companion connected, and zero new error-priority entries.

After observation, all expected product and system units were active, no unit
had failed, and no legacy product process existed. All five gateway processes
resolved to the same current core digest. The launcher, reviewer webhook, and
public HTML probes returned their expected 302, 404, and 401 statuses. The
post-cutover journal contained no warning or error entry. Both recovery
manifests still verified, the remote launchd PID remained stable, and the
server retained 150,992,482,304 bytes of free space.
