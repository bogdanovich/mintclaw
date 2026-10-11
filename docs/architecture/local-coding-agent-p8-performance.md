# Local Coding Agent P8.1 Performance Qualification

Status: implementation packet for
[P8.1 performance and startup budgets](local-coding-agent-p8-hardening.md#h2--p81--performance-and-startup-budgets).
This record qualifies the existing architecture; it does not introduce a
second runtime, context store, or presentation model.

## Environment and method

Measurements were taken on 2026-10-11 from `origin/main` plus this packet on
macOS 26.6.2 build 25G83, darwin/amd64, an Intel Core i9-9980HK, and Go
1.26.9. The release-shaped CLI binary used `-tags goolm,stdjson`.

Go benchmarks report three consecutive runs with synthetic, content-free
fixtures. Runtime construction reopens one already provisioned empty coding
thread. The first-turn fixture provisions an isolated empty thread outside the
timed region, uses an in-process streaming provider, and reports both total
turn time and MintClaw time before the provider request and first chunk. It
therefore excludes provider and network latency without weakening the normal
prompt, workspace snapshot, Seahorse, event, and durable transcript path.

Full-process measurements use `/usr/bin/time -lp`. “First launch” means the
first process after a fresh build, without privileged OS cache eviction;
“warm” means a new process with filesystem and executable caches already
populated. These are release observations, not single-run CI assertions.

## Budgets

The budgets are intentionally above the representative measurements so shared
CI load and supported machines do not create timing flakes. Allocation and
structural guards are deterministic; process latency and RSS are repeated
release gates.

| Path | Latency budget | Memory/allocation budget | Enforcement |
| --- | ---: | ---: | --- |
| First CLI launch after build | 5 s | 64 MiB RSS | release measurement |
| Warm `mintclaw code --help` | 250 ms | 64 MiB RSS | release measurement |
| First empty-state `mintclaw resume --json` | 2 s | 64 MiB RSS | release measurement |
| Warm empty catalogue command | 500 ms | 64 MiB RSS | release measurement |
| Warm native runtime construction | 100 ms | 8 MiB/op | benchmark comparison |
| First streaming turn through first token | 500 ms | 32 MiB/op | loopback-provider benchmark |
| 2,000-thread first catalogue page | 250 ms | 32 MiB/op | benchmark plus bounded scan counters |
| One 40-message leaf compaction | 25 ms | 2 MiB/op | benchmark plus compaction result counters |
| Clean Seahorse revision check | 1 ms | 64 KiB/op | benchmark plus revision equality |
| Forced 100-message reconciliation | 100 ms | 4 MiB/op | benchmark plus exact revision/state checks |

TUI first paint, output update, resize, hydration, viewport, overlay, and
search continue to use the budgets in the
[TUI.15 performance checkpoint](local-coding-agent-tui-15-performance.md).
This packet does not create competing TUI targets.

## Representative results

### Full process

| Command/path | Result | Maximum RSS |
| --- | ---: | ---: |
| First post-build `mintclaw code --help` | 4.08 s | 41.4 MiB |
| Warm `mintclaw code --help`, 10 runs | 0.08–0.09 s | 41.3–41.9 MiB |
| First empty-state `mintclaw resume --json` | 1.43 s | 41.8 MiB |
| Warm empty-state `mintclaw resume --json`, 9 runs | 0.16–0.17 s | 41.8–42.7 MiB |

The first-launch results include macOS loader and executable-validation work.
The warm commands are separate processes, not repeated Cobra execution inside
one process.

### Runtime, context, and catalogue

| Benchmark | Time/op | Bytes/op | Allocations/op |
| --- | ---: | ---: | ---: |
| `BenchmarkNativeCodingRuntimeOpenWarm` | 35.3–43.0 ms | 3.38 MiB | 19,972–19,977 |
| `BenchmarkNativeCodingFirstStreamingTurn` | 281–296 ms | 19.7 MiB | 132,222–132,240 |
| first model request, reported by the turn benchmark | 147.6–160.6 ms | included above | included above |
| first streamed token, reported by the turn benchmark | 147.6–160.6 ms | included above | included above |
| `BenchmarkCatalogFirstPageTwoThousand` | 132–136 ms | 12.6 MiB | 88,898–88,900 |
| `BenchmarkCompact_LeafFortyMessages` | 1.33–1.38 ms | 109.6 KiB | 2,859–2,861 |
| `BenchmarkSeahorseCleanRevisionCheck` | 89–93 us | 6.45 KiB | 152 |
| `BenchmarkSeahorseForcedReconciliation100Messages` | 15.3–16.8 ms | 436.5 KiB | 5,190–5,193 |

The streaming-turn benchmark intentionally includes local durable writes and
workspace/context preparation. The request and first-token metrics stop before
any real provider delay, while the ordinary benchmark time includes final
assistant persistence and turn settlement.

### TUI after the hydration fix

| Benchmark | Time/op | Bytes/op | Allocations/op |
| --- | ---: | ---: | ---: |
| `BenchmarkPresentationFirstPaint` | 3.36–3.40 ms | 3.07 MiB | 34,171 |
| `BenchmarkPresentationOutputUpdate` | 291–293 us | 216 KiB | 728 |
| `BenchmarkPresentationResizeReflow` | 2.68–2.74 ms | 2.14 MiB | 30,831–30,843 |
| `BenchmarkTranscriptHydration` | 2.40–2.47 ms | 842 KiB | 4,019 |
| `BenchmarkLongTranscriptViewport` | 120–121 us | 34.1 KiB | 76 |
| `BenchmarkTranscriptOverlayOpen` | 6.71–7.57 ms | 2.76 MiB | 17,759–17,770 |
| `BenchmarkTranscriptSearch` | 2.92–3.22 ms | 3.25 MiB | 12,893 |

Before this packet, the same hydration fixture took 8.13–8.49 ms and consumed
17.9 MiB with 69,810 allocations per page, outside the existing 8 MiB TUI.15
budget. Tab expansion and overwide-grapheme normalization appended one
grapheme at a time and repeatedly copied adjacent same-style span strings.
The implementation now handles tab-free runs as complete spans and builds a
replacement only when an overwide grapheme actually exists. The result is
below 1 MiB per hydration page without changing tab stops, Unicode grapheme
handling, style roles, or narrow-terminal replacement behavior.

## Deterministic guardrails and diagnostics

- `TestCellSpanNormalizationAllocationsRemainBounded` uses a deliberately long
  same-style span. The former per-grapheme concatenation exceeds the allocation
  ceiling by orders of magnitude; the run-based implementation stays within
  eight allocations.
- Unicode, combining-mark, emoji, multiple-overwide-grapheme, tab-column, and
  style-role regressions are checked directly.
- Catalogue results already expose scanned, matched, skipped, pagination,
  transcript-thread, and transcript-byte counts; catalogue storage tests keep
  the admitted scan bounded.
- `PresentationDiagnostics` exposes hydrated-entry, cell, rendered-line,
  rendered/reused-block, coalescing, truncation, and duration counters without
  transcript content.
- Seahorse compaction results expose summaries and token counts, while
  reconciliation tests assert the exact canonical revision rather than
  treating elapsed time as correctness.
- H1 diagnostic queries and support manifests expose trace-record/file/byte
  limits, truncation, drops, corruption, and applied capture policy.
- Runtime and Seahorse benchmarks suppress ordinary console logs so benchmark
  time does not scale with terminal output.

No MCP server or repository discovery step was deferred. The measurements did
not justify changing tool-snapshot lineage, cache ownership, or correctness in
exchange for a smaller startup number.

## Reproduction

```text
go test -run '^$' \
  -bench '^BenchmarkNativeCoding(RuntimeOpenWarm|FirstStreamingTurn)$' \
  -benchmem -count=3 ./cmd/mintclaw/internal/coding

go test -run '^$' \
  -bench 'Benchmark(PresentationFirstPaint|PresentationOutputUpdate|PresentationResizeReflow|TranscriptHydration|LongTranscriptViewport|TranscriptOverlayOpen|TranscriptSearch)$' \
  -benchmem -count=3 ./pkg/coding/tui

go test -run '^$' -bench '^BenchmarkCatalogFirstPageTwoThousand$' \
  -benchmem -count=3 ./pkg/coding/thread

go test -run '^$' -bench '^BenchmarkCompact_LeafFortyMessages$' \
  -benchmem -count=3 ./pkg/seahorse

go test -run '^$' \
  -bench '^BenchmarkSeahorse(CleanRevisionCheck|ForcedReconciliation100Messages)$' \
  -benchmem -count=3 ./pkg/agent
```

Release-process measurements build `./cmd/mintclaw` with
`-tags goolm,stdjson`, then run the resulting binary through
`/usr/bin/time -lp`. The final P8 exit record owns a fresh exact-head rerun and
the cross-platform release matrix; this host record is not a claim that all
supported platforms have identical latency or RSS.
