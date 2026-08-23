# Gateway Invocation SQLite Operations

The gateway keeps Node Companion invocation authority in the single current
store at `<workspace>/state/node_invocations.db`.

The database contains plans and command inputs. Treat the database, WAL, and
backups as sensitive workspace state. Do not print, attach, or copy them
outside an operator-only location.

## Retention and bounds

Retention cleanup runs at gateway startup and before every new invocation is
prepared. It removes only:

- prepared invocations whose execution plan has expired; and
- dispatched invocations whose last durable update is older than seven days.

It never evicts newer protected authority merely to make room. Incremental
vacuum and a passive WAL checkpoint run at startup; SQLite also checkpoints
automatically after 1,000 WAL pages and bounds the retained WAL journal at
64 MiB. The database has a 4 GiB page budget. That budget is an emergency
ceiling, not the expected steady-state size.

## Inspect health and size

The maintenance command validates the exact current schema, `quick_check`,
every canonical record and indexed projection, file types, permissions, and
the retained database identity. Its output is redacted:

```sh
mintclaw nodes invocation-store inspect --json
```

Use `--config /path/to/config.json` or `--workspace /path/to/workspace` when
inspecting a non-default profile. Relevant fields are:

- `records`, `prepared`, and `dispatched`: bounded counts only;
- `database_bytes`, `wal_bytes`, and `shm_bytes`: filesystem allocation;
- `page_bytes` and `free_page_bytes`: SQLite pages and reclaimable pages;
- `maximum_bytes`: the enforced database page ceiling; and
- `oldest_updated_at` and `retention_seconds`: retention evidence.

No plan, path, argument, environment value, identity, or hash is emitted. A
failed inspection is a node-admission incident: keep the gateway stopped while
investigating rather than deleting files or creating an empty store.

## Backup, restore, and rollback

For an exact rollback point, stop the gateway first so no authority can be
committed after the copy. Back up the matching binary and complete SQLite state
set:

```text
node_invocations.db
node_invocations.db-wal       # when present
node_invocations.db-shm       # when present
```

Keep original modes and ownership. Restore the matching binary and same-time
state as one unit, run `invocation-store inspect`, and perform an exact status
lookup for a retained invocation before considering the restore healthy. Do
not combine files from different backup times. The adjacent `.init.lock` file
contains no authority and does not need to be backed up.

MintClaw does not export this authority to retired storage formats. A release
whose schema is not current must be deployed through a coordinated conversion
or state reset, with an explicit backup and verification plan, rather than a
steady-state compatibility path.

## Capacity exhaustion

`GATEWAY_CAPACITY_EXHAUSTED` is a pre-dispatch refusal. The requested command
was not sent and is not safe to reinterpret as target unavailability. Inspect
the report and host disk first. Normal recovery is expiration followed by the
next bounded prune/startup maintenance pass. Do not delete dispatched rows,
WAL files, or the database. If legitimate seven-day authority can reach 4 GiB,
change the explicit byte budget under review rather than adding unbounded
storage or weakening retention/no-replay semantics.
