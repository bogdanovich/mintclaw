# Coding State: Upgrade, Backup And Recovery

Use this procedure when upgrading a coding deployment or changing its home,
config or state format. A binary-only update is not permission to migrate or
delete user data. MintClaw does not promise compatibility aliases for retired
folder names or formats; inspect the exact target release before changing
paths. P7.6 workspace rewind is deferred and is not a rollback mechanism.

## Identify the owners

Record the old and target source commit, binary checksums, active home/config
and authentication-store override for each affected profile. Do not print
secret values. Inventory these distinct owners:

- `coding/threads/<id>`: metadata, canonical JSONL, per-thread Seahorse SQLite
  and sidecars, presentation/plan, runtime registries, artifacts and references;
- recoverable trash, fork/GC quarantine, shared attachment blobs and catalogue
  coordination state beneath the coding home;
- gateway task, interaction, delivery and invocation stores;
- companion worker/invocation ledgers and remote coding threads on each target;
- configured diagnostic directories, including absolute shared stores;
- config/auth, service units and binaries; and
- project/worktree checkouts and published effects, which are not disposable
  thread state.

The [state ownership inventory](../architecture/local-coding-agent-p8-state-lifecycle.md)
defines deletion and retention boundaries. Support bundles are useful evidence,
but cannot restore a transcript or task registry.

## Quiesce and back up

1. Stop admitting new affected tasks. Let known tasks settle or cancel them
   through their owning runtime, then verify their worker/process outcome.
   A disconnected or uncertain worker is not proof that it stopped.
2. Close affected coding clients and stop only affected gateway/companion
   services. Verify no writer remains on each state owner; stopping a gateway
   alone does not stop every remote worker.
3. Create a new private timestamped backup outside active paths. Preserve
   owners, modes, symlinks, hardlinks and, where supported, ACLs/xattrs. Copy
   canonical JSONL together with its metadata and all state of that owner.
   Include SQLite databases and `-wal`/`-shm` sidecars as a stopped set. Never
   copy a live database file alone; an owner-supported online backup is the
   alternative only when that owner explicitly provides one.
4. Record an inventory of paths, sizes, modes and symlink targets plus a
   checksum manifest for regular files. Recompute its hashes before changing
   anything. Keep credentials inside the private backup, not the evidence
   report. Verify the previous binaries can execute `version` from a separate
   location; do not start them against live mutable state to test rollback.

Do not run `git clean`, reset a private workspace repo, or delete the last
known-good backup. Live profile configs can intentionally differ from Git.

## Migrate only what the target requires

Perform a documented transformation on a stopped-state copy first. Validate
the exact schema/version with the target's owner reader or tests. Do not
rename an unknown format to make it look supported or manually synthesize a
missing task generation. For a home move, update every explicit path, unit,
environment override, broker binding and diagnostic override; no fallback
alias is assumed. Check directory ownership and private permissions before
activation.

Canonical history remains authoritative. Rebuild a derived Seahorse index
through the supported resume/reconciliation path, not by replacing canonical
JSONL with a summary. Keep attachments and references together. Restore a
registry/database with its matching generation and sidecars rather than mixing
files from different snapshots. Any uncertain external effect requires
inspection by its project/machine/service owner, not replay during migration.

Run the exact target binary with the intended profile:

```sh
MINTCLAW_HOME=/path/to/profile \
MINTCLAW_CONFIG=/path/to/profile/config.json \
/path/to/target/mintclaw doctor --config /path/to/profile/config.json
```

Exit 1 is a command/config-loading blocker. Exit 2 reports policy findings;
classify and resolve the relevant ones before rollout. `doctor` is read-only
configuration and owner health inspection, not a universal migration validator.

## Canary and rollout

Start one profile first. Verify version/commit, config load, expected services,
readiness and bounded error journals. In a disposable project, run a harmless
headless turn, record its thread ID, close it, then resume without mutation.
Verify canonical history, presentation settlement and a new trace when capture
is enabled. Select exact IDs with `diagnostics correlate`, then export a
metadata-only support directory and recompute every manifest-listed digest.

When remote owners changed, separately verify the admitted target, policy
revision, task/worker generation, cancellation settlement and final delivery.
Do not infer companion health from gateway readiness. Roll through the
remaining affected profiles only after the first canary passes. Retention and
deletion canaries must use synthetic state, never real user threads.

## Rollback and incident closure

If qualification fails, preserve failed artifacts and passive traces. Stop
the affected writers and restore only the previous binaries and the matching
config/unit snapshot. Mutable data written by the new version is not restored
automatically. Compare formats and select a forward repair or explicit matched
restore; restoring old state can lose new turns or duplicate external effects.
Published commits, deploys, installs and project files require their own
rollback decision.

Re-run health and a harmless canary after rollback. Record revision, backup
manifest digest, affected owners, validation, missing evidence and residual
limits. Follow the [diagnostic closure workflow](diagnostic-support-bundles.md#incident-to-regression-loop):
earliest proven divergence, focused ordinary regression, narrow fix, then a
fresh trace. Never execute historical commands merely because a trace includes
them.
