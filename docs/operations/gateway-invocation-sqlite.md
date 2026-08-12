# Gateway Invocation SQLite Operations

The gateway keeps Node Companion invocation authority in
`<workspace>/state/node_invocations.db`. The adjacent
`node_invocations.json` is a migration marker after the first successful
SQLite start; it is not a second copy of the ledger.

The database contains plans and command inputs. Treat the database, WAL,
backups, and downgrade exports as sensitive workspace state. Do not print,
attach, or copy them outside an operator-only location.

## Retention and bounds

Retention cleanup runs at gateway startup and before every new invocation is
prepared. It removes only:

- prepared invocations whose execution plan has expired; and
- dispatched invocations whose last durable update is older than seven days.

It never evicts a newer dispatched or otherwise protected authority merely to
make room. Incremental vacuum and a passive WAL checkpoint run at startup;
SQLite also checkpoints automatically after 1,000 WAL pages and bounds the
retained WAL journal at 64 MiB. The database has a 4 GiB page budget. That
budget is an emergency ceiling, not the expected steady-state size.

## Inspect health and size

The maintenance command validates the exact schema, `quick_check`, every
canonical record and indexed projection, the migration marker, file types,
permissions, and retained database identity. Its output is redacted:

```sh
mintclaw nodes invocation-store inspect --json
```

Use `--config /path/to/config.json` or `--workspace /path/to/workspace` when
inspecting a non-default profile. Relevant fields are:

- `records`, `prepared`, and `dispatched`: bounded counts only;
- `database_bytes`, `wal_bytes`, and `shm_bytes`: filesystem allocation;
- `page_bytes` and `free_page_bytes`: SQLite pages and reclaimable pages;
- `maximum_bytes`: the enforced database page ceiling;
- `oldest_updated_at` and `retention_seconds`: evidence for retention review;
- `migration_complete`: marker and supported database are both present.

No plan, path, argument, environment value, identity, or hash is emitted. A
failed inspection is a node-admission incident: keep the gateway stopped while
investigating rather than deleting files or recreating an empty store.

## Backup and restore

For an exact rollback point, stop the gateway first so no authority can be
committed after the copy. Back up the binary and the complete state set:

```text
node_invocations.db
node_invocations.db-wal       # when present
node_invocations.db-shm       # when present
node_invocations.json
node_invocations.json.lock    # when present
```

Keep original modes and ownership. Start the same or a newer compatible
binary, run `invocation-store inspect`, and then perform an exact status lookup
for a retained invocation before considering the restore healthy. Never
restore only the database or only the marker, and never combine files from
different backup times.

## Capacity exhaustion

`GATEWAY_CAPACITY_EXHAUSTED` is a pre-dispatch refusal. The requested command
was not sent and is not safe to reinterpret as target unavailability. Inspect
the report and host disk first. Normal recovery is expiration followed by the
next bounded prune/startup maintenance pass. Do not delete dispatched rows,
the marker, WAL files, or the database. If legitimate seven-day authority can
reach 4 GiB, change the explicit byte budget under review rather than adding
unbounded storage or weakening retention/no-replay semantics.

## Safe downgrade to a JSON-store binary

Old binaries deliberately reject the SQLite migration marker. A downgrade
therefore needs a validated legacy snapshot. It is safe only while the gateway
is stopped:

1. Stop the gateway and retain an exact backup of the binary and state set.
2. Export to a new staging file in the same protected `state` directory:

   ```sh
   mintclaw nodes invocation-store export \
     --gateway-stopped \
     --output <workspace>/state/node_invocations.rollback.json
   ```

3. Confirm the command reports the expected record count. The exporter fails
   if the snapshot exceeds the old store's 8,192-record or 32 MiB limits.
4. Atomically replace `node_invocations.json` with the staging export, keeping
   mode `0600`, and only then install the older binary.
5. Start the old gateway and recover a known retained invocation by exact
   status. If that fails, stop it and restore the complete same-time backup.

The exporter uses a read-only SQLite handle, checks the marker/schema/integrity
and every record projection, writes a bounded canonical version-1 JSON
document atomically, and validates the published result. The explicit
`--gateway-stopped` acknowledgement prevents presenting a potentially stale
snapshot as downgrade-safe. Do not bypass it and do not delete the database as
a downgrade shortcut.
