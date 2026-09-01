# Architecture Simplification O2 Seahorse Deployment

Date: 2026-09-01

Outcome: passed. PR #1015 gives each per-agent Seahorse database one
`database/sql` connection owner. Internal writes serialize through that owner;
an out-of-contract external writer fails promptly instead of adding a second
retry owner. The deployment completed startup reconciliation and two concurrent
live turns without `SQLITE_BUSY` or another database-lock failure.

## Release and recovery

The deployed source is merge commit `20cf7a18`. Core, node, and launcher
artifacts were built with `make build`, `make build-node`, and
`make build-launcher`. Only the affected core runtime was installed. It reports
`v0.1.0-p8a.2-1179-g20cf7a18`, built with Go 1.26.6, and has SHA-256 digest
`944584cb688d21ab9a71926746a42cf26070ec45a63e590c00edf535e2ec6247`.

Before the build, the capacity gate recorded 153,393,123,328 bytes free. The
estimated peak additional writes for backup, three builds, and staging were
less than 1 GiB, leaving far more than the required additional 5 GiB headroom.

A private compact recovery set was created at:

```text
/home/server/mintclaw-recovery-o2-pre-20260901T044304Z
```

The 82 MiB, 43-file set stores one copy of the exact pre-deploy core digest,
mappings for the effective build path and installed fallback, the five affected
profiles' configuration, security, environment, and run-script files, user
units and drop-ins, pre-deploy service state, and restore order. `SHA256SUMS`
passed before deployment and after observation. The set excludes sessions,
traces, logs, repositories, worktrees, caches, media, browser profiles, runtime
state, and nested backups.

## Rollout boundary

The installed fallback and repository build path were updated to the same core
digest. Only these gateway units were restarted:

- `mintclaw-main.service`
- `mintclaw-family.service`
- `mintclaw-nutrition.service`
- `mintclaw-reviewer.service`
- `mintclaw-spouse.service`

All five resolved `/proc/<pid>/exe` to the rebuilt core path after restart.
Launcher, node, reviewer queue/webhook, failover, metrics, FRP, and public HTML
services were not restarted. This packet changes neither persisted schema nor
wire protocol and adds no version switch, historical reader, or compatibility
fallback.

## Concurrency and persistence evidence

Two `agent live` requests entered the running main gateway concurrently with
isolated protocol sessions `o2-sqlite-a-20260901` and
`o2-sqlite-b-20260901`. They returned exactly `O2-A` and `O2-B` after 4.121 s
and 4.717 s. The replies carried distinct request, session, and turn identities
(`main-turn-3` and `main-turn-4`).

The deployed Seahorse database contains one conversation for each resulting
session key. Each conversation has exactly two messages with roles
`user,assistant`. The database reports `journal_mode=wal` and
`integrity_check=ok`.

The merged connection-contract regressions additionally prove
`busy_timeout=0`, `synchronous=NORMAL`, and WAL on a replacement connection.
The external-writer regression holds a write lock through a second database
handle and proves that the owned engine rejects that contention promptly rather
than waiting through an independent busy timeout. The URL-character, relative,
and double-separator path reopen regressions all passed on the merged source.

## Trace and observation evidence

The two live turns produced completed redacted diagnostic traces
`trace-turn-2a7336713451fff2cb5f244f` and
`trace-turn-657cd3a7ec7dfba29fec5e6f`. Each has schema
`mintclaw.diagnostic_trace.v1`, eight records, no truncation, its own session
hash and root turn, and one correlated `delivery.outcome` for the same turn.

The optional P5a terminal smoke was not applicable: the connected
`p5a-canary` node's approved catalogue does not expose a terminal command, so
the gateway correctly returned `TERMINAL_DENIED`. No capability or approval
policy was widened for deployment verification.

After the observation window, all expected product and system units remained
active, no unit had failed, no legacy process existed, and the launcher,
reviewer webhook, and public HTML probes returned their expected 302, 404, and
401 statuses. The full restart window contains expected warning-level child
termination records from the intentional service restart. It contains no
`SQLITE_BUSY`, database-lock, stop timeout, `SIGKILL`, panic, fatal, or
error-priority entry. No warning entry appeared after startup completed.
