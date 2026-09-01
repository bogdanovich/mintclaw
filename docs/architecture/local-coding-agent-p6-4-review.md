# P6.4 Git review and change-summary contract

Status: implementation contract; P6.4 remains open until its roadmap exit
record is merged.

This contract makes repository evidence a native coding-runtime capability.
It follows the same responsibility boundary as Codex: deterministic Git
observation, review lifecycle, structured results, persistence, cancellation,
and presentation belong to core; project-specific review policy and pull
request orchestration belong to skills.

## Core and skill boundary

MintClaw core owns:

- passive, bounded, cancellable repository discovery and Git execution;
- a durable per-thread baseline and truthful comparison with later state;
- typed status and diff requests and responses;
- typed review targets and findings;
- native coding-thread authority, persistence, restart, fork, and compaction
  behavior;
- headless events and TUI rendering; and
- safety invariants that apply even when no skill is installed.

Skills may define review checklists, reviewer roles, GitHub API behavior,
continuous-integration monitoring, branch publication, rebase or merge policy,
and pull-request comments or labels. A skill must consume core evidence rather
than implement a second repository scanner, durable baseline, transcript
writer, or destructive Git path. Removing every skill must leave status, diff,
and local review safe and usable.

P6.4 does not add GitHub publication, automatic commit, fetch, pull, push,
rebase, merge, reset, checkout, restore, clean, stash, or worktree cleanup.

## Repository evidence service

The existing `pkg/coding/workspace` capture is the starting point. P6.4
extracts one reusable repository evidence service used by prompt refresh,
model tools, headless commands, review tasks, and TUI surfaces. Callers cannot
assemble Git commands themselves.

Every operation is read-only and uses a fixed executable plus explicit
arguments. It runs with ambient `GIT_*` variables removed, locale fixed,
optional locks disabled, hooks disabled, fsmonitor disabled, external diff and
text conversion disabled, executable clean/process filters neutralized, and
submodule worktree recursion disabled. Repository configuration that cannot be
made passive causes a bounded unavailable result rather than execution.

No evidence operation performs implicit network access or refreshes refs.
Branch comparison uses only verified local refs and a locally computed merge
base. Missing or ambiguous refs are explicit. Evidence collection never writes
project files, the index, refs, config, object storage, or the user's Git
working state.

The service applies independent limits for command duration, stdout, stderr,
changed paths, untracked paths, diff files, hunks, lines, line bytes, total
rendered bytes, binary metadata, baseline fingerprints, and concurrent
operations. Cancellation terminates the command process tree. Truncation and
partial coverage are first-class fields and remain visible to the model and
user.

## Durable thread baseline

Each coding thread owns a versioned baseline outside the project:

```text
<coding-root>/threads/<thread-id>/repository/baseline.json
```

The thread store publishes it atomically under the same selected-thread writer
lease used for other thread-owned state. It records only bounded repository
identity and evidence: project key, canonical Git top level and common-dir
identity, HEAD and branch state, changed-path status, rename source, staged
identity, bounded direct-file fingerprint where available, capture time,
limits, and completeness diagnostics. It never records file contents, diff
hunks, credentials, caller-provided repository paths outside the selected
project, or mutable path authority.

For a newly created thread, baseline publication succeeds before its first
model turn. A pending interactive thread uses the same rule. If a failure is
confirmed before a prompt commits, normal empty-thread cleanup may remove the
unreferenced baseline; once canonical history may have committed, the baseline
is retained.

Resume requires the thread baseline to exist and validate under the selected
thread lease. A rollout that introduces or changes this persisted contract
must seed retained threads during a stopped-state cutover before installing
the strict runtime. Runtime resume never reconstructs missing historical
evidence or rewrites the baseline.

A fork captures a fresh child baseline after acquiring child authority. It
does not copy the parent's baseline or claim that parent-era workspace changes
were made by the child. Compaction may summarize baseline metadata but cannot
replace, rewrite, or delete the authoritative file.

## Provenance semantics

Repository comparison describes observation, not causation. A path is one of:

- `pre_existing`: current evidence is proven identical to complete baseline
  evidence;
- `first_observed_during_thread`: the baseline proves the path or state was
  absent or different and a later authoritative refresh first observed it;
- `resolved_since_baseline`: baseline evidence was dirty and the current state
  proves that condition no longer exists;
- `indeterminate`: bounds, unsupported state, concurrent mutation, missing
  fingerprint, or identity change prevents proof.

