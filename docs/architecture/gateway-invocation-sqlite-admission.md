# Gateway Invocation SQLite Admission

Status: admitted for implementation after this document merges.

## Problem

`state/node_invocations.json` began as a bounded control-plane snapshot. It now
retains every dispatched Node Companion invocation for seven days so the
gateway can preserve ownership, recover status, and refuse replay. Production
filled the 256-record default during ordinary remote-workspace use. Raising the
bound is a necessary operational repair, but serializing and atomically
replacing the complete JSON document on every transition does not scale with
command history.

The gateway invocation ledger is high-churn transactional state. It requires
indexed identity lookups and atomic compare-and-transition operations, so
SQLite is a better fit than JSONL. JSONL would avoid full rewrites but would
still require replaying an increasingly large journal at startup, maintaining
an in-memory index, and implementing compaction and crash reconciliation that
SQLite already provides.

## Objective

Replace only the gateway invocation JSON snapshot with one workspace-local
SQLite database while preserving the existing API and all authority, status,
retention, cancellation, and no-replay semantics.

The change must make retained invocation throughput disk-bound rather than
record-count-bound. It must not introduce a generic persistence layer or
migrate unrelated stores.

## Scope

In scope:

- `GatewayInvocationStore` persistence and its existing callers;
- indexed lookup by invocation identity and provider tool-call ownership;
- transactional prepare, dispatch, cancellation, prune, and restart recovery;
- one-time import of `state/node_invocations.json`;
- fail-closed downgrade behavior;
- bounded database size, retention, observability, and operations guidance.

Out of scope:

- companion invocation, file-transfer, job, terminal, interaction, session,
  media, or node-registry stores;
- changing execution plans, approval, discovery, WSS, or model tool contracts;
- changing the seven-day dispatched retention window;
- replay, automatic retry after dispatch, distributed databases, replication,
  sharding, a persistence framework, or configurable storage backends;
- storing command output, logs, traces, or artifacts in this database.

## Authority And Identity

The existing immutable binding remains canonical:

- invocation ID and idempotency key;
- target and node ID;
- agent, routed session, actor, provider tool-call identity;
- optional workspace and execution identity;
- expected execution-plan hash and pinned descriptor;
- prepared/dispatched state and cancellation request.

Database constraints must enforce uniqueness for invocation ID and idempotency
key. A scoped unique index must prevent two records from claiming the same
agent/session/actor/tool-call/workspace/execution binding. Empty workspace and
execution values remain a valid paired legacy scope, not SQL `NULL` values.

The canonical record remains a strictly validated `GatewayInvocationRecord`.
SQLite columns used for indexing are duplicated projections of that record and
must match it on every read. A mismatch is corruption and fails closed.

## Storage Contract

The authoritative database is `state/node_invocations.db`. It contains:

- a schema metadata table with exactly one supported schema version;
- an invocations table with indexed ownership, state, timestamps, expiry,
  idempotency key, and expected plan hash;
- the canonical validated record encoded as bounded JSON in a BLOB column.

Keeping the canonical record together avoids splitting execution-plan and
descriptor validation across many nullable SQL columns. Indexed projections
make lookup and pruning proportional to the selected rows rather than the
whole history.

SQLite must use WAL journaling, foreign keys, a bounded busy timeout, and
`synchronous=FULL`. Startup verifies `quick_check`, schema version, projection
consistency, record validation, and database size before serving tools.

The implementation retains a hard byte budget, not a normal-operation record
budget. The initial default is 4 GiB per workspace, with SQLite page limits
enforcing the bound. Retention remains the primary lifecycle control. A full
database returns `GATEWAY_CAPACITY_EXHAUSTED`, performs no dispatch, and asks
the operator. It never evicts protected authority to admit a new command.

The 4 GiB default accommodates roughly 500,000 current-size records while
remaining bounded. Actual capacity depends on descriptor and plan size and is
reported in bytes, not promised as a command count.

## Transactions

Every mutation uses one bounded write transaction.

Prepare:

1. prune expired prepared rows and dispatched rows older than retention;
2. check idempotency and scoped tool-call uniqueness;
3. return an exactly matching retained record, reject a conflicting binding,
   or insert one new prepared record;
4. commit before returning created authority.

Dispatch:

1. select the exact row and validate owner and expected plan hash;
2. if already dispatched, return the retained record without dispatching;
3. atomically transition prepared to dispatched with monotonic timestamps;
4. commit the dispatch boundary before WSS transmission.

Cancellation updates only the exact owned row and is idempotent. Lookups run in
read transactions. Concurrent gateway instances must produce one prepare
identity and one dispatch winner under the existing contract.

No transaction spans a network call or human-approval wait.

## Migration And Downgrade Safety

Migration runs under the existing workspace store lock before the gateway
admits node traffic:

1. refuse symlinks, non-regular files, unsafe parent directories, oversized
   input, unsupported JSON versions, and invalid records;
2. create a new database in the same protected directory;
3. import every legacy record in one transaction while enforcing all SQL
   constraints and projection checks;
4. run integrity and count/digest checks against the source snapshot;
5. durably publish the database;
6. atomically replace the old JSON snapshot with a small migration marker.

The marker contains no authority or secrets. It identifies the database and
schema version and deliberately uses a JSON document version rejected by old
binaries. This makes an accidental binary downgrade fail closed instead of
creating an empty JSON ledger and losing no-replay authority.

