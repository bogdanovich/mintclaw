# Local Coding Agent P7.3 Exit Record

Roadmap packet: [P7.3 — Multi-agent coding and worktrees](local-coding-agent-roadmap.md#p73--multi-agent-coding-and-worktrees).

The merge containing this record closes P7.3. MintClaw now has a
transport-neutral repository-mutation boundary that gives each asynchronous
mutating coding task one dedicated linked Git worktree, branch, execution
root, and exclusive owner lease. The task-scoped P7.2 worker can run only
while that exact ownership remains live, and terminal repository state is
captured as durable, bounded handoff evidence before ownership is released.

Read-only investigation continues to use the configured source project under
its immutable tool profile. Local `mintclaw code`, `mintclaw resume`, and
`mintclaw code exec` remain explicit-current-directory foreground commands;
they do not allocate or clean background worktrees.

## Merged implementation

| Boundary | Evidence | Result |
| --- | --- | --- |
| Admission | [#1165](https://github.com/bogdanovich/mintclaw/pull/1165), merge `55bffca3` | Defined the one-owner lifecycle, task modes, identity boundaries, non-destructive Git policy, validation matrix, and P7.4 exclusions. |
| Allocation authority | [#1167](https://github.com/bogdanovich/mintclaw/pull/1167), merge `f8316dfb` | Added `pkg/coding/worktree`, deterministic linked-worktree and branch allocation, schema-v2 durable identity, cross-process owner leases, idempotent preparation recovery, and fail-closed reconciliation. |
| Worker composition and handoff | [#1171](https://github.com/bogdanovich/mintclaw/pull/1171), merge `31233f29` | Bound mutation workers to the exact ready allocation and owner generation, retained authority through process finalization, and persisted digest-bound branch/change/conflict handoff evidence. |
| Conservative cleanup and platform proof | [#1172](https://github.com/bogdanovich/mintclaw/pull/1172), merge `29a8cd45` | Added owner-held cleanup, atomic directory-object claims on Linux/macOS, replacement-safe recovery, normal non-force Git removal, conservative branch release, and real native-worker mutation/crash/recovery coverage. |

## Closed ownership contract

The P7.3 allocation manager is local and transport-neutral. The supervisor,
not the model, supplies the resolved source project, full base object ID,
task and generation IDs, native thread ID, and operator-selected worktree
parent. The manager derives the worktree ID, direct execution-root child, and
branch. It never accepts a model-authored absolute path, repository URL,
branch, moving ref, or cleanup policy.

One allocation durably binds:

- task, task generation, native thread, source project, base object, branch,
  worktree parent, and execution-root identities;
- platform directory-object identity for the source Git common directory and
  created execution root;
- the actual linked-worktree project identity and invocation-directory
  mapping; and
- lifecycle state, latest handoff ID, and retention or recovery reason.

The held OS `owner.lock` is the exclusive mutation authority. Its diagnostic
generation, PID, host, and timestamp never substitute for the kernel lock.
`catalog.lock` separately serializes allocation and cleanup state changes.
Two tasks may allocate distinct worktrees from one repository, but two live
workers cannot own the same allocation.

The state root remains outside both the source and linked checkouts:

```text
<coding-state>/worktrees/
  catalog.lock
  allocations/<worktree-id>/
    allocation.json
    handoff.json
    owner.lock
```

Schema v2 deliberately rejects rather than migrates schema-v1 records: P7.3
had no deployed records or external users when the directory-object identity
contract was introduced.

## Worker and handoff semantics

An investigation worker keeps the source execution root. A mutation worker
must use a distinct linked-worktree execution root and pass matching checks
for allocation, source/common-directory identity, execution-root file
identity, branch, task, thread, and worker generation in both the trusted
launcher and private worker. Ordinary `Launch` rejects mutation bindings;
`LaunchOwned` holds the owner lifecycle through authenticated child exit,
crash or cancellation, terminal capture, and durable release.

If launch or finalization cannot persist terminal state, the owner remains
held behind a typed retry-capable finalization handle. A later worker cannot
enter a still-ambiguous checkout merely because the previous child exited.
After a proven release, an explicit successor generation may reacquire the
same retained allocation; neither allocation nor worker recovery blindly
replays the prior task turn.

The digest-bound `handoff.json` reports the accepted base, current branch and
HEAD, ahead/behind observations, bounded staged, unstaged, untracked and
unmerged paths, and merge, rebase, cherry-pick, revert or bisect markers. It
classifies the result as `ready`, `changes`, `conflicted`, `missing`,
`mismatch`, or `uncertain`. Loading checks the durable allocation's current
handoff ID and immutable identity, so a valid-looking older or replaced file
is not accepted. P7.3 reports conflicts and uncertainty; it never resolves,
aborts, rebases, merges, pushes, or publishes them.

## Conservative cleanup

Cleanup is an explicit owner operation bound to the latest durable handoff.
It first refreshes repository evidence and refuses deletion for changes,
commits, untracked or ignored data, unsafe index flags, an in-progress Git
operation, incomplete output, an active owner, source or target replacement,
catalog ambiguity, a locked worktree, or any identity mismatch.

On Linux and macOS, the final destructive boundary opens and validates the
allocated directory and atomically renames that exact direct child to a
no-replace `.cleanup` claim while retaining its handle. Under catalogue
coordination it then validates the held object again, repairs and verifies the
exact Git registration, and rebuilds all clean evidence at the claimed path.
Only then does it invoke normal `git worktree remove` without `--force`.
Unsupported platforms retain the worktree instead.

Durable `cleanup_pending` state reconciles original-only, claimed-only, and
proven-absent crash states without repeating an unproven destructive effect.
There is no recursive filesystem fallback. The MintClaw branch is deleted
only by exact expected-value update when it still equals the accepted base and
no worktree uses it; committed, reattached, replaced, or ambiguous branches
remain. Allocation and latest handoff records remain as recovery evidence.

## Done-criteria evidence

| P7.3 criterion | Evidence |
| --- | --- |
| Every asynchronous mutation has one explicit execution root and transcript owner | #1167 derives one allocation from the native thread identity; #1171 requires that allocation's live owner generation while the existing P7.2 thread writer lease continues to own the transcript. Investigation remains source-root and read-only. |
| Concurrent mutation of one execution root is excluded | #1167 tests same-process and real cross-process owner contention, successor acquisition, conflicting idempotency, and distinct concurrent allocations from one source repository. #1171 authenticates the live owner at both launcher and worker boundaries. |
| Restart and successor recovery do not duplicate or replay work | #1167 reconciles interrupted preparation and adopts only exact Git evidence without creating a second worktree. #1171 retains ownership on incomplete finalization and exposes an explicit retry handle. #1172's native crash test reacquires the retained allocation with a successor and observes no blind replay. |
| Handoff exposes branch, base, HEAD, changes, conflicts, and uncertainty | #1171 covers clean and committed heads; staged, unstaged, untracked, unmerged and in-progress-operation states; bounded/incomplete observations; changing HEAD; replaced or replayed evidence; and digest/allocation binding. No operation resolves or publishes Git state. |
| Cleanup cannot delete user work and retains recovery evidence | #1172 covers dirty and committed work, ignored files, hidden tracked-index flags, source and execution-root replacement, post-open swaps, branch reattachment, failed removal, incomplete evidence, catalog loss, crash at the cleanup claim, and last-handoff retention. It uses no force or recursive deletion. |
| Linux and macOS exercise the real Git and worker boundary | #1172 extends the production binary integration through isolated mutation, crash handoff, successor resume, and cleanup/recovery. The same integration script passed Linux Integration Tests and macOS Portability; filesystem primitives also compile on Darwin, Linux, and Windows. |
| Existing foreground and one-shot behavior remains unchanged | P7.3 adds no public command and does not compose allocation into the foreground TUI or `code exec`. Full CI continued to exercise the existing coding frontends while the private owned-worker path ran independently. |

## Validation and review gates

Each implementation merge head passed all ten repository checks: frontend,
lint, security, full tests, race, Darwin ARM64 and Windows AMD64 compilation,
macOS portability, Linux integration, and Browser Windows. Local focused
validation additionally included:

- repeated real-Git allocation, same-source concurrency, idempotency,
  cross-process owner exclusion, symlink/path replacement, dirty/detached
  source, interrupted preparation, and race tests;
- complete `pkg/coding/...` tests plus worktree and worker-process race runs;
- exact mutation launcher/worker binding, cancellation, crash, pending
  finalization, successor generation, and durable handoff tests;
- repeated ignored-file, unsafe-index-flag, same-path replacement, atomic
  post-open swap, failed-removal, branch-reattachment, and cleanup-claim crash
  regressions; and
- real MintClaw binary mutation/crash/recovery on macOS and the equivalent
  integration-tagged Linux CI path, plus Darwin/Linux/Windows compile checks.

Automated review was clean on each exact implementation head:

- [#1167 allocation review](https://github.com/bogdanovich/mintclaw/pull/1167#issuecomment-5644821731) at `887071e2`, followed by owner 🚀;
- [#1171 worker/handoff review](https://github.com/bogdanovich/mintclaw/pull/1171#issuecomment-5654568565) at `48a4b9e5`, followed by owner 🚀; and
- [#1172 cleanup review](https://github.com/bogdanovich/mintclaw/pull/1172#issuecomment-5646472905) at `fc0d9052`, followed by owner 🚀.

Repeated safety findings triggered architecture checkpoints rather than an
unbounded patch loop. The worker boundary checkpoints retained one OS lock,
one allocation state machine, and one reachable lifecycle/finalization owner.
The cleanup [cycle-seven scope checkpoint](https://github.com/bogdanovich/mintclaw/pull/1172#issuecomment-5646283817)
and [growth checkpoint](https://github.com/bogdanovich/mintclaw/pull/1172#issuecomment-5646333727)
confirmed that the larger diff remained one cohesive destructive-safety
boundary and that growth was primarily targeted regression coverage.

## What is available now

P7.3 is an internal supervisor boundary, not a new CLI workflow. Developers
can compose `pkg/coding/worktree.Manager`, an acquired `Owner`, and
`workerprocess.LaunchOwned` to run one mutation worker in an isolated checkout
and receive an `OwnedResult` with terminal handoff evidence. Cleanup remains a
separate explicit owner decision so callers can retain any result for
inspection or continuation.

Operators should continue using the existing foreground commands:

```text
mintclaw code "Inspect this repository"
mintclaw resume
mintclaw code exec "Inspect this repository"
mintclaw code exec resume <thread-id> "Continue the task"
```

## Next boundary

P7.4 is next. It may admit the complete channel-to-coding vertical slice:
operator-registered project aliases and task modes, local or paired-node
supervision, typed task start/status/steer/cancel/result commands, gateway
tooling, durable interaction and delivery, and a Telegram canary.

P7.3 does not fetch, push, rebase, merge, open or merge pull requests, resolve
conflicts, accept unrestricted model-authored paths, dispatch from Telegram or
Node Companion, expose a daemon/listener, or remove ACPX/Codex compatibility.
Those remain P7.4 or later decisions and require their own admission.
