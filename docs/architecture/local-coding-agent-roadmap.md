# Local Coding Agent Roadmap

Status: proposed

MintClaw audit baseline: `origin/main` at `7b886f1a`, 2026-08-08

OpenAI Codex comparison baseline: `openai/codex` at `936f5eb3`, 2026-08-08

Pi comparison baseline: `earendil-works/pi` at `025957c2`, 2026-08-08

Oh My Pi comparison baseline: `can1357/oh-my-pi` at `08819b27`, 2026-08-08

OpenCode comparison baseline: `anomalyco/opencode` at `fe82a1b6`, 2026-08-08

## Purpose

This roadmap defines how MintClaw can become both:

- the existing always-on personal agent reached through gateways and channels; and
- a local, project-aware coding agent with a terminal UI, durable file-backed
  threads, cwd-aware resume, code tools, and long-running context compaction.

The coding surface is a new product frontend over shared runtime foundations.
It is not a conversion of routed chat sessions into project sessions, and it is
not an attempt to reproduce every OpenAI Codex feature.

The work is intentionally split into ordered, PR-sized packets. An implementing
agent should take the first uncompleted packet whose dependencies are merged,
keep the PR within that packet, satisfy its exit criteria, and update this
roadmap or a follow-up admission document with evidence when appropriate.

## Executive Decisions

The following decisions are part of the admitted scope:

1. A full terminal UI is required for the first useful release. A readline-only
   loop is not the target experience.
2. Coding mode is trusted-local by default. It does not ask for tool approvals
   and has effective full local execution authority. Existing non-interactive
   catastrophic-operation and tool-loop guards may remain.
3. Trusted-local authority applies only to a narrow coding tool profile. It
   must not silently expose personal messaging, deployment, channel, browser,
   node, or private MCP tools.
4. The gateway and channel runtime retain their current safety, routing,
   delivery, memory, and approval behavior.
5. Coding threads use a separate namespace and catalogue from routed personal
   sessions. They are selected by project identity rather than chat identity.
6. Coding state is stored under the MintClaw home directory, never by creating
   `sessions/`, `state/`, memory, or diagnostic directories in a source checkout.
7. Initial coding execution is local and in-process. A remote gateway cannot
   operate on a laptop cwd without a separate local daemon or node boundary.
8. Canonical JSONL remains the durable conversation source of truth. Seahorse
   remains a derived, rebuildable context and compaction index.
9. Compaction preserves continuity but never becomes the authority for live
   repository state. Git and filesystem observations re-anchor every resumed or
   compacted thread.
10. The first implementation should reuse MintClaw's providers, tool loop,
    runtime events, steering, session journal, and Seahorse. It should not port
    Codex's Rust crates, SQLite thread store, sandbox stack, or app server.
11. The terminal frontend subscribes to one authoritative, bounded current
    presentation view. Updates coalesce so a slow consumer converges to the
    newest view without blocking the running turn.
12. Repository instruction files are declarative context, not permission to
    execute repository-provided plugins, extensions, hooks, or dynamic config
    during startup. Any future executable project resource needs a separate
    admitted trust policy even though model-initiated coding tools are local
    no-prompt by default.
13. Future channel-to-coding delegation is a durable task dispatched to a
    project-bound local coding worker through a typed paired-node capability.
    The gateway, Telegram agent, and generic Codex provider never own or infer
    the development machine's filesystem path.
14. A local coding session may explicitly use operator-granted capabilities on
    paired companions. It reaches the existing node control plane through a
    narrow authenticated local client, not by asking the live agent's model to
    proxy a second agent turn. Remote capability access never changes the local
    thread's cwd or silently grants every paired node to the coding profile.
15. Node Companion P5a durable jobs and P8 remote workspace routing remain
    separate from coding-task ownership. A coding client may invoke a P5a job
    for a bounded remote build or test, while repository-owning remote work
    remains one `CodingTask`, one `CodingThread`, and one node-local execution
    root. The shared relationship is defined in
    [`node-companion-p5a-jobs-admission.md`](node-companion-p5a-jobs-admission.md).

## Intended User Experience

The target command family is:

```console
mintclaw code
mintclaw code "fix the failing tests"
mintclaw resume
mintclaw resume --last
mintclaw resume <thread-id>
mintclaw resume --all
mintclaw code exec "run tests and print a summary"
```

Expected behavior:

- `mintclaw code` starts a new coding thread for the current project and opens
  the terminal UI.
- `mintclaw resume` opens a searchable picker scoped to the current project.
- `mintclaw resume --last` resumes the newest matching thread without a picker.
- `mintclaw resume <thread-id>` resumes an explicit thread and warns before a
  project mismatch. It does not ask for tool approvals.
- `mintclaw resume --all` includes other projects and clearly shows their paths.
- A compatibility `--yolo` flag may be accepted as a no-op, but trusted-local is
  the default and the flag should not be required.
- `mintclaw code exec` is non-interactive, does not enter an alternate screen,
  and can produce stable JSONL for scripts.
- Existing `mintclaw agent`, `mintclaw agent live`, `mintclaw gateway`, and
  channel behavior remain unchanged.

### Future chat-to-coding handoff

An owner should eventually be able to ask an always-running MintClaw agent from
Telegram or another channel to investigate or fix code on a paired development
machine. The correct boundary is a durable coding task placed on a local coding
worker, not a gateway-side agent pretending it owns the remote cwd:

```text
Telegram request
      |
      v
live agent -> durable CodingTask -> typed paired-node coding capability
                                            |
                                            v
                              local coding worker/runtime
                              CodingThread + isolated worktree
                                            |
                      progress / questions / structured result
                                            v
                         task registry + delivery coordinator
                                            |
                                            v
                                      Telegram reply
```

The existing `codex-cli` provider is not this boundary. It launches a fresh
`codex exec --json` subprocess for an LLM request, flattens MintClaw messages
into a prompt, and does not map the returned Codex thread ID into MintClaw's
durable task, project, or coding-thread model. It may remain a compatibility
provider, but native chat-to-coding delegation should use the same MintClaw
coding runtime and thread files as `mintclaw code`.

The future handoff should reuse existing MintClaw foundations:

- the task registry and delivery coordinator for durable parent task identity,
  Telegram origin, progress, completion, and duplicate-delivery suppression;
- paired-node target policy, idempotent invocation, progress, cancellation,
  invocation ledger, and explicit uncertain outcomes;
- the coding thread catalogue, lease, compaction, and frontend snapshot
  protocol defined in this roadmap; and
- isolated worktree ownership from P7.3 for mutating asynchronous tasks.

The live agent supplies a bounded objective, done criteria, attachments, and an
owner-configured target/project alias. It never invents an arbitrary remote
path or forwards its entire personal-chat history. The local node resolves the
alias, enforces node-local policy, and owns filesystem authority. An admitted
task runs without per-tool approval inside that coding profile, but remote task
admission is limited to allowed senders, targets, projects, and task modes.

Progress delivered to chat is a semantic projection, not a stream of every TUI
delta. Useful updates include started, waiting for input, tests running,
compaction, completed, failed, cancelled, and uncertain. The final structured
deliverable should include summary, changed paths, validation results, branch
or patch reference, artifacts, unresolved issues, and the coding thread ID.
That thread can later appear in `mintclaw resume` on the development machine;
the TUI may observe a running thread and acquire its writer lease only after
the remote task releases it.

### Future coding-to-companion capability use

A local `mintclaw code` session should also be able to use explicitly granted
capabilities on paired companions. When a gateway or live-agent process on the
same machine already owns the companion connections, the coding runtime should
reuse that control plane rather than create a second pairing or route the
request through the live agent's LLM:

```text
local coding TUI and CodingThread
              |
              v
   CodingRemoteCapabilityBroker
              |
      authenticated local IPC
              |
              v
gateway node registry + invocation coordinator
              |
       authenticated WSS
              |
              v
       paired companion
```

This supports two deliberately different operations:

- A local coding thread may invoke a typed remote capability such as bounded
  command execution, build status, browser automation, service inspection, or
  artifact retrieval. The local thread remains the reasoning and transcript
  owner and records the durable remote invocation ID and bounded result.
- If the repository itself lives on the companion, the local thread delegates
  a `CodingTask` to a project-bound coding worker there and observes, steers,
  or cancels it. It must not approximate remote editing with a long sequence of
  shell reads and writes or pretend that a remote path is its local cwd.

A durable node job is a third, lower-level operation: it owns one OS process,
bounded logs, cancellation evidence, and declared artifacts. It may be used by
a coding thread for a long build or test, but it never owns repository
reasoning, transcript resume, worktree isolation, questions, or PR delivery.
Likewise, a future live-agent remote workspace routes compatible tools to a
target and working scope; it does not become a coding worker or a second job
manager.

The first slice should require an already-running local gateway/control-plane
endpoint. A Unix-domain socket or equivalent same-user local transport exposes
only node discovery and invocation operations; it does not expose personal
sessions, channel history, provider credentials, or arbitrary gateway state.
If the control plane is unavailable, local coding continues normally and the
remote tools report unavailable rather than spawning a hidden second gateway.

Coding mode remains no-prompt inside an explicitly configured remote grant,
but this does not turn `--yolo` into global fleet authority. Configuration
binds exact target aliases, command families, project aliases, and task modes;
gateway policy may narrow that grant, and node-local policy remains the final
enforcement boundary. Privileged commands are absent by default. The TUI must
make remote placement visible and show offline, denied, cancelled, truncated,
or uncertain outcomes without blindly retrying a mutation.

## Current Architecture Assessment

### What the additional reference audit changes

Codex remains the strongest primary reference for thread lifecycle, model/tool
execution, compaction checkpoints, and deterministic context re-anchoring. It
is not sufficient as the only reference for MintClaw's terminal and
always-on/local dual use case.

The three additional codebases contribute complementary evidence:

