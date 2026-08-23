# Gateway Invocation SQLite Contract

Status: implemented. The original cutover evidence remains in the
[archived production proof](archive/gateway-invocation-sqlite-proof.md).

## Purpose

The gateway invocation ledger is high-churn transactional state. It preserves
command ownership, status recovery, cancellation, and no-replay authority for
Node Companion calls. Indexed identity lookups and atomic state transitions
make SQLite the canonical fit; a document store would require full rewrites or
a journal, in-memory index, compaction, and crash reconciliation.

This contract covers only `GatewayInvocationStore` and its existing callers.
It does not introduce a generic persistence layer or configurable storage
backends, and it does not change execution plans, approval, discovery, WSS, or
model tool contracts.

## Authority and identity

The immutable binding contains:

- invocation ID and idempotency key;
- target and node ID;
- agent, routed session, actor, and provider tool-call identity;
- optional paired workspace and execution identity;
- expected execution-plan hash and pinned descriptor; and
- prepared/dispatched state plus any cancellation request.

Database constraints enforce uniqueness for invocation ID and idempotency key.
A scoped unique index prevents two records from claiming the same
agent/session/actor/tool-call/workspace/execution binding. Empty workspace and
execution values form the current unscoped binding and are stored as paired
empty strings rather than SQL `NULL` values.

The canonical row payload is a strictly validated `GatewayInvocationRecord`.
SQLite columns used for indexing are duplicated projections of that record and
must match it on every read. A mismatch is corruption and fails closed.

## Storage contract

The only authoritative store is `state/node_invocations.db`. It contains:

- a metadata table with exactly one supported schema version;
- an invocations table with indexed ownership, state, timestamps, expiry,
  idempotency key, and expected plan hash; and
- the canonical validated record encoded as bounded JSON in a BLOB column.

Keeping the canonical record together avoids splitting execution-plan and
descriptor validation across many nullable SQL columns. Indexed projections
make lookup and pruning proportional to selected rows rather than the complete
history.

SQLite uses WAL journaling, foreign keys, a bounded busy timeout, and
`synchronous=FULL`. Startup verifies `quick_check`, the exact schema
fingerprint and version, every record and projection, database identity, and
the byte limit before serving tools. A newly created or transactionally empty
database may initialize the current schema; any non-empty different schema is
rejected rather than upgraded in place.

The store has a 4 GiB page budget. Retention remains the primary lifecycle
control. A full database returns `GATEWAY_CAPACITY_EXHAUSTED`, performs no
dispatch, and never evicts protected authority to admit a new command.

## Transactions

Every mutation uses one bounded write transaction.

Prepare:

1. prune expired prepared rows and dispatched rows older than retention;
2. check idempotency and scoped tool-call uniqueness;
3. return an exactly matching retained record, reject a conflicting binding,
   or insert one new prepared record; and
4. commit before returning created authority.

Dispatch:

1. select the exact row and validate owner and expected plan hash;
2. return the retained record without dispatching if it is already dispatched;
3. atomically transition prepared to dispatched with monotonic timestamps; and
4. commit the dispatch boundary before WSS transmission.

Cancellation updates only the exact owned row and is idempotent. Lookups run in
read transactions. Concurrent gateway handles must produce one prepare
identity and one dispatch winner. No transaction spans a network call or human
approval wait.

## Filesystem and concurrency safety

The database, WAL, shared-memory file, and initialization lock stay beneath the
protected workspace state directory. Opens reject symlink substitution,
non-regular files, broad permissions, and path replacement. Startup retains
the validated database file identity and checks it before mutations. The
initialization lock serializes constructors but contains no authority.

SQLite transactions own runtime concurrency. Go mutexes protect one store
handle but are not cross-process authority.

## Retention, privacy, and operations

Pruning runs during prepare and in a bounded startup pass. It removes only
expired prepared records and dispatched records older than seven days.
`VACUUM` does not run in the request path; checkpoint and compaction are bounded
maintenance operations.

The database contains sensitive command inputs held in execution plans. Files
remain mode `0600` beneath operator-only directories. Logs, events, and the
inspection command expose counts, bytes, duration, schema version, and safe
error codes—never record JSON, arguments, paths, environment values, hashes,
or identities.

Backups and rollback restore a matching binary and same-time SQLite state as a
unit. The runtime does not reconstruct retired storage formats. See
[Gateway Invocation SQLite Operations](../operations/gateway-invocation-sqlite.md).

## Failure semantics

- Corruption, projection mismatch, unsafe files, or a non-current schema
  prevents gateway node admission.
- Busy timeout or disk-full before commit is a pre-dispatch failure and cannot
  be reported as target or discovery failure.
- An ambiguous SQLite commit is reconciled by exact identity lookup before any
  retry; it never causes a second dispatch.
- Failure after the durable dispatch transition retains the existing unknown
  outcome and `nodes_status` recovery path.
- Database maintenance never converts unknown into failed or safe-to-retry.

## Validation matrix

| Area | Required evidence |
| --- | --- |
| Format | new database initialization, interrupted initialization recovery, exact schema fingerprint, and rejection of any different non-empty schema |
| Identity | invocation, idempotency, tool-call, actor, routed session, workspace, and execution isolation |
| Concurrency | multi-handle prepare race, dispatch race, cancellation race, prune race, and busy timeout |
| No replay | dispatched retry, ambiguous commit reconciliation, restart after every lifecycle boundary, and unknown WSS outcome |
| Retention | expired prepared removal, seven-day dispatched retention, protected-row behavior, and bounded startup pruning |
| Capacity | page-budget exhaustion returns truthful pre-dispatch denial with zero dispatch; recovery after prune/checkpoint |
| Security | symlink/non-regular/substituted files, permissions, malformed BLOBs, projection mismatch, and redacted inspection |
| Platforms | focused and race tests on Linux and macOS plus CI compile and portability checks |
