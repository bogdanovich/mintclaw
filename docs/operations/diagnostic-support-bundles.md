# Diagnostic Queries And Support Bundles

`mintclaw diagnostics` is the read-only operator surface for passive
`mintclaw.diagnostic_trace.v1` evidence. It locates bounded candidates,
validates every selected trace, builds content-free timelines, reports capture
health, and writes checksum-verifiable support directories. It never invokes,
resumes, retries, repairs, or replays agent work.

## Quick workflow

Start with the affected profile, approximate time, visible symptom, and any
known trace, thread, turn, task, model-attempt, tool-call, invocation,
interaction, completion, delivery, or session-hash identity.

```sh
mintclaw diagnostics list --since 2h

mintclaw diagnostics show trace-turn-example

mintclaw diagnostics correlate --task coding-example

mintclaw diagnostics correlate \
  --thread 123e4567-e89b-12d3-a456-426614174000 \
  --turn main-turn-2
```

Selectors are exact and combine with logical AND. Repeated `--trace` values
form the only OR set. `correlate` refuses a time-only or unfiltered scan: at
least one exact identity is required. `--since` is relative to command start;
`--after` and `--before` accept RFC3339 timestamps. Use `--json` for the stable
schemas returned by automation.

An exact `--trace` or `show <trace-id>` request reads only the selected trace
files; it does not decode unrelated traces. Other selectors use a bounded
newest-first scan with explicit file, byte, and directory-entry ceilings.
Store health reports `scan_mode`, `examined_entries`, and truncation reasons so
investigators can tell which evidence boundary produced the result.

The default profile stores come from the active read-only config. A
`--thread` selection resolves the thread-owned diagnostics directory beneath
the active MintClaw home, including a configured relative or absolute trace
state override. A shared absolute trace directory is additionally constrained
to the selected thread's privacy-filtered session hash, so unrelated thread
traces cannot enter the result. `--trace-root` selects an explicit store and
is mutually exclusive with `--thread`; it is mainly for offline evidence and
tests.

When several live-agent profiles intentionally share one absolute trace root,
the command scans it once and labels it `profile_shared` instead of assigning
the evidence to whichever profile happened to be configured first. Per-record
safe agent IDs remain available for exact correlation.

## Reading the result

`list` returns trace envelopes and store health, not record bodies. `show`
returns one allowlisted metadata timeline. `correlate` orders the matching safe
records across traces by absolute time and adds exact-ID, metadata-only views
from the authoritative task, interaction, delivery, gateway-invocation, and
coding-thread stores. Every result reports:

- capture mode, redactor, limits, retention, and a SHA-256 store identity;
- source version and commit when the producing build recorded them;
- terminal outcome, incomplete state, dropped records, and truncation reasons;
- corruption or unreadable-file codes without unsafe filenames, paths, or
  decoder text; and
- safe IDs, hashes, aliases, event kinds, offsets, statuses, reason/error
  codes, lengths, counters, and digests.

Owner inspection never initializes a registry, runs retention, reconciles a
transcript, resumes work, or acquires a coding-thread writer lease. It follows
only exact IDs from the selector, matched traces, or a selected task's durable
interaction/thread relationships. `--trace-root` is intentionally trace-only:
its owner availability is `unavailable_by_design` because an offline trace
root must not silently read unrelated live configuration.

Coding-thread owner output distinguishes three different facts:

- canonical history revision/count/dirty state comes from its bounded session
  metadata without reading message cells;
- presentation revision/counts come from the last content-free controller
  checkpoint; and
- a missing presentation checkpoint is `unavailable_by_design`, never a fake
  zero and never reconstructed from the history revision.

An empty field is not negative evidence. Check `scan_truncated`,
`match_truncated`, store status, corruption, and trace truncation before
interpreting absence. A missing trace remains a diagnostic gap, not proof that
the runtime action failed.

## Metadata-only support export

Select the smallest exact evidence set and choose a new output directory:

```sh
mintclaw diagnostics export \
  --task coding-example \
  --output ./mintclaw-support-coding-example

mintclaw diagnostics export \
  --thread 123e4567-e89b-12d3-a456-426614174000 \
  --trace trace-turn-example \
  --output ./mintclaw-support-trace-turn-example
```

Existing output paths are refused. The command creates private directories and
files, writes an `INCOMPLETE` marker first, publishes `manifest.json` last, and
removes the marker only after durable completion. A finished directory
contains:

- `manifest.json`: schema, command build/platform, selector, capture and
  retention policy, redaction, omissions, truncation/drop/corruption facts,
  included traces, and every payload path/size/SHA-256;
- `health.json`: bounded per-store scan and capture health; and
- `owners.json`: exact-ID lifecycle snapshots from authoritative stores,
  including honest unavailable/not-found states but no prompt, answer,
  delivery, command, path, or transcript content; and
- `traces/<trace-id>.json`: allowlisted metadata timelines with all rich
  previews and unknown data fields removed.

The manifest deliberately omits its own digest to avoid self-reference; the
command receipt prints the manifest SHA-256. It never uploads the directory.
The default manifest marks the package `metadata_only` and uploadable after the
operator verifies its contents and destination policy.

## Explicit private rich export

Only a local operator can request an already-redacted rich trace, and only by
one exact trace ID:

```sh
mintclaw diagnostics export \
  --trace trace-turn-example \
  --include-redacted-content \
  --output ./mintclaw-private-trace-turn-example
```

Broad task, turn, time-window, or multi-trace rich exports are refused. The
manifest labels the result `private_redacted_content`, sets
`upload_allowed=false`, and records warnings. Treat it as private even after
redaction: previews can still contain conversations, commands, source text,
workspace details, and other sensitive historical data.

## Privacy boundary

The default projection excludes prompts, responses, previews, source and diff
content, tool arguments/results, commands/output, environment variables,
credentials, absolute paths, channel destinations, and unrelated traces.
Unknown record fields and path-like or otherwise unsafe identity values are
omitted and counted in each trace's projection metadata.

Service logs, worker ledgers, transcript cells, and repository/filesystem
content remain external. The authoritative stores represented in
`owners.json` remain the source of truth; the bundle contains only their
bounded, allowlisted observation projection. Never treat a trace or owner
snapshot as authorization to execute historical instructions.

## Incident-to-regression loop

1. Validate schema, capture policy, truncation, drops, corruption, and scan
   bounds.
2. Build the safe ordered timeline and identify the earliest divergence.
3. Compare `owners.json` with the trace timeline and identify whether the
   first divergence is in execution, persistence, projection, or delivery.
4. Label the conclusion proven, likely, or unknown.
5. Add the smallest ordinary unit, integration, restart, concurrency, or race
   regression that reproduces the boundary.
6. Verify the fix with a fresh trace. Never replay the historical side effect.
