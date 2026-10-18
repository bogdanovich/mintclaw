# Outbox Retention Decision — October 2026

Status: R4 retention foundation under validation; no production activation.
Operator controls, whole-store capacity reporting and compact-history cost
qualification remain unfinished.
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
| Coordinator root ownership | Process map and OS lock in the actual outbox exclude cooperating writers | An offline batch owns a fresh coordinator; older binaries must be stopped before cutover |

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


## R4A Implementation Evidence

The first prerequisite freezes identity version 3 independently of record
formats and adds cooperating-process exclusion to `OpenCoordinator`, using
`state/outbox/.writer.lock`. The in-process map remains an early duplicate guard.
The lock lives inside the actual store, so an outbox-directory symlink cannot
create a second lock for shared records. OS ownership ends at coordinator close
or process death; leave the lock pathname
in place while any owner runs. Subprocess tests cover competition, close and
process death, and failure cleanup permits retry. Golden keys pin persisted IDs.

The production writer opens through `OpenCoordinator`; low-level `Store` APIs
remain caller-owned primitives and do not acquire that lifecycle lock. Older
binaries do not honor the new lock. This prerequisite does not establish a safe
mixed-version rollout, invent an old-reader fence, enable retirement, or finish
R4. Those activation gates above remain mandatory.


The existing native eval census recognizes the exact empty outbox owner lock as
lifecycle metadata. It does not count that lock as a document operation or delivery
and still rejects nonempty, private-content, aliased and unsafe surfaces. A live
coordinator regression exercises this compatibility boundary. These deterministic
checks do not qualify the unfinished live PDF/browser evaluation cases.


## Source Completion Requirement Found During R4B

A callback-free delivered message is not sufficient retirement proof. Its source
inbound turn may still await a durable acknowledgement; asynchronous tasks may
also own settlement outside the publication callback. Admission must therefore
remain protected until the authoritative source owner seals completion. A later
callback or binding must revoke eligibility durably before that mutation, and a
protected or legacy record cannot acquire eligibility from a replay candidate.

The existing evaluation program also owns post-stop raw evidence until its
privacy scan. Runtime process exit alone does not prove that obligation settled.
The first eligible source/channel classes must exclude that unqualified evidence
ownership until its existing owner can supply a completion proof. Do not infer
completion from age, a nil callback, a path prefix, or transport status alone.

The first implementation qualifies only ordinary successful final replies from
an enabled channel configured with the Telegram adapter type. A private marker
exists only around that final publication; its source, instance, workspace and
route must match. Tools, interim/error publications, asynchronous results,
interaction bindings, media and guarded intake do not receive that marker.
Configured adapter type matters: a native eval adapter named `telegram` is still
protected. The existing native eval profiles use the MintClaw adapter and retain
full raw evidence through post-stop collection; no new harness is introduced.

Admission writes a version-4 full record with `native_final_source_pending`.
It is ineligible even after transport delivery. The exact final publication
receipt reaches the inbound owner, which seals only after a nonzero native
acknowledgement proves removal of the matching source from the coordinator's
actual instance spool. A missing source or in-memory bus cannot provide that
proof. Steering's earlier canonical-history acknowledgements do not seal final
payloads. The source seal can precede transport completion; cleanup still
requires confirmed delivery and completed canonical transcript projection.
A seal failure retains the full record and never releases an already removed
source for replay. Late callbacks revoke eligibility durably before mutation;
legacy, revoked and unqualified records cannot be promoted by a replay or stale
acknowledgement receipt.

## R4B Receipt And Offline Batch Contract

Full classified records and compact delivered receipts use schema 4. Logical
delivery keys keep identity version 3. A canonical record is its own old-reader
fence: an atomic replacement never separates the retained identity from its
format. Revocation keeps schema 4. Unknown future formats remain fatal to
recovery, including the R1 isolated-record recovery API.

A native executable built from byte-verified, unmodified pre-retirement outbox
sources (`0ac5cda622709ad46d51912752ad681b748c2d49`) accepted a legacy schema-3
fixture and rejected both classified-full and compact schema-4 fixtures on
recovery, lookup and duplicate admission. All logical keys remained stable;
none of the duplicate admissions dispatched. This tests old-reader rejection,
not a mixed-version writer rollout or physical power-loss qualification.

`RetirePayloadsOffline` requires an explicit positive age and 1–64 unique
canonical delivery IDs. It defaults to dry-run, requires an existing store,
and owns a fresh coordinator's OS lock throughout the bounded batch. Selected
records larger than the native 2 MiB inspection bound, nonregular records,
protected classes, uncertain outcomes, and pending settlement are preserved.
Compact receipts have a 16 KiB bound. Repeated apply reconfirms the canonical
receipt write; interruption yields the original full record or the same
no-replay compact receipt. A replay never substitutes its candidate body or
runs a new callback against a retired payload. Late waiters retain delivered
truth.

Compact records retain private delivery/source identity, routing, workspace,
platform message IDs and terminal timestamps. They remove the body, media,
transient errors and retry diagnostics; they are not anonymous or a promise
of secure erasure from filesystem snapshots, backups, channels or providers.
Permanent receipt bytes and excluded-owner payloads still grow. No background
cleaner, default horizon or production migration is enabled by this packet.

R4 remains unfinished until operator controls, whole-store capacity reporting
with budget action, and measured compact-history recovery are implemented and
verified. Activation separately requires stopped older writers, a supported
schema-4 rollback floor, backup evidence and a qualified filesystem.
