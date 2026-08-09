# Node Companion P5a Durable Jobs Admission

## Status And Decision

Status: admitted for implementation as a bounded node capability

P5a admits durable management of one long-running operating-system process on
one already configured companion target. It adds job start, status, bounded
logs, cancellation, and declared regular-file artifacts without turning the
companion into an agent runtime, scheduler, workflow engine, or generic remote
workspace proxy.

This admission also fixes the relationship between three future-facing
features that otherwise risk overlapping:

- a **node job** owns the lifecycle and evidence for one OS process;
- a **remote workspace context** will later select where compatible tools run
  and which node-local working scope they share; and
- a **coding task** owns repository reasoning, a coding thread, and an isolated
  execution root on the machine that owns the checkout.

These are complementary layers, not competing implementations. P5a implements
only the first layer. Node Companion P8 remains unadmitted, and the remote
coding worker remains governed by the local coding-agent roadmap.

P5a reuses the current protocol, WSS session, target policy, discovery catalog,
execution plans, approval manager, gateway invocation store, companion
invocation ledger, node-local policy, transfer spool, runtime events, and
no-blind-replay semantics. It may add one bounded companion-side job store
because job state intentionally outlives the invocation that starts it. It
must not add another gateway task registry, transport, generic durable-tool
framework, or model-facing arbitrary RPC surface.

## Why This Slice Is Admitted

MintClaw can already start long commands in several ways, but none has the
required combination of properties:

- local `exec background=true` returns a process-local session that is lost on
  gateway restart;
- `shell.exec.v1` and `system.exec.v1` are synchronous node invocations whose
  result is bounded by the invocation lifetime;
- an owner can manually use `nohup`, PID files, log files, `tmux`, systemd, or
  launchd, but MintClaw cannot authoritatively correlate or cancel that work;
- the task registry tracks agent, delegate, cron, and tool work; it does not
  own an arbitrary process on a companion; and
- a coding thread owns model/tool history and repository work, not a detached
  process launched by another agent turn.

The deployed node foundation already supplies exact dispatch identity,
durable acceptance, status recovery, actor/session isolation, approval,
no-replay behavior, node-local execution policy, Linux process containment for
owner profiles, and bounded artifact transfer. A job lifecycle is therefore a
specific missing consumer, not evidence for a new distributed runtime.

OpenClaw's `exec` plus `process` surface is a useful interaction reference:
start, poll, log, write, and kill are easier for a model than manual PID files.
Its sessions are explicitly memory-only, however. P5a adopts the small
management surface, not the process-local durability model or its implicit
host routing.

## Whole-System Model

The authoritative ownership boundaries are:

| Domain | Owns | Does not own |
| --- | --- | --- |
| Agent turn | Reasoning and ordered model/tool calls | A process after job acceptance |
| Gateway task record | Parent task progress and final delivery | Node process truth or job logs |
| Node invocation | One prepared and dispatched typed command | The lifecycle of a job returned by `job.start.v1` |
| Node job | Process launch, state, bounded logs, cancel evidence, declared artifacts | Agent transcript, chat delivery, target selection |
| Remote workspace context | Future placement and shared working-scope binding | Process or coding-thread lifecycle |
| Coding task/thread | Repository reasoning, transcript, worktree, questions, and deliverable | Generic node job or arbitrary gateway session |
| Cron | When an agent or command should be requested | A currently running node process |

The intended composition is:

```text
live agent or future coding client
              |
              v
future execution context: target + working-scope alias
              |
       +------+------+----------------+
       |             |                |
       v             v                v
  file tools     foreground exec   durable jobs
                                      |
                                      v
                         node-local process lifecycle
                         logs + status + artifacts
```

A separate repository-owning operation uses a different path:

```text
live agent -> durable CodingTask -> paired coding worker
                                     |
                                     v
                         CodingThread + isolated worktree
```

The coding worker may eventually invoke node jobs for a bounded build or test,
but a node job never becomes a coding thread merely because its command is
`git`, `go`, or `make`.

