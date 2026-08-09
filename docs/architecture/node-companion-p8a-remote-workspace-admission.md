# Node Companion P8a Remote Workspace Admission

## Status And Decision

Status: admitted for implementation as one bounded live-agent vertical slice

P8a admits an explicitly named remote execution workspace for the existing
live AgentLoop. A compatible model tool call may name one operator-configured
workspace alias and run against one paired Linux or macOS companion. The first
slice covers bounded text read, search, whole-file write, structured patch,
foreground direct-argv execution, and the existing durable job lifecycle.

P8a is a routing and path-presentation layer. It does not move the AgentLoop,
model session, memory, channel delivery, approval authority, goal, task store,
or coding transcript to the companion. It reuses the existing target policy,
authenticated discovery, gateway invocation coordinator, durable approval,
WSS transport, companion ledger, P2 file-policy resolver, P5a job manager,
artifact spool, runtime events, and no-blind-replay rules.

The admitted selection is deliberately stateless: every remote-compatible
tool call carries the configured workspace alias. There is no sticky mutable
selection to persist across turns, approvals, `/new`, `/reset`, compaction, or
gateway restart. Omitting `workspace` preserves the current gateway-local tool
behavior exactly.

This is not remote coding-agent admission. Repository-owning autonomous work,
durable reasoning, isolated worktrees, questions, PR handoff, and resumable
coding threads remain under the local coding-agent roadmap.

## Why This Slice Is Worth Building

MintClaw already has the lower-level pieces, but the model must currently
coordinate them as unrelated node operations:

- P2 can inspect and transfer whole regular files, but cannot present bounded
  source text or workspace search results directly to ordinary file tools;
- `system.exec.v1` can run one bounded direct-argv command, but the model must
  repeatedly select a target and working scope;
- P5a can start, inspect, log, cancel, and retrieve artifacts from one durable
  process, but placement is expressed independently for each operation; and
- the local `read_file`, `write_file`, `apply_patch`, `search_files`, and
  `exec` tools always operate on the gateway host.

P8a gives those capabilities one configured placement and one relative path
namespace without introducing another process runner, transfer store, node
connection, or generic RPC layer. It makes small operator-directed work on a
remote checkout practical while retaining truthful placement and durable
effects.

## Operator And Model Experience

The operator configures a bounded alias:

```yaml
execution:
  remote_workspaces:
    mintclaw-vpn:
      target: vpn
      working_scope: mintclaw
      tools: [read_file, search_files, write_file, apply_patch, workspace_exec, jobs]
      revision: mintclaw-vpn-v1
```

`target` resolves through the existing `execution.targets` map. Its
`file_profile` and `job_profile` remain authoritative. `working_scope` is an
existing authenticated `system_exec` discovery alias. At dispatch, the
gateway and companion prove that this scope resolves beneath an allowed P2
file root and is admitted by the selected execution/job profiles.

Examples of model-visible calls are:

```json
{"path":"README.md","start_line":1,"max_lines":120,"workspace":"mintclaw-vpn"}
```

```json
{"pattern":"TODO","path":"pkg","file_glob":"*.go","workspace":"mintclaw-vpn"}
```

```json
{
  "workspace":"mintclaw-vpn",
  "executable":"go",
  "args":["test","./pkg/nodes/..."],
  "mode":"foreground"
}
```

The result always identifies placement as `remote`, the workspace alias,
target alias, effective revision, and invocation or job ID where applicable.
It never pretends that a remote path is the gateway process cwd.

## Whole-System Boundary

```text
live AgentLoop and routed model session (gateway)
                  |
                  | compatible tool + explicit workspace alias
                  v
        RemoteWorkspaceRouter
          | config + agent target policy
          | fresh authenticated catalog
          | exact workspace revision
          v
 gateway invocation coordinator + durable human approval
                  |
                  v
        existing authenticated WSS
                  |
                  v
       companion invocation ledger
          |                    |
          v                    v
 P2-confined workspace I/O   system.exec.v1 / P5a jobs
```

The router is the only new gateway routing seam. It decorates only admitted
tool schemas, resolves one alias into existing authority, prepares the exact
typed invocation, and projects the result back into the ordinary tool shape.
It never calls a model-facing tool from another model-facing tool and never
forwards arbitrary tool names or schemas.

