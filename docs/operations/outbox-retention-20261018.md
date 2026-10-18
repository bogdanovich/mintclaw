# Outbox Capacity And Payload Retirement

Status: operator implementation merged in #1708, following #1703, for R4 of the
[code-health roadmap](../architecture/reliability-code-health-20261017-roadmap.md).
No production inventory, deployment or cleanup has been performed. The
[retention decision](../architecture/outbox-retention-decision-20261017.md)
defines the permanent-identity and source/domain/evidence contracts.

## Select The Instance And Policy

Use the instance workspace returned by the gateway configuration's
`WorkspacePath()`. Its canonical outbox is `state/outbox` beneath that root;
individual agent workspaces are record ownership metadata, not separate stores.
The commands require an explicit `--instance-root` and positive `--min-age`.
They neither load a runtime profile nor bootstrap the normal MintClaw home.

Choose the age and capacity budgets from operational requirements. No production
window or byte/count quota is chosen by this change. Ages use Go durations, such
as hours; `168h` is a syntax example, not an activated policy. Age is measured
from the last durable record update, including source or transcript settlement.
Zero byte/count budgets mean unconfigured; negative budgets are invalid.

Before activation, stop older writers, establish a schema-4 reader/writer and
rollback floor, retain a recoverable backup, and qualify the target filesystem.
A qualified admission can write schema 4 before any cleanup. Older binaries
reject that format, and some older writers do not honor the process lock. The
OS lock excludes cooperating writers; it does not certify a mixed-version
rollout. No command below stops services or deploys a binary.

## Inspect The Whole Store

The following variables are operator-selected values, not defaults:

```sh
mintclaw outbox inspect --instance-root "$INSTANCE_ROOT" \
  --min-age "$MINIMUM_AGE" --max-bytes "$BYTE_BUDGET" \
  --max-records "$IDENTITY_BUDGET" --json > capacity-before.json
```

Inspection requires exclusive offline ownership. It streams the entire outbox
namespace in 256-entry reads, decodes at most the native 2 MiB observation bound
per record, and returns at most 64 candidate delivery IDs. It creates no missing
store and changes no payload, status or delivery identity. It may create the
empty native owner-lock file in an existing store.

The report separates active payloads, other protected payloads, eligible
payloads, permanent receipts and unavailable entries. These groups partition
observed data entries. Each group has count, logical bytes and its oldest known
update timestamp. `outcomes` counts valid delivery statuses independently of
retirement blockers. Reasons, candidate IDs and time/size metadata exclude raw
bodies, routes, workspace paths and unknown filenames.

`total_records` counts canonical identity pathnames, including damaged or
nonregular authorities. `total_entries` additionally counts unexpected entries.
Logical byte counts include those entries' observed sizes but exclude the exact
empty owner-lock file. They do not measure allocated blocks, directory overhead,
filesystem snapshots, backups or provider retention. An unreadable entry has
unknown size, so a report with unavailable entries is not a complete byte bound.

An exceeded configured budget or unavailable entry sets
`operator_action_required`. The CLI returns a nonzero exit status while still
emitting its complete JSON report when a scan succeeds. An incomplete scan also
requires action. Investigate unavailable authorities; never delete a damaged
identity to make a report healthy or admit a replacement send.

## Review And Apply An Explicit Batch

Select exact `out_` delivery IDs from the candidate list, not inbound source
IDs. Candidate order follows the filesystem; `candidate_truncated` means more
eligible records exist. Repeat `--delivery` for at most 64 unique IDs. Retention
rechecks every selected record rather than trusting an earlier inventory.

```sh
mintclaw outbox retire --instance-root "$INSTANCE_ROOT" \
  --min-age "$MINIMUM_AGE" --delivery "$DELIVERY_ID" \
  --max-bytes "$BYTE_BUDGET" --max-records "$IDENTITY_BUDGET" \
  --json > retirement-dry-run.json
```

Dry-run is the default. Review the selected outcomes, predicted byte reduction,
and the whole-store `capacity_before` report. Unknown/corrupt/unreadable formats,
nonregular authorities and records beyond the observation bound fail the
operator preflight before any selected record changes. Valid active or protected
records can coexist with a batch and remain unchanged.

After review, run the same selection with `--apply`:

```sh
mintclaw outbox retire --instance-root "$INSTANCE_ROOT" \
  --min-age "$MINIMUM_AGE" --delivery "$DELIVERY_ID" \
  --max-bytes "$BYTE_BUDGET" --max-records "$IDENTITY_BUDGET" \
  --apply --json > retirement-applied.json
```