## Concrete Operator Outcomes

The first useful P5a deployment supports these cases:

1. start a two-hour build, backup, data conversion, or test command on one
   configured Linux or macOS target and receive a stable job ID quickly;
2. end the initiating agent turn without canceling the accepted process;
3. inspect state and bounded stdout or stderr from a later turn or after a
   gateway restart;
4. request cancellation and distinguish confirmed process-domain termination
   from an unprovable or late cancellation;
5. list declared output artifacts and download an immutable bounded snapshot
   through the existing node transfer path; and
6. receive an explicit `unknown` or `interrupted` result rather than a false
   failure or automatic replay after companion/service loss.

The initial executor is direct argv derived from current `system.exec.v1`
authority and available on Linux and macOS. It may run an operator-allowlisted
executable script, but it does not interpret shell text or select another OS
identity.

A durable arbitrary owner-shell job is deliberately deferred. The current
Linux authority broker proves process-domain termination for one synchronous
shell invocation, but a background job would require the broker to own durable
job state, logs, and artifact access after the companion request returns.
Adding that second lifecycle owner is not a thin adapter. It requires a later
P5b admission based on evidence from P5a rather than review-time expansion of
this slice.

## Terminology

- **Job**: one accepted, non-interactive OS process execution with a stable
  job ID and durable node-local observation.
- **Start invocation**: the ordinary node invocation of `job.start.v1`. It is
  terminal once the job is durably running or has a terminal start failure.
- **Job profile**: operator-owned authority and limits for one job executor
  shape. A target binds to one profile; the model cannot invent one.
- **Payload**: the direct argv admitted by the bound profile.
- **Process domain**: the set of processes an executor can identify and, where
  supported, prove empty after cancellation.
- **Job log**: bounded stdout or stderr captured by the job runner. It is
  active content, not passive diagnostic evidence.
- **Declared artifact**: a named regular-file output declared before launch
  and snapshotted into bounded node-owned storage after a stable observation.
- **Remote workspace context**: a future gateway-side binding from an
  operator alias to a target, node working-scope alias, compatible tool groups,
  and policy revision. It is not `AgentInstance.Workspace`.
- **Coding task**: a durable request processed by a coding worker and one
  `CodingThread`; it may contain many tool calls and OS processes.

## Sources Of Truth And Authority

The job authority intersection is:

1. authenticated actor and routed conversation ownership;
2. gateway workspace, agent, session, turn, tool call, and execution identity;
3. the agent's operator-configured target grant;
4. the target's exact job-profile binding;
5. durable node pairing and the current approved catalog;
6. fresh model-safe command discovery;
7. the canonical prepared execution plan and any durable approval;
8. the node's current command and job-profile policy revisions;
9. the existing direct-exec policy selected by the job profile; and
10. the job store record created before a process launch boundary is crossed.

Possession of a target alias, job ID, artifact reference, discovery revision,
PID, path, or old log output grants no authority by itself. Job status, logs,
cancel, and artifacts recheck the requesting actor, agent, routed session,
target, and job ownership. Operator recovery tooling may be separately
authorized to inspect or stop orphaned work without granting that power to the
model.

The node job record is authoritative for job state. The gateway invocation
record remains authoritative for whether each typed command was prepared and
dispatched. A task-registry projection or runtime event is advisory and cannot
start, complete, cancel, retry, or delete a job.

## Job Profile And Configuration Contract

Defaults are deny-all. Upgrading a gateway or companion does not advertise job
commands until a node-local profile exists, the target binds to it, and the
command catalog is approved.

Illustrative configuration is:

```yaml
execution:
  targets:
    build-mac:
      driver: node
      node: paired-build-mac
      job_profile: project-builds

node_job_profiles:
  project-builds:
    enabled: false
    revision: project-builds-v1
    executor: system_exec
    timeout_seconds_max: 14400
    concurrent_jobs: 2
    stdout_bytes_max: 8388608
    stderr_bytes_max: 8388608
    artifact_count_max: 8
    artifact_bytes_max: 268435456
    retention_seconds: 86400
    cancel_guarantee: process_group
    approval:
      start: required
      read: none
      cancel: required
```