## Configuration And Authority

`execution.remote_workspaces` is a bounded map of operator-owned aliases. Each
entry contains only:

- `target`: an existing execution target allowed to the agent;
- `working_scope`: an authenticated node discovery alias, never a raw caller
  path;
- `tools`: a subset of the fixed P8a compatibility set;
- `revision`: a bounded operator-changed revision; and
- optional tighter inline-read, inline-write, patch, search, result, and
  foreground-timeout limits, never values broader than node policy.

An agent sees only aliases whose target is in its effective `target_policy`.
There is no default remote workspace in P8a. A model cannot supply a node ID,
connection, root, profile, policy revision, executable path, environment
value, helper, or approval decision.

Authority is the intersection of:

1. authenticated inbound actor, route, routed session, and session epoch;
2. selected agent and gateway runtime workspace identity;
3. the operator-configured remote workspace alias and revision;
4. the agent's existing allowed target set;
5. current authenticated node catalog and discovery revision;
6. the execution target's file and job profile bindings;
7. the node-local file, system-exec, and job policies; and
8. the exact command input and provider tool-call/execution identity.

Missing, stale, changed, ambiguous, or disconnected authority fails closed.
An alias revision or catalog change after preparation invalidates retained
approval. The model cannot fall back to the local host after a requested
remote call fails.

Configuration defaults to an empty map. Existing configurations require no
migration. Invalid aliases, unknown targets, empty tool sets, duplicate or
unsupported tools, invalid revisions, and workspaces without the required
target profiles fail configuration validation.

## Selection And Lifecycle Semantics

P8a does not add a `select_workspace` state machine. Selection is an explicit
`workspace` argument on every admitted call:

- omitted or empty means the existing local implementation;
- a configured alias means the remote adapter;
- an unknown or unauthorized alias fails before dispatch; and
- tools outside the fixed compatibility set do not gain the argument and
  cannot be routed remotely.

This contract makes selection naturally scoped to one provider tool call. A
durable approval continuation resumes the already prepared invocation rather
than resolving the alias again. `/new`, `/reset`, session expiry, compaction,
steering, delegation, and restart require no mutable selection cleanup.

Subagents do not inherit remote authority merely because their parent used an
alias. They may use an alias only when their own effective agent/actor/session
policy admits it. Cron and background delegated agents are outside the first
slice unless they already provide the complete authenticated principal and
normal tool pipeline; there is no authority-only shortcut.

## Tool Compatibility Contract

### `read_file`

The existing schema gains optional `workspace`. Local semantics remain
unchanged when absent. Remote mode supports bounded UTF-8 line reads and byte
ranges through `workspace.read.v1`.

The companion resolves a relative path beneath the configured working scope
using the P2 descriptor-relative, no-follow resolver. Absolute paths,
traversal, NUL, symlinks, non-regular files, pseudo-filesystems, cross-mount
escapes, and files outside the selected P2 readable profile are denied.
Results preserve line numbering, byte/line truncation, size, and SHA-256 or
content revision needed for later mutation. Binary content is not returned
inline; the model is directed to the existing P2 download/artifact path.

### `search_files`

The existing schema gains optional `workspace`. `workspace.search.v1`
supports the current bounded content/file-name search shape: regex pattern,
relative path, optional file glob, output mode, context, result limit, and
explicit ignored-file inclusion.

Search runs in companion code under the same confined root. It does not invoke
`rg`, `grep`, `find`, a shell, or caller-selected binaries. It bounds visited
entries, bytes read, individual file size, matches, result bytes, depth, and
wall time. It does not follow symlinks. `.gitignore` behavior and default noisy
directory exclusions must match the local tool closely enough for the shared
description to be truthful; any deliberate difference is surfaced in the
result, not hidden.

### `write_file`

The existing schema gains optional `workspace`. `workspace.write.v1` supports
whole-file `create` and `replace`, corresponding to local `overwrite=false`
and `overwrite=true`. Content is bounded UTF-8 in the exact prepared command;
large or binary writes use P2 artifacts instead.