- **Pi:** cwd-bound runtime services, tree-shaped JSONL sessions, explicit
  fork/clone operations, authoritative snapshots plus progress deltas, and a
  transport-neutral client/server boundary. MintClaw should not copy its
  TypeScript packages, exact session tree, project package system, or
  experimental daemon protocol.
- **Oh My Pi:** durable interrupted-tool diagnostics, append-only history,
  tool-output protection, coding-tool quality as a first-class product concern,
  LSP/DAP integration, and demanding terminal fidelity tests. Its large
  Rust/TypeScript tool surface, renderer, debugger stack, and discovery
  ecosystem are not MVP dependencies.
- **OpenCode:** project-instance separation, lazy scoped instruction loading,
  local server/multiple-client architecture, session diffs, and filesystem
  snapshot/revert semantics. MintClaw should not copy its database schema, HTTP
  server, permission engine, UI framework, or hidden Git snapshot repository.

This audit strengthens the existing roadmap rather than replacing it. The P0
runtime-root refactor is still the correct prerequisite. New requirements are
limited to recoverable frontend state, durable interrupted-tool recovery,
scope-correct instruction loading, a coding-harness quality gate, stronger TUI
fidelity criteria, optional LSP, and a later checkpoint/rewind investigation.

Filesystem rollback is deliberately not moved into the MVP. Both Oh My Pi and
OpenCode demonstrate that useful undo requires an independent snapshot model,
careful treatment of untracked and ignored files, coordination with user edits,
and explicit preview/recovery semantics. A conversational fork alone must not
pretend to rewind the working tree.

### Foundations that should be reused

MintClaw already contains most of the non-UI agent engine required by coding
mode:

| Existing foundation | Coding use |
| --- | --- |
| `AgentLoop` and `Pipeline` | Shared turn, provider, tool-loop, retry, steering, and finalization engine |
| `SessionStore` and JSONL backend | Durable message, tool-call, and tool-result transcript |
| Seahorse | Budget-aware assembly, derived summaries, retrieval, and compaction |
| Runtime event bus | Turn, LLM, tool, steering, error, usage, and compaction observations |
| Provider streaming interfaces | Incremental answer and reasoning output |
| `bus.StreamDelegate` | Existing adapter seam for accumulated stream content |
| Filesystem tools | Read, list, search, write, append, and structured patch operations |
| `exec` tool | Foreground and background local command execution |
| Tool-loop protection | Repeated failure, identical-call, and no-progress protection |
| Context budgets | Mandatory prompt, tool, media, output, history, summary, and recent-tail accounting |
| Steering and abort | Mid-turn input, graceful stop, and hard cancellation primitives |
| Provider routing and fallbacks | Multi-provider and local-model differentiation from Codex |

This means the project does not need a second agent engine. It needs an
explicit local runtime profile, a coding-thread domain, a terminal-facing event
projection, and a TUI.

### Gaps that cannot be papered over in the CLI

The current direct CLI is not a coding frontend:

- It uses a manually supplied session key with a global default.
- It has no project catalogue, session picker, title, preview, git metadata, or
  inter-process lease.
- It prints only final responses in its normal path.
- Streaming is shaped around channel delivery and channel configuration.
- Runtime tool events are operational envelopes, not a stable terminal UI
  protocol.
- The root process prints a large banner before ordinary commands unless a
  narrow machine-output condition suppresses it.
- Personal `AGENTS.md` prose loading is rooted at one configured workspace; it
  does not implement hierarchical project instruction discovery.
- Git branch, HEAD, diff, worktree, and repository movement are not part of the
  session model.

### P0 refactoring is required

The audit found one structural blocker that should be solved before coding
features are layered onto the runtime.

`AgentInstance.Workspace` currently acts as all of the following:

- execution and filesystem-tool root;
- bootstrap, skill, and memory root;
- session directory root;
- Seahorse database root;
- task, interaction, goal, media, and diagnostic state root; and
- runtime/session ownership identity.

This is valid for the existing personal-agent workspace, where those roots are
normally the same. It is invalid for a coding thread. Setting `Workspace` to a
source repository would let construction and runtime services create MintClaw
state inside that repository. Keeping it pointed at the personal workspace
would make code tools and project instructions target the wrong directory.

There is also a construction-order issue: `NewAgentLoop` constructs
`AgentRegistry` and every `AgentInstance` before applying `AgentLoopOption`
values. An option therefore cannot inject coding roots, a coding session store,
or a coding tool profile before instance construction has side effects.

A bounded P0 refactor is warranted. A broad `AgentLoop` rewrite is not.

## Target Architecture

```text
                           shared runtime foundations
                    +-----------------------------------+
                    | providers / Pipeline / tool loop  |
                    | steering / events / compaction    |
                    +-----------------+-----------------+
                                      |
                 +--------------------+--------------------+
                 |                                         |
        personal gateway runtime                  coding runtime profile
        gateway and channels                      local controller and TUI
        routed SessionScope                       project CodingThread
        workspace state                           external coding state root
        channel delivery                          terminal event projection
```

### Coding runtime profile contract

The exact Go type is an implementation decision, but construction must resolve
these values before an agent instance or stateful service is created:

| Value | Meaning |
| --- | --- |
| Coding thread ID | Stable identity used for claims, traces, caches, and state ownership |
| Execution root | Canonical cwd or project root used by code tools and subprocesses |
| State root | Private MintClaw directory for sessions, Seahorse, traces, and locks |
| Instruction roots | Ordered global and project paths used for prompt instructions |
| Session-store factory | Creates the canonical store at the selected state root |
| Tool profile | Explicit allowed tools and their execution roots |
| Context profile | Context manager, budgets, summary policy, and derived-store path |
| Output profile | Channel delivery or terminal-facing stream/event projection |
| Trust profile | Existing gateway approvals or trusted-local no-prompt execution |

The coding profile uses an owner-scoped state root outside its source checkout.
The personal gateway retains its config-owned workspace lifecycle and hot
reload model; it does not adopt coding-thread identity, storage, or trust
semantics merely for structural symmetry.

### Coding thread domain

Do not overload routed `SessionScope` with cwd and git fields. A coding thread
is a separate application concept which owns:

- one opaque UUID;
- one canonical transcript session key;
- project identity and invocation cwd;
- display metadata for the picker;
- a single-writer lease;
- lifecycle state; and
- optional parent/fork metadata.

The existing `SessionStore` can remain responsible for transcript reads and
writes. A new `CodingThreadCatalog` or equivalent owns discovery and metadata.

### Admitted on-disk layout

```text
~/.mintclaw/coding/
  threads/
    <uuid>/
      thread.meta.json
      thread.lock
      sessions/
        <session-key>.jsonl
      context/
        seahorse.db
  diagnostics/
  config/
```

The per-thread root is admitted by P0.3 and P1.1. It gives every thread a
separate canonical JSONL store and disposable Seahorse SQLite file rather than
one ever-growing coding database. These invariants may not change silently:

- no state file is created in the repository;
- every path is schema-versioned and migratable;
- a thread ID maps to one canonical transcript;
- metadata updates are atomic;
- a process must hold the thread lease before appending;
- a stale lease is recoverable without allowing two writers; and
- listing does not require loading entire transcripts.

### Thread metadata

The first schema should include:

```text
schema_version
thread_id
session_key
created_at
updated_at
title
preview
status
project {
  kind
  project_key
  project_root
  invocation_cwd
  git_worktree_root
  git_common_dir
  git_origin
  git_branch
  git_head
}
model
provider
parent_thread_id
last_compaction {
  at
  revision
}
```

`project_key` should use the canonical Git worktree root when available and the
canonical cwd otherwise. The exact cwd remains recorded for prompt and resume
behavior. Separate worktrees remain separate projects by default; `--all`
allows cross-project selection.

## Required Invariants

Implementation PRs must preserve these system-level guarantees:

1. Personal gateway behavior does not change merely because coding support is
   installed.
2. Constructing or running coding mode never writes MintClaw-owned state into
   the source checkout.
3. A coding thread has at most one transcript writer across processes.
4. The root user request is durably journaled before model or tool execution.
5. Canonical JSONL retains the full accepted transcript; compaction state is
   derived and rebuildable.
6. Context assembly never sends an oversized provider request.
7. Compaction never splits a user turn or an assistant tool-call/result group.
8. The active turn and configured recent complete turns remain raw whenever
   they fit the hard context budget.
9. A compaction or resume never treats summarized file state as more current
   than a fresh repository observation.
10. Routine background compaction failure does not corrupt or truncate the
    canonical thread.
11. An emergency compaction failure stops with an actionable error if a safe
    provider request still cannot be assembled.
12. The TUI consumes a typed coding presentation view and does not inspect
    `AgentLoop` mutable state or arbitrary runtime payload internals.
13. Terminal exit, cancellation, panic, and signal paths restore the terminal.
14. Trusted-local mode bypasses interactive approval but does not broaden the
    tool catalogue beyond the coding profile.
15. Resuming a thread in a moved, deleted, or mismatched project is explicit
    and never silently targets a different directory.
16. A side-effecting tool has a durable started marker before execution and a
    terminal result afterward. Resume classifies a dangling marker as
    interrupted or unknown and never automatically repeats the side effect.
17. Frontend presentation updates may be coalesced without changing the
    canonical thread. The subscriber always converges to the newest bounded
    view.
18. Discovering `AGENTS.md` or another declarative instruction file does not
    implicitly load executable repository extensions, hooks, or dynamic
    configuration.
19. A remote coding task names an owner-approved target and project alias. Only
    the local worker resolves that alias to a path and owns the coding thread.
20. Retrying Telegram delivery, gateway dispatch, or a node connection cannot
    create a second coding task for the same idempotency identity.
21. A local coding session reaches companions through the durable node
    invocation coordinator, not through a model-to-model live-agent relay or a
    second competing companion connection.
22. A remote capability invocation cannot change the coding thread's canonical
    project root. Remote target, command, invocation ID, and outcome are
    explicit transcript evidence.