Exact field placement may change, but these semantics are fixed:

- target selection chooses one configured profile; model input does not carry
  a profile, executor, OS user, shell path, state path, or containment mode;
- a direct profile reuses current `system_exec` executable, working-scope, and
  environment authority instead of copying host paths into a new allowlist;
- runtime, concurrency, log, artifact, input, and retention limits have hard
  repository ceilings in addition to configuration limits;
- a profile declares its cancellation guarantee from a fixed vocabulary and
  cannot claim tree termination without executor proof;
- changed policy or safe projection changes descriptor/catalog identity and
  invalidates stale preparation; and
- missing or partially described authority is unavailable, never interpreted
  as unrestricted.

The first release supports only non-interactive direct argv jobs. Arbitrary
shell text, another OS identity, PTY input, detach and reattach, stdin
streaming, port forwarding, and terminal transcript retention remain part of
terminal or later job admissions.

## Typed Command Surface

P5a uses the existing `nodes`, `nodes_invoke`, and `nodes_status` model tools.
It does not add a parallel job transport or require a broad `jobs` gateway
service. The node advertises these typed commands only when its effective job
profile can implement them:

- `job.start.v1`;
- `job.status.v1`;
- `job.logs.v1`;
- `job.artifacts.v1`; and
- `job.cancel.v1`.

The existing `nodes_download` gains a mutually exclusive job-artifact source
form so bytes continue to use the P2 transfer protocol and gateway spool.
Artifact bytes never enter `nodes_invoke` JSON output.

### `job.start.v1`

The model-visible input is projected from the target's single bound profile.
A direct profile has a shape similar to:

```json
{
  "argv": ["build", "--release"],
  "cwd": "project",
  "env": {},
  "timeout_seconds": 7200,
  "artifacts": [
    {"name": "binary", "path": "dist/app"},
    {"name": "report", "path": "reports/tests.json"}
  ]
}
```

The first argv element and `cwd` are model-safe aliases resolved by existing
node-local policy. The model never supplies an executable path, shell path,
UID, GID, root flag, absolute artifact destination, log destination, or
backgrounding primitive. An operator may expose a bounded executable script
as an argv alias; its shebang and OS identity remain node-local configuration,
not model input.

Artifact declarations are optional, bounded, unique names plus normalized
relative regular-file paths beneath the resolved working scope. The initial
version supports no glob, directory, symlink, hardlink, device, FIFO, socket,
archive extraction, or path outside that scope. Product profiles may replace
model-authored relative paths with operator-authored artifact aliases.

The start result is bounded:

```json
{
  "job_id": "job_opaque",
  "state": "running",
  "started_at": 1780000000,
  "timeout_at": 1780007200,
  "cancel_guarantee": "process_domain"
}
```

It contains no PID, raw path, shell, environment, cgroup, service label,
broker address, or command echo.

### `job.status.v1`

Input contains only the job ID. The bounded result reports state, timestamps,
exit code or signal when proven, truncation flags, artifact count, fixed safe
error/recovery codes, and cancellation evidence. It does not include log or
artifact bytes.

### `job.logs.v1`

Input contains job ID, `stdout` or `stderr`, an opaque or numeric cursor, and a
bounded byte/line limit. Output contains one ordered chunk, next cursor,
available/truncated facts, and terminal state. Repeating the same cursor is
read-only and deterministic for retained bytes.

Logs are potentially secret-bearing active content. They may be returned only
to the exact authorized requester through the tool result. Passive events,
approval prompts, diagnostics, reviewer traces, and deployment evidence retain
only byte counts, cursors, truncation, and fixed codes.

### `job.artifacts.v1` and `nodes_download`

`job.artifacts.v1` lists only retained artifact name, opaque reference, size,
digest, content type when safely known, and availability. The opaque reference
is owner- and target-bound and is not a filesystem path.

