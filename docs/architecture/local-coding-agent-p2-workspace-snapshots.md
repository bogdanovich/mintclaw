# Local Coding Agent P2.3 Workspace Snapshots

Status: implemented

Roadmap packet: P2.3 in
[`local-coding-agent-roadmap.md`](local-coding-agent-roadmap.md)

## Decision

Every coding runtime owns a workspace observer rooted at the canonical coding
project and invocation working directory. The observer captures a small,
deterministic repository summary before a turn reaches the model and refreshes
it after a tool can have changed repository state.

The summary is a live observation, not conversation memory. Its prompt block
explicitly supersedes older narrative and compacted claims about the repository.
The model must use tools when it needs file contents or a full diff; neither is
inserted automatically.

## Snapshot contract

The bounded snapshot contains:

- canonical project root and invocation cwd;
- Git worktree root, Git directory, and common directory;
- branch and HEAD, including detached and unborn states;
- clean or dirty status;
- sorted porcelain status paths with rename origins;
- aggregate text additions, deletions, changed files, and binary-file count;
- worktree identity, truncation, and bounded warnings; and
- an explicit unavailable reason for a non-Git project or failed Git
  inspection.

Defaults admit at most 128 changed paths, 512 KiB from any Git command, 24 KiB
in the rendered prompt block, and five seconds for the complete capture. Git
stdout and stderr are drained through bounded writers. Paths are sorted before
identity and rendering, and the observer hashes only structured bounded state.
It never hashes or records file contents.

Prompt rendering admits complete newline-delimited records only. When the next
record would cross the prompt budget, rendering stops and reserves space for an
explicit prompt-truncation marker; it never silently returns half of a quoted
status path.

For an unborn branch, the diff stat combines staged changes against the empty
index view with unstaged changes against the index. Untracked paths are named
by status but their contents and sizes are not treated as an implicit diff.
Outside Git, Git-specific fields remain unavailable without rejecting the
coding project.

## Refresh and prompt ownership

The coding `ContextBuilder` owns the observer because it already owns the
dynamic, non-cacheable runtime prompt block. Initial message assembly refreshes
the observer. Each provider request then replaces that runtime block from the
observer's current state, so a tool write is visible on the very next model
step without invalidating the stable coding-instruction prefix.

After tool completion, the pipeline refreshes when:

- the tool is `exec`, because a command may mutate the worktree even on a
  failed exit; or
- the result contains a verified write audit, including a partial audited
  write followed by an error.

Read-only tools do not run extra Git commands. An `exec` or audited write that
does not change the bounded snapshot produces no duplicate update.

## Runtime and frontend projection

When the observer identity changes, the pipeline emits
`agent.workspace.snapshot` with the typed snapshot. The initial event follows
turn setup; post-tool events follow the corresponding tool-completion event.
This preserves the causal order a frontend needs to render “tool finished” and
then current repository state.

The existing transport-neutral frontend protocol projects the observation as
a `workspace_updated` delta and retains the latest workspace in
`ThreadSnapshot`. Projector and reducer copies deep-clone changed paths so
callers, watchers, and resynchronization cannot alias mutable slices. The
ordinary event bus may remain lossy: the dedicated coding adapter projects
synchronously before forwarding, while frontend revision gaps still recover
through an authoritative snapshot.

This snapshot is not canonical history and is not written to a coding session
database. Resume and later compaction work capture it again from the live
workspace. P5.2 will re-anchor compacted context with the same fresh-observation
rule rather than preserving a stale repository claim in a summary.

## Failure and privacy boundaries

- Git absence, non-Git directories, detached HEAD, and unborn branches are
  ordinary typed states rather than turn failures.
- A failed secondary Git command keeps the available fields and adds a bounded
  warning instead of inventing values.
- Status paths are exposed because they are necessary repository state; file
  bodies, patch hunks, environment variables, remotes, and command output are
  excluded.
- Observer Git commands discard ambient `GIT_*` variables, pin the locale,
  disable `core.fsmonitor`, neutralize every configured clean/process content
  filter, and pass `--no-ext-diff` plus `--no-textconv` to diff-stat commands.
  Opening a repository therefore cannot redirect capture through ambient
  repository/index variables or invoke configured fsmonitor, external-diff,
  textconv, or content-filter commands. When filter configuration cannot be read
  completely within its bound, status and diff fields remain unavailable
  instead of risking command execution. This uses Git's documented
  [missing-filter passthrough semantics](https://git-scm.com/docs/gitattributes#_filter)
  while also forcing each discovered driver's `required` flag off.
- Status and diff-stat capture use `--ignore-submodules=dirty`. They still
  report a changed gitlink, but deliberately do not enter initialized
  submodule worktrees, whose independent configuration could execute content
  filters. The structured snapshot and prompt explicitly state that nested
  submodule worktree state was not inspected.
- The observer runs Git only at the admitted coding project root. Project
  identity resolution already makes a Git project's execution root its exact
  worktree root, keeping separate linked worktrees independent.
- Snapshot capture does not broaden filesystem, command, gateway, or companion
  authority. Those execution contracts remain owned by P2.4 and later remote
  execution packets.

## Done evidence

Automated tests cover clean and dirty repositories, deterministic ordering and
bounds, detached HEAD, staged changes on an unborn branch, linked worktrees,
non-Git fallback, timeout behavior, and changed-only observer emission. A
native scripted coding turn proves that the first model call sees clean state,
an audited write refreshes the prompt before the next model call, file contents
are not injected, and ordered clean/dirty workspace events are emitted.

Sentinel regressions also prove that repository-configured fsmonitor,
external-diff, and top-level or submodule clean/process-filter commands are not
executed, ambient repository-selection variables cannot redirect capture, and
prompt truncation ends at a record boundary with a visible marker.

Frontend tests prove typed event projection, delta reduction, snapshot
convergence, and slice isolation.
