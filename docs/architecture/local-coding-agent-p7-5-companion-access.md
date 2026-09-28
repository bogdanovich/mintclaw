# Local Coding Agent P7.5: Companion Capability Access

Roadmap packet:
[P7.5 — Coding-session access to paired companions](local-coding-agent-roadmap.md#p75--coding-session-access-to-paired-companions).

Status: implementation admitted from the completed P7.1, P7.4, P7.7, and
Node Companion P8a foundations.

Audit baseline: `origin/main` at
`49bc4b5c2b9ce2dbcbda0ddefa84e16258165dad`, 2026-09-27.

## Decision

An explicitly configured local `mintclaw code` or `mintclaw code exec`
session may use bounded capabilities on companions already paired to a
same-host MintClaw gateway. The coding runtime connects to that gateway over
an authenticated owner-only Unix-domain socket. The gateway remains the sole
owner of node discovery, pairing, authenticated WSS sessions, invocation
preparation, and restart reconciliation.

P7.5 supports two distinct operations:

1. A **direct remote capability** runs one configured typed node operation.
   The local `CodingThread` remains the reasoning and transcript owner and
   stores a bounded reference to the gateway invocation and result.
2. A **remote coding task** runs the existing P7.4/P7.7 native coding worker
   when the companion must own repository reasoning, an isolated worktree, or
   machine-wide execution. The remote `CodingThread` owns that work. The local
   thread stores only a task link and may observe, steer, answer, or cancel it.

There is no model-to-model relay, second companion connection, hidden gateway,
remote cwd mounted into the local process, generic gateway RPC, or copy of a
remote transcript. Codex and ACPX remain optional adapters and are not part of
this path.

The first release is intentionally same-host: the coding CLI and gateway must
run as the same operating-system user on one Linux or macOS machine. It does
not tunnel the local IPC protocol over SSH, Tailscale, TCP, Telegram, or a
companion. A user running the TUI on another machine may SSH to the gateway
host and run `mintclaw code` there; a network client protocol requires a
separate security admission.

## Why the current architecture fits

The required execution authorities already exist:

- `nodeAdmissionRuntime` owns the approved node registry, connected session
  hub, gateway invocation store, and WSS dispatch path.
- `NodeInvocationSource` prepares before dispatch, binds the current approved
  descriptor and policy revision, queries the original invocation after
  uncertainty, and does not blindly replay mutations.
- P8a remote workspaces already adapt bounded read, search, write, patch,
  direct-argv execution, durable jobs, and artifact references to typed node
  commands without pretending the gateway has a remote cwd.
- `CodingNodeInvoker` already provides the narrow internal adapter for
  `coding.*.v5` discovery, start, status, steer, and cancel operations.
- P7.4/P7.7 already own remote scope/profile validation, native worker
  lifecycle, questions, compaction, worktree isolation, publication, machine
  authority, terminal reports, and companion-ledger recovery.
- Local coding already has one canonical file-backed transcript, one writer
  lease, Seahorse compaction, headless JSONL events, and a TUI projector.

The missing boundary is therefore small but security-sensitive: a local
authenticated broker protocol, exact coding-client grants, coding-only tools,
and truthful projection of the existing remote authorities. P7.5 must not
move node runtime internals into `cmd/mintclaw/internal/coding` or register the
ordinary live-agent `nodes*` tools in an isolated coding AgentLoop.

## Whole-system architecture

```text
local TUI / code exec
  -> local CodingThread and one writer lease
  -> coding-only remote capability/task tools
  -> CodingRemoteCapabilityBroker client
  -> same-user Unix-domain socket
  -> gateway CodingRemoteCapabilityBroker server
       -> exact local-coding grant + agent target policy
       -> current approved catalog + descriptor revision
       -> existing gateway invocation store
       -> existing authenticated WSS session
  -> paired companion invocation ledger
       -> typed capability handler, or
       -> P7.4/P7.7 native coding worker and remote CodingThread
```

The gateway is a control-plane broker, not a remote filesystem proxy. The
local process never receives a companion's absolute configured roots,
credentials, executable paths, node ID, private channel state, provider
configuration, or remote transcript.

## Ownership matrix

| State | Sole authority |
| --- | --- |
| Local conversation, compaction, local project, and local writer | Local canonical `CodingThread` and lease |
| IPC connection admission and effective local-client grant | Gateway broker and immutable grant snapshot |
| Paired target identity, approved catalog, and connection freshness | Existing gateway node registry and session hub |
| Prepared dispatch, policy revision, and gateway-side uncertainty | Existing gateway invocation store |
| Accepted command, process lifecycle, cancellation, and node restart recovery | Existing companion invocation ledger and typed handler |
| Direct-operation reasoning and result reference | Local coding transcript |
| Remote repository reasoning, worktree, questions, and worker result | Remote P7.4/P7.7 coding thread, worker, and worktree owners |
| TUI and headless presentation | Rebuildable projection from local transcript and current broker observations |

No convenience cache becomes a second authority. A derived local index of
active remote references is allowed only when it is fully rebuildable from
canonical transcript tool records.

## Configuration and deny-by-default grants

P7.5 adds optional, bounded configuration in three layers. Exact public field
names are frozen in the first configuration PR, but the authority split is
not negotiable:

1. A gateway IPC listener is disabled by default and has one explicit socket
   path.
2. A local coding client selects one explicit broker endpoint and grant alias.
   The selection happens before AgentLoop construction and is not model input.
3. A gateway grant binds one revision, one configured agent identity used for
   target policy, an allowed local coding profile set, exact direct capability
   aliases, exact remote coding scope aliases, and exact remote task profiles.

One representative shape is:

```yaml
gateway:
  coding_remote:
    enabled: true
    socket_path: /run/user/1000/mintclaw/coding-remote-v1.sock

coding:
  remote:
    enabled: true
    socket_path: /run/user/1000/mintclaw/coding-remote-v1.sock
    grant: local-development

execution:
  coding_remote_capabilities:
    ab-build-workspace:
      revision: ab-build-workspace-v1
      kind: remote_workspace
      remote_workspace: ab-build
      operations:
        - read_file
        - workspace_exec
        - job_status
        - job_logs
        - job_artifacts
        - job_cancel
    ab-service-status:
      revision: ab-service-status-v1
      kind: node_command
      target: ab-2
      operations: [service.status.v1, service.logs.v1, service.action.v1]
    ab-browser:
      revision: ab-browser-v1
      kind: browser_profile
      target: ab-2-browser
      browser_profile: automation
      operations:
        - browser_open
        - browser_status
        - browser_close
        - browser_context_list
        - browser_context_open
        - browser_context_select
        - browser_context_close
        - browser_observe
        - browser_diagnostics
        - browser_act
  coding_remote_grants:
    local-development:
      revision: local-development-v1
      agent: coding
      local_profiles: [mutate]
      capabilities: [ab-build-workspace, ab-service-status, ab-browser]
      tasks:
        - scope: mintclaw-dev
          profiles: [investigate, mutate, project-yolo]
```

Capability definitions live in a separate bounded map. A capability alias
resolves server-side to one existing target
and one exact typed adapter, descriptor, workspace, browser profile, service
profile, job profile, or artifact operation. The model never supplies an
absolute path, node ID, connection, raw executable, provider credential,
profile revision, or arbitrary gateway method.

The effective authority is the intersection of:

1. successful same-user IPC authentication;
2. client-selected grant alias and exact grant revision;
3. local thread ID, canonical coding session key, and execution profile;
4. configured gateway agent identity and its target policy;
5. exact capability or remote coding scope alias in the grant;
6. current target mapping and current connected node identity;
7. current approved catalog hash and exact command descriptor;
8. selected target profiles and node-local policy; and
9. one stable local tool-call/execution identity.

An `investigate` local profile cannot invoke a write-risk direct capability.
A mutating local profile still sees only explicitly granted mutations. Remote
task profiles are separately enumerated: granting `mutate` does not imply
`project-yolo`, `machine-yolo`, or `machine-yolo-root`. Privileged descriptors,
root execution, generic shell commands, node update, pairing, enrollment, and
gateway administration are excluded from P7.5 unless a later focused
admission names them. `machine-yolo-root` is not a P7.5 direct capability.

The node owner-shell matrix now supports both `local_user` and
`privileged_helper` executors on Linux and macOS, but both deliberately expose
the same `shell.exec.v1` model command. P7.5 therefore excludes the semantic
`shell.exec.*` family before projection and grant validation, independently
of executor mode, OS, version suffix, terminal support, or cancellation
support. A privileged helper becoming available must never make an
owner-shell descriptor discoverable to a local coding thread. P7.7 task
profiles remain the separately admitted route for machine-yolo authority.

The existing P7.4 channel requesters remain unchanged. P7.5 does not invent a
fake Telegram channel or sender and does not weaken `RemoteCodingScopeFor`.
Its local coding grant is a separate first-party principal that may reference
an existing remote scope only through the broker.

## Same-user IPC security contract

The v1 transport is a Unix-domain stream socket on Linux and macOS. Windows is
explicitly unsupported until it has an equivalent authenticated local
transport admission.

The gateway must:

- resolve an explicit absolute socket path outside any coding project;
- create and validate the nearest private runtime directory with mode `0700`;
- refuse symlinked, multiply linked, non-directory, wrong-owner, or
  group/world-accessible path components;
- refuse to replace an unknown pre-existing filesystem object;
- create the socket with mode `0600` and verify its owner and socket type;
- authenticate every accepted connection using kernel peer credentials;
- require the peer effective UID to equal the gateway effective UID; and
- deny root or another UID when the gateway itself is unprivileged.

Linux uses `SO_PEERCRED`; macOS uses `LOCAL_PEERCRED`. Socket permissions are
defense in depth, not the sole authentication decision. The client performs
the symmetric owner/type/mode checks before connecting and rejects a peer UID
that does not match its own effective UID. There is no TCP fallback and no
silent discovery by scanning runtime directories.

The socket contains no reusable bearer credential. The same OS user is the
v1 trust boundary; a process already able to read that user's MintClaw config
and coding transcripts is not isolated by P7.5. Root and a compromised same
user remain outside the threat model and are documented honestly.

Gateway startup owns the listener transactionally after node admission is
ready and closes it before draining node admission. A configured endpoint
path change is restart-required. Grant narrowing or revocation may hot-reload
by atomically replacing an immutable snapshot; it affects the next request
immediately. Grant broadening is not visible to an already constructed coding
runtime until an explicit refresh or new runtime snapshot.

## Closed protocol surface

The protocol is a versioned, length-bounded request/response envelope, not
JSON-RPC and not an internal Go object tunnel. It accepts only these operation
families:

- `capabilities.list` — return the current bounded aliases, schemas, risk,
  placement, availability, catalog/grant revisions, and limits;
- `capability.invoke` — prepare and dispatch one exact granted operation;
- `invocation.status` — observe the original gateway invocation;
- `invocation.cancel` — request cancellation of that invocation;
- `artifact.describe` and `artifact.fetch` — resolve only an artifact already
  referenced by an owned invocation, with existing size and retention bounds;
- `coding_task.start` — start one granted P7.4/P7.7 task generation;
- `coding_task.status` — observe its bounded projection and question;
- `coding_task.steer` — append one ordered steer or exact question answer; and
- `coding_task.cancel` — request cancellation of the exact generation.

Every envelope carries a protocol version, request ID, grant alias/revision,
local thread/session identity, operation identity, and deadline. It has strict
unknown-field rejection, UTF-8 validation, maximum frame/input/output sizes,
bounded connection and in-flight counts, and per-operation deadlines. Large
output is retained by existing artifact owners and returned as an opaque
reference rather than copied through the socket.

The server never exposes config reads, logs, agent sessions, channel history,
provider credentials, task-registry enumeration, arbitrary tool calls,
reflection, method names, raw node transport frames, or a generic `exec`
endpoint. Adding an operation requires a new closed protocol case and tests.

## Direct capability contract

The local AgentLoop receives one coding-only `remote_capability` surface with
`list`, `invoke`, `status`, `cancel`, `artifact_describe`, and
`artifact_fetch` actions. It is registered only when local remote access is
explicitly configured. With no client configuration it is absent, preserving
the existing local tool snapshot exactly.

At runtime construction the client obtains a bounded discovery snapshot. The
model sees only aliases admitted by the local profile, gateway grant, target
policy, current approved catalog, selected target profiles, and node-local
model contract. Internal workspace and coding commands, unavailable or
partially described commands, per-call-approval descriptors, and privileged
commands are not projected. In particular, every `shell.exec.*` descriptor is
absent whether its companion executor is `local_user` or `privileged_helper`;
capability flags such as terminal or confirmed-cancellation support cannot
weaken that exclusion. An invoke carries the exact discovery revision; the
server re-resolves every authority and rejects stale or broadened input.

Configured adapters reuse existing typed implementations rather than exposing
a registry dispatcher or generic model-tool call:

- remote workspace adapters provide bounded read, search, write, patch,
  direct-argv build/test, durable job status/log/cancel, and artifact refs;
- browser adapters retain the existing browser profile, session, action,
  artifact, and human-handoff authorities;
- service adapters retain typed status/log/action policy;
- direct catalog aliases may expose another non-internal model contract only
  when an exact server-side alias binds its target and command; and
- artifact operations retain the producing invocation/job/browser owner and
  never become arbitrary file download.

`workspace_exec` advertises exact foreground and/or durable-job schemas from
the current approved catalog. Each mode retains its own executable aliases,
environment-name allowlist, timeout, and artifact bounds; modes are not
combined into a broader Cartesian product. Job lifecycle operations accept
the caller-visible `job_invocation_id` returned by the producing
`workspace_exec` call. The broker resolves the hidden `job_id` from that exact
workspace/capability invocation and never accepts a model-supplied raw job ID.

P7.5 does not route the local `read_file`, `apply_patch`, or `exec` tool to a
sticky remote cwd. Remote placement is explicit in the tool name, arguments,
observations, and UI. Small remote operations remain convenient, but a task
that needs sustained repository reasoning or edits must use
`remote_coding_task`.

No-prompt coding behavior is allowed only when every authority layer already
admits the operation without per-command approval. An existing `each_command`
or protected-input requirement is not bypassed; that capability is omitted or
reported unavailable in v1.

## Remote coding-task contract

The coding-only `remote_coding_task` surface has `start`, `status`, `steer`,
`answer`, and `cancel` actions. `start` accepts only a granted remote scope
alias, permitted profile, bounded objective, bounded done criteria, and
already-owned attachment or artifact references. It cannot accept a path,
repository URL, target, node, executable, environment, credential, provider,
worktree policy, timeout override, or privilege backend.

The gateway extracts the channel-independent task coordinator from the
existing P7.4 runtime instead of invoking the live-agent `coding_task` tool or
fabricating inbound channel context. Both facades reuse the same validation,
`CodingNodeInvoker`, `coding.*.v5` commands, task identity rules, and bounded
result decoder:

- the channel facade keeps its existing gateway task registry, interactions,
  delivery, and requester ownership; and
- the local facade binds ownership to grant revision, same-user principal,
  local thread/session identity, and stable tool-call/execution identity.

After runtime-capability C1, the local facade obtains actor, session, and
execution identity from the validated turn-bound `runtimecap.Principal`
created by the trusted coding composition root. Model-authored tool arguments
cannot supply or override that principal. Thread, project, grant, and policy
revisions remain additional broker coordinates rather than substitutes for
the runtime principal.

The local task ID and generation are derived and persisted before dispatch.
An accepted start maps to one node invocation, one remote thread, one worker
generation, and, where required, one worktree owner. Duplicate identical
start recovery returns the same link. Changed objective, done criteria, scope,
profile, grant, or generation conflicts instead of starting another task.

The local thread never becomes a second writer for the remote thread and does
not mirror its messages. Status returns only bounded semantic progress,
blocking question metadata, changed relative paths, validation, publication
or external-effect receipts, artifact refs, terminal outcome, uncertainty,
and remote thread/worktree identifiers. `answer` binds the exact question ID
and revision. General steering is ordered and idempotent. Cancellation is a
request, not a rollback claim.

The task may continue while the local CLI is closed. Resuming the local thread
can observe or control it from the retained link. A remote thread becomes
locally resumable only on the companion host under the existing P7.4/P7.7
lease rules; P7.5 does not copy it into the caller's local catalogue.

## Transcript, compaction, and recovery

Every successful preparation writes a bounded structured tool result to the
canonical local transcript before the model relies on it. A direct reference
contains capability alias, target alias, invocation ID, operation/risk,
grant/catalog/policy revisions, and latest safe state. A task reference
contains task/generation, target/scope/profile aliases, remote thread and
worktree/branch identifiers when known, and latest safe state. Absolute remote
paths, node IDs, credentials, command bodies containing protected values, and
full remote output are excluded.

Active references survive compaction in a bounded continuity projection.
Seahorse may derive an active-reference checkpoint for fast resume, but the
canonical tool-call/result records remain source of truth and the checkpoint
must be rebuildable. Compaction never summarizes an active reference down to
prose that cannot be used for status or cancellation.

The shared prompt-cache planner treats enabling or disabling the
`remote_capability` tool as a tool-schema change and starts an explicit cache
lineage. Grant, catalog, target, and descriptor refreshes are appended as
ordered bounded discovery/tool observations; they never rewrite a completed
turn or silently mutate an already fingerprinted provider prefix. Cache hits
or misses cannot change dispatch, recovery, or no-replay semantics.

Recovery follows these rules:

- failure before durable gateway preparation is safely retryable with the
  same local tool-call identity;
- timeout after preparation or dispatch returns the original invocation/task
  reference as `uncertain` and permits only status/cancel recovery;
- reconnect, UI refresh, provider retry, and explicit status query observe the
  same identity and never dispatch a new mutation;
- stale grant, target policy, catalog, descriptor, workspace, scope, or task
  profile prevents new invoke/steer operations immediately;
- revocation still permits a narrowly authorized status or cancel of an
  already-owned operation when existing policy can prove the owner; it never
  permits a new effect;
- output truncation is explicit and includes an artifact reference only when
  the existing artifact owner durably retained one;
- node disconnect is `offline` before dispatch and `uncertain` after an
  unproven dispatch; and
- cancellation races settle from the original invocation or worker state as
  completed, cancelled, failed, or uncertain, never as an invented rollback.

## TUI and headless behavior

Both frontends project the same semantic events. A remote operation always
shows:

- placement `remote`;
- target and capability or scope aliases;
- invocation or task identity;
- queued, dispatching, running, waiting-for-input, completed, failed,
  cancelled, offline, denied, stale, truncated, or uncertain state; and
- safe artifact, branch, commit, pull-request, deployment, or validation
  references when present.

The TUI may render a compact remote badge and an expandable progress card. It
must not stream raw transport frames or every remote worker token. `code exec`
emits the same identities and state transitions as JSONL events and includes
the terminal references in its final result. Plain mode gives an equivalent
human-readable result.

Remote capability outcomes reuse the coding frontend's canonical tool event
path. Failed or skipped operations carry only a sanitized `ToolObservation`
and bounded diagnostic through `ToolExecEnd` or `ToolExecSkipped`; the existing
projector and activity grouping render the failure context. P7.5 must not add a
parallel remote-only event or renderer that can diverge from headless output.

Gateway absence is not fatal to local coding. If P7.5 is configured but the
socket is unavailable, local tools continue to work and the remote surfaces
report `broker_unavailable`. MintClaw never starts a gateway automatically,
opens a second node connection, or falls back to executing the requested
remote operation locally.

## Implementation sequence

Each packet begins from the latest merged `origin/main`. Later packets start
only after the prior contract and focused tests are merged.

R2 was refreshed against `origin/main` at
`9d6b275a0db332ee909ad46ce194c178f1c34fc2`. The shared atomic
`RuntimeToolPlan` introduced there supersedes direct coding-registry mutation:
P7.5 contributes its trusted facade through a feature-owned contributor, and
the final coding policy may narrow but never broaden that facade.

### R0 — Admission and stale-status cleanup

- Merge this contract and link it from the main architecture index and
  roadmap.
- Mark P7.7 complete and link its merged exit record.
- Keep all P7.5 configuration absent in production.

Done when implementation can proceed without relying on conversation history
and no P7.7 roadmap status incorrectly remains `admitted`.

### R1 — Broker contracts, config, and authenticated IPC

- Add transport-neutral broker request/result types and strict validation in
  a package that depends on neither Cobra nor the coding TUI.
- Add bounded optional client, listener, grant, and capability configuration.
- Implement Linux/macOS owner-only socket creation, peer authentication,
  bounded framing, startup rollback, shutdown drain, and atomic grant reload.
- Add a client health/discovery call and a coding runtime bootstrap hook; do
  not expose any node operation yet.

Done when same-user Linux/macOS clients can fetch an empty or filtered
revisioned snapshot, wrong UID/path owner/mode/type/symlink/version/grant is
denied, gateway absence is non-fatal to local coding, and an unconfigured
runtime is byte-for-byte/tool-for-tool unchanged.

### R2 — Direct typed capability vertical slice

- Add server-side exact capability alias resolution over current target
  policy, approved catalog, gateway invocation source, and node policy.
- Add coding-only `remote_capability` list/invoke/status/cancel actions.
- Compose that facade through the shared `RuntimeToolPlan` as the
  `coding.remote` contributor; do not mutate an admitted live registry or
  bypass final coding tool policy.
- Integrate tool enablement and discovery refresh with the shared cache-plan
  lineage contract: schema changes reset lineage, while refreshed authority
  is an ordered observation rather than a historical-prefix rewrite.
- Bind invoke, status, and cancel to the existing turn-bound
  `runtimecap.Principal`; construction-time discovery is non-authoritative and
  cannot dispatch an operation.
- Reuse P8a workspace adapters for a bounded read and mutation; preserve
  stable invocation identity and uncertain no-replay recovery.
- Project placement, progress, errors, truncation, and refs through the
  existing sanitized tool-observation and diagnostic event path so TUI,
  grouped activity, JSONL, and plain output share one failure contract.

Done when deterministic Linux and macOS real-process tests run one read and
one bounded mutation from a native local coding thread through IPC, production
gateway, authenticated WSS, and companion ledger, while stale and uncertain
paths do not replay.

### R3 — Jobs, browser, service, and artifact adapters

Delivered R3A surface:

- exact direct-argv foreground and durable-job modes through `workspace_exec`;
- invocation-owned `job_status`, `job_logs`, `job_artifacts`, and
  `job_cancel` aliases;
- omission of read/write operations by local profile risk and omission of any
  job descriptor that still requires per-call approval; and
- durable no-replay recovery for each typed node invocation, including a lost
  cancellation response.

Delivered R3B surface:

- closed `artifact.describe` and ranged `artifact.fetch` operations for an
  immutable artifact already referenced by the caller-owned
  `workspace_exec` invocation;
- reconstruction of ownership from the durable gateway invocation after CLI
  restart, rather than reliance on a process-local link cache; and
- bounded 256 KiB chunks, a 32 MiB total limit, full SHA-256 verification, and
  import into the local thread's attachment store without exposing node, job,
  transfer, or filesystem identities.

Delivered R3C surface:

- exact `service_status`, `service_logs`, and `service_action` aliases backed
  by `service.status.v1`, `service.logs.v1`, and `service.action.v1`;
- projection of only the target-selected service profile and its model-safe
  service aliases, action pairs, and log limits;
- omission of `service_action` from investigate sessions and from every
  target that lacks an explicit operator approval bypass; and
- durable status/cancel observation after grant revocation without replay,
  while raw unit names, node IDs, manager paths, generic commands, shell,
  updates, and every other privileged descriptor remain absent.

The final R3 slice is the closed browser adapter. It reuses existing browser
profile, session, artifact, and human-handoff ownership rather than projecting
a generic node command.

Delivered R3D surface:

- one exact `browser_profile` capability binding to an enabled node-placed
  browser target and one non-attached profile already granted to the configured
  agent;
- closed `browser_open`, `browser_status`, `browser_close`, context lifecycle,
  `browser_observe`, `browser_diagnostics`, and `browser_act` aliases over the
  existing gateway browser broker, with one stable browser owner per local
  coding thread and capability;
- omission of all writes from `investigate`, and omission of action/context
  mutations unless the bound profile uses `approval_mode: none`;
- server-side rebinding of every supplied session to the configured
  target/profile plus current actor, agent, session, profile revision, browser
  policy revision, readiness, and action catalogue checks;
- preservation of the browser durability boundary: page observations,
  diagnostics, context results, and action results stay live-only, while nested
  fill/dialog values use the existing protected-input projection; and
- explicit exclusion of attached-user consent, handoff/resume, privileged
  browser execution, screenshots, uploads, downloads, file chooser actions,
  provider credentials, runtime paths, and raw `browser.*.v1` node commands.

The adapter retains a bounded same-gateway invocation receipt before returning
an IPC result. A lost local response or local CLI restart can therefore query
the original terminal receipt without reopening a session or replaying an
action, including after grant removal. Browser session/action state remains
owned by the existing durable browser ledger. If the gateway process itself is
replaced before the wrapper receipt is observed, the caller receives an
explicit unavailable/unknown outcome and must not replay; R5 qualification
must preserve that fail-closed behavior rather than inventing a second browser
ledger.

- Add exact aliases for direct-argv build/test, durable jobs and logs, typed
  browser operations, typed service inspection/action, and owned artifacts.
- Retain every existing profile, approval, redaction, size, retention,
  cancellation, and human-handoff boundary.
- Keep unsupported, privileged, internal, and per-call-approval descriptors
  absent rather than approximating them.

Done when each advertised adapter has focused policy/recovery tests, artifact
ownership cannot escape its producing operation, and no adapter adds a raw
path, shell, credential, or generic command escape.

### R4 — Remote coding-task link and control

- Extract the channel-independent coordinator from P7.4 without changing its
  Telegram behavior or durable task delivery.
- Add local task start/status/steer/answer/cancel with transcript-owned links.
- Add bounded active-reference continuity across compaction and resume.
- Prove one remote writer/worktree, question correlation, idle continuation,
  disconnect recovery, revocation behavior, and no duplicate task generation.

Done when a local thread can start and link an `investigate` and isolated
mutation task, observe progress, answer a blocking question, steer, cancel,
resume control after CLI restart, and never claim the remote cwd or transcript.

### R5 — Integrated qualification and production rollout

- Run the full focused suites, race-sensitive tests, Linux CI, macOS CI, and
  the real gateway/WSS/companion vertical harness.
- Back up gateway config, binary, service definitions, node config, node
  binary, and relevant state before enablement.
- Deploy the same merged production revision to gateway and canary companion.
- Enable one exact same-user grant and only the minimum paired commands.
- Run harmless direct read, isolated bounded mutation, artifact, remote-task
  question/control, cancellation or terminal observation, reconnect/no-replay,
  local-no-grant, and health/redaction canaries.
- Merge an exit record with exact revisions, IDs, receipts, rollback, and
  residual limits.

Done only when production evidence satisfies the complete matrix below.

## Required test and evidence matrix

| Boundary | Mandatory evidence |
| --- | --- |
| Config | Empty defaults; invalid aliases/revisions/targets/operations; duplicate grants; local-profile and task-profile separation; semantic `shell.exec.*`/privileged exclusions across versions and executor modes |
| Socket | Linux `SO_PEERCRED`; macOS `LOCAL_PEERCRED`; same UID success; different UID/root denial; `0700`/`0600`; symlink, replacement, owner, mode, frame, deadline, and shutdown tests |
| Discovery | Exact grant intersection; target policy; connected state; approved catalog; descriptor freshness; no hidden/internal/approval-required/private fields; no owner-shell descriptor for either `local_user` or `privileged_helper` |
| Direct read | Native local coding runtime to IPC to gateway to real companion and back, with placement and invocation ID |
| Direct mutation | One prepared mutation, changed-path receipt, uncertainty/status recovery, and exact-one execution after reconnect |
| Cancel and output | Running cancellation, completion race, truncation, retained artifact ownership, expiry, and no arbitrary fetch |
| Task start | Stable task/generation, one remote thread/worker/worktree, immutable scope/profile/objective, duplicate recovery, and conflict rejection |
| Task control | Progress, blocking question and exact answer, ordered steer, status after local restart, cancel, terminal result, and revoked-grant behavior |
| Transcript/compaction | Bounded structured refs persisted before use, active refs survive compaction, derived checkpoint rebuild, fork disclosure, and no second writer |
| Frontends | Equivalent TUI, plain, and JSONL placement/state/ID/error/ref projection; broker outage leaves local coding usable |
| Compatibility | Existing local coding, live-agent node tools, P8a remote workspaces, Telegram P7.4/P7.7, browser, tasks, and node pairing unchanged without a grant |
| Privacy | No credentials, absolute node paths, channel history, provider config, protected content, raw command bodies, or remote transcript in IPC logs/traces/transcript refs |

The portable vertical tests must run actual local broker sockets, gateway
invocation storage, authenticated WSS, companion ledgers, and native helper or
worker processes on Linux and macOS. Pure mocks are useful for fault
injection but cannot close R2, R4, or R5.

## Production rollout, rollback, and stop conditions

Rollout order is gateway binary with the listener disabled, companion binary
and exact catalog, local coding binary, configuration validation, owner-only
socket enablement, one read-only capability, one bounded mutation, then one
remote coding scope. Broad grants are never used as a canary.

Rollback is disable-first:

1. remove or disable the P7.5 local client grant so no new local operation is
   admitted;
2. stop and remove the broker socket without stopping ordinary local coding;
3. observe or cancel already accepted invocation/task identities through the
   retained gateway/node authorities;
4. restore matched gateway, node, and local binaries/config only if required;
5. verify gateway/channel/node health and absence of replay; and
6. retain transcripts, invocation records, ledgers, remote threads,
   worktrees, and artifacts until their normal retention policy expires.

Do not delete durable state to manufacture a clean rollback.

Implementation or rollout stops immediately if any of these is observed:

- a caller without the exact grant lists or invokes a capability;
- peer UID, socket ownership, or endpoint type cannot be proven;
- the IPC surface exposes arbitrary gateway methods, private agent/channel
  data, provider credentials, raw node identity, or absolute companion paths;
- a stale or revoked policy still starts a new operation;
- provider retry, reconnect, refresh, or resume duplicates a mutation or task;
- a read-only local profile performs a write-risk operation;
- the local runtime obtains a remote cwd or a second remote transcript writer;
- an uncertain result is reported as success or cancellation as rollback;
- remote failure silently falls back to local execution;
- P7.4/P7.7 Telegram delivery or existing local coding behavior regresses; or
- logs, traces, transcript refs, or UI expose protected content.

## Completion criteria

P7.5 is complete only when:

- all R1–R5 implementation packets are merged with required review and CI;
- exact same-user IPC authentication and deny-by-default grants work on Linux
  and macOS;
- a native local coding thread completes one production companion read and one
  bounded mutation with durable invocation identity and no replay;
- the same thread starts, observes, steers or answers, cancels or reaches a
  terminal state for one remote P7.4/P7.7 coding task without a second writer;
- TUI and headless output truthfully expose remote placement, progress,
  failures, uncertainty, and artifact/publication references;
- no-grant local coding and personal live-agent behavior remain unchanged;
- production health, redaction, backup, disable-first rollback, and retained
  state are verified; and
- a merged P7.5 exit record contains exact commits, task/thread/invocation
  identities, changed paths, validation receipts, canary results, known
  limits, and rollback evidence.

P7.5 does not admit networked coding-to-gateway access, fleet-wide generic
RPC, remote filesystem mounts, local resumption of a copied remote transcript,
automatic deployment, privileged direct capabilities, or P7.6 filesystem
rewind. Those remain separate decisions.