`nodes_download` accepts either its existing regular-file path input or a job
artifact reference, never both. Job artifact transfer reuses the existing
chunking, digest, transfer ledger, gateway spool, delivery, cancellation,
retention, ownership, and no-replay behavior. A missing, expired, changed, or
wrong-owner reference fails closed before bytes are exposed.

### `job.cancel.v1`

Input contains only the job ID. Cancellation is a separate idempotent mutation,
not cancellation of the already completed `job.start.v1` invocation.

The result distinguishes:

- `cancel_requested`: the request was durably recorded;
- `canceled`: the executor proved the admitted process domain is empty;
- `cancel_unknown`: a signal or request crossed the boundary but complete
  termination cannot be proven;
- `already_terminal`: no process mutation occurred; and
- `unavailable`: policy or ownership did not authorize the request.

The direct executor may advertise only the guarantee its platform can prove.
A process-group signal is not proof that a hostile descendant did not escape.
The initial direct executor does not claim a stronger process-tree guarantee
than it can prove. The implementation never sends a signal to a recycled PID
based only on a persisted number.

## Durable Lifecycle And Start Boundary

The fixed job states are:

```text
accepted -> launch_attempted -> running
                              -> succeeded
                              -> failed
                              -> cancel_requested -> canceled
                                                   -> cancel_unknown
                              -> timed_out
                              -> unknown

accepted -> failed_before_launch
```

`accepted`, `launch_attempted`, and every later transition are durably written
and fsynced before the corresponding externally visible claim. The process is
launched at most once for a job ID. A duplicate exact `job.start.v1` returns
the existing job observation; changed input under the same invocation or
idempotency identity is a conflict.

There is an unavoidable boundary between persisting intent and asking the OS
to create a process. P5a handles it pessimistically:

- before `launch_attempted`, a proven local preparation failure can become
  `failed_before_launch`;
- after `launch_attempted`, a crash or lost proof never causes an automatic
  second launch; and
- if the node cannot prove whether launch occurred, the job is `unknown`, not
  failed or safe to retry.

The start invocation returns only after the job store proves `running` or a
terminal start outcome. If its response is lost, the normal companion
invocation ledger and `nodes_status` recover the same job ID or explicit
uncertainty. The gateway never synthesizes a new start plan from a status
failure.

Once `running` is committed, ordinary turn cancellation, steering, `/stop`, a
closed chat request, gateway restart, or WSS reconnect does not cancel the job.
Only timeout, an authorized `job.cancel.v1`, executor failure, or node/service
loss changes its process lifecycle.

## Restart And Disconnect Truth

P5a deliberately separates durable evidence from process survivability:

- gateway restart and WSS disconnect must preserve a running job on a live
  companion and allow later status/log/artifact access;
- graceful companion shutdown attempts bounded cleanup but does not report a
  stronger cancellation guarantee than the executor provides;
- after companion process restart, service restart, node update, or host
  reboot, any previously nonterminal job is reconciled to `unknown` or
  `interrupted` unless independently authenticated executor evidence proves a
  terminal result;
- the initial release does not reattach to a PID, relaunch a process, or claim
  that work survives companion/service or host restart; and
- retained logs and already committed artifact snapshots may remain available
  even when process outcome is unknown.

This is still durable job management: identity, prior transitions, logs,
artifacts, and truthful uncertainty survive control-plane restart. Process
continuity across companion or host restart requires a separately admitted
stable supervisor or systemd/launchd job integration and is explicitly
deferred.

## Logs, Artifacts, Retention, And Backpressure

The job runner opens bounded stdout and stderr sinks before launch. It does not
ask the payload to select log paths. Each stream has a hard size ceiling and a
fixed truncation policy. A slow reader never backpressures the child through
the gateway; node-local file writes absorb output until the configured limit,
after which output is deterministically truncated or discarded while the
process continues according to profile policy.