23. A coding profile sees only explicitly granted target and command aliases.
    Trusted-local execution cannot broaden gateway or node-local policy, and an
    uncertain remote mutation is never automatically replayed.

## Compaction and Long-Session Continuity

### What MintClaw already has

Seahorse is useful for coding mode and should be retained. The current engine
already provides:

- canonical JSONL to derived SQLite reconciliation using logical revisions and
  watermarks;
- separate history, summary, mandatory-prompt, output, and hard context budgets;
- selection of complete user turns rather than arbitrary messages;
- protection of a configurable recent turn tail;
- tool-call/result-safe projection;
- proactive background compaction under pressure;
- synchronous `CompactUntilUnder` during provider overflow recovery;
- leaf summaries and recursive condensed summaries;
- bounded retries and deterministic truncation fallbacks; and
- runtime events with token and compaction counts.

The existing leaf prompt already asks for decisions, constraints, active tasks,
technical detail, and file operations. Coding mode therefore does not need a
new compaction database or a wholesale context-manager fork.

### Useful Codex lessons

The audited Codex implementation treats compaction as a first-class lifecycle:

- automatic and manual triggers are visible to clients;
- a compacted item persists replacement history and a context-window identity;
- the active history is replaced by a summary plus selected user context;
- current turn context and world state are re-established after compaction;
- mid-turn compaction protects the last real user request; and
- resume reconstructs the latest compacted window from the rollout.

MintClaw should adopt the lifecycle clarity and deterministic re-anchoring, but
not Codex's exact storage design. MintClaw's canonical transcript should remain
intact while Seahorse stores rebuildable summaries and coverage relationships.

### Two-plane continuity model

Coding continuity requires two different inputs:

1. **Conversation continuity** comes from compacted history: objective,
   decisions, constraints, attempts, validation outcomes, unresolved failures,
   and next actions.
2. **Workspace continuity** comes from a fresh deterministic snapshot: cwd,
   project root, branch, HEAD, dirty status, changed paths, diff stat, and
   relevant worktree identity.

The model receives both. The second plane corrects stale statements in the
first. Full diffs and complete file contents are not injected automatically;
the agent uses tools to inspect them when needed.

### Coding summary contract

Seahorse should support a coding summary policy, selected through the coding
context profile rather than hard-coded as the only global prompt. A compacted
coding segment must preserve, when present:

- current user objective and definition of done;
- explicit user decisions and constraints;
- repository, package, or component being changed;
- created, modified, deleted, and renamed paths with the reason for each;
- important implementation decisions and rejected alternatives;
- commands, tests, linters, and builds that were run, including whether each
  passed, failed, timed out, or was not run;
- known failures and the evidence supporting the current diagnosis;
- active plan state and the next concrete action;
- unresolved questions or blockers;
- durable artifact references such as paths and commit IDs; and
- a bounded description of detail that was compressed or omitted.

The summary must not claim that a working tree is clean, a branch is current,
or a test still passes without a contemporaneous deterministic observation.
It must not copy secrets, unlimited command output, binary data, or historical
image payloads into the prompt.

### Tiered context reduction

Coding context should become smaller in stages before asking a model to
summarize everything:

1. Project a bounded provider-safe view of old tool results, replacing bulky
   observational output with explicit omission metadata while retaining tool
   identity, arguments needed for interpretation, outcome, and durable artifact
   references.
2. Protect recent turns and semantically important evidence such as failed or
   passing validation, file mutations, plans, and user decisions.
3. Generate leaf summaries for older complete turns.
4. Condense summaries recursively only when the hard budget requires it.

This projection never edits or truncates canonical JSONL. Tool classes whose
results may be reduced must be explicit and tested; a generic size threshold
must not discard the only evidence that an edit, test, or migration occurred.

### Compaction lifecycle

Coding mode should support four triggers:

| Trigger | Behavior |
| --- | --- |
| Proactive | Background compaction after budget pressure is observed; current turn proceeds with bounded selected context |
| Post-turn | Deduplicated background compaction after a completed final reply when configured thresholds are crossed |
| Emergency | Synchronous compaction and reassembly after a provider context error; active turn tail remains protected |
| Manual | `/compact` or TUI action; visible lifecycle with an explicit success, no-progress, interruption, or failure result |

Compaction may begin only with a stable transcript revision. It records the
revision it summarized, and a result is installed only if its coverage is still
valid or the context manager can safely merge later messages as the raw tail.
In-flight tool execution is never summarized out from under the active turn.

### Resume after compaction

Resume performs this sequence:

1. Acquire the coding-thread lease.
2. Load and validate thread metadata.
3. Open the canonical JSONL store and repair dirty metadata if necessary.
4. Open Seahorse and reconcile its watermark against canonical history.
5. Assemble bounded summary plus recent complete turns.
6. Capture a fresh workspace snapshot.
7. Load current hierarchical project instructions.
8. Render the TUI with the reconstructed transcript view and current context
   budget status.
9. Accept the next user turn only after all mandatory state is ready.

If Seahorse is missing or corrupt, it is rebuilt from JSONL. If rebuilding is
too slow for immediate interaction, the runtime may show a bounded recent-tail
context and an explicit degraded status, but it must not report the derived
store as current until reconciliation completes.

### Compaction evaluation contract

Compaction is not complete based only on token reduction. A deterministic
long-session evaluation must prove that, after multiple compactions and a
process restart, the agent can still identify:

- the active objective and done criteria;
- the paths intentionally changed;
- the last known test outcomes without turning “not run” into “passed”;
- the current unresolved failure and next action;
- user constraints and rejected approaches; and
- the fact that current git/filesystem observations supersede old summary text.

The evaluation must also prove:

- provider-safe tool-call/result pairing;
- bounded prompt size;
- no cross-project or cross-thread summary leakage;
- rebuild from canonical JSONL after deleting derived state;
- no duplicate side effect during resume;
- graceful handling of compactor timeout, empty output, and no-progress output;
- bounded behavior with large tool results, media references, and pasted logs;
- compaction lifecycle events visible to the TUI; and
- stable behavior across at least two successive summary-condensation levels.

## Terminal UI Architecture

### Framework decision

MintClaw already uses Lip Gloss for styled CLI output. The default implementation
candidate is Bubble Tea with Bubbles components and the existing Lip Gloss
styles. P0 includes a bounded framework spike to confirm:

- reliable incremental stream rendering;
- multiline editing and bracketed paste;
- viewport performance with long transcripts;
- terminal resize and narrow-width behavior;
- IME composition, grapheme clusters, and consistent terminal-cell width;
- behavior under SSH and common terminal multiplexers;
- an explicit native-scrollback versus alternate-screen model, including what
  remains visible after exit;
- deterministic state-transition testing;
- clean signal and panic restoration; and
- acceptable binary size and supported-platform behavior.

The TUI itself is mandatory even if the spike selects a different event-loop
library.

### Frontend presentation boundary

The TUI consumes one bounded `ThreadSnapshot` containing coding-specific
presentation state such as:

```text
activity and last turn outcome
bounded transcript entries
tool and command lifecycle
verified changed files
workspace and context usage
compaction state
```

The presentation store atomically returns its current view and a bounded stream
of later views. Each subscriber has one coalescing slot: when the TUI is slow,
an older pending view is replaced by the newest one. Snapshots remain compact,
while old transcript pages and large tool output are hydrated lazily.

The view is not a second source of truth. The projector may consume runtime
events, stream callbacks, tool audits, and thread metadata, but the TUI does not
depend on their internal representations or direct runtime state. A future web,
IPC, or reconnecting client requires a separately admitted transport contract;
the in-process presentation store does not anticipate one with versions,
revision logs, replay, or gap recovery.

The controller side should expose typed commands such as submit, interrupt,
hard-cancel, manual compact, rename thread, start new thread, and close. It
should not allow the TUI to call unexported turn internals.

### Required TUI surfaces

The first useful TUI includes:

- a scrollable conversation viewport;
- a multiline composer with paste-safe input;
- incremental answer streaming;
- optional collapsed reasoning output;
- tool cards with name, bounded arguments, state, duration, and exit status;
- changed-file and diff-stat summaries;
- a status bar showing project, branch, model, context use, and activity;
- visible compaction state and token reduction;
- a searchable project-scoped resume picker;
- help and command discovery;
- clear interruption behavior; and
- plain/no-color behavior for limited terminals.

Recommended interruption semantics:

- the first `Ctrl+C` during an active turn requests graceful interruption;
- a repeated `Ctrl+C` within a short interval hard-cancels the turn;
- `Ctrl+C` while idle exits after flushing thread metadata;
- terminal restoration happens even when cancellation or shutdown fails; and
- an interrupted command is represented as interrupted or unknown, never
  silently successful.

## Delivery Phases

Each work packet below should normally be one focused PR. A packet may be split
further when reviewability improves, but dependent packets must not be combined
merely to save PR overhead.

### P0 — Admit the runtime boundaries

Goal: make coding mode possible without contaminating personal-agent behavior
or source repositories.

#### P0.1 — Runtime layout contract

Dependencies: none

Scope:

- Define explicit runtime owner, execution root, state root, instruction roots,
  and state-path ownership.
- Classify current `AgentInstance.Workspace` consumers by responsibility.
- Document the current personal path inventory and one-time deployment mapping.
- Add focused contract tests proving explicit owner identity, distinct roots,
  state-path ownership, and source-pollution rejection.
- Store canonical resolved paths and fail closed when a symlink ancestor cannot
  be resolved unambiguously.
- Compare filesystem identity when checking containment so case-only aliases on
  case-insensitive volumes cannot place state under the execution root.

Done when:

- No new coding code needs to guess which meaning of `Workspace` applies.
- Every state-producing subsystem relevant to coding has an assigned root.
- Personal session, memory, task, interaction, and media semantics have an
  explicit migration owner without requiring old path compatibility.
- The contract explicitly prohibits source-checkout state pollution.

Out of scope:

