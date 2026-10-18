# Manual Agent Evals: SQLite Inspection Probe

## Scope

On 2026-10-18 UTC, two synthetic native-store experiments investigated a prerequisite for the unfinished eval
privacy witness. Both reproduced residual encoded data after native session clearing; the second added FTS/part
counts and explicit fixture-cleanup verification. These were format experiments, not real-model eval attempts,
privacy qualification, production access or a secure-erasure product test.

Source: clean identified head `dd032f3c0fce3b70d1ddc21e27b7668218256088`, now merged in
[PR #1716](https://github.com/bogdanovich/mintclaw/pull/1716). Platform: macOS amd64, Go 1.26.9,
`goolm,stdjson`, `modernc.org/sqlite v1.53.0`. No Linux/Windows format-probe result is claimed.
The expanded local diagnostic helper had SHA-256
`c3bbb0258124cce7ee9cf0b4ec6fbc1ab7e96f07561539ce16bed4c96fb51316`; it is an operational diagnostic, not a
shipped eval command or a versioned harness test. No credentials or deployed workspace were used; model calls: zero.

## Experiment

1. Create a private temporary directory and a real native `seahorse.NewEngine` with its own database path and
   no completion callback. Do not point this constructor at a retained or deployed profile: it creates directories,
   selects WAL mode and runs schema setup.
2. Ingest 32 synthetic user messages in one session. Their JSON text contains one known synthetic canary represented
   entirely with JSON Unicode escapes, not as a plaintext literal. Close the native engine.
3. Read the original bounded database image before any removal or sanitization. Observe literal/escaped predicates,
   image size and SHA-256; check whether WAL/SHM sidecars remain.
4. Use the existing SQLite driver's `sql.Conn.Raw`/`Deserialize` interface on separate in-memory connections.
   Try the exact image, then a private copy with header bytes 18/19 set to rollback-journal format. Query integrity,
   current row counts and free-page count. Compare the original file SHA-256 afterward.
5. Reopen only this synthetic fixture with the native engine, call `ClearSession`, close it, and repeat observation.
   Delete only the known owned database/sidecars and empty parent after inspection.

The header projection was confined to a private in-memory decoding copy. It did not modify the original image,
checkpoint it or establish WAL completeness for an arbitrary retained store. This is not an implementation recipe
that permits normalizing an uncertain production snapshot into passing evidence.

## Expanded Results

| Observation | Populated | After Native ClearSession |
| --- | --- | --- |
| Original database bytes | 212,992 | 221,184 |
| Raw plaintext canary literal present | false | false |
| Full JSON-Unicode-escaped canary sequence present | true | true |
| WAL-format header | true | true |
| WAL and SHM sidecars absent after close | true | true |
| Exact raw image queryable by this in-memory probe | false | false |
| Private header-projected copy passes quick check and queries | true | true |
| Current `messages` rows | 32 | 0 |
| Current `message_parts` rows | 0 | 0 |
| Current `messages_fts` rows | 32 | 0 |
| Current `summaries` rows | 0 | 0 |
| Free pages reported by decoded image | 4 | 8 |
| Original file SHA-256 unchanged by probes | true | true |

The inspection connections reported `secure_delete: 0`. This was measured on the decoded in-memory connections,
not by querying the original native connection. The experiment did not locate the residual sequence in a particular
free page, free cell or FTS shadow structure. Do not attribute its exact location from the free-page count alone.
Expanded-run fixture cleanup was verified. The first outcome was not hidden or retried until green.

## Consequences For The Goal

A current-row-only SQL scan plus a raw literal search would miss this known encoded value after native clearing.
An empty current `messages`/FTS result must therefore not promote the raw file to complete privacy coverage.
The existing eval observer remains conservative: an opaque SQLite image is unavailable evidence, not a pass.
Earlier inconclusive live reports remain unchanged.

This does not show that production protected facts entered Seahorse, that `ClearSession` promises secure erasure,
or that enabling secure deletion is the right fix. The probe deliberately inserted synthetic encoded content.
Changing storage defaults, vacuuming/checkpointing, clearing sessions or removing bytes before inspection would
not be an acceptable way to make the eval's historical privacy observation green.

The next native witness must keep original raw-byte inspection, account honestly for encoded residual content,
validate native schema and snapshot/WAL completeness, bound bytes/rows/strings/time, and avoid opening retained
stores through the mutating constructor. Reuse the native owner and existing SQLite decoder rather than duplicating
schema/authority in the eval runner. A logical snapshot may be useful evidence, but its scope is not whole-file
absence of leaked values. Retain this scenario as an adversarial control when implementing the witness.

The full [E0-E5 goal](../architecture/agent-evals-goal.md) remains active. The compiled public-system-file predicate
in #1716 solves a separate raw-file classification problem; it grants no SQLite or domain privacy qualification.
