# Node Companion P8b Remote Coding Checkpoint

Status: architecture contract recorded; implementation is not admitted until
the readiness gate in this document is satisfied on merged main.

This checkpoint reconciles P8 in the
[Node Companion roadmap](node-companion-roadmap.md) with P7.4 in the
[Local Coding Agent roadmap](local-coding-agent-roadmap.md). It deliberately
does not create a second remote-coding program. Repository-owning remote work
is one local-coding `CodingTask` executed by one project-bound worker on a
paired node, not an extension of P8a file routing or P5a process jobs.

## Decision

P8b is the Node Companion integration portion of Local Coding Agent P7.4. It
is not an independently implementable milestone.

The eventual model-facing path is:

```text
authenticated channel request
        |
        v
gateway-owned CodingTask + delivery + interaction
        |
        v
existing target policy and gateway invocation coordinator
        |
        v
existing authenticated WSS and companion invocation ledger
        |
        v
node-local project alias and coding-task policy
        |
        v
one native CodingThread + one isolated worktree + one coding worker
        |
        v
typed status/question/result -> gateway task projection -> one delivery
```

There is no separate P8b task store, transcript, provider adapter, worktree
manager, transport, or delivery system. When implementation becomes ready,
the P7.4 admission owns the complete vertical slice and cites this checkpoint
for the node boundary.

## Why implementation stops here

Merged main currently provides useful foundations:

- versioned `CodingThread` metadata, canonical project identity, bounded
  catalogue, writer lease, transcript, workspace snapshots, project
  instructions, coding tool profile, and durable turn/tool records;
- the shared task registry, typed progress, waiting-for-input projection,
  `DeliverableReport`, delivery coordinator, and durable interaction manager;
- Node Companion target policy, authenticated WSS, gateway and companion
  invocation ledgers, status/cancel recovery, no-replay semantics, P2
  artifacts, P5a jobs, and P8a explicit workspace routing.

The parts that would make remote coding truthful do not yet exist on main:

| Required owner | Missing merged prerequisite |
| --- | --- |
| Local coding runtime | P7.1 stable non-interactive new/resume execution and machine events |
| Worker lifecycle | P7.2 decision and implementation for start/resume/status/steer/cancel independent of TUI state |
| Repository isolation | P7.3 isolated worktree allocation, writer ownership, safe cleanup, and conflict handoff |
| Durable coding task | a typed task-to-thread/worktree binding and terminal deliverable projection |
| Node coding host | project alias catalogue, node-local coding policy, worker adapter, and accepted-task reconciliation |

There is no `CodingTask` type or coding worker command family, and current
thread status is only active/archived. The `mintclaw code` frontend is not yet
a supervised headless worker that can be resumed after a companion restart.
Implementing P8b now would therefore either duplicate the active local-coding
program or approximate autonomous coding with P8a reads/writes and P5a shell
jobs. Both outcomes violate the roadmaps.

## Readiness gate

P8b implementation remains forbidden until all of these are merged and
evidenced:

1. P7.1 exposes one stable non-interactive coding execution contract for new
   and resumed native threads, with typed events and cancellation outcomes.
2. P7.2 selects one worker boundary that supports start, resume, status,
   steer, cancel, crash recovery, and upgrade without depending on a TUI.
3. P7.3 owns isolated worktree creation, branch identity, exclusive writer
   lease, dirty/conflict handoff, cancellation, and non-destructive cleanup.
4. The canonical `CodingThread` lifecycle can represent running,
   waiting-for-input, completed, failed, cancelled, and uncertain worker
   observations without treating frontend state as authority.
5. The existing task registry and interaction manager can project a coding
   task without adding a second task or question store.
6. Linux and macOS can run the same selected worker boundary and worktree
   contract in focused real-process tests.

The readiness review must cite exact merged PRs, packages, tests, and retained
limitations. A branch, draft PR, or roadmap proposal does not satisfy a gate.
Once the gate passes, update this status through a new docs-only P7.4
admission before writing node commands.

## Non-redundant capability model

The three nearby features retain different owners:

| Capability | Reasoning/transcript owner | Execution owner | Use |
| --- | --- | --- | --- |
| P8a remote workspace | live gateway agent | individual typed node invocation | bounded read/search/write/patch/exec in one turn |
| P5a durable job | calling agent/thread | one node-local OS process | long build/test with logs, cancellation, artifacts |
| P8b/P7.4 coding task | remote native `CodingThread` | project-bound coding worker and isolated worktree | autonomous repository investigation or mutation across many model/tool turns |