- Renaming every existing field in one PR.
- Moving channel or gateway state unrelated to coding.

#### P0.2 — Pre-construction runtime profile

Dependencies: P0.1

Implementation evidence:
[`local-coding-agent-p0-runtime-profile-admission.md`](local-coding-agent-p0-runtime-profile-admission.md)

Scope:

- Replace or extend the current post-registry option timing with a builder or
  resolved profile applied before `AgentRegistry` and `AgentInstance` creation.
- Replace obsolete construction entry points rather than retaining wrappers
  solely for internal path compatibility.
- Make construction return errors for invalid roots before partial state is
  created.
- Add cleanup for partially constructed session/context resources.

Done when:

- A test can construct an agent with different execution and state roots.
- Construction creates no directory under the execution root unless a coding
  tool intentionally writes there during a turn.
- Existing gateway constructors and tests retain their behavior.
- Failure leaves no split registry or leaked context manager.

#### P0.3 — Session-store and context-store injection

Dependencies: P0.2

Scope:

- Inject a session-store factory before agent instance creation.
- Route Seahorse database construction through the resolved state root.
- Keep derived context owner-scoped: one Seahorse database per personal agent
  and one separate Seahorse database per coding thread; never one global coding
  SQLite file.
- Remove direct assumptions that the canonical session directory is always
  `<execution workspace>/sessions`.
- Preserve canonical personal JSONL data through the one-time deployment
  migration; do not add indefinite old-location fallback reads.

Done when:

- Execution root A can use a canonical transcript and Seahorse database under
  state root B.
- Reconciliation still treats JSONL as authoritative.
- Multi-agent personal workspaces retain their current per-agent separation.
- Two coding-thread owners resolve to different Seahorse database files.
- Fault-injection tests cover store/context construction rollback.

#### P0.4 — Explicit tool bootstrap profiles

Dependencies: P0.2

Implementation evidence:
[`local-coding-agent-p0-tool-profiles.md`](local-coding-agent-p0-tool-profiles.md)

Scope:

- Resolve tool registration before the agent is exposed to a turn.
- Define `personal` and `coding` profiles without mutating persisted user config.
- Coding defaults include only local coding tools and deliberately selected
  plan/context tools.
- Personal/channel-only tools and MCP profiles are excluded unless explicitly
  admitted for coding.
- Coding trust selects allow-all execution without changing gateway approvals.
- Repository-provided executable extensions, hooks, and dynamic tool
  configuration remain disabled unless a later trust design admits them.

Done when:

- A coding-profile test enumerates the exact visible tools.
- No messaging, deploy, restart, channel, node, personal browser, or personal
  MCP tool appears by default.
- Merely opening a repository cannot execute project-local extension code.
- Shell and filesystem operations use the execution root.
- Gateway tool catalogues remain unchanged.

#### P0.5 — Terminal frontend feasibility and event contract

Dependencies: P0.1

Implementation evidence:
[`local-coding-agent-p0-terminal-frontend.md`](local-coding-agent-p0-terminal-frontend.md)

Scope:

- Build a disposable TUI spike using the preferred framework.
- Project existing stream callbacks and runtime events into a small typed event
  model without changing turn behavior.
- Exercise answer streaming, tool start/end, resize, multiline paste, and
  cancellation.
- Compare alternate-screen and native-scrollback behavior, including long
  history, tmux/SSH, IME input, resize, and final transcript visibility.
- Specify an authoritative bounded current-view subscription and demonstrate
  convergence when a slow consumer misses intermediate views.
- Record any missing engine event needed for P3; do not add speculative event
  types in the spike.

Done when:

- The framework choice is recorded with evidence.
- The screen/scrollback model and supported fallback are recorded with evidence.
- A typed frontend presentation and controller boundary are admitted.
- Slow consumers converge without direct runtime-state access.
- Missing core observations are listed as bounded follow-up work.
- The spike is either converted into testable foundation code or removed.

#### P0 exit gate

P1 may start only when:

- distinct execution and state roots work in tests;
- session and Seahorse locations are injected before construction;
- coding tools can be isolated from personal tools;
- existing personal-agent behavior remains green; and
- the TUI framework and frontend presentation boundary are decided.

### P1 — Durable project thread catalogue

Goal: create and resume project-scoped threads safely across process restarts.

#### P1.1 — Thread metadata and project identity

Dependencies: P0.3

Implementation evidence:
[`local-coding-agent-p1-thread-metadata.md`](local-coding-agent-p1-thread-metadata.md)

Scope:

- Define versioned `CodingThread` metadata.
- Resolve canonical cwd, Git worktree root, common dir, remote, branch, and HEAD.
- Define project-key behavior for non-Git directories, symlinks, moved paths,
  and separate worktrees.
- Generate title and preview from the first accepted user request without an
  extra model call.

Done when:

- Metadata round-trips atomically.
- Project matching is deterministic across restart.
- Symlinked cwd behavior is tested.
- A missing or moved project produces an explicit state, not silent rebinding.

#### P1.2 — Catalogue listing and filtering

Dependencies: P1.1

Implementation evidence:
[`local-coding-agent-p1-thread-catalog.md`](local-coding-agent-p1-thread-catalog.md)

Scope:

- List threads without loading their full JSONL transcripts.
- Sort by update time and filter by current project by default.
- Support explicit ID, `--last`, and `--all` queries.
- Bound corrupt metadata handling and report skipped entries.

Done when:

- Thousands of metadata entries can be listed within an agreed local latency
  budget.
- Current-project filtering and `--all` have contract tests.
- Corrupt one-thread metadata cannot hide healthy threads.
- Pagination or bounded result handling is defined for the TUI.

#### P1.3 — Cross-process thread lease

Dependencies: P1.1

Implementation evidence:
[`local-coding-agent-p1-thread-lease.md`](local-coding-agent-p1-thread-lease.md)

Scope:

- Add an OS-appropriate single-writer lease around a coding thread.
- Record enough owner information for diagnostics and stale-owner recovery.
- Define behavior when a live process already owns the requested thread.
- Keep lease state outside canonical transcript content.

Done when:

- Two processes cannot append concurrently.
- Crash and stale-lock recovery tests pass on supported platforms.
- Read-only listing does not require the writer lease.
- Lease errors identify the owning process when safely available.

#### P1.4 — CLI thread commands

Dependencies: P1.2, P1.3

Implementation evidence:
[`local-coding-agent-p1-thread-cli.md`](local-coding-agent-p1-thread-cli.md)

Scope:

- Add `mintclaw code` and top-level `mintclaw resume` command plumbing.
- Suppress the root banner for alternate-screen and machine-output paths.
- Support new, explicit ID, `--last`, `--all`, model override, and initial prompt.
- Keep the first implementation frontend-neutral so it can use a temporary
  plain renderer before P4.

Done when:

- New and resumed threads survive process restart.
- No coding state is written into the project.
- Unknown IDs and project mismatches are actionable.
- Existing root commands and help output remain compatible.

#### P1 exit gate

The runtime can create, list, acquire, reopen, and append to a project coding
thread from files outside the repository. A polished TUI is not yet required.

### P2 — Native coding runtime profile

Goal: run useful code turns through MintClaw's own agent engine.

#### P2.1 — Coding prompt and identity

Dependencies: P0.4, P1.4

Scope:

- Define the coding-mode base instructions and tool-use expectations.
- Exclude personal persona, routed sender context, channel delivery language,
  and personal long-term memory by default.
- Include project identity, cwd, trust mode, and current thread metadata.
- Keep the prompt provider-neutral.

Done when:

- Prompt snapshots contain coding context and omit channel/persona context.
- A coding thread never enters a routed personal session.
- Prompt-cache stability and dynamic sections are intentional.

#### P2.2 — Hierarchical project instructions

Dependencies: P2.1

Implementation contract:
[`local-coding-agent-p2-project-instructions.md`](local-coding-agent-p2-project-instructions.md)

Scope:

- In each directory select exactly one of `AGENTS.override.md`, `AGENTS.md`, or
  compatibility-fallback `CLAUDE.md`, in that priority order; never merge
  same-directory files.
- Discover the selected files from project root toward the invocation cwd for
  initial context.
- Define precedence for global coding instructions, repository instructions,
  and nested directory instructions.
- When a tool first accesses a path below a more deeply nested instruction
  file, attach that scoped instruction before the path content is used for
  reasoning; deduplicate it within the active turn/context window.
- Bound total bytes and report truncation or unreadable files.
- Cache by path identity and invalidate on change.

Done when:

- Nested instructions apply only within their directory scope.
- Work that moves from one subtree to another receives the applicable nested
  instructions without globally applying either subtree's rules.
- Precedence, byte limits, symlinks, and cache invalidation are tested.
- Personal root `AGENTS.md` prose and hierarchical coding `AGENTS.md` scopes
  are not conflated.

#### P2.3 — Deterministic workspace snapshot

Dependencies: P1.1, P2.1

Implementation contract:
[`local-coding-agent-p2-workspace-snapshots.md`](local-coding-agent-p2-workspace-snapshots.md)

Scope:

- Capture bounded project root, cwd, branch, HEAD, status, changed paths, and
  diff stat before the first turn and after relevant tool writes.
- Mark fields unavailable outside Git without failing non-Git projects.
- Do not inject full diffs automatically.
- Emit a frontend update when repository state changes.

Done when:

- Dirty, detached-HEAD, unborn-branch, non-Git, and worktree cases are covered.
- Snapshot output is deterministic and bounded.
- The prompt clearly treats this snapshot as newer than compacted narrative.

#### P2.4 — Coding tool execution and audit

Dependencies: P0.4, P2.3

Implementation contract:
[`local-coding-agent-p2-tool-execution.md`](local-coding-agent-p2-tool-execution.md)

Scope:

- Wire read, list, search, write/append, `apply_patch`, and `exec` to the coding
  execution root.
