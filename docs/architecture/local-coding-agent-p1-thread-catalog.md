# Local Coding Agent P1.2 Bounded Thread Catalog

Status: implemented

Roadmap packet: P1.2 in
[`local-coding-agent-roadmap.md`](local-coding-agent-roadmap.md)

## Decision

MintClaw admits `thread.Catalog` as the bounded, read-only discovery boundary
over P1.1 `thread.meta.json` descriptors. It never opens canonical JSONL,
Seahorse, diagnostics, or future lease files.

The catalog supports the selectors required by P1.4 without importing CLI or
TUI packages:

| Product selector | Catalog query |
| --- | --- |
| Default `resume` picker | current canonical `project_key` |
| `resume --all` | `All` scope |
| `resume --last` | current project plus `Last` |
| `resume --all --last` | all projects plus `Last` |
| `resume <thread-id>` | exact `ThreadID` |

An exact ID is direct-addressable at `threads/<uuid>/thread.meta.json`. It does
not scan and deliberately bypasses project filtering so P1.4 can report an
explicit mismatch rather than pretending the thread does not exist. The
rooted lookup rejects symbolic links at the threads root, thread directory,
and descriptor file. Exact ID cannot be combined with list options.

List queries require either one validated project key or explicit `All`.
Implicit cross-project discovery is not admitted.

## Ordering and filtering

Healthy descriptors are ordered deterministically by:

1. `updated_at`, newest first;
2. `created_at`, newest first; and
3. canonical thread UUID, ascending.

Project filtering happens from metadata alone. Git branch, HEAD, origin, and
the current existence of a project path do not affect catalog membership.
P1.4 inspects the selected thread's current location after selection.

`Last` is the first item in the same deterministic ordering and cannot be
combined with offset or page size. `All` may be combined with `Last`, but an
all-project query cannot also carry a project key.

P4.4 adds bounded case-insensitive metadata search before ordering and
pagination. The query is trimmed, must be valid UTF-8, and is limited to 256
bytes. It matches only the thread ID, title, preview, project root, invocation
cwd, and persisted branch; canonical transcript content is never opened or
searched. Exact-ID lookup cannot be combined with search.

## Hard bounds

Production defaults are also hard maxima; custom options may only tighten
them:

| Work | Bound |
| --- | ---: |
| Directory entries examined | 10,000 |
| Bytes read per descriptor | 32 KiB, inherited from P1.1 |
| Default returned page | 100 threads |
| Maximum returned page | 500 threads |
| Returned skip diagnostics | 50 |
| Skip entry text | 128 UTF-8 bytes |
| Skip reason text | 256 UTF-8 bytes |
| Search query | 256 UTF-8 bytes |

The directory is read in batches of at most 128 rather than with an unbounded
`os.ReadDir` call. Each read is also capped by the remaining scan allowance;
after the allowance is exhausted, one bounded lookahead detects whether the
result is truncated. Every examined entry counts against the scan cap,
including junk, so an adversarial state directory cannot make work or memory
unbounded. Context cancellation is checked between batches and entries.

The catalog keeps a bounded oldest-first heap containing only the newest
`offset + limit` matching descriptors while it scans. A normal first page
therefore retains 100 descriptors even when 10,000 are examined; a caller can
never retain more than the scan cap. The result copy remains at most 500.

## Corruption and privacy

One malformed entry never terminates a list query. The catalog skips:

- non-UUID thread directories;
- symbolic-link entries;
- missing descriptors;
- symbolic-link, FIFO, device, socket, or other non-regular descriptors;
- oversized descriptors;
- invalid JSON or schema versions; and
- descriptors that fail any P1.1 identity, timestamp, or credential-redaction
  invariant.

`SkippedTotal` counts every skipped entry within the scan bound, while
`Skipped` returns at most 50 deterministic, bounded diagnostics. Diagnostics
contain the bounded entry name and one fixed safe reason code. Raw JSON,
validation errors, paths embedded in errors, remote values, and transcript
content are never copied into picker diagnostics.

On Unix, the store root, threads root, and each thread directory are opened
atomically with directory-only, nonblocking, no-follow handles. The descriptor
is likewise opened nonblocking and no-follow, then the opened handle is statted
and must be a regular file before any read. This closes substitution races
where any validated path component is replaced by a FIFO before open.

Windows uses parent-handle-relative `NtCreateFile` calls with
`OBJ_DONT_REPARSE` and `FILE_OPEN_REPARSE_POINT`, then rejects reparse-point
attributes on the opened handle. Thus in-root junctions and symbolic links are
not accepted merely because their targets remain beneath the store root.

Filesystem errors on the catalog root itself remain fatal because a partial
directory read cannot be represented as a trustworthy complete page. A
missing `threads/` directory is a healthy empty catalog and is not created by
listing.

## Pagination and truncation

Pages use zero-based offsets into the project-filtered ordering inside the scan
bound. `Matched` reports the number observed, while `HasMore` and `NextOffset`
describe only additional items inside that bounded set.

`ScanTruncated` is independent. It means the directory contained more than
10,000 entries and the result is incomplete. The catalog intentionally does
not emit a next offset beyond the cap: repeating the same query cannot safely
discover an arbitrary filesystem-order tail. The TUI must show the truncation
warning and offer project narrowing, an exact ID, cleanup, or a future indexed
catalog migration.

Offset pagination is a fresh metadata observation, not a stable cursor. If a
thread changes between pages, the frontend refreshes from offset zero rather
than assuming old offsets still identify the same rows. P4 owns that refresh
behavior.

## Latency budget and evidence

The admitted local latency budget is one second to scan, validate, filter,
sort, and return the first page from 2,000 metadata entries on a local
filesystem in a normal Go test build. Fixture creation is outside the timed
section.

The deterministic regression creates 2,000 independent version-1 descriptors
and requires all 2,000 to be scanned with no skips or truncation and a 100-item
first page. It records elapsed time for diagnostic visibility but does not use
host wall-clock speed as a routine pass/fail condition. A separate
`BenchmarkCatalogFirstPageTwoThousand` measures the admitted one-second target
on controlled or developer hosts without making shared CI flaky. The earlier
5,000-entry development probe completed the query in approximately 280 ms on
macOS; the smaller committed fixture keeps routine test setup proportionate
while still exercising thousands of real metadata files.

Additional focused tests prove:

- default current-project filtering excludes other projects;
- `All`, project `Last`, all-project `Last`, and exact ID select the intended
  descriptor;
- exact ID rejects in-root thread-directory and metadata-file symlinks on
  Unix and Windows;
- ordering and two-page offsets are deterministic;
- an unreadable deliberately invalid transcript does not affect listing;
- malformed, oversized, missing, invalid-ID, and symlink metadata entries do
  not hide healthy threads;
- a metadata FIFO is rejected before open and cannot block a Unix catalog scan;
- coordinated replacement of a validated descriptor with a FIFO cannot block
  the subsequent handle open;
- coordinated replacement of either the threads root or one thread directory
  with a FIFO cannot block its atomic directory open;
- skip counts remain complete while returned diagnostics remain bounded;
- a tightened three-entry scan reports truncation and never offers pagination
  beyond the observed set;
- exact-limit and limit-plus-one scans prove bounded lookahead across the
  128-entry batch boundary;
- invalid selector combinations and project keys fail before scanning; and
- canceled contexts and attempts to expand hard limits fail immediately.

P1.3 can now add a writer lease without changing list queries: read-only
catalog discovery does not need or inspect `thread.lock`. P1.4 can map CLI
selectors directly to this transport-neutral query contract.