Artifact snapshotting occurs only after a terminal process observation or an
explicit profile-supported checkpoint. The implementation:

- resolves declarations relative to the already resolved working scope;
- anchors directory traversal and rejects symlink/hardlink/type swaps;
- copies only bounded regular files into private job-owned storage;
- detects concurrent source mutation and fails that artifact rather than
  publishing mismatched bytes;
- records size and SHA-256 before exposing an artifact reference; and
- fsyncs artifact and index state before reporting availability.

Job records, logs, and snapshots live in one private, instance-local, bounded
store with a single writer/lock contract and atomic updates. Limits cover
record count, active jobs, per-stream bytes, artifact count, per-artifact and
total bytes, and terminal retention. Active or cancellation-pending records
cannot be pruned. When only protected state remains and the store is full, new
starts fail before launch.

P5a does not add live log streaming, automatic channel delivery, arbitrary log
file following, directory artifacts, workspace synchronization, or indefinite
retention. A later UX may project completion into `task_status`, but that
projection must recover from the node job source of truth and cannot become a
second lifecycle owner.

## Approval, Security, And Redaction

Job start uses the risk and approval mode of the effective executor profile.
Job starts require durable human approval by default unless the exact actor
and target have the existing out-of-band approval bypass. Read-only status,
log, and artifact operations plus cancellation have independently configured
policy; a model argument cannot disable approval.

Approval binds the exact target, node, job profile and revision, executor safe
projection, argv digest, working-scope alias, environment
names and values, timeout, artifact declarations, actor, agent, route, session,
tool call, execution identity, descriptor/catalog identity, plan hash, and
expiry. It may display bounded target/profile aliases, a summarized command,
runtime, and artifact names. It must not expose credentials, fixed environment
values, broker endpoints, hidden host paths, or unrestricted script/output.

The design assumes command and log content may be malicious, secret-bearing,
or prompt-injecting. Job content is data returned to the authorized model; it
is never treated as policy or approval. Events and traces contain correlation,
state, counts, duration, exit/signal classification, truncation, and fixed
codes only. They exclude argv, script, cwd, environment values, raw logs,
artifact bytes, host paths, PIDs, cgroups, sockets, and credentials.

If an operator deliberately runs a companion as root or exposes a privileged
executable wrapper, a direct job has that configured blast radius. P5a does
not claim otherwise. It protects who may enter that authority, retains exact
evidence, and avoids spreading it to other targets, agents, sessions, or
default profiles.

## Remote Workspace Compatibility Contract

P5a must preserve a clean later P8 path without implementing it prematurely.
The future remote workspace is a gateway-side execution-context binding with
an operator-owned shape similar to:

```yaml
remote_workspaces:
  mintclaw-build:
    target: build-mac
    working_scope: project
    tools: [read, write, patch, search, exec, jobs]
    revision: mintclaw-build-v1
```

Internally this should be named distinctly from the current runtime/state
workspace, for example `RemoteExecutionContext` or `NodeWorkspaceBinding`.
Selecting it must not replace `AgentInstance.Workspace`, move session or task
stores, or relocate the AgentLoop.

When P8 is admitted, a compatible job tool may obtain target and working scope
from the execution context instead of requiring the model to repeat them. The
canonical node command, job profile, plan, job ID, store, logs, artifacts,
cancel semantics, and policy remain exactly the P5a implementation. P8 adds
routing and path presentation; it does not add another job runner.

P8 must also define per-tool remote adapters for read, write, patch, search,
and foreground exec. It cannot forward arbitrary current or future gateway
tools. Unsupported tools remain gateway-local or fail explicitly, and no tool
may silently execute on the wrong machine.

The first remote-workspace mode should be remote-canonical: the configured
node working scope is the real state. Automatic local/remote mirroring, seeding,
or synchronization is deferred because it introduces conflict and deletion
semantics unrelated to routing.

## Coding-Agent Compatibility Contract

The local coding-agent roadmap already defines two remote operations:

1. a local coding thread invokes a bounded capability on a companion through
   the gateway control plane; and
2. a live agent delegates repository-owning work to a paired coding worker,
   which creates one `CodingTask`, one `CodingThread`, and an isolated
   worktree.

P5a strengthens the first operation: builds, tests, backups, or conversions
can return a durable job ID and bounded artifacts. It does not implement the
second operation.

A live agent using a future remote workspace may perform bounded file and
command operations on a remote checkout. That is suitable for inspection and
small operator-directed changes. It must not claim the stronger guarantees of
a coding task: durable reasoning, thread resume, isolated worktree ownership,
questions, semantic progress, PR handoff, or a structured coding deliverable.

When repository reasoning or mutation must continue independently of the chat
turn, the live agent delegates a `CodingTask` to the project alias. It must not
simulate that worker with a long series of remote workspace reads/writes or
start `mintclaw code` as an opaque shell job. Conversely, a coding worker may
use P5a internally for a long test process, but the coding thread remains the
parent reasoning and deliverable owner.

The future coding `RemoteCapabilityBroker` and live-agent remote-workspace
router should share the existing node discovery/invocation client boundary.
They must not open competing companion connections or create a generic gateway
RPC surface.

## Explicit Non-Goals

P5a does not admit:

- Docker, bubblewrap, VM, container, GPU, or batch executors;
- a scheduler, cron replacement, dependencies, DAGs, retries, queues, fan-out,
  fleet placement, priorities, or resource bidding;
- process migration, checkpoint/restore, host-reboot continuation, or automatic
  relaunch after companion/service loss;
- interactive PTY, stdin, detach/reattach, or terminal sharing;
- arbitrary shell-text jobs or durable Linux owner-shell broker jobs;
- arbitrary log-file following, live log push, or unlimited retention;
- directory, glob, symlink, device, socket, or unbounded artifacts;
- a second gateway task registry, transfer protocol, artifact spool, approval
  manager, event subsystem, or invocation store;
- remote workspace selection or remote filesystem tool routing;
- a remote coding worker, coding thread, worktree, PR workflow, or ACP adapter;
- automatic local/remote workspace sync or mirroring;
- companion bootstrap, fleet management, or another transport; or
- macOS root-shell parity, which was not admitted by P1.

## Required PR Sequence

Every implementation PR starts from fresh `origin/main` after its dependent
predecessor merges.

1. **Job domain and direct executor**
   - Add the bounded job record/store, lifecycle transitions, ownership,
     retention, restart reconciliation, stdout/stderr sinks, regular-file
     artifact snapshots, and direct argv runner on Linux and macOS.
   - Add no gateway/model surface and keep all configuration disabled by
     default.
   - Prove at-most-once launch boundaries, store-full behavior, malformed
     records, log truncation, artifact races, and restart truth.
2. **Typed companion commands and discovery**
   - Add job profiles, safe projection, `job.start/status/logs/artifacts/cancel`
     handlers, schema validation, node-local ownership, and direct-executor
     cancellation semantics.
   - Reuse current catalog and invocation ledger. Do not add a job transport.
3. **Gateway/model and artifact slice**
   - Bind a target to one job profile, project safe discovery, run the commands
     through existing `nodes_invoke`/`nodes_status` and durable approval, and
     allow `nodes_download` to consume one owner-bound job artifact reference
     through the existing P2 transfer path.
   - Add metadata-only runtime events and no new task lifecycle owner.
4. **Proof, documentation, and deployment**
   - Add one real-process Linux direct job canary and one macOS direct job
     canary when the supported environment is available.
   - Prove later-turn and gateway-restart recovery, disconnect continuity,
     cancellation truth, artifact download, redaction, deny-by-default config,
     merged-main validation, deployment, and rollback instructions.

Boundaries may be combined only when the result remains smaller and
independently reviewable. No review finding may silently add P8, a coding
worker, Docker, or restart-surviving supervision.

## Validation Matrix