- Preserve write audits and tool-call/result journal pairing.
- Define full-host trusted-local path behavior and environment inheritance.
- Ensure command cancellation kills process groups where supported.

Done when:

- A deterministic scenario reads, patches, tests, and reports a small fixture.
- Tool results survive resume.
- Interrupted commands cannot be reported as success.
- The gateway's tool restrictions are unchanged.

#### P2.5 — Durable turn and tool lifecycle

Dependencies: P2.4

Implementation contract:
[`local-coding-agent-p2-durable-lifecycle.md`](local-coding-agent-p2-durable-lifecycle.md)

Scope:

- Persist the accepted user turn before provider or tool execution.
- Persist a bounded tool-start marker before invoking a side-effecting tool and
  a correlated terminal result after it settles.
- Record graceful interruption and best-effort abnormal-exit evidence without
  relying on terminal UI state.
- On resume, repair dangling assistant/tool lifecycle state to
  interrupted/unknown and never automatically replay the call.

Done when:

- Crash fixtures stop the process before tool start, after tool start, during
  mutation, and before result persistence with an unambiguous resumed state.
- No crash point produces a false successful tool card or duplicate side
  effect.
- Tool-start metadata is bounded and redacted but sufficient for diagnostics.
- Personal session recovery remains compatible.

#### P2.6 — Native end-to-end coding turn

Dependencies: P2.2, P2.5

Scope:

- Run an initial request and follow-up through the real MintClaw pipeline.
- Reopen the process and continue the same thread.
- Verify provider selection, tool definitions, context assembly, journal writes,
  and repository refresh.
- Use a deterministic provider fixture in CI.

Done when:

- The agent completes a fixture edit across restart.
- The resumed model sees prior decisions and current repository state.
- No Codex subprocess is required for the native path.
- Failures preserve an inspectable thread.

#### P2.7 — Coding harness quality gate

Dependencies: P2.6

Scope:

- Evaluate the actual read, search, patch/edit, write, and command contracts on
  representative small and large repository fixtures.
- Measure first-attempt edit correctness, stale-patch behavior, search
  precision, token/output volume, cancellation, and recovery across at least
  two materially different model/tool-call families.
- Exercise awkward inputs: long lines, Unicode, generated files, binary paths,
  deleted/renamed files, large command output, and concurrent external edits.
- Record whether the existing tools are sufficient or a bounded tool-contract
  change is required before freezing P3 tool presentation.

Done when:

- Deterministic fixtures and metrics make tool regressions visible without a
  live provider.
- At least one opt-in live smoke scenario validates that fixture behavior is
  representative.
- Any proposed edit/search protocol change has measured evidence and remains a
  focused follow-up rather than an unbounded tool rewrite.
- Tool result truncation preserves actionable diagnostics and durable artifact
  references.

#### P2 exit gate

MintClaw is a functional native coding agent with plain output, crash-safe turn
semantics, and evidence that its core tool contracts are usable. It is not yet
a release candidate because the required terminal UI and long-session
compaction experience are incomplete.

### P3 — Terminal event and control plane

Goal: expose the engine to a TUI without coupling UI state to runtime internals.

Status: complete. See the [P3 exit record](local-coding-agent-p3-exit.md)
for the merged packets, validation evidence, and explicit P4 boundary.

#### P3.1 — Coding event projector

Dependencies: P0.5, P2.7

Scope:

- Translate runtime events, stream callbacks, write audits, and thread metadata
  into the admitted current presentation view.
- Publish an authoritative bounded thread snapshot through one coalescing
  in-process subscription.
- Preserve ordering and thread/turn/tool correlation.
- Bound arguments, output, errors, and diff previews.
- Redact secrets using existing diagnostic/tool redaction boundaries.

Done when:

- Projected event sequences are deterministic for success, tool error,
  provider retry, fallback, interruption, and compaction.
- A slow consumer can miss intermediate views and converge to the newest
  visible terminal state.
- The TUI never needs `Payload any` type assertions.
- Slow UI consumers cannot block the agent indefinitely.

#### P3.2 — Terminal stream sink

Dependencies: P3.1

Scope:

- Adapt provider accumulated streams to answer and reasoning presentation
  updates.
- Avoid duplicate final content when a stream is finalized.
- Support providers without streaming through the same final view.
- Define backpressure and bounded buffering.
- Coalesce pending views without blocking the provider or presenting partial
  state assembled from unrelated updates.

Done when:

- Streaming and non-streaming providers produce equivalent final transcript
  state.
- Fallback before visible output can retry; failure after visible output is
  represented without duplicating text.
- Unicode chunk boundaries are safe.
- A slow subscriber receives the newest view rather than causing unbounded
  memory or a permanently inconsistent transcript.

#### P3.3 — Controller and interruption

Dependencies: P3.1

Scope:

- Run the synchronous agent call outside the UI loop.
- Expose submit, graceful interrupt, hard cancel, compact, and close commands.
- Reuse existing steering and abort ownership where behavior matches.
- Serialize actions against the thread lease and active turn.
- Keep one mutation coordinator even if future adapters allow multiple
  read-only observers.

Done when:

- The UI remains responsive during tools and model calls.
- Interrupt semantics are deterministic.
- A second user prompt during a turn is either admitted as steering by explicit
  policy or retained in the composer; it is never lost ambiguously.

#### P3.4 — Command-output and file-change observations

Dependencies: P3.1

Scope:

- Add only the missing bounded observations required for useful tool cards.
- Prefer tool-owned progress interfaces or execution events over log scraping.
- Represent stdout/stderr truncation and background process state explicitly.
- Project verified file-write audits into changed-file events.

Done when:

- Long command output cannot exhaust UI memory.
- Tool start, progress, completion, and cancellation correlate to one tool ID.
- Diff/file cards derive from verified changes, not model claims.

#### P3 exit gate

A headless test frontend can drive turns, receive bounded ordered events, and
interrupt work without importing TUI packages.

Completed by the [P3 exit record](local-coding-agent-p3-exit.md). The required
interactive terminal application remains P4 work.

### P4 — Required terminal UI

Goal: replace the direct coding renderer with a stable interactive TUI.

Status: complete. See the [P4 exit record](local-coding-agent-p4-exit.md)
for the merged packets, validation evidence, current-view architecture, and
explicit P5 boundary.

#### P4.1 — Application shell and terminal lifecycle

Dependencies: P3.2, P3.3

Implementation evidence:
[`local-coding-agent-p4-terminal-shell.md`](local-coding-agent-p4-terminal-shell.md)

Scope:

- Add the TUI model/update/view shell.
- Implement the screen/scrollback model admitted by P0.5 and own resize, focus,
  signal, panic, and restoration.
- Integrate existing no-color and terminal capability detection.
- Keep TUI dependencies outside core agent packages.

Done when:

- Start, normal exit, `Ctrl+C`, SIGTERM, and induced panic restore the terminal.
- Exit leaves the documented final transcript/scrollback behavior on every
  supported screen mode.
- Tiny and resized terminals remain usable.
- Non-TTY invocation falls back or fails with an actionable suggestion.

#### P4.2 — Transcript viewport and composer

Dependencies: P4.1

Implementation evidence:
[`local-coding-agent-p4-transcript-composer.md`](local-coding-agent-p4-transcript-composer.md)

Scope:

- Render user, assistant, reasoning, tool, warning, and error entries.
- Add multiline composition, history, bracketed paste, IME-aware cursor
  placement, Unicode cell-width handling, and scroll behavior.
- Keep live streaming stable while the user scrolls away from the bottom.
- Page or hydrate old transcript state lazily and bound retained rendered state
  independently of the canonical transcript.

Done when:

- Large paste and Unicode input round-trip correctly.
- CJK, combining-mark, emoji, RTL-adjacent, and IME scenarios keep cursor and
  clipping behavior within terminal-cell bounds.
- Streaming does not steal scroll position after manual scroll.
- Replacing the current view preserves composer and scroll state where their
  referenced entities still exist.
- View-state tests use semantic assertions rather than fragile full-screen
  snapshots where possible.

#### P4.3 — Tool, diff, and status surfaces

Dependencies: P3.4, P4.2

Implementation evidence:
[`local-coding-agent-p4-tool-diff-status.md`](local-coding-agent-p4-tool-diff-status.md)

Scope:

- Render collapsed tool cards with optional expansion.
- Show command state, duration, exit status, and bounded output.
- Show changed paths and diff stat, with explicit refresh behavior.
- Add project, branch, model, context usage, and activity status.

Done when:

- Success, failure, interruption, unknown outcome, and truncation are visually
  distinct without relying only on color.
- The status line updates after branch or repository changes.
- Sensitive arguments remain redacted.

#### P4.4 — Resume picker

Dependencies: P1.2, P1.3, P1.4, P4.1

Implementation evidence:
[`local-coding-agent-p4-resume-picker.md`](local-coding-agent-p4-resume-picker.md)

Scope:

- Add searchable, paged thread selection.
- Show title, preview, age, branch, path, dirty/stale/missing state, and short ID.
- Scope to the current project by default with an explicit all-project toggle.
- Define keyboard and screen-reader-friendly selection behavior.
- Make interactive `mintclaw resume`, explicit-ID resume, and `--last` enter the
  same resumed TUI after selection; preserve bounded plain/JSON listing for
  non-TTY callers.
- Keep the picker as one pre-controller current catalogue page. Search, scope,
  refresh, and pagination replace that page directly; they do not add another
  reducer, revision log, delta protocol, replay window, or reconnect contract.
- Keep catalogue, Git, location, and lease observations as discovery hints.
  The command layer must acquire the selected thread lease, reload canonical
  metadata, and revalidate the current project before constructing a
  controller.

Done when:

- Empty, large, corrupt-entry, missing-project, and currently-locked catalogues
  are usable.
- Selection never silently bypasses a project mismatch or live lease.
- Picker cancellation owns no lease or controller, and an available picker row
  cannot substitute for final admission under the authoritative OS lock.
