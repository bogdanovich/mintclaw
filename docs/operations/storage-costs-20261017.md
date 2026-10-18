# Storage Cost Measurements — October 2026

Status: evidence for R4/R10 of the
[reliability/code-health roadmap](../architecture/reliability-code-health-20261017-roadmap.md).
The measured trigger admits a focused task-event pruning optimization. A storage
backend replacement is not admitted by these results.

## Workload And Method

Baseline storage sources: `902179e0196afe67e15912de167480b71e33ed49`.
The later base through `d59b99a9f986889141373c8f0ed3cf73ac84c9b4` has identical
`pkg/tasks` and `pkg/outbox` storage sources. The benchmarks are synthetic and
contain no production content.

Host: shared macOS/AMD64, Go 1.26.9, Intel Core i9-9980HK at 2.40 GHz,
16 logical CPUs, 64 GiB RAM. The temporary filesystem was not independently
qualified. Runs were sequential; background host load was not isolated.
These figures do not establish Linux or deployed service capacity.

Task fixtures use 512-byte objectives and protected running records. Smoke
cohorts cover 100, 1,000, and 2,000 records, zero or five requested historical
events per record, and one or four concurrent callers. The reader validates
fixtures and applies the normal retention policy before timing. In the primary
1,000-record history cohort, 5,000 seeded events become 1,813 retained events
near the 2 MiB byte budget. Protected tasks remain intact. This illustrates that
record/byte retention budgets preserve active work even when a count is exceeded.

Primary comparisons use 50 durable updates per repetition, three repetitions,
with a native `Get` following every update. Timings include lock waiting,
cloning, pruning, serialization, the real atomic write, and sync; setup and final
statistics are excluded. Reported p95 ranges span the three separate runs, not a
population confidence interval. Allocation figures are per operation.

Diagnostic budgets were written before execution: task update p95 100 ms,
read p95 50 ms; 10,000-record outbox warm scan 1 second and first scan 5 seconds.
These are investigation thresholds, not new product/CI latency promises.

## Task Results

| 1,000-record cohort | Update p95 before / after, ms | Read p95 before / after, ms | Allocation before / after, MiB/op |
| --- | --- | --- | --- |
| No initial events, one caller | 67–77 / 66–81 | under 0.02 / under 0.02 | 11.0–11.6 / 10.9–11.2 |
| No initial events, four callers | 199–223 / 195–261 | 68–121 / 98–126 | 11.2–11.4 / 10.7–11.4 |
| Near byte budget, one caller | 260–621 / 116–131 | under 0.03 / under 0.03 | 131–134 / 38–40 |
| Near byte budget, four callers | 961–1,026 / 338–347 | 557–629 / 117–207 | 130–133 / 39–40 |

The unchanged no-history control shows host variation. The near-budget cohort
shows about a threefold four-caller p95 improvement and roughly 70% fewer
allocated bytes. Both implementations retain the same event counts and encoded
snapshot sizes after the measured updates.

The baseline CPU profile attributed 54.9% of sampled CPU to snapshot sizing; its
allocation profile attributed 86.4% there. Profiles include fixture/setup work.
The original event-tail binary search repeatedly encoded every task and every
retained event under the registry lock. The change sizes the snapshot once for
that search, then subtracts the exact encoded contribution of removed head
events. It retains the longest suffix that fits, including correct omission of
the events field when the last event is removed.

Exact boundary tests compare every possible suffix's native JSON encoding,
including one byte above/below each boundary, escaping, Unicode and invalid
UTF-8. Protected task state, persisted format, rollback and atomic-write owners
remain unchanged. Both full packages and both race suites pass, as does lint.

The diagnostic budgets remain unmet for concurrent callers and the near-budget
single-caller update. Keep those misses visible. Further work needs Linux and
representative workload qualification and another measured proposal; do not
weaken durability, evict protected tasks, or replace the store to hide them.

## Outbox Results

All fixtures are valid terminal text intents with no pending settlement or
transcript projection. They produce no dispatch work. Each repetition creates
its own files, measures its first `Store.Recover` scan, then five warm scans.
There are three repetitions. The file creation warms filesystem caches: the
first scan is not an OS-cold-cache or total gateway-startup measurement.

| Retained records / content each | Encoded retained bytes | Warm scan mean range, ms | First scan range, ms | Allocated per warm scan |
| --- | --- | --- | --- | --- |
| 100 / 1 KiB | 184 KiB | 6–9 | 7–9 | 0.66 MiB |
| 1,000 / 1 KiB | 1.80 MiB | 51–57 | 57–63 | 6.47 MiB |
| 10,000 / 1 KiB | 18.0 MiB | 565–633 | 644–663 | 64.8 MiB |
| 1,000 / 16 KiB | 16.4 MiB | 150–153 | 159–190 | 36.7 MiB |

The file store meets the recorded scan thresholds at the tested scale. Retained
history still grows without bound, and a scan reads payloads that yield no
recovery work. R4 should separate permanent duplicate-prevention identities
from eligible expired payloads and active recovery enumeration, while preserving
all unresolved obligations. These measurements support that lifecycle design;
they do not justify a database migration at this scale.

## Reproduction

From the repository root:

```sh
go test -count=3 -timeout 6m -tags goolm,stdjson -run '^$' \
  -bench '^BenchmarkRegistryDurableUpdate$/^records=1000$/^history=(0|5)$/^workers=(1|4)$' \
  -benchtime=50x ./pkg/tasks

go test -count=3 -timeout 3m -tags goolm,stdjson -run '^$' \
  -bench '^BenchmarkRecoverTerminalHistory$' -benchtime=5x ./pkg/outbox
```

Use the same fixture code against the baseline production pruner to compare the
before result. Profile the near-budget one-caller case with 20 iterations using
`-cpuprofile`, `-memprofile`, and an output binary outside the worktree; inspect
with `go tool pprof`. Keep platform, event counts, actual serialized bytes,
concurrency, sample counts, filesystem/cache limits, and every budget miss in
any subsequent report. Performance benchmarks remain manual rather than a
machine-dependent CI threshold.
