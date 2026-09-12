# Local Coding Agent P7.3 Worktree Admission

Roadmap packet: [P7.3 — Multi-agent coding and worktrees](local-coding-agent-roadmap.md#p73--multi-agent-coding-and-worktrees).

Status: admitted for implementation after the completed P7.2 task-scoped
worker boundary. This record owns isolated Git execution roots only. It does
not admit Node Companion commands, Telegram dispatch, pull-request
publication, automatic rebase/merge, or removal of the ACPX compatibility
path.

## Decision

MintClaw will allocate one fresh linked Git worktree and one dedicated branch
for every asynchronous **mutating** coding task. An investigation task keeps
the configured source project as its read-only execution root. Existing local
foreground `mintclaw code`, `mintclaw resume`, and `mintclaw code exec`
commands keep their current explicit-current-directory behavior.

The allocation owner is a new local, transport-neutral coding package. It is
not part of the gateway, terminal UI, worker protocol transport, or coding
thread store. A future P7.4 Node supervisor will compose this package with the
P7.2 worker process; local tests can compose the same boundary without a node.

This is deliberately narrower than a Git automation service. P7.3 creates,
observes, leases, and conservatively releases MintClaw-owned worktrees. It
never fetches, pushes, opens a pull request, rebases, merges, resolves a
conflict, resets, restores, cleans, stashes, or deletes an unproven branch.

## Why worktree per mutation task

A thread writer lease protects one transcript, but it does not protect a Git
index or working directory shared by two different threads. A unique linked
worktree gives every task an independent index, branch, execution root, and
repository snapshot while sharing the source repository's object database.
That is the smallest isolation boundary that fits the existing native coding
runtime and keeps ordinary Git tools usable.

The boundary is not used universally:

- read-only investigations do not need a disposable checkout and retain the
  immutable P7.2 read-only tool profile;
- foreground local coding remains an intentional user-owned checkout workflow;
- non-Git directories, bare repositories, and unborn repositories are not
  admitted for asynchronous mutation in the first slice; and
- submodule recursion and worktree creation inside a repository are never
  implicit.

The caller must provide an already resolved source `ProjectIdentity`, a full
base object ID, a canonical operator-selected worktree parent, and bounded
task identities. The model cannot author an absolute path, branch name,
repository URL, base revision, or cleanup policy.

A dirty or detached source worktree is allowed because allocation is based on
the explicit committed base object and does not mutate the source index or
checkout. Its uncommitted files are not copied into the task worktree; that
fact is returned in allocation evidence rather than silently treating them as
part of the task.

## Ownership model

One allocation has these immutable identities:

- worktree ID, task ID, task-generation ID, native coding thread ID, and
  source project key;
- source worktree root, Git directory, common directory, credential-free
  origin, source branch observation, and full base object ID;
- canonical worktree parent and derived execution root;
- MintClaw-derived branch name; and
- creation timestamp and schema version.

The worktree ID is derived from the native thread identity so the existing
closed P7.2 worker binding can locate the allocation without adding an
anonymous path-bearing protocol field. The allocation record remains a
separate identity and is not treated as thread metadata.

One process-scoped OS file lock is the exclusive writer authority for an
allocation. Its bounded diagnostic record adds the worker-generation ID, PID,
host, and acquisition timestamp. A PID, timestamp, JSON record, directory
presence, or branch name alone never proves ownership. The held kernel lock is
authoritative.

The allocation manager serializes catalogue changes separately from the
per-allocation writer lock. It publishes immutable identity before invoking
Git, records preparation progress, and publishes `ready` only after it has
re-resolved and matched the linked worktree. Another request with the same
idempotency identity returns the established allocation; a conflicting
identity fails closed.

The owner lease must remain held from worker launch through authenticated
worker termination and handoff capture. A successor worker generation may
take the same allocation only after the earlier process lock is available and
the retained allocation revalidates. It never creates a second worktree or
replays the previous turn merely because the previous process disappeared.

The P7.2 thread lease and the P7.3 worktree lease protect different state and
both are required for a mutating worker:

```text
supervisor
  |
  +-- P7.3 worktree owner lease ---- execution root + Git index + branch
  |
  +-- P7.2 worker process
        |
        +-- P1 thread writer lease - transcript + runtime checkpoints
```

## Durable layout and lifecycle

Allocation state lives under the external MintClaw coding state root, never
inside a source checkout or linked worktree:

```text
<coding-state>/worktrees/
  catalog.lock
  allocations/<worktree-id>/
    allocation.json
    owner.lock
```

The configured worktree parent is a separate canonical directory. Execution
roots are direct children with a fixed MintClaw prefix plus the worktree ID.
The manager rejects a parent inside the source worktree, inside the Git common
directory, inside MintClaw's durable state, or through a symlink/replaced path.

The durable allocation lifecycle is:

```text
reserved -> preparing -> ready -> retained
                 |         |          |
                 +---------+----------+-> cleanup_pending -> released
                              \
                               +-> uncertain
```

- `reserved` means identity is durable and no Git effect is claimed.
- `preparing` means a branch/worktree command may have taken effect; restart
  must inspect exact Git and filesystem evidence rather than repeat it.
- `ready` means the execution root, Git common directory, Git directory,
  branch, and initial HEAD were re-resolved and matched.
- `retained` means a terminal handoff exists but cleanup was not authorized or
  safe; it remains resumable/inspectable.
- `cleanup_pending` means exact owned cleanup was requested and may need
  reconciliation after interruption.
- `released` means the linked worktree is proven absent. The record remains as
  idempotency and recovery evidence.
- `uncertain` means observation cannot prove a safe transition. No destructive
  operation follows automatically.

A crash between durable preparation and the Git result is reconciled from
`git worktree list --porcelain -z`, canonical filesystem identity, exact Git
directory/common-directory identity, and the recorded branch/base. A matching
worktree is adopted into `ready`. A proven absence can return to a retryable
reserved state only when no recorded branch or unexpected filesystem entry
exists. Ambiguity becomes `uncertain`.

## Source and execution identity

The configured project remains the source authority in the P7.2 worker
binding. For mutation, the native worker resolves a second execution-project
identity from the allocated root and requires:

- a Git linked worktree whose root exactly equals the bound execution root;
- the same canonical Git common directory and credential-free origin as the
  source project;
- a distinct Git directory and project root from the source worktree;
- the exact MintClaw-owned branch recorded for the allocation; and
- a live P7.3 owner lock whose task, task generation, thread, worker
  generation, execution-root identity, and allocation record match the P7.2
  binding.

The coding thread stores the execution-project identity because its baseline,
diffs, tools, instructions, and resume-time filesystem checks describe the
actual checkout it can mutate. The allocation record retains the configured
source identity for later P7.4 project-alias reporting. `mintclaw resume --all`
can discover the idle thread; the source checkout is not silently treated as
the execution checkout.

The relative invocation directory is mapped from the source worktree into the
linked worktree and must remain local. Project instructions are discovered
from the execution checkout plus the external coding configuration root. This
preserves repository-local instruction semantics without reading a different
mutable checkout.

## Branch and conflict handoff

Branch names are deterministic, bounded, validated by `git check-ref-format`,
and derived under an operator-owned prefix from the worktree/thread ID. The
first slice uses the exact accepted base object already present locally; it
does not fetch or infer a moving remote tip.

The owner exposes one bounded handoff snapshot with:

- worktree ID, thread ID, source project key, execution root, branch, base,
  current HEAD, and clean/dirty state;
- bounded staged, unstaged, untracked, and unmerged paths;
- ahead/behind observations relative to the accepted base when available;
- in-progress merge, rebase, cherry-pick, revert, or bisect markers; and
- an explicit `ready`, `changes`, `conflicted`, `missing`, `mismatch`, or
  `uncertain` classification.

An unmerged index or in-progress Git operation is `conflicted`. P7.3 reports
it and retains the worktree. It does not choose ours/theirs, continue, abort,
or manufacture a clean result. Rebase and merge remain a user or later
publication-workflow handoff, never an allocation cleanup side effect.

## Cancellation, retention, and cleanup

Interrupt and hard cancellation stop only the exact worker generation. They
do not roll back Git state and do not release the worktree owner before the
worker process outcome and handoff have been captured.

Cleanup is an explicit, idempotent owner operation. It may remove a worktree
only when all of the following are proven in one revalidated observation:

- the caller holds the exact allocation owner lock;
- the durable record is known and matches the current task/thread identity;
- the root is the direct derived child of the pinned parent and has not been
  replaced or redirected;
- Git's worktree catalogue maps that exact root to the recorded Git directory
  and common directory;
- no other thread/worker generation is active;
- the index and working tree are clean, with no untracked or unmerged paths;
- no merge/rebase/cherry-pick/revert/bisect operation is in progress; and
- terminal branch/patch/report references have been durably handed off.

Cleanup uses normal `git worktree remove` without force. It does not recursively
delete the root. A changed, missing, replaced, user-created, duplicate,
ambiguous, locked, or externally modified worktree is retained with an
actionable reason. Failure after Git may have removed the worktree is
reconciled before any retry.

The MintClaw-created branch is deleted only when the worktree removal is
proven complete, the branch still points exactly at the accepted base, and no
commit or other deliverable exists. Otherwise the branch is retained. Records
and the last handoff are not deleted by worktree cleanup.

## Package and composition boundaries

Implementation is split into focused dependent packets:

1. **P7.3a — allocation authority:** add the transport-neutral worktree
   catalogue, immutable records, deterministic paths/branches, catalogue and
   owner leases, idempotent allocation, and restart reconciliation.
2. **P7.3b — worker composition and handoff:** require a live allocation owner
   for mutation workers, resolve the execution-project identity and relative
   cwd, capture its baseline, and expose bounded branch/change/conflict
   handoff. Investigate mode and foreground commands remain unchanged.
3. **P7.3c — cleanup and proof:** add conservative release, crash/cancel/resume
   integration cases, concurrent-agent exclusion, path-replacement and dirty
   worktree refusal, and Linux/macOS portability evidence.
4. **P7.3 exit:** record merged PRs and exact done-criteria evidence. Only this
   merge closes the roadmap packet and unlocks a separate P7.4 admission.

The allocation package owns Git mutations needed for `worktree add` and safe
`worktree remove`. The existing workspace repository remains the passive
status/diff/review owner. The thread store remains transcript authority. The
worker process remains process and protocol authority. P7.4 will own remote
task dispatch and delivery rather than moving those concerns into P7.3.

## Required validation

Focused tests must cover:

- two concurrent processes attempting the same allocation and distinct
  allocations from the same source repository;
- duplicate idempotent admission and conflicting identity rejection;
- dirty and detached source observations without copying uncommitted state;
  source and parent symlinks/path replacement; missing Git;
  bare/non-Git/unborn inputs; invalid branch/base; and a worktree already
  registered elsewhere;
- crash reconciliation before Git, after branch creation, after worktree
  registration, and after filesystem removal;
- exact worker binding, live owner generation, thread lease, execution root,
  common directory, branch, and relative cwd validation;
- committed, staged, unstaged, untracked, unmerged, and in-progress-operation
  handoff states with deterministic bounds and no secret-bearing remote URLs;
- cancellation and successor-generation resume without duplicate worktree or
  blind turn replay;
- cleanup success for an unchanged owned allocation and refusal for dirty,
  conflicted, active, unknown, replaced, or ambiguous roots;
- retention of any branch containing commits and of the last durable handoff;
- unchanged local interactive and `code exec` behavior; and
- the same real-Git owner/worker lifecycle on Linux and macOS CI.

Repository-wide format, lint, security, tests, race checks, and platform
compilation remain required through normal CI. Git command output and durable
records are bounded and parsed as data; credentials and arbitrary config or
hook execution are excluded from diagnostics.

## Done criteria

P7.3 is complete only when:

- every asynchronous mutating worker can be composed only with one ready,
  exclusively leased allocation and explicit execution root;
- two live coding agents cannot mutate the same worktree by default, while
  distinct tasks can use distinct worktrees from one repository;
- restart can reconcile one retained allocation and start an explicit
  successor generation without creating or replaying another task;
- the terminal handoff makes branch, base, HEAD, changed paths, conflicts, and
  uncertainty explicit without resolving or publishing them;
- cleanup never deletes user work and every refusal leaves recovery evidence;
- Linux and macOS exercise the real Git and worker boundaries; and
- a merged P7.3 exit record links all implementation, CI, review, and safety
  evidence while leaving P7.4 unimplemented.
