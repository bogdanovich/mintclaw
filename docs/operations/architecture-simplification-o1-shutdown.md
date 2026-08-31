# Architecture Simplification O1 Shutdown Deployment

Date: 2026-08-30

Outcome: passed. PR #1007 gives node admission, terminal sessions, invocation
stores, and the shared transfer spool one bounded gateway lifecycle. The
merged release was built, installed, restarted, and exercised under the same
loaded condition that had previously forced systemd to send `SIGKILL`.

## Release and recovery

The deployed source is merge commit `7e52c1dd`. The affected core, node, and
launcher artifacts were built with `make build`, `make build-node`, and
`make build-launcher`. The installed core reports
`v0.1.0-p8a.2-1167-g7e52c1dd` built with Go 1.26.6.

Before installation, a private compact recovery set was created at:

```text
/home/server/mintclaw-o1-recovery-20260831T060515Z
```

It is 255 MiB and contains 33 files: affected configurations, run scripts,
units and drop-ins, pre-deploy service state, restore order, and one copy of
each of six distinct effective binary digests. Its `SHA256SUMS` passed before
and after the operation. It excludes complete MintClaw homes, sessions, media,
browser profiles, logs, traces, repositories, worktrees, and caches.

The capacity gate recorded 139 GiB free before the build. After verification,
the clean merged worktree, superseded recovery sets, and the regenerable Go
build cache were removed. Free space increased to 149 GiB. The retained
recovery roles are the 13 GiB monthly R1 baseline, the 474 MiB targeted Z1
archive, the 209 MiB immediately previous P0 compact set, and the new O1 set.

## Rollout boundary

The first restart stopped the previously installed `b7bf8b25` process. That
old process reproduced the known 30-second timeout and was killed before the
new binary started, so it is transition evidence rather than an O1 test.
Every measured stop below therefore began with `7e52c1dd` already running.

The local P5a node was upgraded with the release. `ab-local-test` remained on
the immediately previous release and reconnected through protocol 1. The VPN
companion retained its older, still additive protocol-1 surface and supplied
the currently advertised `root` terminal profile and `root` working scope.
This rollout added no historical schema reader, version switch, or fallback
execution path.

## Loaded shutdown evidence

Each cycle opened a real VPN PTY, started a ten-minute `sleep` child that wrote
its PID to a unique marker, and restarted `mintclaw-main.service`. The old CLI
attachment then reached terminal disconnected state. A new PTY verified the
recorded PID was gone and removed the marker.

| Cycle | Restart duration | Remote child | Result |
| ---: | ---: | ---: | --- |
| 1 | 4.510 s | 3435393 | gone |
| 2 | 2.272 s | 3435502 | gone |
| 3 | 2.148 s | 3435607 | gone |
| 4 | 1.993 s | 3435714 | gone |
| 5 | 2.476 s | 3435801 | gone |

The matching journal window contains five systemd stop requests, five clean
stops, and five starts. It contains zero stop timeouts, `SIGKILL` records,
failed results, or automatic restarts. The five old gateway PIDs were absent,
and the active unit cgroup contained only the current gateway and its current
owned hook and MCP children.

## Post-deploy verification

The explicit current-contract terminal smoke used `main / vpn / root / root`.
It proved UID 0, 31 rows, 100 columns, marker `MINTCLAW_PTY_OK`, and final
`state=closed` with `close_reason=close`.

A stateless live-agent smoke returned exactly `O1_OK`. Its completed redacted
diagnostic trace is `trace-turn-e72b424572e33744b24faa5e`, schema
`mintclaw.diagnostic_trace.v1`, with eight records and no truncation. The final
deployment status reported all expected units active, no failed unit, no
legacy process, expected HTTP 302/404/401 probes, and zero error-level entries
for every product unit in the ten-minute window.