A coding worker may start a P5a job for a long test and may return P2 artifact
references. Those remain linked child operations; neither becomes the coding
task transcript or worktree authority. P8a remains useful for small explicit
file operations and must not grow sticky reasoning state.

## Frozen future contract

The remaining sections define the boundary the eventual P7.4 admission must
preserve. They do not authorize implementation before the readiness gate.

### Objective and first slice

An owner-authorized live agent can start one read-only investigation or one
isolated-worktree mutation on one configured project alias at one paired
target, inspect status, answer one or more bounded questions, steer or cancel,
and receive one deduplicated final report.

The first slice supports an already-paired Linux or macOS development machine.
It does not bootstrap MintClaw, install dependencies, choose arbitrary
repositories, or repair a missing coding runtime.

### Ownership and identities

One task binds these immutable identities before dispatch:

- gateway `CodingTaskID`, generation, requester route, channel, topic,
  authenticated actor, routed session/epoch, and selected agent;
- provider tool-call/execution identity and canonical objective/done-criteria
  hash;
- configured target alias, node identity, project alias and revision, task
  mode, worker protocol revision, and approved descriptor revision;
- gateway invocation ID and idempotency key;
- after node acceptance, node task ID, native coding thread ID, project
  identity, worktree ID/root identity, branch/HEAD baseline, and writer-lease
  generation.

The existing task registry is the gateway authority for requester lifecycle,
interaction projection, progress, result delivery, and retention. The gateway
invocation store is dispatch authority. The companion ledger is accepted-node
execution authority. `CodingThread` files are transcript/context authority.
The isolated-worktree owner is repository mutation authority. No one store
pretends to own all four layers.

### Project alias and authorization

The gateway config exposes only an operator-owned remote coding alias that
selects an existing allowed target plus a node-local project alias. The model
may select:

- that combined alias;
- `investigate` or `mutate`, when the configured grant permits it;
- a bounded objective and done criteria; and
- bounded attachment/artifact references already authorized to the route.

It cannot select a node ID, connection, absolute path, repository URL,
worktree root, branch prefix, provider credential, executable, environment,
service, policy revision, approval answer, or cleanup behavior.

The node owns a private project catalogue. Each alias pins a canonical
repository identity, allowed task modes, worker profile, worktree parent,
branch policy, artifact bounds, resource/time limits, and policy revision.
Resolution rejects symlinks, path replacement, ambiguous Git identity, stale
revision, forbidden dirty state, and projects outside admitted roots.

Effective authority is the intersection of authenticated sender/route,
selected agent target policy, configured coding alias, current node discovery
and descriptor, gateway approval policy, node-local project/task policy,
operating-system user, and exact retained task/invocation identity. Missing or
changed authority fails closed. There is no local/P8a/P5a fallback.

### Provider and credential boundary

The gateway sends objective, done criteria, bounded attachments, task mode,
and immutable identities. It never forwards its channel history, general
agent memory, provider API keys, OAuth material, MCP credentials, or full
gateway configuration.

The selected coding worker uses the provider/model policy already admitted
for native coding on that machine. Credential references are resolved
node-locally from operator configuration and are not model-selectable or
returned through status, events, traces, artifacts, or deliverables. A future
credential-broker design requires separate admission; P8b does not invent
one.

### Durable task, thread, and worktree lifecycle

The gateway persists a queued `CodingTask` before preparing the node
invocation. The node atomically accepts the stable task identity or returns
the already accepted identity. Only the node creates the native coding thread
and execution root.

For `investigate`, the configured project may be observed read-only and no
write-capable tool profile is installed. For `mutate`, the worker gets a new
isolated worktree and branch derived from node policy, never a caller path.
Exactly one worker generation owns the thread writer lease and worktree.

The lifecycle projection is:

```text
queued -> dispatching -> running <-> waiting_for_input
                              |
                              +-> completed | failed | cancelled | uncertain
```

These are task/worker observations, not a replacement for transcript records
or companion invocation states. A completed/paused thread becomes visible to
local `mintclaw resume` only after the remote worker releases its writer lease.
A local TUI may observe a running thread read-only but cannot become a second
writer.

Cleanup is conservative. Terminal retention may release an empty disposable
worktree after its report/patch/branch references are durable, but never
deletes uncommitted changes, an unknown worktree, a user-owned worktree, or the
last recovery evidence. Cleanup failure is reported and retried by the
node-local owner, not hidden by gateway task cleanup.