`first_observed_during_thread` never means "written by MintClaw". Verified
tool write-audit receipts are presented separately as actions known to have
occurred through this thread. External edits may happen between any two
captures, so the UI and model must not promote correlation into ownership.

If repository top-level, common-dir identity, HEAD lineage, index identity, or
captured path identity changes during an operation, the result is stale or
indeterminate. The service never silently compares evidence from different
repository authorities.

## Status and diff actions

Core exposes typed `status` and `diff` actions to the coding agent and the
frontend controller. Their data model is shared by plain, JSON, and interactive
surfaces.

`status` reports repository identity, branch/HEAD state, staged, unstaged,
untracked, rename, deletion, conflict, binary, submodule, and provenance
summaries. It distinguishes unavailable, clean, dirty, truncated, stale, and
indeterminate states. A non-repository is a successful typed unavailable result
rather than a shell error.

`diff` accepts an explicit target:

- current worktree, including staged, unstaged, and bounded untracked content;
- a local base branch through its merge base; or
- one locally available commit.

Thread-baseline provenance is a separate typed comparison over the immutable
baseline and a current repository capture; it is not a placeholder diff
target.

The result contains file summaries and bounded hunks with old/new paths,
line ranges, additions/deletions, binary or submodule metadata, provenance,
and completeness diagnostics. Untracked files are read directly under pinned
project authority and size budgets; they are not passed to a configured Git
helper. Large or changing files yield metadata and an explicit omission.

Paths are project-relative display values. Control characters are escaped.
Symlinks are reported as links and never followed for untracked-content reads.
Findings and hunks never expose the external coding-state root.

## Review lifecycle

Review is a native read-only controller operation, not a model-authored shell
convention and not a skill requirement. It accepts these typed targets:

- current changes;
- changes against a local base branch and computed merge base;
- one locally available commit; or
- bounded custom review instructions attached to one of the evidence scopes.

The controller freezes a bounded evidence generation, starts one review task,
and emits entered, progress, finding, completed, interrupted, or stale events.
The review task may use a configured review model but receives only coding
read/search and repository-evidence capabilities. Mutating tools, network
search, GitHub tools, delegation, and user-delivery tools are absent. It never
becomes a second writer of the canonical coding transcript.

The final result is recorded once by the owning coding runtime as a canonical
review item linked to its target and evidence generation. Restart may display a
completed result or resume ordinary coding after an interrupted review; it
never blindly reruns a review or duplicates findings. Forked conversation may
copy the visible canonical result, but not live task authority.

Each finding contains severity, title, bounded explanation, confidence,
current project-relative path, and the shortest useful current line range.
Locations must overlap reviewed changed lines. Rename mapping may translate a
stable old path to its current path. If the file changed after evidence capture
or a position cannot be proven current, the finding is marked stale or
unlocated rather than attached to a guessed line.

## Presentation and prompt boundaries

Status and diff output is structured frontend state. TUI views provide a
bounded file list, summary counts, provenance labels, navigable hunks, review
progress, prioritized findings, and explicit truncation/staleness markers.
Plain output is deterministic; JSON output is schema-versioned. The model sees
only the selected bounded evidence, not every historical diff on every turn.

Canonical JSONL and Seahorse remain conversation authorities. Repository
baseline and evidence files are thread-owned supporting state, not chat
messages and not Seahorse databases. Compaction summaries may reference a
baseline ID or completed review ID, but later status/diff requests refresh from
the authoritative repository and baseline.

## Failure and deletion behavior

Missing Git, non-repositories, unborn or detached HEAD, linked worktrees,
conflicts, missing refs, binary files, submodules, sparse checkout, partial
clone objects, replaced paths, concurrent external edits, timeout,
cancellation, and output limits all have explicit bounded outcomes.

Thread trash/archive follows the existing thread lifecycle. Permanent
thread-state garbage collection may delete its baseline and cached review
evidence only when that thread's recoverable authority is being deleted. It
never deletes project content, Git objects, refs, indexes, or state shared by
another thread.

## Admitted implementation packets

P6.4 implementation should proceed through focused dependent packets:

1. reusable passive repository evidence and typed bounded status/diff;
2. durable baseline ownership and truthful provenance comparison;
3. model/headless/TUI status and diff integration;
4. typed read-only review lifecycle and structured findings;
5. restart, fork, compaction, large-repository, race, and platform closeout;
6. docs-only evidence and roadmap exit record.

Each implementation packet requires focused tests, changed-package lint,
relevant tagged and race tests, cross-platform compilation, exact-head review,
green CI, owner rocket approval, and a merge commit. P6.5 structured code
intelligence and all later roadmap work remain out of scope.