Publication reuses P2 staging, hash verification, no-follow resolution, and
atomic rename semantics. Replacement binds the observed destination identity
or SHA-256 when supplied by a preceding read. Create refuses an existing
name. A remote write never widens `allow_create`, `allow_overwrite`, writable
roots, maximum bytes, or approval from the target's P2 profile.

### `apply_patch`

The existing schema gains optional `workspace`. `workspace.patch.v1` accepts
the current bounded Codex patch format for add, update, and delete operations;
move remains unsupported.

The companion parses and applies the patch inside the confined workspace. It
prepares all resulting file contents before publication, validates every path
and precondition, then publishes in deterministic order. Since portable
multi-file filesystem transactions do not exist, a crash may leave a proven
prefix committed. The durable invocation result reports each committed path;
an unprovable post-dispatch outcome is `unknown` and is never replayed. The
tool must not claim multi-file atomicity. A single-file replacement is atomic.

The operation is bounded by patch bytes, file count, per-file bytes, total
prepared bytes, and timeout. Delete, create, and replacement require the
corresponding P2 writable authority and exact human approval when configured.

### `workspace_exec`

Remote execution has a dedicated model tool because local `exec` accepts an
ordinary shell command string while admitted `system.exec.v1` deliberately
accepts direct argv and forbids shells. Pretending those schemas are
interchangeable would be unsafe and confusing.

Input is limited to:

- `workspace`;
- authenticated executable alias;
- bounded argument array;
- allowlisted environment names/values under existing system-exec policy;
- `mode`, exactly `foreground` or `job`;
- bounded timeout; and
- for job mode, the existing declared artifact-relative paths.

Foreground mode is a thin adapter over existing `system.exec.v1`. Job mode is
a thin adapter over existing P5a `job.start.v1`. Both derive target and working
scope from the workspace alias. No shell text, executable path, cwd, profile,
or target is caller-selectable. Existing P5a status, logs, cancel, and artifact
operations remain authoritative; P8a may add workspace-scoped presentation
adapters only where they remove repeated target/scope input without changing
job identity or lifecycle.

P5b owner-shell jobs are not admitted by this document. Operators may keep
using the separately admitted owner shell/terminal tools, but those calls do
not become remote-workspace calls.

## Approval, Dispatch, Recovery, And No Replay

The router participates in the existing pipeline approval-plan interface. It
prepares one canonical node invocation before asking for approval. Approval
text identifies workspace alias, target alias, operation class, bounded path
or executable alias, and effect summary without exposing content, argv values,
environment values, or credentials.

Approval is never performed by recursively calling `nodes_invoke` or another
model tool. The approval continuation reuses the same plan, invocation ID,
workspace revision, discovery revision, principal, tool-call identity, and
content digest. Changed arguments or authority create a different request and
cannot consume the answer.

Read and search are semantically retryable only before durable dispatch; the
normal invocation status path should still recover the original result when
possible. Write, patch, foreground exec, and job start are mutations:

- denial before dispatch is a proven non-effect;
- after the dispatch boundary, disconnect or timeout is `unknown` unless the
  companion ledger proves a terminal result;
- restart reconciliation queries the original invocation;
- duplicate delivery returns the ledger result and does not re-execute; and
- no gateway, model, steering, or UI retry automatically prepares a second
  mutation.

Job start returns the existing stable job ID. Later status, logs, cancel, and
artifact calls use the P5a ownership and retention contract. Cancel does not
erase the starting invocation or convert an uncertain outcome into a proven
failure.

## Result Truth And Model Guidance

Every remote result includes a bounded placement envelope:

```json
{
  "placement":"remote",
  "workspace":"mintclaw-vpn",
  "target":"vpn",
  "workspace_revision":"mintclaw-vpn-v1",
  "state":"completed",
  "invocation_id":"inv_opaque"
}
```

The envelope may additionally contain relative paths, bounded text/search
matches, changed-file summaries, exit classification, job ID, truncation, and
safe recovery action. It excludes host connection details, raw configured
roots unless already deliberately projected by current node discovery,
credentials, unrestricted environment, and unbounded output.

The prompt/tool descriptions state:

- use the explicit alias for every remote operation;
- paths are relative to that workspace;
- use `workspace_exec`, not local `exec`, for direct remote argv;
- use job mode for work that can outlive one foreground timeout;
- use P2 artifact transfer for binary or large files; and
- delegate a `CodingTask` when the remote machine must own autonomous
  repository reasoning rather than simulating it through many file calls.

