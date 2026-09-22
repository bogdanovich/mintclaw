# Code Health V2 R3 Compatibility Audit

Status: completed read-only audit on 2026-09-22. No deployed state was
modified or deleted.

This evidence closes R3 of the
[Code Health Maintenance V2 Roadmap](../architecture/archive/code-health-maintenance-v2-roadmap.md).
It covers the two inbound spool compatibility readers and the companion
invocation-ledger version 1 reader. The audit deliberately excludes backups,
retired deployments, and Git history because none of those paths is consumed by
an active runtime.

## Baseline

The production gateway host reported core revision
`f41b47d52142a68f96238942619744583d99562a` and version
`mintclaw v0.1.0-p8a.2-2229-gf41b47d5`. The standard deployed-ops status check
reported every expected service active, no failed units, no error-level journal
entries in the ten-minute window, and no legacy product process.

The main node registry contained three connected protocol-v2 nodes:

| Node | Platform | Reported build |
| --- | --- | --- |
| `p5a-canary` | Linux amd64 | `f41b47d5` |
| `ab-local-test` | Darwin amd64 | `f41b47d52` |
| `vpn` | Linux amd64 | `82d7b398` |

The registry had no disconnected identities. `p4-isolated` is a separately
maintained local coordinator and companion, so it was audited even though it is
not enrolled in the main registry.

## Method

The audit used the deployed-ops status script, bounded SSH commands, `find`, and
`jq`. Commands returned only revisions, service states, file counts, schema
versions, aggregate invocation states, and booleans indicating whether a
retired key existed. They did not emit configuration values, raw message
metadata, invocation input, results, or credentials.

For each configured gateway profile, the equivalent of the following checks
was run against `workspace/state/ingress-spool/inbound`:

```bash
find "$spool" -maxdepth 1 -type f \
  \( -name '*.json' -o -name '*.processing' -o -name '*.failed' \)
jq -e '(.message.context.raw // {}) | has("session_id")' "$record"
jq -e '(.message.context.raw // {}) |
  has("interaction_choice") or has("interaction_response") or
  has("interaction_response_candidate") or has("interaction_short_id") or
  has("interaction_response_error") or has("interaction_option_index") or
  has("interaction_response_message_id")' "$record"
```

Each suffix was counted separately. Every candidate was also parsed with `jq`;
malformed files were counted rather than skipped. The companion audit located
the active state directory from each service's effective configuration, then
projected only `version`, record count, aggregate states, coding-task count,
and whether the loaded schema still required migration.

## Inbound Spool Inventory

All five configured gateways use the expected workspace under
`/home/server/.mintclaw/<profile>/workspace`.

| Profile | Pending | Processing | Failed | Malformed | Pre-M4 session | Pre-F3 interaction |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| `main` | 0 | 0 | 0 | 0 | 0 | 0 |
| `family` | 0 | 0 | 0 | 0 | 0 | 0 |
| `nutrition` | 0 | 0 | 0 | 0 | 0 | 0 |
| `reviewer` | 0 | 0 | 0 | 0 | 0 | 0 |
| `spouse` | 0 | 0 | 0 | 0 | 0 | 0 |

The inbound spool has no age-based retention sweep. Pending and processing
records are replay candidates; failed records are retained but are not replayed
automatically. The audit therefore included all three suffixes. A source scan
also confirmed that maintained MintClaw and Telegram adapters write
`InboundContext.ClientSessionID` and `InboundContext.Interaction` directly and
do not produce the retired raw keys.

Decision: remove the pre-M4 and pre-F3 hydration readers. Current writers and
persisted state are both at the typed boundary. The spool now rejects either
retired representation before a new record is written and rejects a persisted
retired record without interpreting, rewriting, or deleting it.

## Companion Ledger Inventory

| Deployment | Runtime | Ledger | Records | Coding tasks | Decision |
| --- | --- | --- | ---: | ---: | --- |
| `ab-local-test` | active local launchd, `f41b47d52` | v2 | 12 succeeded | 0 | current |
| `p5a-canary` | active server user service, `f41b47d5` | absent | 0 | 0 | current on first write |
| `vpn` | active system service, `82d7b398` | v1 | 5 succeeded | 0 | retain reader |
| `p4-isolated` | active isolated local deployment, `v0.1.0-p4.3` | v1 | 2 succeeded | 0 | retain reader |

A retired `p3-canary` ledger and historical backups were observed but excluded:
no maintained service reads them. Both active v1 ledgers contain terminal
records only and have no coding tasks, but that does not make them disposable.
The ledger is bounded to 256 records and 32 MiB by default; it prunes an expired
terminal or unknown identity only under record or byte capacity pressure. It
has no ordinary age sweep that guarantees those v1 records will disappear.

Decision: retain the version 1 companion reader and its transactional startup
migration unchanged. Removing it now would prevent two maintained companions
from starting or would require discarding durable no-replay identities.

The exact removal gate is:

1. Back up each active v1 ledger, binary, effective service definition, and
   non-secret configuration with checksums.
2. Upgrade `vpn` and `p4-isolated` to a binary containing the v1-to-v2
   transactional migration while preserving their state directories.
3. Allow startup recovery and migration to commit, then verify both services
   are active and connected where applicable.
4. Re-run a read-only inventory proving ledger version 2, no migration pending,
   and no loss or replay of retained invocation identities.
5. Remove the v1 reader only in a later focused PR with matching rollback
   evidence.

## Rollback And Validation

The inbound change does not migrate or delete spool files. If a retired record
appears after this audit, stop replay and restore the preceding merged binary;
that binary can still hydrate the record. Preserve the record unchanged until
its provenance and delivery state are understood. Rolling back the binary is
sufficient because the on-disk spool schema version is unchanged.

The companion path has no code change in R3. Its current migration and recovery
tests remain required until the deployment gate above is complete. R3 code
validation covers the bus cutover plus the unchanged companion and gateway
recovery packages.
