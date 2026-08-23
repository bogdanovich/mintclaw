# Cron current-contract cutover

The cron runtime reads one store schema at a time. Store version 2 removes the
independent `deleteAfterRun` flag and requires explicit schedule, payload, and
delivery fields. It does not contain an in-process reader or migration path for
version 1.

Use this procedure during a coordinated upgrade of every first-party MintClaw
service on the server:

1. Inventory every active workspace `cron/jobs.json` file and record its owner,
   mode, version, job count, and SHA-256 digest.
2. Confirm every version-1 job already has exactly one valid schedule shape,
   an explicit payload kind (`agent_turn`, `deliver_text`, or `command`), a
   non-empty message, and a non-empty channel and recipient. Stop if any job
   needs a product decision.
3. Stop the corresponding services and wait for claimed cron executions to
   drain. Do not rewrite a live store.
4. Create a mode-preserving backup beside each store.
5. For each validated store, set `version` to `2` and remove
   `deleteAfterRun` from every job. Do not infer missing values or retain the
   removed field.
6. Validate the converted JSON independently: reject unknown fields, duplicate
   ids, invalid cron expressions or timezones, mixed schedule shapes, and
   payload/command mismatches.
7. Install the new binaries, start the services, and verify that each service
   loads its expected job count without cron-store errors.
8. Exercise one representative `agent_turn` job and one `deliver_text` job,
   then compare the persisted version, ownership, mode, and job inventory with
   the pre-cutover record.

Rollback requires stopping the services, restoring both the previous binaries
and the untouched version-1 backups, and then restarting. Do not run an older
binary against a version-2 store or a newer binary against a version-1 store.

After the fleet has run successfully and the rollback window closes, delete the
version-1 backups according to the normal retention policy. They are deployment
artifacts, not a supported runtime format.