Migration is idempotent across crashes at every boundary:

- JSON only: retry import;
- unpublished temporary database: validate or remove only that exact temp;
- published database plus legacy JSON: validate the database, compare the
  import proof, then publish the marker;
- database plus marker: open the database and never re-import;
- marker without a valid database: fail closed.

Rollback after migration requires a version-aware exporter that recreates a
validated JSON snapshot before installing an older binary. Operations must not
delete the database or marker as a shortcut.

## Filesystem And Concurrency Safety

The database, WAL, shared-memory file, lock, migration temporary file, and
marker stay beneath the existing protected workspace state directory. Opens
must reject symlink substitution and non-regular files. Migration and startup
must verify file identity before and after publication.

The existing cross-process lock remains the migration/startup ownership lock.
SQLite transactions own runtime concurrency; Go mutexes may protect one store
handle but are not the authority across processes.

## Retention, Privacy, And Operations

Pruning uses indexed timestamps and runs opportunistically during prepare plus
a bounded startup pass. It removes only expired prepared records and dispatched
records older than seven days. `VACUUM` must not run in the request path;
checkpoint and compaction are explicit bounded maintenance operations.

The database retains the same potentially sensitive command inputs currently
held in execution plans. Permissions remain `0600`; directories remain
operator-only. Logs and events expose counts, bytes, duration, schema version,
and safe error codes, never record JSON, arguments, paths, environment values,
hash material, or identities.

Operations documentation must cover database health, size, WAL checkpoint,
retention lag, migration state, backup, restore, capacity exhaustion, and safe
downgrade export.

## Failure Semantics

- Corruption, projection mismatch, unsafe files, unsupported schema, failed
  migration proof, or a missing database behind a marker prevents gateway node
  admission.
- Busy timeout or disk-full before commit is a pre-dispatch failure and cannot
  be reported as target/discovery failure.
- An ambiguous SQLite commit is reconciled by exact identity lookup before any
  retry. It never causes a second dispatch.
- Failure after the durable dispatch transition retains the existing unknown
  outcome and `nodes_status` recovery path.
- Database maintenance never converts unknown into failed or safe-to-retry.

## Validation Matrix

| Area | Required evidence |
| --- | --- |
| Migration | empty, 256-record production-shaped, maximum-sized, malformed, duplicate identity, invalid record, and unsupported-version snapshots |
| Crash boundaries | before DB commit, after DB commit, before publication, after publication, and before marker replacement |
| Downgrade | old store rejects the marker; exporter round-trip preserves every validated record and binding |
| Identity | invocation, idempotency, tool-call, actor, routed session, workspace, and execution isolation |
| Concurrency | multi-handle prepare race, dispatch race, cancellation race, prune race, and busy timeout |
| No replay | dispatched retry, ambiguous commit reconciliation, restart after every lifecycle boundary, and unknown WSS outcome |
| Retention | expired prepared removal, seven-day dispatched retention, protected-row behavior, and bounded startup pruning |
| Capacity | page-budget exhaustion returns truthful pre-dispatch denial with zero dispatch; recovery after prune/checkpoint |
| Security | symlink/non-regular/substituted files, permissions, malformed BLOBs, projection mismatch, and redacted logs |
| Platforms | focused and race tests on Linux and macOS; CI compile/portability checks |
| Production | real JSON import, healthy restart, record-count parity, new invocation, status recovery, and natural-language remote-workspace smoke |

## Implementation PR Sequence

1. SQLite store and migration behind the existing `GatewayInvocationStore`
   API, including schema, transactions, fail-closed marker, and focused tests.
2. Operations proof: exporter/downgrade procedure, metrics/events, production
   migration evidence, latest-main deployment, and remote-workspace smoke.

The second PR may be docs-only only if the first PR already contains every
runtime operation needed for safe deployment. No prerequisite persistence
framework or unrelated store migration is permitted.

## Architecture Checkpoint

Stop and reassess before continuing if any of the following occurs:

- implementation requires changing callers outside gateway invocation
  construction and node tools;
- the canonical execution or approval state machine is duplicated in SQL;
- filesystem safety requires a custom SQLite VFS;
- migration cannot make old-binary downgrade fail closed;
- production code exceeds twice the initial implementation baseline;
- four substantive review/fix cycles occur or the same invariant is challenged
  on three successive heads.

Prefer narrowing or replacing the migration over adding a generic abstraction.

## Definition Of Done And Mandatory Stop

The work is complete only when:

- the gateway no longer rewrites a complete invocation-history JSON snapshot;
- existing JSON authority migrates once with count and digest evidence;
- prepare, dispatch, cancellation, lookup, pruning, and restart semantics are
  transactionally equivalent to the current contract;
- 500,000 production-shaped retained records fit under the configured default
  byte budget in a deterministic capacity test or measured fixture;
- concurrent and crash-boundary tests prove no duplicate dispatch or replay;
- downgrade without export fails closed;
- Linux and macOS validation is green;
- merged-main deployment is healthy and a model-facing remote-workspace smoke
  succeeds without local fallback;
- documentation matches the deployed behavior.

Immediately stop after this gateway store migration is evidenced. Do not
migrate companion or other JSON stores, change retention, begin P8b, or add a
generic storage system as part of this work.
