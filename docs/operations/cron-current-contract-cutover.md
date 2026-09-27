# Cron current-contract cutover

The cron runtime atomically migrates version-1 `cron/jobs.json` stores to the
current version-2 contract when they are loaded. Version 2 removes the
independent `deleteAfterRun` flag and requires explicit schedule, payload, and
delivery fields.

Migration preserves the jobs and their run state, removes the legacy deletion
flag, and applies the defaults used by the version-1 runtime:

- a payload with a command becomes `command`;
- a payload without an explicit kind becomes `agent_turn`;
- missing delivery coordinates become `cli` and `direct`.

The migrated store is validated before publication and installed with the
normal durable atomic-write path. Unknown fields, duplicate IDs, invalid
schedules, and payload/command mismatches still fail closed. If persistence
fails, the service keeps the store unavailable instead of running an
uncommitted migrated snapshot.

For an upgrade, inventory and back up active stores, install the new binary,
then verify that each store reports version 2 and the expected job count. Keep
the version-1 backup for the rollback window because an older binary must not
be started against the migrated version-2 file.
