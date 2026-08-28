# Local coding agent P6.1 thread lifecycle

This record admits the P6.1 lifecycle work as three implementation packets:

1. atomic rename and archive/unarchive metadata changes;
2. confirmed, recoverable deletion of MintClaw-owned thread artifacts;
3. conversational fork from the latest state or a stable user-turn boundary.

The packets are intentionally sequential. Rename and archive establish the
metadata mutation boundary used by the more destructive operations, while
delete and fork each require separate ownership and restart evidence.

## Rename and archive contract

The thread lease owner is the only process allowed to rename or change archive
state. The controller serializes these commands with turns and compaction, and
the coding composition root performs one atomic replacement of
`thread.meta.json`. A failed pre-commit write leaves the previous metadata and
projection unchanged; a committed-write durability warning keeps the new state
and is reported when the controller closes.

Rename changes only the bounded title and `updated_at`. Archive changes only
`status` and `updated_at`. Neither operation moves a thread directory, touches
the canonical transcript, rewrites Seahorse state, or reads or writes any file
under the project root.

Active and archived catalog views are disjoint. Normal list, last, and picker
queries select active threads. `mintclaw resume --archived` and the picker `Z`
toggle select archived threads, while an exact thread ID remains directly
addressable regardless of status. This makes archive reversible without
loading transcript history or requiring an index rebuild.

Inside an idle coding TUI, `/rename <title>`, `/archive`, and `/unarchive` use
the controller boundary and update the current projection after persistence.
The next catalog or picker refresh observes the atomic metadata file directly,
so lifecycle changes have no separate cache to invalidate.

## Delete contract

Deletion is exposed as `mintclaw threads delete <thread-id>`. Without
`--confirm`, it produces a bounded plan naming the exact external thread root
and each recognized MintClaw-owned top-level artifact. Unknown entries and
symbolic links fail closed. Confirmation must repeat the exact thread ID and
the command must run from the owning project.

After confirmation, the command acquires the thread lease, rebuilds the plan,
and atomically renames the complete thread root into `coding/trash/threads` on
the same filesystem. The active catalog loses the thread immediately while
the complete directory remains recoverable at the reported trash path. The
move does not walk artifact contents. Its source and destination derive only
from the canonical external store plus a validated UUID; project root and
transcript content never participate in a deletion target. On Windows the
lock handle permits delete sharing while the byte-range lock remains the
exclusive writer authority, allowing the directory move without opening a
post-release race.

## Conversational fork contract

`mintclaw threads fork <thread-id>` copies conversation through the latest
stable root user turn. `--at-turn N` selects a one-based historical root turn
and includes that turn's messages up to, but not including, the next root turn.
Canonical `root_turn_start` markers define boundaries. A bounded legacy prefix
without markers remains readable, while unmarked user-shaped messages after the
first canonical marker are not treated as roots.

The source lease must be idle and remains held while its canonical transcript
is read. One fork admits at most 4,096 visible messages, a 32 MiB source JSONL
file, and the canonical memory reader's 10 MiB per-record limit; larger inputs
fail before a target is allocated. The selected root message gets a stable
SHA-256 prefix identity. Metadata records that identity, the source transcript
revision, source index/turn, copied count, and source thread ancestry.

The source thread, sessions directory, metadata, and JSONL are opened through
anchored no-follow handles and remain pinned for the complete read. Forking is
read-only: an unfinished dirty-history transaction fails closed for normal
runtime recovery instead of being repaired by this administrative command.
Publication is verified through the anchored target directory and its held
lease identity, so replacing the target path cannot classify another directory
as the committed fork.

The child gets a fresh UUID, session key, external directory, lease, current
project snapshot, and future writer. It inherits only model/provider selection
and the selected canonical message prefix. Seahorse state, compaction state,
runtime artifacts, diagnostics, and workspace files are not copied; they are
rebuilt independently as needed. Metadata is published last, after the child
JSONL is durable, and committed status requires reading back the exact child
descriptor, so incomplete preparation is absent from the catalog.

Both human and JSON results state that the fork uses the current live
filesystem and provide `mintclaw resume <child-id>`. Historical conversation is
context, never a claim that project files were rolled back.

## First-packet done criteria

- invalid titles and lifecycle timestamps cannot be persisted;
- rename/archive/unarchive are rejected during an active turn or compaction;
- persistence and the live projection converge after a successful operation;
- active and archived catalog/picker views change on the next read;
- exact-ID resume remains possible for an archived thread;
- focused tests cover metadata validation, catalog separation, controller
  serialization, native persistence/projection, TUI commands, and picker
  toggling.
