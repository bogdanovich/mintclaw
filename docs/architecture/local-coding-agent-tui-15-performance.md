# Local Coding Agent TUI.15 Performance Checkpoint

Status: second TUI.15 implementation packet. The complete initiative is closed
by the later [TUI.15 exit record](local-coding-agent-tui-15-exit.md).

This checkpoint implements the performance, bounded-session, and privacy-safe
diagnostics portion of
[TUI.15](local-coding-agent-codex-tui-roadmap.md#tui15--migration-performance-and-parity-closeout).
It complements the earlier
[authoritative-state migration](local-coding-agent-tui-15-migration.md) and is
not the final TUI.15 exit record.

## Budget provenance and representative workload

The original roadmap said that TUI.0 had established numeric performance
budgets. It had not: TUI.0 admitted deterministic fixtures and the renderer
seam, but no latency or allocation targets. TUI.15 therefore records the first
explicit TUI budgets here instead of attributing them retroactively to TUI.0.

The benchmarks use bounded, content-free synthetic fixtures:

- first paint starts with 128 completed two-message turns, filling the default
  256-message projection;
- output update retains 128 committed messages plus one changing active cell;
- resize/reflow cycles through widths from 40 to 120 columns with the full
  256-message projection;
- hydration admits one 64-entry page with 512-byte messages;
- the long-transcript viewport, overlay, and search retain 256 live messages
  plus the maximum 256 hydrated entries; and
- search matches both historical and live rows and preserves Unicode-aware
  folded matching.

The engineering budgets are deliberately loose enough for shared CI and debug
build variance. They are comparison gates, not wall-clock assertions inside
unit tests; deterministic structural tests enforce the actual correctness and
memory bounds.

| Operation | Latency budget | Allocation budget per operation |
| --- | ---: | ---: |
| First paint at the live-message bound | 100 ms | 8 MiB |
| One accumulated-output update and visible frame | 10 ms | 1 MiB |
| Resize/reflow across the retained live transcript | 100 ms | 8 MiB |
| One 64-entry historical hydration page | 100 ms | 8 MiB |
| Cached visible frame with 512 retained entries | 10 ms | 1 MiB |
| Open the complete 512-entry transcript overlay | 100 ms | 16 MiB |
| Search the complete 512-entry transcript | 50 ms | 16 MiB |

## Baseline

The following median-like representative values are from three consecutive
runs on 2026-09-09 using Go 1.26.6 on macOS 26.6.2, darwin/amd64, Intel Core
i9-9980HK. Scheduler and hardware differences are expected; each result is
well inside its declared budget.

| Benchmark | Time/op | Bytes/op | Allocations/op |
| --- | ---: | ---: | ---: |
| `BenchmarkPresentationFirstPaint` | 2.42 ms | 1,237,547 | 9,914 |
| `BenchmarkPresentationOutputUpdate` | 320 us | 218,715 | 714 |
| `BenchmarkPresentationResizeReflow` | 2.10 ms | 646,512 | 8,109 |
| `BenchmarkTranscriptHydration` | 2.62 ms | 1,038,570 | 4,463 |
| `BenchmarkLongTranscriptViewport` | 140 us | 35,948 | 101 |
| `BenchmarkTranscriptOverlayOpen` | 6.87 ms | 2,884,606 | 17,516 |
| `BenchmarkTranscriptSearch` | 2.79 ms | 3,374,923 | 12,765 |

The transcript-search implementation first folds each complete logical line
and maps a match back to source bytes only for matching lines. This preserves
Unicode folding and reduced the benchmark from roughly 16 ms and 41 MiB per
search to roughly 2.8 ms and 3.4 MiB on the same fixture.

## Bounded renderer state

The renderer now enforces these structural limits:

- authoritative live presentation remains bounded independently to 256
  messages, 128 tools, and 64 plan/compaction/turn observations;
- lazy history retains at most 256 hydrated entries;
- every semantic or static cell retains at most four render documents across
  width, theme, color capability, and compact/full/plain mode changes;
- the complete overlay builds one derived line cache when opened, reuses it
  across unchanged frames and key input, and releases both line and match
  caches when closed; and
- diagnostics keep counters and high-water marks only; they retain no
  per-message history or unbounded identity map.

`TestFourHourPresentationSessionRemainsStructurallyBounded` advances 240
logical one-minute checkpoints. Every minute contains a ten-revision streaming
burst and a completed command; every 15 minutes adds a plan; every hour adds
oversized output, opens and searches the full transcript, then closes it; and
terminal width cycles through 40, 80, and 120 columns. The scenario begins
with 300 historical entries and verifies the 256-entry hydration cap throughout.

The test proves category bounds, older-history signaling, revision-gap
coalescing, truncation visibility, maximum cache cardinality, derived-overlay
release, and a 710-cell upper bound that includes live, hydrated, and static
surfaces. A representative run completed in 1.24 seconds; it advances logical
time and does not sleep for four hours.

## Privacy-safe diagnostics

`tui.PresentationDiagnostics` records only durations and integer counters or
high-water marks:

- first paint, snapshot-handler-to-ready presentation latency, render work,
  overlay build, transcript search, and hydration duration;
- accumulated-output revisions skipped by a coalescing subscriber;
- rendered versus reused blocks;
- truncation-bearing surfaces and hydration failures; and
- current/peak hydrated entries, cells, and rendered lines.

The default coding CLI emits one debug-level `coding.tui` summary after Bubble
Tea returns and the terminal is restored. The record never contains prompts,
paths, tool names or arguments, command output, source, diffs, transcript text,
search terms, account data, or attachment metadata. The diagnostics callback
is also directly testable without enabling logs.

## Validation owned by this packet

The implementation is covered by these focused commands:

```text
go test -count=1 ./pkg/coding/frontend ./pkg/coding/tui \
  ./cmd/mintclaw/internal/coding
go test -race -count=1 ./pkg/coding/frontend ./pkg/coding/tui
go test -run '^$' \
  -bench 'Benchmark(PresentationFirstPaint|PresentationOutputUpdate|PresentationResizeReflow|TranscriptHydration|LongTranscriptViewport|TranscriptOverlayOpen|TranscriptSearch)$' \
  -benchmem -count=3 ./pkg/coding/tui
```

The final [TUI.15 exit record](local-coding-agent-tui-15-exit.md) owns the
SSH/tmux/narrow-terminal, interruption, compaction, crash/restart, and
provider-fallback PTY matrix, current user-facing rich/raw and binding
documentation, and the complete requirement-by-requirement audit.