- P4.4 adds no compatibility path for the removed revision/delta frontend.

#### P4.5 — Commands and help

Dependencies: P4.2, P4.3

Implementation evidence:
[`local-coding-agent-p4-commands-help.md`](local-coding-agent-p4-commands-help.md)

Scope:

- Add discoverable commands for help, new thread, status, model, diff, compact,
  rename, and exit.
- Keep command parsing frontend-owned and typed at the controller boundary.
- Document keyboard bindings in-app.
- Read status and diff information from the subscribed current
  `ThreadSnapshot`; do not reconstruct a second frontend state machine or
  retain command-specific mirrors of runtime state.
- Apply command results through the same current-view subscription used by
  ordinary streaming, tool, workspace, and compaction updates.
- Do not invent thread-lifecycle persistence in the terminal layer. P4.5 must
  surface actionable typed-command errors where the native controller does not
  yet implement rename or in-place controller switching; P6.1 owns durable
  rename, while new-thread guidance reuses `mintclaw code` until switching is
  separately admitted.

Done when:

- Every command has state-transition tests.
- Unknown commands remain in the composer or produce an actionable error.
- `/compact` invokes the real compaction lifecycle, not a synthetic prompt.
- Slow/coalesced presentation updates converge to the command result visible
  in the authoritative current view.
- Unsupported lifecycle commands identify the safe available workflow instead
  of claiming success or mutating frontend-only metadata.

#### P4 exit gate

The mandatory TUI can create/resume threads, stream work through the current
presentation subscription, render tools and repository changes, interrupt
safely, and restore the terminal. The resume picker and active-thread TUI each
own one current in-process view and do not reintroduce the removed speculative
IPC protocol. This is an alpha-quality coding agent; long-session continuity
remains a release blocker.

Completed by the [P4 exit record](local-coding-agent-p4-exit.md). P5 remains
required before the local coding agent is release-ready.

### P5 — Coding compaction and resume continuity

Goal: make multi-hour and multi-day coding threads remain useful across context
pressure, repeated compaction, and process restart.

#### P5.1 — Coding compaction policy

Dependencies: P0.3, P2.1

Implementation contract:
[`local-coding-agent-p5-compaction-policy.md`](local-coding-agent-p5-compaction-policy.md)

Scope:

- Make Seahorse summary policy selectable by runtime context profile.
- Add the coding summary contract without changing personal-summary behavior.
- Version the policy so derived summaries can be invalidated and rebuilt.
- Preserve tool-result projection and recent complete-turn guarantees.
- Add deterministic, tool-aware output elision before model-generated
  summarization while leaving canonical JSONL untouched.
- Protect mutation and validation evidence from generic size-based elision.

Done when:

- Personal and coding summaries use their intended policies.
- A policy version change causes safe derived-state reconciliation.
- Coding summaries preserve explicit validation status and next action.
- Large historical tool output becomes bounded without losing tool outcome,
  failure evidence, or artifact references.

#### P5.2 — Workspace re-anchoring after compaction

Dependencies: P2.3, P5.1

Implementation contract:
[`local-coding-agent-p5-workspace-reanchoring.md`](local-coding-agent-p5-workspace-reanchoring.md)

Scope:

- Inject a fresh bounded workspace snapshot after compacted context is assembled.
- Mark deterministic snapshot fields separately from model-generated summary.
- Refresh on resume and relevant repository mutations.
- Resolve conflicts in favor of live repository state.

Done when:

- A branch/HEAD/file change made outside MintClaw between turns is reflected on
  resume.
- Stale summary claims cannot override the fresh snapshot.
- Snapshot failure is visible and bounded.

#### P5.3 — Compaction lifecycle and ownership

Implemented contract:
[`local-coding-agent-p5-compaction-lifecycle.md`](local-coding-agent-p5-compaction-lifecycle.md)

Dependencies: P3.1, P5.1

Scope:

- Implement proactive, post-turn, emergency, and manual triggers for coding.
- Correlate compaction with transcript revision and thread identity.
- Define interaction with active-turn state, leases, shutdown, and cancellation.
- Add started, progress/no-progress, completed, interrupted, and failed frontend
  events.

Done when:

- Background work deduplicates per thread.
- Manual and emergency compaction cannot race a transcript mutation.
- Process exit during derived compaction leaves canonical history intact.
- No-progress results do not loop indefinitely.

#### P5.4 — Rebuild and resume recovery

Dependencies: P2.5, P5.2, P5.3

Implementation evidence:
[`local-coding-agent-p5-resume-recovery.md`](local-coding-agent-p5-resume-recovery.md)

Scope:

- Reconcile coding Seahorse state against canonical thread revisions on open.
- Rebuild missing/corrupt/outdated derived state.
- Define bounded degraded startup for large histories.
- Preserve interrupted or incomplete turn semantics without replaying side
  effects.

Done when:

- Deleting the coding Seahorse database does not lose thread continuity.
- Crash points around summary creation and metadata update recover safely.
- A trailing incomplete tool call is shown as interrupted/unknown and is not
  automatically re-executed.

#### P5.5 — TUI compaction experience

Dependencies: P4.3, P5.3

Implementation evidence:
[`local-coding-agent-p5-compaction-experience.md`](local-coding-agent-p5-compaction-experience.md)

Scope:

- Show background and blocking compaction distinctly.
- Report trigger, context use, tokens before/after, summaries created, duration,
  and failure/no-progress state.
- Keep the composer usable during safe background work.
- Explain when starting a new focused thread is preferable after many
  compactions.

Done when:

- Users can tell whether the turn is waiting on compaction.
- Failure messages say whether work can continue.
- Manual compaction has one unambiguous terminal result.

#### P5.6 — Long-session deterministic evaluation

Dependencies: P5.4, P5.5

Implementation evidence:
[`local-coding-agent-p5-long-session-evaluation.md`](local-coding-agent-p5-long-session-evaluation.md)

Scope:

- Build a synthetic repository and multi-turn coding transcript large enough
  for leaf and condensed compaction.
- Include edits, failed and passing tests, rejected approaches, large tool
  output, media references, restart, and external repository mutation.
- Evaluate semantic continuity and strict context bounds.
- Record runtime and token baselines without making timing assertions flaky.

Done when:

- The complete compaction evaluation contract in this roadmap passes.
- The scenario runs deterministically with a fixture provider.
- Tests demonstrate two compaction generations and restart reconciliation.

#### P5 exit gate

The coding agent can sustain a long thread, compact in the background, recover
from emergency overflow, restart from files, and continue with both historical
intent and current repository state. This is the minimum beta gate.

Completed by the [P5 exit record](local-coding-agent-p5-exit.md). P6 and later
roadmap work remains outside this beta continuity gate.

### P6 — Coding UX completion

Goal: fill the high-value gaps after the native TUI and continuity model are
stable.

#### P6.1 — Thread rename, archive, delete, and fork

Dependencies: P4.4, P5.4

Implementation contract: [P6.1 thread lifecycle](local-coding-agent-p6-thread-lifecycle.md).

Scope:

- Add explicit lifecycle operations with atomic metadata updates.
- Fork from the latest state or a selected stable user-turn boundary, recording
  source thread, source revision, and source message identity.
- Keep conversational fork semantics separate from workspace rollback; a fork
  starts from the live filesystem unless an explicit future checkpoint feature
  says otherwise.
- Make deletion recoverable where the platform supports trash.
- Never delete a repository or project file.

Done when:

- Picker state reflects lifecycle changes immediately.
- Forked threads have independent writers and future history.
- A historical fork cannot imply that files were reverted to that historical
  message.
- Delete confirmation identifies only MintClaw-owned files.

#### P6.2 — Historical thread search

Dependencies: P5.4

Scope:

- Search thread title/preview and bounded transcript or Seahorse content.
- Scope to current project by default.
- Preserve privacy boundaries across project roots.
- Support expansion of selected historical results.

Done when:

- Search cannot leak another project without an explicit all-project action.
- Results identify source thread and time.
- Output is bounded and cancellable.

#### P6.3 — Rich input and attachments

Dependencies: P4.2

Scope:

- Add file references, pasted logs, and supported image attachments without
  embedding historical base64 in every future prompt.
- Store copied durable attachments in a content-addressed MintClaw blob area,
  while recording explicit availability for external path references.
- Define ownership, deduplication, retention, redaction, and garbage collection
  for blobs shared by multiple threads.
- Define missing attachment behavior after restart.

Done when:

- Old images or logs are loaded only when selected or contextually required.
- Missing attachments do not corrupt the thread.
- Prompt-size accounting includes selected media.
- Deleting one thread cannot remove a blob still referenced by another thread.

#### P6.4 — Git review and change summaries

Dependencies: P2.3, P4.3

Scope:

- Add explicit status, diff, and review actions.
- Render file-level changes and bounded hunks.
- Distinguish pre-existing user changes from changes observed during the thread.
- Never reset or discard changes implicitly.

Done when:

- The agent and UI do not claim ownership of pre-existing changes.
- Review output links findings to current paths and line positions where stable.
- Large diffs remain bounded and navigable.

#### P6.5 — Structured code intelligence

Dependencies: P2.7, P6.4

Scope:

- Add an optional LSP-backed tool for diagnostics, definitions, references,
  symbols, hover, and previewable rename operations.
- Detect applicable servers without installing or executing repository-provided
  binaries implicitly.
- Bound startup, request, idle, output, and shutdown lifecycles per project.
- Route LSP workspace edits through the same write audit, repository refresh,
  cancellation, and frontend event paths as ordinary coding tools.
- Treat LSP as an enhancement: projects without a supported server remain fully
  usable through read/search/edit/exec.

Done when:

- Diagnostics and navigation work on deterministic fixtures for at least two
  language-server families.
- Rename preview and apply handle multi-file edits, stale documents, and partial
  failure without bypassing write audit.
- Missing, crashing, or slow servers degrade cleanly and do not block startup.
- Structured code intelligence demonstrates measurable benefit over baseline
  search/edit fixtures before being enabled by default.