### Dispatch, retries, restart, and unknown outcomes

The gateway creates one task and one prepared invocation before any approval
or WSS send. Approval continuation reuses the exact task, argument hash,
target/project revisions, and prepared invocation.

The node acceptance transaction maps one gateway task generation to one node
task, coding thread, and worktree generation before starting the worker.
Duplicate start returns that mapping. A conflicting objective, mode, project,
or generation is rejected.

Before durable node acceptance, denial is a proven non-effect. After dispatch,
timeout or disconnect is uncertain until status proves whether acceptance
occurred. Gateway restart reconciles the original invocation and task; node
restart reconciles the accepted task, lease, thread, worktree, and worker
process. Neither side automatically starts a second coding task or worktree.

The first slice uses bounded status polling over the existing request/response
WSS command path. It does not require a new streaming transport. Semantic
progress is projected into existing task events and delivery; polling gaps do
not affect authority.

### Questions, steering, and cancellation

A worker can expose one bounded, versioned question at a time through task
status. The gateway creates an existing durable question interaction bound to
task, thread, worker generation, question ID/revision, actor, route, and
session. Only an authorized answer from that route can be consumed once.

The answer is sent as a typed steer operation carrying the retained question
identity. Free-form channel text that is not correlated through the interaction
manager is ordinary steering or a new turn, not approval or an answer.

General steering is append-only, bounded, ordered, and idempotent by steer ID.
It never mutates the original objective or grants broader policy. Cancellation
is an idempotent request: before node acceptance it prevents dispatch; after
acceptance it asks the exact worker generation to stop. A cancellation race
may end as completed, cancelled, or uncertain according to node evidence. It
does not delete the thread/worktree or claim that external effects were
rolled back.

### Typed node surface

The future admission may add only the minimum versioned commands required by
the selected worker boundary:

- `coding.projects.v1` for bounded allowed-alias discovery;
- `coding.task.start.v1`;
- `coding.task.status.v1` including bounded semantic progress and question;
- `coding.task.steer.v1`;
- `coding.task.cancel.v1`; and
- `coding.task.result.v1` only if terminal status cannot carry the bounded
  report/reference safely.

These commands use the existing target descriptor, gateway invocation plan,
WSS, companion ledger, output-schema validation, cancellation, and status
recovery. They are not a generic agent RPC, arbitrary tool proxy, shell
wrapper, or second node transport.

### Result, artifacts, and PR handoff

The final `DeliverableReport` is bounded and typed. It includes task/thread,
target/project aliases, mode, summary, changed relative paths, validation
commands as redacted facts and outcomes, branch/commit/patch references,
approved artifact references, unresolved issues, cleanup state, and terminal
outcome. It does not include unrestricted diffs, logs, prompts, reasoning,
absolute paths, credentials, environment, or repository secrets.

The worker may create a commit, branch, patch artifact, or PR only when the
node-local project profile explicitly admits that publication mode. GitHub or
other forge credentials remain node-local. The model cannot select a remote,
base branch, fork, reviewer, merge mode, or credential. Creating a PR is a
separately visible mutating step; task completion never implies merge.

### Redaction, retention, and operations

Gateway events contain task/invocation IDs, safe aliases or hashes, state,
progress kind, counts, durations, truncation, question lifecycle, and fixed
error codes. Node events add worker/thread/worktree opaque IDs and lifecycle.
They exclude objectives beyond bounded approved summaries, prompts,
reasoning, file contents, diffs, commands, logs, absolute paths, provider
payloads, credentials, and connection details.

The existing task registry retains requester lifecycle and delivery. The
interaction registry retains questions. The gateway and companion ledgers
retain dispatch/recovery. Coding thread retention follows the native coding
contract; worktree cleanup follows the node-local owner. Artifact retention
uses P2. P8b adds no audit database and no copy of the transcript in gateway
state.

Operations must expose redacted task/thread/worktree state, worker health,
retention/cleanup backlog, and exact recovery action. Backups preserve thread
metadata/transcript, accepted coding-task mapping, project catalogue, and
worktree/branch recovery evidence as one version-aware set.

### Linux and macOS

The same logical contract applies to Linux amd64/arm64 and macOS amd64/arm64.
Platform adapters may differ for process supervision, signals, file locking,
and Git executable discovery, but task/thread/worktree identity, exclusive
writer behavior, cancellation truth, no replay, redaction, and result schema
must agree. Windows, mobile, containers, SSH, and privileged helpers are not
part of the first slice.

