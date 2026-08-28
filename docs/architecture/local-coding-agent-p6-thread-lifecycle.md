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

## Boundaries reserved for later packets

Deletion may target only an enumerated set of files under the external
MintClaw coding state root. Its confirmation must name those artifacts, refuse
an active lease, and use platform trash where available. It must never derive a
deletion target from a project root or transcript content.

Forking copies bounded conversational state into a newly allocated thread with
its own session key, directory, lease, and future writer. Fork metadata records
the source thread, source transcript revision, and source message identity. A
historical conversational fork always starts against the live filesystem; it
must not claim or imply workspace rollback.

## First-packet done criteria

- invalid titles and lifecycle timestamps cannot be persisted;
- rename/archive/unarchive are rejected during an active turn or compaction;
- persistence and the live projection converge after a successful operation;
- active and archived catalog/picker views change on the next read;
- exact-ID resume remains possible for an archived thread;
- focused tests cover metadata validation, catalog separation, controller
  serialization, native persistence/projection, TUI commands, and picker
  toggling.