#### P6 exit gate

The beta UX supports normal thread lifecycle, historical discovery, rich input,
trustworthy repository review, and optional evidence-backed code intelligence
without expanding into remote execution.

### P7 — Non-interactive and always-on extensions

Goal: add automation and optional persistence beyond one foreground TUI without
making the initial local design dependent on a daemon.

#### P7.1 — Non-interactive `code exec`

Dependencies: P3.1, P5.4

Scope:

- Add stable text and JSONL event output.
- Support new and resumed thread execution.
- Define exit codes for success, model failure, tool failure, interruption,
  context failure, and invalid project.
- Never print the banner or terminal control sequences in machine mode.

Done when:

- JSONL is schema-versioned and parseable under all terminal outcomes.
- Automation can resume by ID without a TTY.
- Signals and command cancellation return stable exit semantics.

#### P7.2 — Optional local coding daemon investigation

Dependencies: P7.1

Scope:

- Evaluate whether a local daemon materially improves warm startup, background
  tasks, MCP reuse, or remote attachment.
- Compare a supervised one-shot coding worker with a persistent daemon behind
  the same project/thread/task interface.
- Define local authentication, protocol versioning, process ownership, upgrade,
  crash recovery, and project filesystem authority.
- Compare in-process CLI, daemon, and existing gateway/node approaches.
- Keep the paired-node companion as a capability host: it may launch or contact
  the coding worker but does not absorb agent-loop or thread-store ownership.

Done when:

- A design decision is recorded with measured startup/resource evidence.
- The chosen worker boundary can start, resume, status, steer, and cancel a
  coding thread without depending on terminal UI state.
- No daemon is added unless it has a bounded product benefit.
- The foreground local path remains supported.

#### P7.3 — Multi-agent coding and worktrees

Dependencies: P6.1, P7.1

Scope:

- Evaluate isolated worktree-per-agent execution.
- Define change ownership, merge/rebase handoff, cancellation, and cleanup.
- Prevent concurrent agents from modifying the same worktree by default.

Done when:

- Each agent has an explicit execution root and transcript owner.
- Worktree cleanup cannot delete user work.
- Conflicts surface to the user rather than being silently resolved.

#### P7.4 — Channel-to-coding task handoff

Dependencies: P7.2, P7.3

The Node Companion side of this packet is constrained by the
[`P8b remote-coding checkpoint`](node-companion-p8b-remote-coding-checkpoint.md).
P8b is not a parallel roadmap: this P7.4 packet owns the eventual complete
vertical slice. Implementation remains unadmitted until the checkpoint's
merged readiness gate is met and a focused P7.4 admission replaces the
checkpoint status.

Scope:

- Add an owner-scoped live-agent tool for starting, inspecting, steering, and
  cancelling a coding task on an allowed paired target and project alias.
- Register project aliases on the development machine; never accept an
  unrestricted model-authored filesystem path. Registration declares allowed
  task modes such as read-only investigation or isolated-worktree mutation.
- Create the durable parent task and idempotency identity before dispatch, then
  map it to one native `CodingThread` and one isolated execution root.
- Define versioned node commands for project discovery and coding task
  start/status/steer/cancel/result, with bounded progress and artifacts.
- Route questions through durable human interaction and route semantic progress
  and the final `DeliverableReport` through existing task delivery.
- Allow an idle completed/paused thread to appear in local `mintclaw resume`;
  allow read-only observation while running without granting a second writer.
- Keep `codex-cli`, ACP/ACPX, and generic `system.exec.v1` as optional adapters
  or bootstrap paths, not the native protocol contract.

Done when:

- An allowed Telegram sender can start a root-cause or fix task, receive an
  immediate task ID, request status, answer a blocking question, cancel it, and
  receive one deduplicated final result.
- A mutating task defaults to an isolated execution root; an investigation mode
  cannot silently escalate to file writes.
- The result includes thread ID, project/target aliases, changed paths,
  validation outcomes, branch/patch/artifact references, and unresolved work.
- Disconnect after dispatch recovers through the node invocation ledger and
  coding task status; it never blindly starts a duplicate coding task.
- An offline node, stale project alias, busy thread, ambiguous project, or
  uncertain result is explicit and actionable in chat.
- The gateway never reads or writes the development checkout and cannot escape
  the node-local project allowlist.
- Per-tool prompts are unnecessary inside an admitted task, while sender,
  target, project, and task-mode policy remain enforced at dispatch.

#### P7.5 — Coding-session access to paired companions

Dependencies: P7.1, P7.4

Scope:

- Define a `CodingRemoteCapabilityBroker` boundary for bounded discovery,
  invoke, status, progress, cancellation, and artifact references.
- Add an authenticated same-user local IPC client to the existing gateway node
  registry and invocation coordinator. Do not expose private agent sessions,
  channel state, provider credentials, or a generic internal RPC surface.
- Add coding-profile configuration for exact target aliases, command families,
  project aliases, and task modes. Keep all remote access disabled by default.
- Project bounded, fresh remote capability descriptors into the model tool
  snapshot and make remote placement visible in the TUI.
- Use direct typed invocation for remote build, test, browser, service, and
  artifact operations. Use the P7.4 coding-task protocol when the remote
  machine must own repository reasoning or edits.
- Record remote invocation and coding-task references in the canonical coding
  transcript while retaining the gateway invocation store, companion ledger,
  and remote coding thread as their respective authorities.
- Define disconnect, cancellation, output truncation, policy change, stale
  catalog, and uncertain mutation behavior without blind replay.

Done when:

- A local coding thread can list only its allowed companion capabilities and
  invoke one read operation and one bounded mutating operation through the
  production gateway-to-companion path.
- A repository-owning remote coding task can be started, observed, steered,
  cancelled, and linked from the local thread without the local process
  claiming the remote cwd or becoming a second transcript writer.
- Same-user local IPC authentication, target policy, approved catalog, gateway
  invocation identity, and node-local policy are enforced end to end.
- The no-prompt coding UX applies only within the configured remote grant;
  privileged commands remain absent unless separately admitted.
- Gateway absence, node disconnect, denial, stale policy, cancellation, and
  uncertain outcome are explicit in both headless events and the TUI.
- Reconnect and UI retry observe the original invocation or task and never
  duplicate an accepted mutation.
- Local-only coding behavior and personal live-agent behavior remain unchanged
  when no coding remote grant is configured.

#### P7.6 — Workspace checkpoint and rewind investigation

Dependencies: P6.1, P6.4

Scope:

- Evaluate whether message-level undo should also offer an explicit filesystem
  checkpoint and rewind operation.
- Compare patch journals, content-addressed blobs, and an isolated Git object
  store without changing the user's branch, index, commits, or ignore rules.
- Define behavior for pre-existing changes, external edits after a checkpoint,
  untracked/ignored files, renames, large files, symlinks, and partial failure.
- Require preview, conflict reporting, and a recoverable inverse operation.

Done when:

- A design decision and measured large-repository cost are recorded.
- No implementation is admitted that can silently discard user or externally
  created changes.
- Conversational fork/revert remains truthful when filesystem rewind is
  unavailable or declined.
- Any implementation work is split into separately reviewed packets after the
  storage and conflict contracts are admitted.

### P8 — Hardening and release

Goal: make the coding surface supportable as a stable MintClaw feature.

#### P8.1 — Performance and startup budgets

Dependencies: P5.6

Scope:

- Measure cold start, warm start, catalogue listing, TUI first paint, first
  token, compaction, reconciliation, presentation latency, historical
  hydration, and memory use.
- Defer coding MCP startup and expensive discovery where safe.
- Bound transcript rendering and catalogue scans.

Done when:

- Budgets and representative baselines are documented.
- No optimization changes correctness or context ownership.
- Regressions have focused benchmarks or deterministic counters.

#### P8.2 — Cross-platform terminal and process verification

Dependencies: P4.5, P7.1

Scope:

- Verify macOS, Linux, Windows, SSH, tmux, narrow terminals, no-color, and
  common shells.
- Verify IME input, grapheme/cell-width behavior, bracketed paste, native
  scrollback or alternate-screen semantics, and long streaming output.
- Exercise path canonicalization, leases, process cancellation, and terminal
  restoration.
- Document unsupported combinations.

Done when:

- Every supported release target compiles.
- Platform-specific contract tests cover locks, paths, signals, and process
  groups where available.
- Terminal failures leave a usable shell.

#### P8.3 — Privacy, redaction, and local-state controls

Dependencies: P3.1, P5.4

Scope:

- Audit transcript, summaries, logs, picker previews, command output, and
  diagnostics for secrets and cross-project disclosure.
- Add retention and deletion configuration for coding state.
- Document trusted-local authority clearly.

Done when:

- Redaction tests cover environment variables, credentials, command arguments,
  diffs, and compaction summaries.
- Thread deletion removes or trashes all derived state associated with that
  thread.
- No telemetry includes source content by default.

#### P8.4 — Documentation and migration

Dependencies: all admitted release packets

Scope:

- Document commands, keybindings, storage, trust, resume, compaction, recovery,
  provider configuration, channel-to-coding delegation, and troubleshooting.
- Add schema migration and rollback guidance.
- Document differences between personal and coding sessions.

Done when:

- A new user can start and resume a project without reading architecture docs.
- Upgrade and rollback preserve canonical transcripts.
- Limitations and deferred remote features are explicit.

#### P8 exit gate

The coding surface meets documented performance, platform, privacy, recovery,
and migration requirements and can be presented as stable.

## Recommended Sequential PR Order

The default order is:

```text
P0.1 -> P0.2 -> P0.3 -> P0.4
                 |
P0.5 ------------+
                 v
P1.1 -> P1.2 -> P1.3 -> P1.4
                 v
P2.1 -> P2.2 -> P2.3 -> P2.4 -> P2.5 -> P2.6 -> P2.7
                 v
P3.1 -> P3.2 -> P3.3 -> P3.4
                 v
P4.1 -> P4.2 -> P4.3 -> P4.4 -> P4.5
                 v
P5.1 -> P5.2 -> P5.3 -> P5.4 -> P5.5 -> P5.6
                 v
P6.* -> P7.* -> P8.*
```

