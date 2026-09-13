# Local Coding Agent P7.4: Channel-to-Coding Task Admission

Roadmap packet: [P7.4 — Channel-to-coding task handoff](local-coding-agent-roadmap.md#p74--channel-to-coding-task-handoff).

Status: implementation admitted from merged P7.1, P7.2, and P7.3. This
document owns the complete vertical slice. The
[P8b checkpoint](node-companion-p8b-remote-coding-checkpoint.md) remains the
Node Companion boundary, not a separate implementation program.

## Decision

MintClaw will let an authorized live agent start and control one native coding
task on one operator-configured project alias on a paired Linux or macOS node.
The task can investigate a checkout read-only or mutate a new isolated
worktree. It reuses the existing coding thread, task-scoped worker, worktree,
gateway task, interaction, delivery, target-policy, authenticated WSS, and
invocation-ledger authorities.

The model sees one owner-scoped `coding_task` tool. Node commands remain an
internal typed protocol. There is no new daemon, transport, generic agent RPC,
task database, question store, transcript, provider adapter, or gateway access
to a development checkout.

Within one admitted task, worker tools run under the immutable node-local
profile without per-tool approval prompts. This is not unrestricted `--yolo`:
sender, route, agent, target, project, mode, executable, provider, resource,
and operating-system policy are resolved before dispatch and fail closed.

## Readiness review

The P8b gate is satisfied on merged `main`:

| Gate | Exact merged evidence | Retained boundary |
| --- | --- | --- |
| Stable non-interactive native execution | [#1088](https://github.com/bogdanovich/mintclaw/pull/1088), merge `623a9aa1`, in the [P7.1 exit record](local-coding-agent-p7-1-exit.md) | `code exec` remains a foreground automation frontend, not the remote transport. |
| Task-scoped worker control | [#1094](https://github.com/bogdanovich/mintclaw/pull/1094) `f1d029d9`, [#1119](https://github.com/bogdanovich/mintclaw/pull/1119) `46a300b8`, [#1122](https://github.com/bogdanovich/mintclaw/pull/1122) `97bdf395`, [#1135](https://github.com/bogdanovich/mintclaw/pull/1135) `27cd9dcf`, [#1145](https://github.com/bogdanovich/mintclaw/pull/1145) `b69eb62f`, [#1148](https://github.com/bogdanovich/mintclaw/pull/1148) `5b25d192`, [#1155](https://github.com/bogdanovich/mintclaw/pull/1155) `8fe0025f`, and [#1158](https://github.com/bogdanovich/mintclaw/pull/1158) `03fa77c9`, summarized by the [P7.2 exit record](local-coding-agent-p7-2-exit.md) | One private inherited-pipe worker per task; no listener or process reattachment. |
| Semantic lifecycle and questions | [#1139](https://github.com/bogdanovich/mintclaw/pull/1139) `92f030cb` and [#1143](https://github.com/bogdanovich/mintclaw/pull/1143) `e803a973` | Worker snapshots, question revisions, authenticated stop events, and `workerprocess.Result.Outcome()` are runtime truth. Thread `active/archived` remains catalogue availability, not worker status. |
| Immutable investigate authority | [#1147](https://github.com/bogdanovich/mintclaw/pull/1147), merge `661d0621` | Investigate mode has no mutation, command-execution, or out-of-root capability. |
| Isolated mutation and recovery | [#1165](https://github.com/bogdanovich/mintclaw/pull/1165) `55bffca3`, [#1167](https://github.com/bogdanovich/mintclaw/pull/1167) `f8316dfb`, [#1171](https://github.com/bogdanovich/mintclaw/pull/1171) `31233f29`, [#1172](https://github.com/bogdanovich/mintclaw/pull/1172) `29a8cd45`, and exit [#1173](https://github.com/bogdanovich/mintclaw/pull/1173) `a66a1795`, summarized by the [P7.3 exit record](local-coding-agent-p7-3-exit.md) | One linked worktree, branch, owner lease, worker generation, and bounded handoff; no publication or destructive fallback. |
| Gateway task and question projection | Existing `pkg/tasks`, `pkg/interactions`, `pkg/taskresult`, task delivery, and outbox tests on merged main | Extend the existing records with a bounded coding projection; do not add a coding-task or question store. |
| Paired-node transport and recovery | Existing target policy, `GatewayInvocationStore`, authenticated WSS, companion invocation ledger, cancellation/status recovery, and [P8a proof](../operations/node-companion-p8a-proof.md) | P7.4 adds typed commands only; it does not add a transport or generic remote execution escape. |
| Linux and macOS portability | #1158 exercises the real worker lifecycle on Linux and macOS; #1172 exercises real Git/worktree mutation, crash, successor, and cleanup on both CI platforms | The first remote slice supports paired Linux and macOS development machines only. |

Representative merged tests include
`TestCodeExecJSONLNewAndResumeUseSameThread`, the P7.2 real-binary lifecycle
matrix, worker protocol idempotency and question-correlation tests, P7.3
cross-process allocation exclusion, mutation crash/successor recovery, handoff
classification, and replacement-safe cleanup tests. Branches or open PRs are
not prerequisites for this admission.

## One vertical slice and its authorities

```text
Telegram or another authenticated channel
        |
        v
gateway CodingTask record + interaction + delivery
        |
        v
existing target policy + prepared gateway invocation
        |
        v
authenticated WSS + companion invocation ledger
        |
        v
node project catalogue + coding-task host
        |
        v
native CodingThread + P7.2 worker + optional P7.3 worktree
```

| State | Sole authority |
| --- | --- |
| Requester lifecycle, progress projection, final delivery, retention | Existing gateway task registry |
| Blocking question and one authorized answer | Existing interaction registry |
| Prepared dispatch and gateway-side uncertainty | Existing gateway invocation store |
| Accepted node operation, cancellation, and restart reconciliation | Existing companion invocation ledger, extended only with a bounded coding binding when required |
| Conversation, compaction, durable turns, and writer exclusion | Native `CodingThread` store and writer lease |
| Live activity, question, terminal stop, and uncertainty | P7.2 worker snapshot/event/result contract |
| Repository mutation and cleanup evidence | P7.3 worktree allocation, owner, and handoff |
| Provider credentials and executable selection | Node-local operator configuration |

No component copies another authority merely to simplify polling. In
particular, the gateway does not mirror the transcript or repository, and the
node does not add a second general task registry. The accepted
`coding.task.start.v1` invocation is the node-side task authority. Its ledger
record may carry the stable task/thread/worktree binding and latest bounded
projection needed for reconciliation.

## Identities and idempotency

Before dispatch, the gateway durably binds:

- coding task ID and generation;
- authenticated sender, route/channel/topic, routed session epoch, and agent;
- provider tool-call/execution identity and objective/done-criteria digest;
- configured gateway alias, target alias, node identity, node project alias
  and advertised revision, task mode, and policy revision; and
- prepared gateway invocation ID and idempotency key.

Node acceptance binds that exact generation to one node invocation, native
thread, worker generation, canonical project identity, and, for mutation, one
worktree allocation and owner generation. The mapping is written before the
worker starts. A duplicate start with identical immutable fields returns the
same mapping. A changed objective, digest, generation, project, mode, or policy
revision is a conflict, never a second task.

The gateway creates its task record and prepared invocation before WSS send.
Timeout before proven acceptance is `uncertain`; it is reconciled by status
against the original identities and is never retried as a new start. Companion
restart converts an unproven running invocation according to the existing
ledger contract. The coding host then reports exact retained thread/worktree
evidence; it does not replay a prompt. A new worker generation may resume only
after the prior writer and worktree owner are proven released.

## Operator-owned project catalogue

The node configuration defaults to an empty coding-project catalogue. Each
descriptor has a revision and declares:

- a safe alias and canonical repository identity;
- an operator-resolved source root beneath an allowed parent;
- allowed `investigate` and/or `mutate` modes;
- worker executable/build identity and protocol revision;
- provider/model profile references resolved only on the node;
- worktree parent and branch prefix for mutation;
- timeout, idle, event, result, artifact, and concurrency bounds; and
- retention and conservative cleanup policy.

Loading and every start resolve and validate the current filesystem and Git
identity. Symlinks, replaced directories, ambiguous Git identity, stale
descriptor revisions, an invalid or dirty source state, forbidden submodule or
worktree topology, missing credentials, and unsupported platform fail closed.

The gateway separately maps a model-visible alias to one existing target and
one node project alias. Its effective grant is the intersection of sender and
route authorization, selected agent target policy, gateway alias, current
node descriptor, allowed mode, and node-local policy. The model can provide
only that safe alias, a permitted mode, bounded objective and done criteria,
and already-authorized attachment or artifact references. It cannot provide
an absolute path, repository URL, node ID, executable, environment, provider
credential, worktree parent, branch policy, timeout override, cleanup policy,
or command.

There is no gateway-local filesystem fallback, even when the gateway and
paired node happen to run on the same machine. Offline, stale, busy,
ambiguous, forbidden, and uncertain conditions are explicit results.

## Runtime lifecycle

The gateway projection is:

```text
queued -> dispatching -> running <-> waiting_for_input
                              |
                              +-> completed | failed | cancelled | uncertain
```

These states project existing worker and invocation evidence. They do not
replace transcript records or add terminal values to thread catalogue status.
The task host reports one bounded revisioned snapshot, latest progress,
question, cancellation state, and terminal result reference.

An investigation uses the validated source checkout and P7.2's immutable
read-only profile. A mutation allocates and owns a P7.3 linked worktree before
worker launch. Exactly one worker generation holds the thread lease; exactly
one owner generation holds the worktree lease. Idle or terminal threads appear
through normal local `mintclaw resume` after release. A local frontend may
observe a running thread but cannot acquire a second writer.

Worker idle exit is not task loss. The retained task remains observable and
its released thread can be explicitly resumed by an authorized steer/resume
operation under the same project and mode grant. No timer invents a new turn.

## Internal node commands

The Node Companion surface is closed and versioned:

- `coding.projects.v1` returns only bounded aliases, modes, revisions, busy
  state, and availability;
- `coding.task.start.v1` accepts an immutable admitted task binding and returns
  the accepted node task/thread/worktree identities;
- `coding.task.status.v1` returns the latest bounded lifecycle projection,
  question, progress, cancellation, and terminal reference;
- `coding.task.steer.v1` appends one ordered idempotent steer or exact
  question answer;
- `coding.task.cancel.v1` requests cancellation of the exact generation; and
- `coding.task.result.v1` is added only if the bounded terminal report cannot
  fit safely in status.

All use the existing target descriptor, output-schema checks, gateway
invocation coordinator, WSS, companion ledger, cancellation, status recovery,
and redaction. They are not model tools, shell wrappers, arbitrary file APIs,
or a generic agent protocol.

## Gateway tool, questions, and delivery

The owner-scoped model tool has actions `start`, `status`, `steer`, and
`cancel`. `start` takes a configured alias, permitted mode, bounded objective,
and done criteria and immediately returns a durable task ID. Later actions
require that ID and the current route/agent ownership. They cannot change its
target, project, mode, or policy.

The gateway projects semantic progress into the existing task event stream.
When worker status exposes one revisioned blocking question, the gateway
creates one task-bound interaction carrying the exact task, thread, worker,
question, actor, route, and session identities. The authorized channel answer
is consumed once and becomes an exact typed steer. Duplicate, stale, late,
wrong-route, or uncorrelated answers do not grant authority. General steering
is bounded, append-only, ordered, and idempotent and cannot amend the original
grant.

Cancellation is a request, not a rollback claim. Before node acceptance it
prevents dispatch; after acceptance it targets the exact worker generation.
A completion race may truthfully settle as completed, cancelled, or uncertain.
It never discards a thread, worktree, or possible filesystem effect.

One terminal outcome produces one deduplicated existing
`deliverable_report.v1`. It includes safe task/thread and target/project
aliases, mode, bounded summary, changed relative paths, validation outcomes,
branch/commit/patch or approved artifact references, cleanup state, and
unresolved work. It excludes reasoning, prompts, channel history, full diffs,
unbounded logs, absolute paths, commands containing secrets, environment,
provider payloads, credentials, and repository secrets.

## Security, retention, and operational truth

The first slice does not fetch arbitrary repositories, install MintClaw or
dependencies, select arbitrary commands, publish or merge Git changes, open a
pull request, repair a broken runtime, forward gateway credentials, or infer
authority from conversational text. Codex/ACPX integrations remain available
as optional separate adapters and are neither required nor removed.

Task, interaction, dispatch, thread, worktree, and artifact retention remain
with their existing owners. Active, waiting, or uncertain work is protected.
Cleanup never deletes dirty, committed, conflicted, unknown, replaced, or
user-owned state and never uses a recursive fallback. Failures and retained
recovery evidence are visible in status and the deliverable.

Operations expose only safe aliases or hashes, opaque IDs, revisions,
lifecycle, durations, counts, truncation flags, worker health, cleanup backlog,
and stable error codes. Backup and rollback treat node project config,
companion accepted mappings, coding thread state, and worktree evidence as a
version-aware recovery set. Rollback disables gateway aliases first so no new
tasks are admitted, then lets retained tasks be inspected or cancelled under
the prior compatible binary.

## Focused implementation sequence

Every code PR starts from the latest merged `origin/main` and stops for an
architecture checkpoint if it grows beyond one boundary:

1. **P7.4a — task domain and node host:** add bounded coding-task types,
   operator-owned node project catalogue, and an idempotent node-local host
   that composes the existing thread, worker, and worktree owners. Extend the
   companion ledger projection rather than create a store. No WSS command or
   gateway tool.
2. **P7.4b — Node Companion commands:** expose project discovery and
   start/status/steer/cancel through the existing invocation/WSS path, with
   restart, no-replay, policy, cancellation, and redaction tests. No model tool
   or channel delivery.
3. **P7.4c — gateway task handoff:** add the owner-scoped `coding_task` tool and
   compose existing task, interaction, invocation, outbox, and final-delivery
   authorities. No new task/question store and no gateway repository access.
4. **P7.4d — portable vertical proof:** exercise real Linux and macOS processes
   for investigate, mutate, question/answer, steering, cancellation races,
   crash/restart, retention/cleanup, and one-delivery behavior; document
   operator configuration and compatibility.
5. **P7.4e — deploy and exit:** back up configuration/state, deploy disabled,
   enable one explicit target/project/sender grant, run one bounded Telegram
   investigate canary and one isolated mutation canary, capture health and
   rollback evidence, and merge a final exit record.

## Validation and done criteria

Focused tests cover invalid config and revisions; path/Git replacement;
investigate write denial; one mutation root; duplicate and concurrent starts;
gateway, node, and worker loss at each acceptance boundary; question and steer
deduplication; cancellation races; bounded result/redaction; conservative
cleanup; one final delivery; and compatibility with local coding, P8a, P5a,
browser, and unrelated node tools. The real-process matrix runs on Linux and
macOS. Each code PR passes repository formatting, lint, security, full tests,
race, portability, exact-head review, owner rocket, and merge gates.

P7.4 closes only when an allowed Telegram sender can start either mode,
receive the durable ID, inspect progress, answer a correlated question, steer
or cancel, and receive one bounded final report; restart/disconnect evidence
proves one task/thread/worktree and no replay; the gateway never touches the
checkout; deny-by-default deployment and rollback are recorded; and an exit
record links every merged PR, review, CI run, platform proof, configuration,
and canary.

Stop before P7.5 coding-session access to companions, generic remote agents,
arbitrary Git publication or merge, additional platforms, machine bootstrap,
credential brokering, a resident daemon, or ACPX/Codex removal. Each requires
a later focused admission.