## Security, Redaction, And Retention

Node-local policy remains the final authority even if the gateway or model is
compromised. The first slice must preserve P2 protections against traversal,
symlink and rename races, special files, pseudo-filesystems, unauthorized
mounts, and oversized content. Search must not turn ignored files or adjacent
roots into an enumeration oracle.

Runtime events record only typed operation, workspace/target hashes or safe
aliases, invocation/job identity, lifecycle, counts, sizes, duration,
truncation, approval outcome, and fixed error codes. Events and ordinary logs
exclude file contents, patches, search patterns and matching lines, argv and
environment values, absolute host paths, artifact bytes, credentials, and WSS
details. Diagnostic traces follow the same redaction boundary.

Prepared command content exists only in the existing bounded durable
invocation/approval records for their configured retention and is protected as
sensitive operational state. Search/read results follow normal bounded tool
transcript retention. P8a adds no audit database, artifact store, transcript,
or second retention system.

## Linux And macOS Contract

The unprivileged slice must work on Linux amd64/arm64 and macOS amd64/arm64
using the companion's existing native path and process implementations.
Platform behavior must agree on relative path presentation, no-follow
confinement, result bounds, direct argv, job identity, cancellation truth, and
unknown outcomes.

Linux administrator file helpers remain usable only through an explicitly
configured P2 profile and its existing approval policy. P8a does not add a new
privileged helper verb. Privileged macOS filesystem access remains outside the
admitted scope.

## Explicit Non-Goals

P8a does not admit:

- a remote AgentLoop, provider, memory store, session, goal, task registry, or
  channel delivery process;
- remote `CodingTask`/`CodingThread`, autonomous PR creation, isolated
  worktrees, or coding-TUI attachment;
- sticky workspace selection, automatic local/remote fallback, mirroring,
  synchronization, seeding, conflict resolution, or deletion propagation;
- generic tool proxying, arbitrary RPC, arbitrary shell, P5b shell jobs,
  interactive PTY, or stdin streaming;
- browser, MCP, camera, desktop, clipboard, or other P7 workspace routing;
- directory transfer, archive extraction, mounts, Docker, sandbox executors,
  fleet placement, scheduling, bootstrap, SSH transport, or more platforms;
- arbitrary absolute paths, caller-selected roots/profiles/targets, or
  automatic approval; or
- refactors of local tools unrelated to the one routing seam.

## Required PR Sequence

Every implementation PR starts from fresh merged `main` and is independently
valid. Dependent branches are not stacked.

### PR 1: Workspace contract and bounded read/search vertical

- Add config/domain validation for operator-owned remote workspace aliases.
- Add the single router seam and optional `workspace` schema decoration for
  `read_file` and `search_files` only.
- Add authenticated `workspace.read.v1` and `workspace.search.v1` using the
  existing P2 resolver and execution-target binding.
- Preserve exact local behavior when `workspace` is absent.
- Prove agent/actor/session/target isolation, discovery and revision freshness,
  path confinement, bounds, redaction, disconnect truth, and Linux/macOS
  focused behavior.

### PR 2: Bounded write and patch

- Add remote adapters and typed `workspace.write.v1` and
  `workspace.patch.v1`.
- Reuse P2 publication, approval, plan, ledger, restart recovery, and no-replay
  semantics.
- Prove create/replace/delete authority, hash/identity conflict handling,
  partial multi-file truth, duplicate/concurrent dispatch, timeout/cancel
  races, and fail-closed policy changes.

### PR 3: Foreground exec and durable jobs composition

- Add `workspace_exec` as a thin direct-argv adapter over `system.exec.v1` and
  P5a `job.start.v1`.
- Derive target and working scope solely from the workspace alias.
- Reuse existing P5a job status, logs, cancel, and artifacts; add only the
  smallest workspace presentation needed for model usability.
- Prove foreground success/failure/unknown, job persistence across turns and
  restart, logs/artifacts/cancel, exact ownership, and no second process start.

### PR 4: Real vertical-slice proof, docs, and deployment

- Add deterministic real-process Linux and macOS canaries covering
  read-search-write-patch, foreground exec, durable job, reconnect/status, and
  one uncertain mutation without replay.