Preflight, the bounded batch and `capacity_after` share one fresh coordinator
and OS lock. A runtime cannot acquire that ownership between the scan and
mutation. Apply is atomic per identity, not an all-or-nothing transaction across
64 records. Preserve partial reports and `write_unconfirmed` outcomes. After an
interruption, inspect again and safely repeat the exact IDs; each canonical file
remains a full record or its permanent delivered receipt. Cleanup never changes
an uncertain outcome into a retry or creates a new transport effect.

Only ordinary successful Telegram-adapter native final replies can currently
become eligible, and only after exact durable source acknowledgement, confirmed
transport delivery and required transcript settlement. Configured adapter type
is authoritative, including aliases. Legacy, unqualified, asynchronous, media,
interaction and guarded-intake payloads remain protected. Native eval adapters
retain their post-stop raw evidence. A delivered status, source absence, process
exit or an old replay candidate does not create eligibility.

## Act On Capacity Without Discarding Obligations

The configured budgets signal operator action; they are not an eviction quota.
Eligible payload retirement can reduce bytes, while permanent identity counts
stay unchanged. A successful batch can therefore still return a nonzero exit
status because `capacity_after` exceeds a budget. Check the item outcomes before
interpreting that exit as an absence of mutation.

If protected payloads or receipt metadata exceed the budget, preserve them and
choose an operational response such as more storage or a separately measured
storage design. Do not remove active work, ambiguous records or delivery keys.
The file backend remains in place; a database replacement is not admitted by
these commands. Lifetime identity growth and recovery cost remain visible.

Compact receipts retain private source/delivery identity, routing, workspace,
platform IDs and terminal timestamps. Payload retirement is not anonymity,
secure erasure of filesystem history, or deletion from canonical conversations,
other durable owners, backups or external channels.

## Paired Recovery Measurements

Measured on 2026-10-18 UTC using Go 1.26.9, darwin/amd64, macOS 26.6.2
(25G83), a MacBookPro16,1 with Intel i9-9980HK at 2.40 GHz, 16 logical CPUs
and 64 GiB RAM. Other work on the shared host was not controlled. The session's
other tests, builds and lint processes had finished before measurement.

Each row contains three independent benchmark repetitions with five timed
recovery scans per repetition. Ranges below are the minimum and maximum of the
three per-repetition averages. Fixture creation warms the filesystem cache;
the separately observed first scan is also warm and is not OS-cold startup.
Histories are synthetic valid schema-4 terminal records written directly by
the benchmark, with identical ownership and transport metadata in each pair.
They measure recovery cost, not live source qualification, storage durability,
production traffic, Linux behavior or provider delivery.

| History | Format | Retained logical bytes | Warm recovery, ms | First scan, ms | Allocated MiB/scan |
| --- | --- | ---: | ---: | ---: | ---: |
| 100 × 1 KiB | full | 181,200 | 4.82–5.74 | 5.50–6.18 | 0.67–0.67 |
| 100 × 1 KiB | compact | 60,400 | 3.85–4.29 | 4.48–4.65 | 0.32–0.32 |
| 1,000 × 1 KiB | full | 1,812,000 | 49.91–52.05 | 50.19–62.54 | 6.57–6.58 |
| 1,000 × 1 KiB | compact | 604,000 | 38.09–39.38 | 38.98–40.06 | 3.09–3.09 |
| 10,000 × 1 KiB | full | 18,120,000 | 530.31–531.19 | 523.75–542.02 | 65.82–65.82 |
| 10,000 × 1 KiB | compact | 6,040,000 | 431.64–444.66 | 428.47–468.39 | 31.03–31.03 |
| 1,000 × 16 KiB | full | 17,172,000 | 125.65–127.71 | 126.82–129.92 | 36.85–36.85 |
| 1,000 × 16 KiB | compact | 604,000 | 38.79–39.69 | 38.57–44.70 | 3.09–3.09 |

Compact receipts reduced retained logical bytes by 66.7% for 1 KiB payloads
and 96.5% for 16 KiB payloads. Comparing the three-repetition means, warm recovery
improved by 18–24% in the 1 KiB cohorts and 69% in the 16 KiB cohort. These are
synthetic comparisons,
not operational latency guarantees. At 10,000 identities, compact recovery still
averaged 432–445 ms and allocated about 31 MiB per scan. Recovery still walks
and decodes every identity: payload retirement reduces per-record costs but
does not bound identity count, lifetime disk growth or startup duration.

Reproduce from the repository root using the native Go toolchain:

```sh
go test -tags=goolm,stdjson ./pkg/outbox -run='^$' \
  -bench='^BenchmarkRecoverRetainedPayloads$' -benchtime=5x -count=3 \
  -timeout=8m
```

The benchmark checks that terminal histories produce no recoverable sends or
record issues. Native source-proof, duplicate-delivery and mutation safety are
qualified separately by the outbox tests; these direct fixtures establish no
additional owner or live-evaluation claims.
