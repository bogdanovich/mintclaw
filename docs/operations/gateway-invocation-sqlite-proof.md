# Gateway Invocation SQLite Production Proof

Status: complete on 2026-08-12 for the `main` profile.

This record closes the implementation sequence admitted by
[Gateway Invocation SQLite Admission](../architecture/gateway-invocation-sqlite-admission.md).
It covers only the gateway invocation store. No unrelated JSON store was
migrated.

## Merged revisions

| Scope | PR | Merge commit | Evidence |
| --- | --- | --- | --- |
| Immediate bounded JSON capacity repair | #707 | `477260a8` | 8,192-record and 32 MiB limits plus truthful pre-dispatch capacity refusal |
| SQLite admission | #708 | `a4284a94` | frozen store, migration, retention, capacity, security, and operations contract |
| SQLite store and one-time migration | #711 | `fc5a1b23` | schema/integrity/projection checks, transactions, retention, WAL maintenance, and fail-closed marker |
| Inspection, downgrade export, and runbook | #714 | `d7211013` | redacted inspection, bounded no-clobber export, protected-artifact checks, and operator procedure |

All nine required checks passed on the implementation heads, including tests,
race, lint, integration, Darwin ARM64 portability, and Windows AMD64 compile.
The final #714 reviewer found no high-confidence issues after its two
filesystem-publication findings were fixed.

## Backup and preflight

The deployed source was clean `bogdanovich/mintclaw` main at `e05a3748`. The
legacy snapshot was a version-1 JSON document with 75 records and size 285,213
bytes. All 75 records were inside the retained lifecycle window at cutover.

The checksum-verified rollback snapshot is:

```text
/home/server/.mintclaw/backups/core-20260812T093844Z
```

It contains the exact previous runtime/CLI/node/launcher binaries, source SHA,
main unit and drop-in inventory, service states, and both initial and
gateway-stopped legacy invocation snapshots. Restore files only as a same-time
set. The original `workspace/state` mode was `0775`; migration correctly
failed closed because invocation plans and command inputs require an
operator-only directory. The deployment corrected only that directory to
`0700` and retained `0600` for every invocation artifact.

The first gateway stop reached its shutdown timeout after a child MCP process
failed to exit, so systemd killed the already-stopping process after 30
seconds. The ledger remained an unchanged version-1/75-record snapshot. This
shutdown behavior predates and is independent of SQLite; it did not cross the
migration boundary or lose authority.

## Migration and storage evidence

Merged main `d7211013` built successfully for core, node, and launcher on the
deployment host. Only `mintclaw-main.service` was restarted; reviewer and the
other profiles were not restarted.

The stable startup produced:

| Check | Result |
| --- | --- |
| Migration marker | version 2, backend `sqlite`, database `node_invocations.db` |
| Record parity | 75 legacy records → 75 SQLite records |
| States | 0 prepared, 75 dispatched |
| Schema/integrity/projections | inspection passed |
| Filesystem | state directory `0700`; database, WAL, SHM, marker `0600` |
| Initial database/WAL | 466,944 / 498,552 bytes |
| Byte ceiling | 4,294,967,296 bytes |
| Retention | 604,800 seconds (seven days) |
| Runtime identity | running process SHA-256 matched the `d7211013` build |

Retention runs at startup and before prepare, removing only expired prepared
rows and dispatched rows older than seven days. WAL autocheckpoint,
incremental vacuum, and the hard database ceiling remain active. A full store
fails before dispatch instead of evicting protected authority.

## Downgrade export proof

SQLite's online backup command created a consistent, private offline database
copy while production remained available. The merged exporter validated that
copy and its marker, then atomically published a mode-`0600` legacy version-1
snapshot containing exactly 75 records and 285,213 bytes.

The exporter did not modify the production database. Default publication is
atomic no-clobber; explicit replacement still rejects the database, marker,
WAL, SHM, case variants, and existing filesystem aliases. A real downgrade
must still stop the gateway, export the latest store, atomically replace the
marker, and install the older binary in that order, as described in the
[operations runbook](gateway-invocation-sqlite.md).

## Real model-facing and recovery proof

An authenticated live request used ordinary model-facing file tools against
`vpn-workspace`:

1. `read_file` returned `p8a-patch-ok` from `p8a-patch-live.txt` with remote
   placement on `vpn-workspace-target`.
2. `search_files` returned one match on the same remote target.
3. The ledger grew from 75 to 77 dispatched records, one per tool call.

Passive trace
`trace-turn-3810fabf8b1b330031ec5c42` has schema
`mintclaw.diagnostic_trace.v1`, completed status, 29 records, redacted content,
and no truncation. Records 8/11 prove the remote read; records 20/21 prove the
remote search. There was no local fallback.

A second turn in the same routed session requested only `nodes_status` for
the two retained invocation IDs. Trace
`trace-turn-c3020bc9cb67a04f41a6b414` completed without truncation; records
9/10 and 11/12 are the two status calls/results. Both returned `succeeded`, and
the SQLite ledger remained at 77 records. This proves durable result recovery
without replay or a second dispatch.

## Final health and rollback

After the canaries:

- all expected MintClaw units were active;
- no MintClaw unit was failed and no legacy process was running;
- launcher HTTP returned the expected redirect and reviewer webhook GET the
  expected 404;
- the main error journal after stable startup was empty; and
- the running core SHA was `d7211013`.

Rollback to a SQLite-aware build restores the exact previous binary while
leaving current mutable SQLite authority in place. Rollback to a pre-SQLite
binary requires the stopped-gateway exporter procedure; never restore only the
old marker, delete the database, or reuse the pre-migration JSON snapshot after
new invocations have committed.

This proof completes the admitted SQLite store work. Further persistence
migrations, generic storage abstractions, retention-policy changes, and remote
workspace expansion remain separate decisions.
