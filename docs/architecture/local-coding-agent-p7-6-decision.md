# Local Coding Agent P7.6: Workspace Rewind Decision

Roadmap packet:
[P7.6 — Workspace checkpoint and rewind investigation](local-coding-agent-roadmap.md#p76--workspace-checkpoint-and-rewind-investigation).

Status: deferred; implementation is not admitted for P8.

Decision baseline: `origin/main` at
`63804ea1f2652338861e224fbbe0b391504514c9`, 2026-10-10.

## Decision

MintClaw will not add filesystem checkpoint or rewind semantics during the P8
hardening program. Conversation fork/revert remains a transcript operation and
must not imply that files, Git state, processes, packages, services, or remote
effects were rolled back.

The current coding surface already has safer, narrower recovery boundaries:

- mutating project tasks use isolated worktrees and conservative handoff;
- repository state is re-observed from Git and the filesystem after resume or
  compaction rather than reconstructed from conversation history;
- tool calls and task starts have durable lifecycle evidence and uncertain
  mutations are not blindly replayed; and
- project-yolo and machine-yolo reports state external effects without
  promising rollback.

A general rewind layer would introduce another owner for workspace state and
would have to reconcile pre-existing changes, concurrent external edits,
untracked and ignored files, large files, symlinks, process effects, and remote
side effects. No current product failure demonstrates that this complexity is
needed before the coding surface can be made stable.

## Product behavior while deferred

- Forking or reverting a message changes only the conversation lineage.
- MintClaw never describes a conversational operation as a filesystem undo.
- The TUI and headless output continue to show current repository state after
  a fork, resume, compaction, or external edit.
- Users recover project changes through Git, the P7.3 isolated-worktree
  handoff, ordinary backups, or an explicit manual edit.
- Machine-wide and published effects remain non-transactional and are reported
  as verified, failed, or uncertain.

No patch journal, content-addressed workspace store, hidden Git object store,
checkpoint database, compatibility reader, dormant command, or feature flag is
admitted by this decision.

## Reconsideration gates

P7.6 may be reconsidered only after P8 if all of the following are true:

1. real incident evidence shows that transcript-only fork/revert is materially
   insufficient despite Git and isolated worktrees;
2. the desired operation is narrower than general machine rollback and has an
   explicit user-facing conflict model;
3. measurements cover dirty large repositories, untracked and ignored files,
   renames, symlinks, sparse worktrees, and concurrent external edits;
4. preview, partial failure, cancellation, and a recoverable inverse operation
   are specified before implementation;
5. the design cannot silently discard pre-existing or externally created
   changes and does not rewrite the user's branch, index, commits, or ignore
   rules; and
6. storage ownership, retention, deletion, privacy, and bounded cost are
   admitted independently of conversation compaction.

Meeting these gates permits a new investigation; it does not automatically
admit implementation.
