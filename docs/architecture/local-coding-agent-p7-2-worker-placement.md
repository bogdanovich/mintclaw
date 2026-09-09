# Local Coding Agent P7.2: Worker Placement Decision

Roadmap packet: [P7.2 — Optional local coding daemon investigation](local-coding-agent-roadmap.md#p72--optional-local-coding-daemon-investigation).

Status: complete. Merged implementation and validation evidence are recorded
in the [P7.2 exit record](local-coding-agent-p7-2-exit.md).

## Decision

Do not add a machine-wide MintClaw coding daemon.

The admitted remote-coding execution boundary is instead one supervised,
task-scoped coding worker process for one coding task, one native
`CodingThread`, and one execution root. The already-running Node Companion is
the capability host and process supervisor. The worker, rather than the
companion, owns the coding controller, agent loop, thread writer lease, and
repository runtime for the lifetime of the task.

The first worker protocol is private, versioned JSON Lines over inherited
stdin/stdout pipes. It is not a discoverable local socket, HTTP service,
general agent RPC, or second node transport. A worker exits after its task
settles or its bounded idle window expires. Local `mintclaw code` and
`mintclaw code exec` continue to construct their runtimes directly and do not
depend on the companion or worker protocol.

This placement supports the intended Telegram flow without retaining an idle
agent runtime:

```text
Telegram owner
      |
      v
live agent -> gateway CodingTask and delivery
      |
      v
typed paired-node invocation
      |
      v
Node Companion supervisor
      |
      v
one task-scoped worker -> one CodingThread -> one execution root
      |
      v
semantic status / question / result -> one Telegram delivery
```

An investigation and a later fix are deliberately separate authority grants.
The investigation uses a read-only execution profile and finishes with a
verdict and thread reference. If the owner then says "fix it", the live agent
starts a mutation task linked to that result. P7.3 gives the mutation a fresh
isolated worktree. The new task may fork the bounded investigation history,
but it does not change the completed read-only task's mode or reuse its
working directory.

The placement trade-off is:

| Placement | Telegram reachability | Active-turn control | Failure isolation | Idle cost | Decision |
| --- | --- | --- | --- | --- | --- |
| Local in-process CLI | no | yes, from its own frontend | separate local process | none after exit | keep for TUI |
| Repeated one-shot `code exec` | through a supervisor | progress and signals only | separate process per turn | none after exit | keep for automation |
| Coding runtime inside Node Companion | yes | yes | shares the node connection process | node retains runtime state | reject |
| Machine-wide coding daemon | yes | yes, including multiple clients | separate resident service | continuous | defer until measured need |
| Task-scoped worker behind Node Companion | yes | yes, for one bound task | separate process per active task | none when idle | admit |

### Intended Telegram conversation

P7.4 should make the common flow explicit rather than requiring the owner to
know process or thread commands:

1. The owner says, for example, "investigate why the deployment test fails".
2. The live agent selects one configured target/project alias and the
   `investigate` mode. The gateway durably creates the task before dispatch and
   immediately returns its task ID.
3. The paired companion starts one read-only worker. The live agent remains
   responsive while semantic progress and any bounded question are projected
   through task status.
4. The worker finishes with a verdict, evidence, validation, unresolved work,
   and a native coding thread ID. The gateway delivers that result once.
5. If the owner replies "fix it", the agent creates a new `mutate` task linked
   to the investigation. P7.3 creates its isolated worktree, and the worker
   receives bounded forked context rather than silently escalating the old
   task's authority.
6. The fix result reports changed paths, tests, branch/commit/patch references,
   and remaining risks. Publication or merge remains a separately configured
   action.

The target may be a companion on the gateway machine or another paired
development machine; both use the same alias and node-policy boundary. The
gateway process does not special-case a loopback target by acquiring direct
filesystem authority.

## Evidence

### MintClaw baseline

Measurements were taken on 2026-09-07 from `origin/main` at `285a3ee5` on
macOS 26.6.2 amd64 with Go 1.26.6. The binary was built from the clean P7.2
worktree with the repository-supported `goolm,stdjson` tags because the host
does not have external libolm headers.

| Operation | Runs | Wall time | Peak RSS | Notes |
| --- | ---: | ---: | ---: | --- |
| `mintclaw code exec --help` | 30 | mean 0.071 s, 0.07–0.08 s | mean 36.0 MiB | process and CLI construction only |
| `mintclaw resume --json` | 20 | mean 0.147 s, 0.14–0.15 s | mean 36.7 MiB | project catalogue with retained benchmark threads |
| new `code exec --json` turn | 3 | mean 3.86 s, 3.24–4.76 s | about 50.6 MiB | direct OpenAI OAuth, `gpt-5.6-sol`, no tool call |
| resumed `code exec --json` turn | 3 | mean 3.06 s, 3.00–3.11 s | about 50.4 MiB | same durable thread, fresh process each time |

The live prompts requested a fixed one-word response and did not modify the
repository. The first new turn reported 6,201 used context tokens. These are
small local samples rather than release performance budgets, but they bound
the placement decision:

- CLI and catalogue startup are tens to low hundreds of milliseconds, while
  even a trivial provider round trip is measured in seconds.
- A resumed thread was faster despite using a fresh process, so durable thread
  state already supplies continuity without keeping a process alive.
- Every one-shot process releases its memory at exit. A global daemon would
  retain its base runtime continuously and would still pay provider latency.
- Coding mode currently uses `WithIsolatedToolBootstrap`; its
  `ensureMCPInitialized` path returns without creating an MCP manager. There is
  therefore no current coding MCP connection cost for a daemon to amortize.

Reconsideration requires a measured workload, not the possibility of future
reuse. Examples include a future admitted coding MCP profile with expensive
repeatable startup, a demonstrated task launch service-level objective that
the task-scoped worker misses, or multiple independent clients that must
attach concurrently to already-running local threads.

### Current architecture fit

P7.1 already supplies most of the worker interior:

- `pkg/coding/controller.Controller` serializes mutations independently of
  the TUI and owns submit, interrupt, hard-cancel, compaction, snapshots, and
  bounded subscriptions;
- `mintclaw code exec --json` proves new/resumed native thread execution,
  semantic events, stable terminal outcomes, signals, settlement, and lease
  release without terminal state;
- the coding catalogue, transcript, metadata, workspace baseline, and
  single-writer lease are durable and project-bound; and
- interrupted tool lifecycle repair and compaction recovery already happen
  when a native coding runtime is reopened.

The missing boundary is control, not another agent implementation.
`code exec` owns exactly one turn and closes. It has no bidirectional external
control channel, and `Controller.Submit` rejects a second input while a turn
is active. P7.2 therefore still needs a focused worker-control implementation
for status, same-turn steering, question answers, and cancellation. The shared
`AgentLoop.Steer` queue already supplies bounded between-tool-call injection;
the work is to expose it through the coding runtime and controller with
thread-scoped identity and durable acceptance semantics.

The existing task and node infrastructure should be composed, not copied:

- the gateway task registry owns requester identity, progress projection,
  terminal deliverables, retention, and delivery state;
- the interaction manager owns durable questions and authorized answers;
- gateway and companion invocation ledgers own dispatch, idempotency, and
  uncertain transport outcomes;
- Node Companion owns process supervision and node-local policy; and
- the coding thread and its worker own transcript, controller state, and
  repository execution.

P5a durable jobs demonstrate bounded process launch, status, cancellation,
logs, artifacts, and conservative unknown outcomes. A coding task may reuse
small process-supervision primitives, but it must not become a P5a job: a job
does not own a coding transcript, questions, steering, worktree lifecycle, or
multi-turn repository reasoning.

## Codex comparison

The Codex reference was refreshed to `openai/codex` `769a6a5b` on
2026-09-07. Codex now has an experimental `app-server-daemon`, but its own
documentation scopes it to machine-readable remote clients such as desktop
and mobile apps and to Codex instances reached over SSH. The TUI retains an
embedded-server fallback, and explicit remote endpoints remain separate.

That reinforces two boundaries rather than requiring MintClaw to copy the
daemon:

1. A transport-neutral thread/turn API is useful for remote clients.
2. Process discovery, persistent environment, auto-update, socket security,
   cross-platform detachment, and daemon recovery are a substantial separate
   product.

MintClaw already has an authenticated always-on gateway and paired-node
transport. Adding another discoverable machine-wide service would duplicate
lifecycle and authentication work. The task-scoped pipe protocol obtains the
useful controller boundary without making every local coding command depend on
that service.

Codex's `thread/start`, `thread/resume`, `thread/list`, `thread/read`,
`turn/start`, `turn/steer`, and `turn/interrupt` remain useful semantic
references. MintClaw should implement only the smaller one-task worker subset
needed behind the node contract, not the full Codex app-server surface.

## Rejected placements

### Machine-wide coding daemon

Rejected for the current roadmap because startup is already small relative to
provider work, coding MCP reuse is currently zero, and Node Companion already
provides the required always-on reachability. It would add local
authentication, discovery, socket ownership, version skew, persistent
environment, update/restart coordination, multi-client arbitration, and idle
resource retention before those solve a measured problem.

### Coding runtime inside Node Companion

Rejected because it would make the capability host own agent-loop,
provider-credential, transcript, compaction, and repository-runtime concerns.
A coding crash or leak would then share the companion failure domain, and
upgrading the coding engine would require coupling it to node connection
lifecycle.

### Repeated `code exec` subprocess with no control protocol

Useful as the P7.1 automation baseline but insufficient for P7.4. The parent
can parse progress and send a process signal, but it cannot deliver same-turn
steering or a correlated question answer. Treating every message as a new
process also leaves no explicit worker-generation handshake or truthful
running-task recovery contract.

### ACPX or `codex-cli` as the native boundary

Retain both as compatibility and rollout comparison paths, but do not make
them the native contract. The current `codex-cli` provider flattens MintClaw
messages and tools into a prompt for a fresh `codex exec --json` process and
does not map the Codex thread into MintClaw's task, thread, project, worktree,
interaction, or delivery identities. ACPX can preserve a Codex-side session,
but it still creates a second transcript and lifecycle authority.

During rollout, an operator-owned project profile may explicitly select a
native MintClaw worker or an ACPX/Codex adapter behind the same outer
`CodingTask` result contract. Their internal sessions are not merged. Keep the
adapter until a native canary demonstrates investigate, mutate, question,
cancel, restart, and result delivery. Removal is a later product decision, not
part of P7.2 or P7.4.

## Admitted worker-control boundary

P7.2 implementation should add one internal command intended only for a
trusted parent process. The exact CLI spelling is not a public compatibility
promise; the protocol is the contract.

### Ownership

- One worker process binds one immutable task ID, worker generation, thread
  ID, canonical project identity, execution-root identity, task mode, provider
  profile, and protocol revision during its handshake.
- The worker exclusively acquires the coding thread writer lease and owns one
  controller. The companion never opens the transcript as a writer.
- The companion owns the child process handle, bounded event journal, and the
  mapping from accepted node task to worker generation.
- Only the later P7.3 worktree owner creates or removes a mutation execution
  root. The worker receives a pre-admitted identity, not a model-authored path.

### Protocol

The inherited pipe protocol is schema-versioned JSONL with bounded records:

- parent commands: `initialize`, `turn.start`, `turn.steer`, `turn.interrupt`,
  `turn.cancel`, `snapshot.read`, and `shutdown`;
- worker responses: command acknowledgements with request IDs and stable error
  codes; and
- worker events: `worker.ready`, semantic item revisions, status changes,
  question state, context usage, terminal turn outcome, and `worker.stopped`.

`initialize` completes before repository/model work and reports the worker
build identity and supported protocol versions. Unknown fields are ignored
only according to the negotiated revision; unknown commands fail closed.
stdout contains protocol records only, stderr contains bounded diagnostics,
and malformed or oversized records terminate the worker as an uncertain
supervisor outcome.

No local authentication token is needed for anonymous clients because there
is no listener: the companion creates the pipes and starts the exact worker
binary under the same OS account. Authorization still happens before launch
through sender, agent, target, project alias, mode, and node policy. If a
future shared socket is admitted, same-user peer authentication and a new
threat review are mandatory.

### Steering and questions

P7.2 must add an explicit controller steering capability rather than
overloading `Submit` or parsing user prose. Same-turn steering is append-only,
ordered, bounded, and idempotent by steer ID. A non-steerable operation returns
a typed error; it is not silently converted into a new turn.

A question exposes one versioned question reference in status. The authorized
answer reaches the worker as a correlated steer carrying task, worker,
question, and answer identities. Ordinary Telegram follow-up text can be
queued as general steering only when the live agent explicitly calls the
coding-task tool; it is never treated as an approval answer by proximity.

### Cancellation and restart

Cooperative interrupt targets the active turn. Hard cancellation targets the
exact worker generation and its process domain, then preserves the thread and
worktree for inspection. Neither operation claims filesystem rollback.

The companion durably records acceptance and launch attempt before starting a
worker. After companion or worker loss it never launches a second task from
the original start request. It reconciles the retained task/thread/worktree
identities and classifies the last generation as interrupted or uncertain.
Only an explicit, idempotent resume operation may start a successor generation
on the same retained task and thread after lease and interrupted-tool recovery
succeed. A conflicting identity or live lease fails closed.

The first implementation does not reattach to orphaned pipes or trust a
persisted PID. This intentionally follows the conservative no-blind-replay
behavior already used by node jobs. Persistent cross-restart attachment would
be one reason to reconsider a local socket or daemon later.

## Sequencing

1. **P7.2 worker control:** implement and real-process test the single-task
   pipe protocol, controller steering, snapshot/status, cancellation, and
   crash classification. No node commands or Telegram tool yet.
2. **P7.3 worktrees:** implement isolated execution-root allocation, exclusive
   ownership, branch/conflict handoff, and non-destructive cleanup. Multiple
   coding agents never write the same worktree by default.
3. **P7.4 admission:** re-check the Node Companion P8b readiness gate, then add
   the project catalogue, typed node commands, gateway `CodingTask` tool,
   interaction/delivery projection, and one Telegram canary.

P7.2 worker control and P7.3 may be implemented as independent focused PR
series after this decision, but P7.4 cannot start until both are merged and
evidenced.

### Suggested implementation packets

| Packet | Scope | Expected complexity | Dependency |
| --- | --- | --- | --- |
| P7.2a | Protocol domain types, bounds, version negotiation, and explicit coding-controller `Steer` | medium | this decision |
| P7.2b | Internal worker process, parent client/harness, new/resume/status/steer/interrupt/cancel | medium-high | P7.2a |
| P7.2c | Disconnect/crash/lease recovery, Linux/macOS real-process matrix, and P7.2 exit evidence | medium-high | P7.2b |
| P7.3 | Worktree allocation, ownership, conflict handoff, and conservative cleanup | high, Git-safety critical | P6.1 and P7.1; may proceed beside P7.2 |
| P7.4 | Node project catalogue and worker adapter, typed node commands, gateway task tool, interactions/delivery, and canary | very high; keep as several dependent PRs | completed P7.2 and P7.3 |

The labels above are implementation packets within the existing roadmap
items, not new product milestones. Each production PR should retain one clear
state owner and stop if review starts pulling task, worker, worktree, transport,
and delivery ownership into the same package.

## P7.2 worker-control done criteria

- A real parent process can initialize a worker, start or resume one native
  thread, read a bounded status snapshot, deliver same-turn steering, and
  interrupt or hard-cancel the exact generation without any TUI state.
- The worker owns one thread lease and rejects a mismatched project, execution
  root, task mode, worker generation, or protocol revision before a turn.
- Command acknowledgements and semantic events are bounded, schema-versioned,
  idempotent where required, and contain no terminal control sequences or
  secrets.
- Parent disconnect, malformed input, worker crash, cancellation races, and
  restart are represented as explicit interrupted or uncertain outcomes with
  no blind replay.
- Local interactive coding and one-shot `code exec` remain supported and do
  not require Node Companion or a resident daemon.
- Linux and macOS real-process tests cover start, resume, steer, cancel,
  process loss, lease exclusion, and clean shutdown.

## Daemon reconsideration gate

A later proposal may admit a machine-wide daemon only if it supplies measured
benefit that the task-scoped worker cannot provide. It must include:

- repeatable cold/warm latency and resource measurements on representative
  coding tasks;
- a concrete client that requires persistent multi-client attachment or
  cross-supervisor reattachment;
- a security design for endpoint discovery, same-user authentication,
  credentials, project authority, and environment isolation;
- upgrade, version skew, crash recovery, stale process/socket, and foreground
  fallback contracts; and
- evidence that the extra service does not become a second gateway, node
  registry, task store, transcript store, or worktree owner.

Until that gate is met, `mintclaw code`, `mintclaw code exec`, and the
task-scoped worker are the only admitted native coding execution boundaries.