| Area | Required evidence |
| --- | --- |
| Input | Unknown fields, oversized argv/env, invalid UTF-8, empty aliases, excessive timeout/artifacts, absolute/traversing artifact paths |
| Authority | Wrong actor, agent, session, target, job profile, stale discovery, changed policy, revoked pairing, guessed job/artifact reference |
| Approval | Exact start binding, changed payload/artifacts denied, allow/deny/timeout, no model self-approval, target bypass narrowly scoped |
| Start identity | Duplicate exact start, changed idempotency conflict, concurrent duplicate, crash before/after `launch_attempted`, response loss, no replay |
| Process | Success, nonzero exit, signal, timeout, descendant output descriptor, concurrency limit, store full |
| Logs | Separate streams, cursor replay, bounded reads, truncation, invalid cursor, retention expiry, no passive raw content |
| Cancellation | Before running, running race, after terminal, duplicate cancel, timeout race, unproven process-group result, no PID reuse signal |
| Artifacts | Missing file, regular file, empty file, oversized file, symlink/hardlink/device/directory, concurrent mutation, duplicate names, digest, expiry, wrong owner |
| Disconnect | WSS loss while running, gateway restart, status recovery, no process cancellation from turn end, no duplicate launch |
| Companion restart | Nonterminal reconciliation to unknown/interrupted, retained logs/snapshots, no reattach or relaunch |
| Platforms | Linux amd64/arm64 build and direct-run behavior; macOS amd64/arm64 build and direct-run behavior |
| Existing behavior | Node invoke/status/cancel, shell/system exec, file transfer, service, update, browser, task delivery, and local exec remain unchanged |

Use deterministic state and fault injection at lifecycle boundaries. Timing-only
tests, sleeps as correctness evidence, raw secret fixtures, and tests that
weaken platform truth are rejected.

## Architecture Checkpoints

Stop implementation and reassess before another review/fix push when any of
these occurs:

- four substantive review/fix cycles;
- production scope or changed production files doubles from the first ready
  PR baseline;
- the same launch, cancellation, ownership, retention, or artifact invariant
  is challenged on three successive heads;
- arbitrary shell or different-UID execution is added to make the direct job
  slice useful;
- task-registry state becomes necessary to decide node job truth;
- P8 routing or coding-thread ownership is added to make P5a work; or
- reliable restart/reboot continuation cannot be implemented without a stable
  supervisor not admitted here.

At the checkpoint prefer narrowing or deleting behavior over creating another
foundation. Durable owner-shell work remains a separate product decision after
P5a rather than a reason to broaden this slice.

## Definition Of Done And Mandatory Stop

P5a is complete only when all of the following are evidenced:

- an authorized model discovers and starts one configured long-running node
  job through the existing invocation path and receives one stable job ID;
- the process is launched at most once across duplicate tool calls, response
  loss, disconnect, and gateway restart;
- a later turn can recover bounded status and ordered stdout/stderr without a
  process-local session;
- timeout and cancellation report only the termination guarantee actually
  proven by the executor;
- one declared regular-file artifact is snapshotted, listed, and downloaded
  through the existing transfer spool with owner, size, and digest checks;
- wrong actor, agent, session, target, profile, stale discovery, changed input,
  and guessed references fail closed;
- companion/service restart produces truthful unknown/interrupted state and
  never reattaches, signals a guessed PID, or relaunches work;
- logs, traces, events, approvals, and deployment evidence satisfy the
  redaction contract;
- direct jobs work on Linux and macOS through existing configured executable,
  working-scope, and environment authority;
- defaults remain deny-all and existing node, browser, coding, local exec,
  task, and channel behavior remains healthy;
- all admitted PRs are merged, merged `main` is validated, deployment and
  rollback evidence are recorded, and the architecture docs match behavior.

Once these conditions are met, mark the P5a goal complete and stop. Do not
begin remote workspace P8, coding-worker work, Docker/sandbox executors,
restart-surviving supervision, scheduling, fleet work, or any other deferred
item under this admission.
