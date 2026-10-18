# Outbox Retention Decision — October 2026

Status: design decision for R4; payload retirement is not implemented or activated.
The [reliability roadmap](reliability-code-health-20261017-roadmap.md) remains
active. No production records were inventoried, migrated, or deleted.

## Decision

Keep the existing file store. Preserve logical delivery identity indefinitely;
retire payload separately from the receipt that blocks another send. Make
cleanup opt-in with an explicit payload age supplied at activation. Do not
silently choose a production retention window from a benchmark.

The measured workload supports this choice: 10,000 terminal records recovered
in roughly 0.6 seconds on the assessment host. The separate R10 measurement PR
[#1690](https://github.com/bogdanovich/mintclaw/pull/1690) records its warm-cache,
platform, workload and durability limits. This is evidence against a database
replacement at that scale, not a bound on lifetime history or production startup.

Do not implement a TTL that deletes delivery identities. A candidate replay is
not a substitute for the original canonical message. Compression alone does not
bound payload retention. A new database, service or generic storage abstraction
is not admitted by the current evidence.

## Existing Consumers And Obligations

| Boundary | Current contract | Retirement requirement |
| --- | --- | --- |
| `outbox.DeliveryID` | Hash includes record version 3, source ID, ordinal and kind | Freeze identity version 3 independently of future storage formats; golden vectors must preserve old keys |
| `Store.Create` / `Get` | Duplicate admission returns the original canonical intent, even when the candidate changes route or body | Return an explicit retired terminal receipt; never recreate a body from the replay candidate |
| `Coordinator.admit` / `AwaitTerminal` | Dispatch leases and immutable admission receipts own publication and terminal truth | A retired delivered identity stays delivered and never receives a dispatch lease; preserve late waiters |
| `agent.admitDurableMessage` / `admitDurableMedia` | Dereference the original message/media; bind the delivery before admission | Handle retirement before dereferencing or creating a new binding; retain original receipt truth |
| `publishTransaction*ReceiptAtBoundary` | A duplicate non-dispatch admission can still invoke a commit callback | Retired replays must not invoke a candidate callback or invent a new domain obligation |
| `outbound_recovery.go` | Replays unsettled document settlement and canonical transcript projection separately from transport | Keep full payload until exact durable acknowledgements are complete |
| `bus.OutboundMessage` / `OutboundMediaMessage` | Carry trace settlement, result output, interaction metadata, routing and media recovery claims | Inventory each owner before admitting that payload class to retirement |
| Coordinator root ownership | In-process map excludes a second coordinator in this process | This is not an operating-system lock; cleanup/cutover must establish exclusion of every writer |

`RecoverySettlementPending` currently covers protected PDF form delivery. Its
absence does not prove that browser media claims, interaction callbacks, trace
settlement or result-output consumers have finished. `StatusDelivered` alone is
therefore not an eligibility test.

## First Implementation Boundary

1. Separate identity version from record/layout versions and pin old IDs.
2. Introduce an explicit compact terminal-receipt contract at the store,
   coordinator, admission and publication boundaries. Test changed replay
   candidates and callbacks before enabling payload deletion.
3. Start with confirmed delivered plain text whose owners can prove no remaining
   obligations. Exclude ambiguous, attempting, pending, retryable, damaged,
   unsettled and actively owned records. Exclude media and domain-bearing
   messages until their individual settlement/cleanup contracts are proven.
4. Add a bounded cleanup batch with an explicit age and dry-run evidence.
   Admission and cleanup must share the actual ownership/exclusion boundary.
   Keep protected records even when they exceed an operator storage budget.
5. Measure compact-history recovery at the same retained counts and payload
   workloads as R10. Separate active payload bytes, retained receipt bytes,
   protected bytes, count, age and skipped/error reasons.

These are ordered implementation gates, not permission to report R4 complete
from this decision document. A class excluded from retirement remains a visible
limitation rather than silently becoming eligible.

## Crash, Reader And Rollback Rules

Persist a strict old-reader fence before activating any layout that an older
binary could misread as missing identities. The supported rollback floor must
understand retired receipts. Keep logical IDs stable across that fence.

Make the no-replay receipt durable before removing the body. Prefer one atomic
replacement when a canonical record can represent both states; if directories
change, confirm the relevant directory syncs. Interrupted coexistence must be
validated against the same identity and terminal proof. Missing, conflicting,
unknown-version or damaged authorities fail closed and retain evidence. R1's
isolated damaged records remain admission blockers.

A stopped-writer migration requires evidence that all writers stopped; the
current coordinator map is insufficient cross-process proof. A live cleaner
requires a real shared exclusion contract. Do not add an offline command that
merely assumes another process is absent.

Test interruption before/after receipt persistence, rename, directory sync and
body removal; restart, duplicate admission, old-reader rejection and supported
rollback; failed writes; corrupt/conflicting receipts; pending domain work;
late waiters and active leases. Keep all existing atomic-write durability tests.

## Capacity And Privacy

Permanent receipts grow with distinct delivery identities. Payload retention
bounds do not imply constant disk usage. Report this residual growth explicitly;
never age out deduplication identities to satisfy a disk cap. A configured byte
or count budget must apply backpressure or request operator action while
preserving active obligations, not discard them.

Retire message/media bodies, URLs and unnecessary diagnostics once eligible.
Document retained identity, routing, workspace and terminal metadata; compact
receipts are not anonymous. Private permissions and bounded redacted diagnostic
samples still apply. A retained local deduplication receipt is not a statement
about provider or channel retention.

Activation remains a separate operation with an explicit horizon, dry-run
counts/bytes and excluded-owner evidence, recoverable backup, reader/rollback
floor and qualified filesystem. No production policy is implied by this merge.