- Validate merged main, update architecture/operations/config documentation,
  and record a requirement matrix with package, test, and deployment evidence.
- Deploy deny-by-default configuration, then enable one bounded operator
  workspace on `vpn` and one on `ab-local-test` without restarting unrelated
  reviewer or browser workspaces.
- Verify health, redaction, tool discovery, local-default behavior, and no
  duplicate effect.

No review correction may silently add a prerequisite protocol, store,
transport, generic workspace framework, remote coding worker, or P7 routing.

## Validation Matrix

At minimum the implementation must cover:

| Area | Required evidence |
| --- | --- |
| Config | empty default, invalid/unknown/duplicate aliases, unsupported tools, target-policy intersection |
| Selection | explicit per-call alias, local omission unchanged, no fallback, no sticky state across lifecycle commands |
| Freshness | stale discovery, changed workspace/profile revision, disconnect/reconnect, approval continuation |
| Filesystem | traversal, symlink, rename race, cross-root/mount denial, special files, bounds, binary refusal |
| Read/search | pagination, line numbers, ignored files, limits, timeout, deterministic ordering, truncation |
| Write/patch | create/replace/delete, conflicts, multi-file partial truth, duplicate/concurrent calls, no replay |
| Exec | alias-only argv, forbidden shell/path/cwd/env, foreground timeout, result truncation, uncertain outcome |
| Jobs | one start, durable status/logs/cancel/artifacts, restart recovery, workspace ownership projection |
| Ownership | wrong actor, agent, route, routed session, session epoch, workspace, target, tool call, execution ID |
| Approval | allow/deny/timeout/cancel, exact plan reuse, changed args/authority, no model self-approval |
| Observability | typed redacted events, no content/patch/search/argv/env/credential leakage |
| Compatibility | Linux/macOS focused and race tests, local tools unchanged, unrelated node/browser behavior healthy |

Run focused race tests for touched config, tool, node protocol, companion,
gateway invocation, approval, P2, P5a, and WSS packages; tagged lint; relevant
broad tests; and CI. Tests must be deterministic and avoid timing-only
assertions.

## Architecture Checkpoints

Stop patching and report evidence before continuing when any of these occurs:

- four substantive review/fix cycles on one PR;
- the same lifecycle or security invariant is challenged three times;
- production code grows to twice that PR's admitted baseline;
- correctness appears to require sticky workspace state, a second gateway
  store, a new transport, a generic durable-tool abstraction, or a second job
  manager;
- P2 confinement cannot be reused without weakening it;
- local and remote tool semantics cannot be made truthful under one model
  schema; or
- reliable mutation recovery would require blind replay.

At a checkpoint, prefer narrowing or deleting scope. Do not invent another
prerequisite PR without explicit user approval.

## Definition Of Done And Mandatory Stop

P8a is complete only when all of the following are evidenced:

1. one operator alias selects exactly one allowed target and authenticated
   working scope without caller-supplied connection, root, or profile;
2. ordinary read/search/write/patch calls can explicitly use that alias while
   omitted aliases retain current local behavior;
3. `workspace_exec` performs bounded foreground direct argv and starts the
   existing durable P5a job without another runner or lifecycle;
4. files and processes share the same configured remote working scope;
5. approval binds and resumes the exact prepared mutation once;
6. stale authority, disconnect, duplicate calls, restart, and post-dispatch
   uncertainty never cause automatic replay or local fallback;
7. P2 confinement, limits, publication, transfer/artifact compatibility, and
   P5a status/log/cancel/artifact semantics remain authoritative;
8. typed redacted events make placement and lifecycle auditable without
   exposing content, patch, search, argv, environment, paths, or secrets;
9. deterministic Linux and macOS real-process canaries prove the complete
   model-tool-to-companion path and local-default compatibility;
10. all admitted PRs are merged, merged main is validated, bounded production
    profiles are healthy on `vpn` and `ab-local-test`, and documentation matches
    behavior.

Immediately mark the implementation goal complete and stop when these gates
are met. Do not begin remote coding workers, P5b shell jobs, browser/MCP
workspace routing, synchronization, Docker, fleet, bootstrap, P9, or other
deferred work under this admission.
