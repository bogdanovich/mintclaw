# Local Coding Agent P1.1 Thread Metadata and Project Identity

Status: implemented

Roadmap packet: P1.1 in
[`local-coding-agent-roadmap.md`](local-coding-agent-roadmap.md)

## Decision

MintClaw admits `pkg/coding/thread` as the persistence-neutral domain boundary
for durable local coding threads. It owns:

- schema-versioned, transcript-independent `Metadata`;
- canonical project observation and matching;
- locally derived title and preview text;
- a direct-addressable per-thread state root; and
- atomic metadata save/load.

It does not own JSONL appends, catalogue scans, writer leases, CLI selection,
or the terminal frontend. Those remain P1.2 through P1.4 responsibilities.
Neither this package nor its project resolver imports the agent loop or TUI.

## State layout

The admitted coding state layout is:

```text
<coding-state-root>/
  threads/
    <thread-uuid>/
      thread.meta.json
      sessions/                 canonical JSONL, opened by RuntimeLayout
      context/
        seahorse.db             derived and disposable
      thread.lock               reserved for P1.3
```

`Store.ThreadRoot` returns `threads/<uuid>` as the owner-scoped state root for
the existing strict `RuntimeLayout`. The UUID is validated before path joining,
so an explicit ID cannot escape the store. Direct addressing avoids a full
catalogue scan for `resume <id>` and gives each thread its own JSONL and
Seahorse database. No global coding SQLite file is introduced.

Constructing `Store` is side-effect free. The first atomic save creates the
external directory. Coding state-root selection remains a P1.4 responsibility;
the store does not infer it from the source checkout.

## Metadata schema

Schema version 1 records:

- canonical thread UUID and the exact `coding:<uuid>` transcript session key;
- UTC creation and update timestamps;
- bounded title, preview, lifecycle status, model, and provider selection;
- a complete `ProjectIdentity` snapshot;
- optional parent-thread identity; and
- an optional timestamped compaction revision.

Unknown version-1 fields, non-canonical UUIDs, mismatched session keys,
impossible timestamps, invalid UTF-8, oversized display/selection fields, and
invalid project observations are rejected. One metadata file is capped at 32
KiB before catalogue work begins. Future schema changes must increment the
version and add an explicit migration; silently interpreting a different
schema as version 1 is not admitted.

Before the first replacement, `fileutil.MkdirAllDurable` creates the complete
owner-only `0700` directory chain from the nearest existing canonical ancestor
and syncs each new parent entry. `fileutil.WriteFileAtomic` then supplies
temp-file write, mode application, fsync, atomic replacement, and final-parent
sync. Metadata is owner-only `0600`. A committed-write error from either
directory or file durability remains classified through `Store.Save`; callers
must not retry as though no filesystem change occurred.

## Display derivation

The first accepted, non-empty user request produces title and preview locally:

1. Unicode whitespace is collapsed to single spaces.
2. The title is bounded to 80 UTF-8 bytes.
3. The preview is bounded to 240 UTF-8 bytes.
4. Truncation preserves valid UTF-8 and adds an ellipsis.

There is no provider request, heuristic model call, or later transcript load.
P1.4 must persist this descriptor only after the initial request has passed its
normal acceptance boundary.

## Project identity

`ResolveProject` first makes the invocation cwd absolute, resolves symlinks,
and requires an existing directory. It then performs read-only Git commands
with a stable C locale.

For a Git worktree it records:

- canonical invocation cwd;
- canonical worktree root;
- canonical Git common directory;
- safely classified, credential-stripped `origin`, when configured;
- branch, empty when detached; and
- HEAD object ID, empty for an unborn branch.

For a non-Git directory, both project root and invocation cwd are the canonical
cwd and all Git fields are empty.

Absolute and scheme-relative URLs lose userinfo, query, and fragment. SCP-like
remotes lose the user component. Safe local paths remain intact; malformed or
credential-bearing values that cannot be classified are omitted rather than
persisted verbatim.

The `project_key` is a typed SHA-256 digest of the canonical project root. The
type separates a plain directory from a Git worktree at the same path. A linked
Git worktree has its own key even though it shares `git_common_dir` with the
main worktree. Branch, HEAD, and remote changes do not change the project key;
they are restart/resume observations, not project ownership.

Symlink aliases resolve to the same key. Moving a directory changes its key.
This path-based behavior is deliberate: filesystem paths are execution
authority, and remote URL or common-directory resemblance is insufficient to
silently grant a thread authority over a new path.

If Git is not installed, a directory remains a valid non-Git project. If Git
is installed and a repository command fails for a reason other than the
specific optional observation being absent, resolution fails instead of
silently downgrading a broken repository to a plain directory.

## Resume-time location states

`InspectLocation` returns one explicit state without mutating metadata:

| State | Meaning |
| --- | --- |
| `available` | Persisted root and invocation cwd resolve to the same project key |
| `missing` | Persisted root or invocation cwd no longer exists |
| `moved` | The persisted path now resolves to a different identity, or a caller supplied a candidate after the old location disappeared |
| `mismatch` | The persisted location exists but an explicit candidate belongs to another project |

A candidate in `moved` is diagnostic only. P1.4 may offer an explicit migration
or warning, but it cannot replace persisted identity merely because a remote,
repository name, or HEAD appears similar.

## Evidence

Focused tests prove:

- construction is side-effect free and save/load round-trips the complete
  descriptor atomically with `0600` mode;
- first-save directory creation durably syncs each parent and preserves
  committed-write classification when a directory sync cannot be confirmed;
- an injected pre-commit failure leaves the previous descriptor readable;
- unknown, substituted, malformed, and oversized descriptors fail closed;
- title and preview normalization remains valid UTF-8 at their byte bounds;
- direct and symlinked cwd resolution produces identical restart-stable keys;
- Git root, common directory, sanitized origin, branch, and HEAD are observed;
- absolute, scheme-relative, SCP-like, local, and malformed remote forms obey
  the credential-redaction contract;
- unborn and detached repositories have explicit empty ref observations;
- linked worktrees share a common directory but have different project keys;
- available, missing, moved, and mismatched locations remain distinct; and
- deleting only the recorded invocation subdirectory is reported as missing
  instead of becoming a different cwd implicitly.

P1.2 can now scan only bounded `thread.meta.json` files, isolate corrupt
entries, and filter by `project_key`. P1.3 can place its OS-backed writer lease
at the reserved per-thread root. P1.4 can bind `Store.ThreadRoot` directly into
the existing coding `RuntimeLayout`.