Configuration defaults to no remote coding aliases and no advertised coding
commands. Existing configs require no migration. Enabling a profile requires
an exact allowed target, current project alias/revision, allowed modes, worker
profile, resource bounds, approval policy, and node-local provider policy.

## Future validation matrix

| Area | Required evidence after readiness |
| --- | --- |
| Readiness | exact merged P7.1/P7.2/P7.3 packages and tests; no draft/branch-only dependency |
| Config | empty default; invalid alias/mode/project/provider policy; target-policy intersection |
| Ownership | wrong actor/agent/route/session/epoch/tool-call/task/generation/target/project denied |
| Project | symlink/replacement/ambiguous/dirty/detached/unborn/submodule/worktree boundaries |
| Modes | investigation cannot write; mutation gets one isolated execution root and branch |
| Dispatch | duplicate/concurrent start, approval continuation, changed args/policy, ambiguous commit |
| Restart | gateway/node/worker crash at every acceptance boundary; one task/thread/worktree only |
| Interaction | question delivery/answer authorization, duplicate/stale/late answer, sequential questions |
| Steering | ordered duplicate steering, steering/cancel/complete races, no authority expansion |
| Cancellation | pre-acceptance non-effect, running stop, late completion, uncertain process/effects |
| Result | bounded report, changes/validation/artifacts/PR reference, partial and unknown truth |
| Delivery | semantic progress bounds and one deduplicated final delivery across restart |
| Security | no arbitrary path/remote/provider/env/shell; node-local policy final; redacted evidence |
| Retention | active/waiting/unknown protected; safe terminal thread/artifact/worktree cleanup |
| Platforms | deterministic real-process Linux and macOS investigate, mutate, question, restart, cancel |
| Compatibility | P8a, P5a, local coding, gateway, and unrelated node/browser tools unchanged |

Run focused and race tests for touched coding thread/worker, task,
interaction, delivery, target policy, invocation, companion, WSS, artifact,
and worktree packages. Let required CI run the broad tagged suite. Tests must
assert durable states and identities, not timing alone.

## Future focused PR sequence

This sequence is illustrative and remains unauthorized until a new P7.4
admission confirms the readiness gate:

1. Node-local project catalogue and selected coding-worker adapter, reusing
   merged P7.1–P7.3 thread/worktree ownership; no gateway model tool.
2. Typed start/status/steer/cancel commands over existing companion ledger and
   WSS, with restart/no-replay tests; no delivery integration.
3. Gateway `CodingTask` model tools and composition with existing task,
   interaction, approval, invocation, and delivery authority.
4. Linux/macOS real-process question/restart/cancel/result proof, accurate
   docs/config, deny-by-default deployment, and one bounded operator canary.

Every PR starts from fresh merged main. Do not add a prerequisite PR under the
P8b label; missing coding foundations return to their owning local-coding
roadmap packet.

## Architecture checkpoints

Stop before more patching when a PR reaches four substantive review/fix
cycles, the same invariant is challenged three times, production scope doubles
from its ready baseline, or correctness starts requiring:

- a second task/thread/worktree/invocation/interaction/delivery store;
- a new transport, generic remote-agent RPC, generic worker framework, or
  model-selected provider/path/remote;
- reconstructing repository work from P8a calls or P5a shell logs;
- two transcript writers, gateway-owned remote cwd, or caller-managed
  worktree cleanup; or
- replay after uncertain task acceptance or repository mutation.

At a checkpoint, narrow, replace, or defer the PR. Do not grow another
foundation program inside review.

## Definition of Done and mandatory stop

P8b/P7.4 is complete only when one authorized channel request creates exactly
one gateway coding task, one node task, one native coding thread, and one
isolated worktree; the requester can inspect, answer, steer, and cancel it;
restart and disconnect recover without replay; the final bounded report and
artifacts are delivered once; local resume sees the released thread; Linux and
macOS proof passes; all admitted PRs are merged; docs match behavior; and the
deny-by-default deployment is healthy.

Immediately stop at that point. Do not continue into coding-to-companion P7.5,
P5b shell jobs, browser/MCP workspace routing, synchronization, fleet, P9,
bootstrap, additional platforms, generic remote agents, or deferred work.

For the current checkpoint, the mandatory action is earlier: merge this
docs-only decision and stop P8b implementation until the readiness gate is
met. Do not create a coding-worker or node-command PR from current main.
