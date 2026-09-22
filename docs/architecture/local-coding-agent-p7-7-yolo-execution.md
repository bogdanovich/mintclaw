# Local Coding Agent P7.7: Yolo Execution Profiles

Roadmap packet:
[P7.7 — Codex-style yolo execution profiles](local-coding-agent-roadmap.md#p77--codex-style-yolo-execution-profiles).

Status: implementation admitted.

Audit baseline: `origin/main` at `90d26c826af0d4972e40944dfff1f0f190a2bf09`,
2026-09-20.

## Outcome

MintClaw will extend the completed P7.4 Telegram-to-coding path with three
explicit operator-configured authority profiles:

| Profile | Execution root | OS authority | Intended work |
| --- | --- | --- | --- |
| `project-yolo` | One P7.3 isolated Git worktree | Companion user | Code, test, commit, push, open a pull request, and run an explicitly requested deployment. |
| `machine-yolo` | One configured directory, without requiring Git | Companion user | Diagnose the host, create projects and repositories, install user-level software, and operate user services or processes. |
| `machine-yolo-root` | One configured directory, without requiring Git | Explicit root execution profile | Perform the same work when an operating-system change actually requires UID 0. |

These profiles use the existing durable coding task, `CodingThread`, Seahorse
compaction, worker, question, steering, cancellation, paired-node transport,
and Telegram delivery path. They do not introduce ACPX or Codex as a second
agent, a resident coding daemon, or a second task/session store.

The profiles are intentionally yolo after dispatch: they do not ask for
per-command approval. Authority still comes from the exact authenticated
requester, route, agent, gateway alias, paired target, node scope, profile,
and revision configured by the operator. A general chat message cannot invent
or broaden any of those bindings.

## Why this fits the architecture

The current implementation already has most of the required shape:

- P7.4 owns durable start, status, steer, cancel, blocking questions, restart
  reconciliation, and one final channel delivery.
- P7.2 owns one private task-scoped native coding worker rather than a daemon.
- P7.3 owns one isolated worktree and writer for mutating repository tasks.
- The coding project resolver already supports both `git_worktree` and plain
  `directory` identities, so a machine task does not need a new thread model.
- The mutating coding profile already exposes unrestricted local command
  execution under its OS user. Git, GitHub CLI, package managers, deployment
  tools, and network clients can therefore remain ordinary coding-agent tools
  with node-local credentials.
- Linux already has a root-owned authority broker with bounded profile,
  identity, process-tree, cancellation, and audit contracts.

The missing work is policy and lifecycle composition: the current P7.4
contract names only repository projects, forces mutation through a worktree,
does not admit publication, has no direct writable non-Git profile, and does
not project a privileged execution backend into the coding worker.

## Product contract

### One model tool and one durable task

The live agent continues to use `coding_task`. A start selects one safe scope
alias and one allowed profile; it never accepts an absolute path, executable,
credential, repository URL, privilege backend, or arbitrary environment from
the model. The call returns the durable task ID immediately. Existing
`status`, `steer`, and `cancel` actions continue to address that same task.

The remote worker is a normal MintClaw coding session. It may compact and
continue, ask a blocking question, receive steering, and be resumed under the
same immutable scope and authority. The completed task appears in the coding
thread catalogue according to the existing retention rules.

### Scope and profile are different facts

P7.7 replaces the operator-facing `coding_projects` vocabulary with
`coding_scopes`. There is no compatibility reader because there are no
external users; deployment must migrate configuration atomically with the
binary.

Coding skill selection is orthogonal to this authority. A coding turn may
select and freeze repository or personal skill instructions after the task has
been bound, but a skill cannot select, grant, or broaden an execution profile.
Provider retries and compaction retain both immutable snapshots independently:
the selected skill content and the operator-admitted scope/profile binding.

Each node-local scope has:

- a safe alias and revision;
- kind `git_project` or `machine`;
- a canonical initial directory below an operator-configured parent;
- one worker executable/build and native provider profile;
- an allowed profile set;
- worktree policy for `project-yolo`;
- an optional named privileged executor for `machine-yolo-root`; and
- the existing concurrency, timeout, result, artifact, retention, and cleanup
  bounds.

The gateway maps a model-visible alias to one target and one node scope
revision through `remote_coding_scopes`. Exact requester grants and agent
target policy remain mandatory. Node discovery exposes only safe aliases,
kinds, profiles, revisions, availability, and bounded limits.

The validation matrix is closed:

| Scope kind | Admitted profile | Filesystem behavior |
| --- | --- | --- |
| `git_project` | `investigate` | Validated source root, read-only tools. |
| `git_project` | `mutate` | P7.3 isolated worktree; existing conservative handoff. |
| `git_project` | `project-yolo` | P7.3 isolated worktree plus admitted external side effects. |
| `machine` | `machine-yolo` | Direct writable directory identity under the companion user. |
| `machine` | `machine-yolo-root` | Direct directory identity plus the configured root executor. |

Unsupported combinations fail during node configuration and again at start.
`machine-yolo` and `machine-yolo-root` intentionally do not promise rollback:
they can change host state outside their initial directory through shell
commands. Their names and final reports must state that truth.

### Project publication and deployment

`project-yolo` treats normal Codex-like publication work as supported rather
than accidental:

- create commits and branches in the task worktree;
- fetch and push through node-local Git credentials;
- create or update pull requests through node-local provider credentials;
- run repository-provided build, test, release, and deployment commands when
  the objective requests them; and
- create a new remote repository when credentials and the objective permit it.

MintClaw does not broker GitHub credentials or add a typed API for each
provider. The worker inherits only its configured node-local credential
environment. Credentials, command text containing secrets, and absolute paths
remain absent from gateway projections and diagnostic traces.

The final report adds bounded external-effect receipts: kind, outcome, and a
redacted human-usable reference such as a commit, pull-request URL, repository
URL, deployment URL, package name, or service name. These are evidence, not an
exactly-once claim.

Arbitrary shell side effects cannot be made transactionally idempotent. The
durable task prevents blind prompt replay; after a disconnect or uncertain
command, the same worker must inspect local and remote state before retrying.
The report distinguishes verified, failed, and uncertain external effects.

### Machine authority

`machine-yolo` starts in the scope's configured directory but runs with all
authority of the companion service account. Its shell may access other paths,
network endpoints, processes, user package managers, and user services that
the account itself can access. File-oriented coding tools remain rooted at the
configured directory; the unrestricted shell is the explicit machine-wide
escape hatch.

The profile supports a plain directory with no `.git`, creating a new project
or repository, cloning elsewhere, diagnosing a machine, installing user-level
software, and operating long-running processes. The canonical coding thread
continues to be keyed by the configured directory identity even if the task
creates other repositories.

### Root authority

`machine-yolo-root` is absent and disabled by default. It is a separate scope
profile, gateway grant, safe descriptor revision, and model-visible choice; it
is never inferred from a failing unprivileged command. No password, sudo token,
or root credential crosses the gateway or enters a prompt.

On Linux, the coding worker reuses the existing root-owned authority broker.
It receives a `privileged_exec` tool bound to one exact broker profile and
working-scope alias. The broker remains the owner of identity selection,
process containment, cancellation, and bounded output.

On macOS, unrestricted `sudo -n sh -c ...` from the ordinary exec tool is not
an acceptable production implementation because the unprivileged parent
cannot prove termination of arbitrary root descendants. Investigation did not
find a stable public macOS process-domain primitive equivalent to the Linux
subreaper-backed broker. `launchd` process groups do not contain descendants
that create a new session, process enumeration is racy, and Endpoint Security
descendant tracking requires a restricted entitlement. Therefore v5 rejects
`machine-yolo-root` configuration on macOS. Ordinary `machine-yolo` remains
supported, including any ambient noninteractive authority already held by the
companion account, but MintClaw does not label that authority as a controlled
root backend. A future macOS backend requires a separate admission proving
root-owned configuration, authenticated terminal outcome, and cleanup of
arbitrary detached descendants; a general `sudoers` rule remains prohibited.

The root helper is deliberately command-only. The agent uses shell commands
for privileged file, package, process, and service work; P7.7 does not add a
second privileged file or service API.

## Recovery, reporting, and safety truth

Existing task/thread/worker identities remain the no-replay boundary. A
restart or disconnect observes the accepted invocation and retained worker
state; it does not create another task. Steering and question answers preserve
their existing ordered idempotency. Cancellation requests termination but is
not a rollback claim.

For direct machine profiles, the terminal report records:

- thread, task, target, scope, profile, and policy revisions;
- initial directory kind and Git identity when present;
- bounded changed-path evidence when it can be observed truthfully;
- validation outcomes;
- external-effect receipts;
- privilege backend and whether privileged commands ran, without command
  bodies or secret output;
- final worker/process outcome; and
- unresolved or uncertain effects.

The final Telegram response includes the useful references and uncertainty.
Status and recent-task listing continue through the existing task registry;
P7.7 may add a first-class `coding_task list` action only as a small projection
over that registry, not as another store.

Full yolo means a correctly authorized model can damage the selected project,
account, remote services, or host. Prompt rules, command scanning, and
redaction are not a sandbox. The security contract protects the decision to
grant that authority and prevents it from leaking to a different requester,
scope, target, or default agent profile.

## Mini-roadmap

Each phase is a focused PR group based on the latest merged `origin/main`.
Later phases may start only after the prior contract is merged and its focused
tests pass.

### Y0 — Admission and configuration migration plan

- Merge this decision and link it from the main coding roadmap.
- Record the incompatible `coding_projects` to `coding_scopes` and
  `remote_coding_projects` to `remote_coding_scopes` deployment migration.
- Keep all new profiles disabled in production.

Done when the authority matrix, OS boundary, recovery truth, delivery contract,
implementation order, rollout, and stop conditions are reviewable without
consulting conversation history.

### Y1 — Shared scope and execution-profile foundation

- Add canonical scope kind and execution-profile types shared by task, worker,
  node, gateway, and configuration validation.
- Migrate node/gateway configuration and typed discovery to revisioned coding
  scopes; remove the old public config keys and compatibility parser.
- Generalize worker binding validation so plain-directory direct execution is
  first-class while preserving immutable investigate and isolated mutate.
- Preserve P7.4 task, thread, question, steering, cancellation, compaction,
  delivery, and no-replay behavior.

Done when old configuration is rejected, all five matrix entries validate,
invalid combinations fail closed, and the unchanged investigate/mutate
vertical tests pass on Linux and macOS.

### Y2 — `project-yolo`

- Add the profile to isolated worktree preparation and the coding worker
  system contract.
- Admit commit, push, provider CLI, repository creation, pull request, release,
  and deployment work through unrestricted user shell execution.
- Add bounded structured external-effect receipts and uncertain-effect
  reporting.
- Exercise local bare remotes and fake provider/deploy CLIs without using real
  credentials in CI.

Done when a real worker can edit, validate, commit, push, and return a remote
reference from one isolated worktree; restart and cancellation never replay an
accepted task or claim rollback of a possible remote effect.

### Y3 — `machine-yolo`

- Add direct writable execution for a configured plain directory.
- Support non-Git startup, new repository creation, user-level installation,
  network access, process lifecycle, and user-service operations.
- Make machine-wide authority and the absence of rollback explicit in prompts,
  status, and final delivery.

Done when real Linux and macOS workers complete a harmless non-Git project
creation and user-process canary, report effects, survive compaction/steering,
and leave no orphaned process after cancellation.

### Y4 — `machine-yolo-root`

- Bind Linux `privileged_exec` to the existing authority broker.
- Carry the root binding only in private worker protocol v5; keep paths,
  scripts, environment, credentials, and raw output out of gateway state.
- Reject macOS root configuration until a public process-domain primitive can
  prove cleanup of arbitrary detached root descendants.
- Require exact node profile, gateway requester grant, revision, root-owned
  configuration, and noninteractive credential-free launch.
- Add privilege-use metadata and redaction without retaining commands or
  secret output.

Done when denial is the default; opt-in Linux real-process tests prove UID 0,
cancellation, descendant cleanup, timeout, output bounds, configuration
ownership, stale revision denial, and zero password transport; and macOS tests
prove the profile remains unavailable rather than falling back to `sudo`.

### Y5 — Operations, production canaries, and exit

- Back up the deployed gateway/node configuration and retained coding state.
- Atomically migrate configuration with binaries and initially expose only
  exact owner aliases on `ab-2`.
- Run a harmless Telegram `project-yolo` canary that commits and pushes to a
  disposable remote, a `machine-yolo` non-Git/process canary, and, only after
  Y4 qualification, a read-only UID canary for the root profile on a supported
  Linux companion. Record root as unavailable when the selected companion is
  macOS.
- Verify final Telegram delivery, task listing/status, health, redaction,
  cleanup, restart recovery, and rollback.
- Merge an exit record with exact revisions, canary task/thread IDs, receipts,
  residual limits, and the boundary for any later work.

Done when the two portable workflows and every supported-platform root
workflow are proven through the production Telegram path, unsupported root
targets fail closed, no alias is broader than the owner grant, source
checkouts and unrelated MintClaw features remain healthy, and rollback is
executable.

## Required validation

Focused tests must cover configuration migration and stale revisions; exact
requester/target/scope/profile grants; non-Git identity; worktree isolation;
node-local credentials; local and fake remotes; publication receipts;
direct-machine mutation; process cleanup; Linux broker opt-in and macOS root
denial; root-owned configuration; redaction; question/steer/cancel races;
compaction; node/gateway restart; uncertain external effects; one final
delivery; and unchanged local CLI, investigate, mutate, browser, node, and live
agent behavior.

Every code PR passes formatting, changed-package lint, focused and full tests,
race where practical, Linux/macOS real-process coverage, exact-head review,
owner approval, and merge gates.

## Stop conditions

Stop the affected implementation packet and return to an architecture decision
if it requires:

- running the complete companion or gateway as root;
- sending credentials, passwords, absolute paths, or raw privileged commands
  through the gateway task record;
- a second coding transcript, task database, question store, or remote agent
  protocol;
- silently converting an uncertain external effect into success or replaying
  the task to discover what happened;
- weakening requester, route, target, scope, profile, or revision checks;
- using a general unrestricted sudoers rule as the macOS root backend;
- allowing a machine profile to masquerade as isolated or rollback-safe; or
- coupling completion of P7.7 to removal of ACPX/Codex or to P7.5 companion
  access from a local coding session.