Within a phase, packets whose declared dependencies are satisfied may proceed
in parallel only when they do not edit the same contracts. The default should
remain sequential until P5 because runtime layout, thread identity, frontend
events, TUI state, and compaction ownership are foundational and easy to make
incompatible in parallel.

## Validation Matrix

Every production packet should select relevant rows from this matrix:

| Area | Required evidence |
| --- | --- |
| Runtime layout | Owner-scoped roots, personal data migration, no project pollution, construction rollback |
| Thread storage | Atomic metadata, JSONL durability, corrupt entry isolation, schema migration |
| Concurrency | Two-process lease contention, stale recovery, active-turn serialization, race tests |
| Tools | Exact catalogue, cwd, cancellation, durable start/result, crash recovery, write audit, tool pairing, bounded results, harness quality fixtures |
| Instructions | Precedence, nesting, lazy path-scoped attachment, cache invalidation, byte limits, symlinks, executable-resource isolation |
| Git | Non-Git, dirty, worktree, detached, unborn branch, external mutation |
| Frontend presentation | Authoritative current view, ordering, correlation, slow-subscriber convergence, bounded memory, redaction, streaming equivalence |
| TUI | Screen/scrollback contract, resize, IME, Unicode width, paste, scroll, interruption, restoration, tmux/SSH, no-color, non-TTY |
| Compaction | Tiered tool-output projection, protected evidence, budgets, recent turns, tool pairs, multiple levels, failure, rebuild, resume |
| Code intelligence | Optional-server startup, diagnostics, navigation, audited workspace edits, timeout/crash fallback |
| Chat handoff | Allowed sender/target/project, idempotent dispatch, progress, question/answer, cancel, offline recovery, deduplicated delivery, local resume |
| Coding remote capabilities | Local IPC identity, exact grants, discovery freshness, typed invoke, task delegation, cancel, disconnect, uncertainty, no replay |
| Privacy | Cross-project isolation, preview redaction, summary redaction, diagnostics |
| Cross-platform | Build, paths, locks, process groups, signals, terminal lifecycle |

Tests should prefer deterministic fixture providers and temporary repositories.
Live-provider tests may exist as opt-in smoke tests but are not acceptable as
the only evidence for persistence, compaction, or TUI correctness.

## Observability

Coding mode should expose privacy-safe measurements for:

- thread create, open, resume, mismatch, lock contention, archive, and delete;
- remote coding task create, dispatch, target/project resolution, progress,
  question, steer, cancel, completion, uncertainty, and delivery;
- coding-session remote discovery, grant denial, invoke, task delegation,
  disconnect, cancellation, uncertainty, and reconciliation;
- cold and warm startup duration;
- first paint and first model token;
- tool start/end, duration, outcome, and bounded output size;
- interrupted tool-start recovery and unknown-outcome classification;
- context window, mandatory reserve, selected history, selected summaries, and
  recent-tail degradation;
- compaction trigger, start, completion, no-progress, failure, tokens saved,
  summaries created, and duration;
- derived-state reconciliation and rebuild duration;
- frontend update coalescing, presentation latency, and lazy hydration; and
- terminal restoration failures.

Raw prompts, source code, diffs, command output, user text, and summary content
must not be included in metrics by default.

## Explicit Non-Goals for the Initial Release

- Full feature parity with OpenAI Codex.
- A port of Codex's Rust TUI, app server, rollout store, or sandbox.
- A complex approval selector or approve-once UI.
- Automatic execution of repository-provided extensions, hooks, or dynamic
  agent configuration merely because a project was opened.
- Direct remote editing by the deployed gateway during the initial release;
  P7.4 delegates to a project-bound worker on a paired machine instead.
- An unrestricted remote shell, automatic access to every paired companion, or
  using the live agent's model as a proxy for coding-session node invocations.
- Multi-agent concurrent writes to one working tree.
- Automatic commit, push, reset, rebase, or merge without an explicit user
  request.
- Automatic workspace rewind coupled to conversational fork or message undo.
- A debugger/DAP stack before the native coding runtime, TUI, compaction, and
  optional LSP path are stable.
- A project-local `.mintclaw` state directory.
- Replacing canonical JSONL with Seahorse or another derived database.
- Making generic personal memory part of coding context by default.
- Automatically injecting full diffs, complete logs, or all historical media.

## Risks and Mitigations

| Risk | Mitigation |
| --- | --- |
| P0 grows into a general runtime rewrite | Admit only root, construction, store, and tool-profile changes required by coding invariants |
| Coding mode changes gateway behavior | Compatibility mapping and full personal regression tests in every P0 packet |
| Trusted-local exposes unrelated capabilities | Exact coding tool allowlist independent of approval mode |
| Trusted-local is confused with project startup trust | Keep declarative instructions separate from executable repository resources |
| Two processes corrupt a thread | Cross-process single-writer lease before append |
| Crash leaves a tool looking successful or causes replay | Durable tool-start/result markers and interrupted/unknown resume repair |
| Summary hallucinates current repo state | Fresh deterministic workspace snapshot always wins |
| Compaction loses an important decision | Versioned coding policy, canonical transcript, deterministic continuity evaluation |
| TUI blocks the agent or sees stale state | Authoritative current views, bounded coalescing subscription, headless presentation tests |
| TUI corrupts terminal history or Unicode input | Admit one screen model and test IME, width, resize, tmux, SSH, and restoration |
| LSP adds slow or fragile startup | Optional lazy per-project lifecycle with timeouts and baseline-tool fallback |
| Workspace rewind discards user changes | Keep it post-MVP until checkpoint ownership, preview, conflict, and inverse semantics are admitted |
| Derived state slows resume | Revision watermarks, lazy/bounded rebuild, visible degraded mode |
| Remote gateway edits the wrong filesystem | Gateway sends only allowed target/project aliases; the node-local worker owns path, thread, and execution |
| Chat retry starts duplicate coding work | Create durable parent task and idempotency identity before dispatch; reconcile through node and coding-task ledgers |
| Telegram receives noisy or duplicate progress | Deliver semantic milestones and one structured final report through the existing durable delivery coordinator |
| Local coding silently gains fleet authority | Require exact coding-profile target/command grants and retain gateway plus node-local policy intersection |
| Local and live processes compete for a companion | Reuse the running gateway's node control plane through authenticated local IPC; never create a second pairing |
| Remote mutation is repeated after disconnect | Persist and reconcile the original invocation identity; surface uncertain instead of replaying |
| Existing Codex CLI provider becomes a nested agent | Do not use it as the native coding runtime; keep it optional as a provider or spike tool |

## Global Definition of Done

This roadmap is complete when:

1. `mintclaw code` opens a stable TUI in a project and executes through the
   native MintClaw agent engine.
2. `mintclaw resume` lists current-project threads and resumes one from durable
   files after a process restart.
3. Coding state never pollutes the project checkout.
4. Local coding runs without approval prompts while exposing only the coding
   tool profile by default.
5. Hierarchical project instructions and fresh repository state are present in
   every turn.
6. Tool activity, streaming answers, diffs, context use, and compaction are
   visible and interruptible in the TUI.
7. Long threads compact and continue through multiple compaction levels without
   losing objective, constraints, validation status, or next action.
8. Deleting derived Seahorse state and restarting rebuilds usable context from
   canonical JSONL.
9. Concurrent resume cannot create two writers for one thread.
10. Personal gateway, channel, routing, session, memory, and approval behavior
    remains compatible.
11. Non-interactive execution has stable output and exit semantics.
12. Supported platforms meet documented terminal, path, process, privacy,
    performance, recovery, and migration requirements.
13. A crash between side-effecting tool start and result persistence resumes as
    interrupted/unknown without automatic replay.
14. A slow frontend subscriber converges to the authoritative current view
    without corrupting the transcript or composer.
15. An allowed channel user can dispatch and manage a coding task on an allowed
    paired project, receive a deduplicated structured result, and later resume
    the same coding thread locally without giving the gateway filesystem
    ownership.
16. A local coding session can use explicitly granted typed capabilities on a
    paired companion through the existing durable node control plane, or
    delegate repository-owning work to a remote coding worker, without routing
    through the live agent's LLM or gaining implicit fleet authority.

## Handoff Rules for Implementing Agents

Before starting a packet:

1. Fetch the latest `origin/main` and verify that prerequisite packets are
   merged, not merely open.
2. Re-read the current runtime and any admission document because this roadmap
   records invariants, not frozen implementation details.
3. Declare the exact packet ID in the PR body.
4. Keep unrelated cleanup out of the PR.
5. Preserve canonical history, personal-runtime behavioral semantics, and
   project cleanliness even when disk paths intentionally change.
6. Add the packet's focused tests and the relevant validation-matrix evidence.
7. Record deferred observations under the later packet that owns them rather
   than expanding current scope.
8. Update architecture documentation whenever the implemented contract differs
   from a proposed type or package name in this roadmap.

Stop and request an architecture decision when:

- satisfying a packet requires changing one of the Executive Decisions;
- the same path or identity would again own both project execution and private
  runtime state;
- canonical JSONL would cease to be reconstructive authority;
- the TUI would need direct access to mutable `AgentLoop` internals;
- coding trusted-local mode would alter gateway approval behavior; or
- opening a repository would execute project-provided extension or hook code
  without an admitted trust decision; or
- a channel-to-coding design would accept a model-authored remote path, bypass
  sender/target/project policy, or make the gateway own the coding thread; or
- a local coding session would open a competing companion connection, inherit
  all live-agent node access, or use a model-to-model relay for typed remote
  invocation; or
- a remote component would gain filesystem authority not admitted by this
  roadmap.
