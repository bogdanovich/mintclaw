# Self-Improvement Control Plane Roadmap

Status: active. S0 and S1A are delivered. Owner-requested skill management in
S1B-S1D remains in scope. Autonomous retrospective skill creation in S2A-S2C
is frozen as of 2026-09-11 pending evidence that it produces useful proposals.
Later packets remain subject to their stated admission and stop gates.

MintClaw baseline: `origin/main` at `f822d39b`, 2026-09-07.

Comparison baselines:

- `openclaw/openclaw` at `8b84af54`, 2026-09-07;
- `NousResearch/hermes-agent` at `3a7bf745`, 2026-09-07; and
- `zeroclaw-labs/zeroclaw` at `ab72fe5d`, 2026-09-07.

## Objective

Make MintClaw able to improve its procedures, install bounded capabilities,
and coordinate changes to its own source from an owner conversation without
turning the live gateway into an uncontrolled self-modifying process.

From the owner's perspective, the completed system should support this loop:

```text
explicit owner chat request
                |
                v
       inspect current capability
                |
        +-------+--------+
        |                |
        v                v
 skill proposal    extension/core proposal
        |                |
        v                v
 owner policy gate   isolated validation
        |                |
        +-------+--------+
                |
                v
       activate with rollback
                |
                v
       report durable evidence
```

The model may discover, draft, test, and explain changes. Runtime-owned code
retains authority over identity, policy, approval, activation, audit, rollback,
and recovery.

This roadmap is not a general plugin rewrite and is not permission to refactor
stable subsystems merely because another agent framework has a different
extension API.

## What Self-Improvement Means

The product must keep three kinds of change separate:

| Layer | Examples | Activation boundary |
| --- | --- | --- |
| Procedure | `SKILL.md`, supporting scripts, prompts, workflow knowledge | Validated workspace skill revision |
| Capability | MCP server, process hook, external executable tool | Installed manifest plus explicit enable/reload |
| Core | Go source, tests, configuration schema, release packaging | Isolated worktree, PR, CI/review, merge, deploy |

Most useful self-improvement should stop at the procedure layer. A repeated
workflow does not justify a plugin, and a plugin does not justify changing the
Go runtime. Escalation to the next layer requires evidence that the lower layer
cannot provide the needed contract, authority, performance, or reliability.

## Current Architecture Assessment

MintClaw already has useful foundations:

| Existing foundation | Current value | Missing product boundary |
| --- | --- | --- |
| Workspace, global, and builtin skill loader | Skills are discovered from disk on current turns | No canonical managed revision or mutation owner |
| `find_skills` and `install_skill` | Registry discovery and transactional installation | No first-class inspect, propose, update, remove, or rollback lifecycle |
| Origin metadata and malware result handling | Third-party installs retain source facts and block known malware | Metadata ownership is local to the integration tool and is not a shared contract |
| File and shell tools | A sufficiently privileged agent can author skill files | Generic writes bypass skill validation, revision checks, and lifecycle audit |
| In-process and process hooks | External processes can observe/intercept runtime activity and inject tools | No package manifest, install transaction, capability policy, or health lifecycle |
| MCP manager and discovery | Typed external tools can be loaded without rebuilding Go | Configuration, credentials, installation, and rollback remain operator-managed |
| Runtime events | Turn, LLM, tool, usage, channel, and MCP activity is observable | No self-improvement-specific proposal, activation, or evaluation events |
| Durable interactions | Human questions and approvals survive restart | Skill and extension changes do not yet bind activation to this authority |
| Task registry and delivery | Long-running work can report durable completion | No self-improvement task kind or bounded retrospective scheduler |
| Native coding runtime and coding specialist | MintClaw can perform project-aware coding or delegate to Codex | Chat-to-worktree-to-PR ownership is not one product workflow |
| Safe restart and `gateway_deploy` | Activated binaries can hand off and resume safely | No capability/core change coordinator owns preflight, canary, rollback, and evidence |

The primary gap is therefore a control plane, not model intelligence and not
the Go language. Go makes in-process native code less dynamic than Python or
TypeScript, but skills, MCP, process hooks, external workers, and controlled
core delivery provide the required runtime flexibility without changing the
language.

## Lessons From Other Agents

The comparison informs product boundaries rather than feature parity:

- [OpenClaw skills](https://github.com/openclaw/openclaw/blob/main/docs/tools/skills.md)
  combine install/update, managed revisions, reload, and a proposal-oriented
  workshop. Its
  [plugin lifecycle](https://github.com/openclaw/openclaw/blob/main/docs/tools/plugin.md)
  and
  [restart recovery](https://github.com/openclaw/openclaw/blob/main/docs/gateway/restart-recovery.md)
  make extension and update operations feel like one product.
- [Hermes skill management](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/guides/work-with-skills.md)
  exposes create and update operations directly to the agent and can run a
  background review. That produces fast procedural learning, but reported
  [approval-bypass concerns](https://github.com/NousResearch/hermes-agent/issues/99729)
  show why generic file and shell tools cannot be treated as an equivalent
  governed mutation path.
- ZeroClaw's
  [background skill review](https://github.com/zeroclaw-labs/zeroclaw/pull/6667)
  demonstrates that a Rust single-binary runtime can still perform opt-in
  procedural self-improvement. This confirms that static compilation is not
  the architectural blocker.

MintClaw should combine OpenClaw's lifecycle clarity with its own durability,
no-blind-replay, and explicit-authority standards. Background proposal
generation remains an unproven optional extension rather than a prerequisite.

## Product Priorities

### Required

1. A canonical managed-skill inventory with deterministic revisions and origin
   classification.
2. A proposal-first skill lifecycle for create, update, fork, and remove.
3. Explicit owner-controlled apply, reject, and rollback operations that do
   not depend on the model interpreting an approval sentence.
4. Skill validation, duplicate detection, stale-revision rejection, and an
   immutable audit trail.
5. A manifest and lifecycle for external executable capabilities built on MCP
   and process isolation rather than Go native plugins.
6. A durable chat-to-coding workflow that uses an isolated worktree, local
   validation, PR checks/review, and configured merge/deploy authority.
7. Activation health checks, safe restart, rollback, and continuation using
   the existing gateway mechanisms.
8. Status and diagnostics that report what changed, why, who authorized it,
   token/cost budget, current revision, and rollback result without exposing
   secrets or private conversation content.

### Frozen Pending Evidence

- automatic or scheduled post-turn review that creates skill proposals;
- transcript or diagnostic-trace mining for autonomous skill discovery; and
- automatic activation of any retrospectively generated skill change.

Explicit owner requests to create or update a skill remain in scope through
S1B-S1D. They are not classified as autonomous learning and must use the same
validation, approval, audit, and rollback contracts as every managed change.

### Useful After The Required Loop Works

- promote a reviewed workspace skill to selected agent profiles;
- export or publish a skill to a configured private registry;
- notify about upstream skill updates without applying them automatically;
- compare proposal quality against a small task fixture or evaluation suite;
- allow scheduled maintenance reviews with explicit frequency and budgets;
- recommend consolidation when multiple local skills overlap materially;
- provide Web UI views over the same proposal and audit contracts; and
- show per-agent capability drift and installed revision differences.

These features are not prerequisites for the first useful release and must not
delay the required owner-chat workflow.

### Not Admitted

- rewriting or replacing the running Go binary in place;
- loading arbitrary Go shared objects into the gateway process;
- letting a model merge, deploy, or rotate credentials without configured
  owner authority;
- automatically applying background-review output;
- giving a retrospective worker generic messaging, deployment, node, browser,
  secret, or unrestricted shell tools;
- treating arbitrary `write_file`, `apply_patch`, or `exec` operations as
  managed skill changes;
- silently installing system packages or executing repository setup scripts;
- globally updating every installed skill to `latest`;
- training or fine-tuning a model as part of this roadmap; and
- building a universal workflow engine, dependency-injection framework, or
  second agent runtime.

## Executive Architecture Decisions

1. The live model is a proposer and orchestrator, never the source of
   authorization.
2. Skills, executable extensions, and core changes use separate stores,
   policies, and activation paths.
3. Only workspace skills are mutable. Global and builtin skills are read-only;
   changing one requires an explicit workspace fork.
4. Third-party skills retain immutable origin identity. Local modification
   creates a fork or records a divergence; it never masquerades as the
   registry revision.
5. Every mutable object has a deterministic content revision. Update, remove,
   apply, and rollback use compare-and-swap against an expected revision.
6. Proposals are durable, immutable records. Status transitions are append-only
   or transactionally versioned and survive gateway restart.
7. Owner apply/reject commands are parsed deterministically by the command or
   interaction layer. The LLM may explain a proposal but does not decide that
   an arbitrary message constitutes approval.
8. Applying a skill proposal creates a restorable previous revision before
   changing the active directory. Directory replacement is atomic where the
   supported filesystem permits it and fail-closed elsewhere.
9. Files under a managed skill are bounded regular files. Symlinks, devices,
   sockets, path traversal, control filenames, and oversized trees are denied.
10. Installed executable extensions run out of process. MCP is preferred for
    typed tools; the existing JSON-RPC process-hook protocol remains suitable
    for interception and observation.
11. A package manifest declares tools, hooks, executables, environment names,
    secret references, network needs, platform support, health checks, and
    requested authority before installation.
12. Package installation and package enablement are distinct. Downloading or
    staging a package does not grant runtime authority.
13. Retrospective review, if separately admitted in the future, is asynchronous
    to user delivery, bounded, deduped, and disabled by default. Failure never
    changes the completed user turn.
14. Core changes always use a source checkout separate from the live runtime,
    an isolated worktree, and repository-defined format, test, lint, CI, and
    review gates.
15. Codex or another coding backend may implement code changes. MintClaw owns
    task identity, policy, progress, questions, evidence, and activation.
16. Safe restart and deploy are reused rather than reimplemented. The new
    control plane supplies validated candidates and consumes durable outcomes.
17. Configuration can disable skill mutation, retrospectives, extension
    installation, core-change orchestration, merge, and deploy independently.
18. Cross-profile or cross-machine promotion is explicit. One agent cannot
    mutate another profile merely because both run under the same Unix user.

## Authority Modes

The final configuration surface should support independent monotonic modes:

| Domain | Modes |
| --- | --- |
| Skill management | `off`, `propose`, `owner_apply` |
| Retrospective review | `off`, `manual`, `eligible_turns`, `scheduled` |
| Extension packages | `off`, `inspect`, `stage`, `owner_enable` |
| Core development | `off`, `plan`, `branch`, `pull_request` |
| Merge | `off`, `owner_approved` |
| Deploy | `off`, `owner_approved`, `post_merge_allowed_targets` |

Modes grant maximum authority, not instructions to exercise it. A narrower
agent capability policy, turn profile, node policy, repository rule, or
runtime safety check always wins.

Defaults for new mutation and background behavior remain `off` or
proposal-only until live evidence admits a broader default. Existing explicit
deploy configuration remains authoritative and is not silently narrowed by
introducing this table.

## Canonical Domain Model

### Managed skill

A managed skill view should include:

- canonical skill name and effective source;
- workspace-relative directory, never an unbounded absolute path in model
  output;
- deterministic tree revision;
- parsed name and description;
- origin kind, registry, slug, source URL, installed version, and install time
  when present;
- editable, divergent, valid, and shadowing status; and
- bounded validation findings.

The active skill directory remains the execution source. Inventory and
revision data are derived from that directory and origin record rather than
becoming a second mutable copy of skill contents.

### Skill proposal

A proposal should contain:

- immutable proposal ID, creation time, source kind, and requesting actor;
- target skill and operation: create, update, fork, or remove;
- expected active revision or explicit expectation that the skill is absent;
- complete candidate tree or a content-addressed reference to it;
- bounded human summary, rationale, and evidence references;
- validation result and candidate revision;
- status with revision: proposed, rejected, superseded, applied, failed, or
  rolled back; and
- activation and rollback receipts when applicable.

Proposal records must not contain provider credentials, environment values,
private tool output, or an unbounded transcript. Evidence references use
durable turn/task IDs and privacy-safe summaries.

### Extension package

An extension package should include a signed or digest-pinned manifest, staged
payload, source metadata, requested capabilities, dependency plan, validation
receipt, enabled revision, health state, and rollback target. Secrets are
referenced by configured names and resolved only at process composition.

### Core change

A core-change record should bind the owner request, acceptance criteria,
repository and base revision, isolated worktree, coding thread/task, branch,
commits, validation, PR head, CI/review state, merge authorization, deployed
revision, health evidence, and rollback outcome.

## Delivery Packets

### S0: record architecture and scope

Scope:

- add and index this roadmap;
- distinguish procedure, capability, and core evolution;
- identify mandatory, optional, and rejected features;
- select S1A as the first implementation packet; and
- preserve comparison baselines and explicit completion gates.

Completion gate:

- the architecture index links the active roadmap;
- every implementation packet has dependencies, acceptance criteria, and a
  stop gate; and
- no production behavior changes in S0.

### S1A: canonical managed-skill inventory

Status: admitted as the first implementation packet.

Scope:

- move installed-skill origin metadata ownership from the integration tool
  into `pkg/skills` without changing the on-disk version-1 format;
- introduce one workspace-skill inventory API that validates canonical names,
  directory containment, regular-file trees, and origin metadata;
- compute a deterministic revision over relative paths, file modes, and bytes
  with explicit file-count and byte limits;
- distinguish local, third-party, malformed-origin, and absent skills;
- make `install_skill` validate and persist through the shared domain API;
- keep current registry install output and transactional reinstall behavior;
  and
- add focused Linux, macOS, and Windows-safe tests for ordering, content and
  mode changes, symlink rejection, malformed metadata, limits, and concurrent
  independent inventory reads.

Acceptance criteria:

- one package owns the origin schema and managed revision contract;
- identical valid trees produce identical revisions regardless of directory
  enumeration order;
- a symlink or non-regular entry cannot be admitted as a managed skill;
- malformed origin metadata is visible as invalid rather than downgraded to a
  local skill;
- failed install validation still restores the exact previous installation;
- no new model-visible mutation tool or behavior is introduced; and
- affected tests, `make fmt`, and changed-package lint pass.

Stop gate:

- do not add a database, background watcher, proposal format, or mutation API
  to this packet;
- do not rewrite the general skill loader unless a focused correctness test
  proves that S1A cannot safely reuse it; and
- if deterministic revision requires following links or reading outside the
  skill root, stop and revise the design.

### S1B: durable proposal store and candidate validation

Status: in scope for explicit owner-requested skill changes; not admitted as a
background-learning dependency.

Dependencies: S1A.

Scope:

- add a workspace-scoped proposal repository with atomic writes and bounded
  listing;
- support create, update, fork, and remove candidates with expected revisions;
- validate the complete staged candidate before accepting a proposal;
- deduplicate identical open proposals and supersede explicitly replaced
  proposals;
- add append-only audit events for proposal creation and status transitions;
  and
- publish privacy-safe runtime events.

Acceptance criteria:

- proposal creation cannot change an active skill;
- stale expected revisions are rejected before staging;
- restart preserves every committed proposal and status transition;
- duplicate model calls cannot create an unbounded identical queue;
- corrupt or partial records fail closed without hiding valid records; and
- store, recovery, limit, and concurrent-create tests pass.

Stop gate:

- do not add owner approval, model-triggered apply, background review, or
  registry publishing to this packet;
- use file-backed records unless measured scale or required transactions prove
  SQLite necessary; and
- do not store full conversation transcripts as proposal evidence.

### S1C: model proposal tool and deterministic owner controls

Status: in scope for explicit owner-requested skill changes only.

Dependencies: S1B and the current durable interaction contract.

Scope:

- expose a focused model tool for inspect, view, and propose operations;
- keep apply, reject, rollback, and destructive cleanup behind deterministic
  owner commands or runtime-owned interaction controls;
- bind each approval to the immutable proposal ID, candidate revision, active
  expected revision, agent, workspace, and one-time authorization;
- present a bounded diff and validation summary before approval;
- revalidate active and candidate revisions immediately before apply;
- atomically activate the candidate and retain a bounded rollback revision;
  and
- reload the effective skill catalogue for subsequent model calls without
  interrupting the current user reply.

Acceptance criteria:

- a model cannot apply its own proposal by composing approval-like text;
- generic file tools do not create false managed audit records;
- stale, expired, denied, duplicated, or replayed approvals cannot mutate a
  skill;
- crash before commit leaves the old skill active, while crash after commit is
  reconciled to one inspectable revision;
- rollback restores the exact recorded tree and creates its own audit receipt;
  and
- channel-independent approval, restart, replay, and catalogue-refresh tests
  pass.

Stop gate:

- do not grant cross-workspace writes;
- do not permit in-place edits of global, builtin, or pristine third-party
  revisions; fork them instead; and
- do not weaken generic tool approval or `allow_all` semantics to special-case
  this feature.

### S1D: skill CLI and operational recovery

Status: in scope as recovery and operator support for S1B-S1C.

Dependencies: S1C.

Scope:

- provide deterministic CLI operations to list skills and proposals, inspect
  revisions, validate candidates, apply/reject, and rollback;
- add bounded retention and explicit prune behavior for terminal proposal and
  rollback records;
- document backup, restore, and zero-secret diagnostics; and
- prove live install, propose, approve, reload, rollback, and restart behavior.

Acceptance criteria:

- every chat operation has an equivalent operator recovery path that does not
  require an LLM;
- pruning cannot remove an active revision or the only rollback for a recent
  activation;
- CLI JSON output is versioned and scriptable; and
- live evidence identifies exact binary, configuration, skill revisions, and
  cleanup state.

### S2A: bounded retrospective eligibility and queue

Status: frozen pending the evidence gate below. Do not implement from the
general roadmap execution order.

Dependencies: S1C and runtime event/task ownership audit.

Scope:

- define eligible successful turns using typed terminal outcomes and durable
  identities;
- exclude trivial chat, failed/cancelled turns, sensitive workflows, approval
  continuations, duplicate delivery, and already-reviewed turns;
- enqueue retrospective work after final delivery without extending user
  response latency;
- enforce per-agent concurrency, cooldown, daily token/cost budget, queue
  capacity, and deduplication; and
- recover queued/running work conservatively without replaying an uncertain
  model call.

Acceptance criteria:

- user delivery never waits for retrospective execution;
- feature default is `off` and manual mode works independently;
- one eligible turn creates at most one durable review record;
- restart cannot duplicate an uncertain review call;
- excluded turns produce a bounded reason rather than hidden work; and
- timing, capacity, cancellation, restart, and no-latency-regression tests pass.

Stop gate:

- do not use a synchronous turn-end observer for model work;
- do not start a second task scheduler when the existing task/runtime event
  foundations can own the lifecycle; and
- do not include raw secret-bearing tool arguments or results in review input.

### S2B: restricted retrospective reviewer

Status: frozen with S2A.

Dependencies: S2A and S1B.

Scope:

- run the reviewer in a separate bounded model context;
- expose only skill inventory/view and proposal creation capabilities;
- require evidence of a reusable procedure, repeated correction, stable user
  preference, or demonstrable skill defect;
- prefer updating an existing skill over creating overlapping skills;
- emit no proposal when evidence is weak; and
- attach bounded rationale and validation evidence to accepted proposals.

Acceptance criteria:

- the reviewer cannot send messages, mutate active skills, invoke arbitrary
  tools, deploy, access secrets, or operate nodes;
- one-off investigations and transient failures do not automatically become
  skills in characterization fixtures;
- deliberate reusable procedures do produce valid deduplicated proposals;
- reviewer failure is diagnostic only and cannot alter the original turn; and
- model, tool-allowlist, privacy, and adversarial-instruction tests pass.

Stop gate:

- do not auto-apply proposals;
- do not expand the reviewer tool set to make one fixture pass; and
- if useful proposal precision cannot be demonstrated on a small curated
  evaluation set, keep only manual review mode.

### S2C: skill quality gate

Status: frozen with S2A. Deterministic candidate validation needed by manual
skill management belongs in S1; this packet covers retrospective quality.

Dependencies: S2B.

Scope:

- lint frontmatter, naming, size, links, referenced local files, and executable
  declarations;
- scan candidate scripts and instructions for secret requests, policy
  escalation, hidden downloads, prompt injection, and unsupported platforms;
- run declared deterministic checks in a restricted staging environment;
- compare changed skills against curated trigger/non-trigger fixtures; and
- include quality evidence in the owner approval view.

Acceptance criteria:

- invalid references and unsafe executable declarations block activation;
- warnings and hard failures remain distinct;
- validation is bounded and does not execute undeclared candidate code;
- fixtures catch both over-triggering and missed intended triggers; and
- quality checks add no work to ordinary turns when no proposal is evaluated.

### S3A: extension package manifest and staging

Dependencies: S1 operational patterns and an explicit process-hook/MCP trust
audit.

Scope:

- define a versioned extension manifest for skills, MCP servers, process hooks,
  executables, platforms, dependencies, permissions, secret references,
  health checks, and entry points;
- support local path and digest-pinned registry/Git sources initially;
- download into a staging directory with size, archive, path, and file-type
  validation;
- display the dependency and authority plan before enablement; and
- keep package stage, validate, enable, disable, update, and remove as explicit
  lifecycle states.

Acceptance criteria:

- staging never edits active config or starts a process;
- manifests cannot embed resolved secret values;
- mutable remote references are resolved to and recorded as immutable digests;
- unsupported platform and missing dependency failures are actionable;
- archive traversal, symlink, oversized package, digest mismatch, and partial
  download tests pass; and
- an extension can be completely removed without leaving enabled config.

Stop gate:

- do not add Go native plugins;
- do not invent a second remote tool protocol when MCP is sufficient;
- do not execute package-provided installers during inspection or staging; and
- do not support every package manager in the first slice.

### S3B: policy-bound activation and health

Dependencies: S3A, config repository transactions, and safe reload audit.

Scope:

- project manifest requests into explicit effective policy;
- obtain owner authorization for the exact package revision and authority;
- transactionally write managed MCP/process-hook configuration;
- start or reload through existing lifecycle ownership;
- verify declared health and bounded tool discovery;
- roll back configuration and process state on activation failure; and
- preserve an inspectable uncertain state when side effects cannot be proven.

Acceptance criteria:

- package code cannot grant itself tools, environment, network, or secret
  references beyond approved effective policy;
- injected tool definitions cannot bypass runtime ownership and approval rules;
- activation survives restart or rolls back to the previous known-good state;
- disabling removes model visibility before process shutdown;
- health timeout does not stall gateway startup indefinitely; and
- config transaction, reload, crash, rollback, and malicious-package tests
  pass.

### S3C: discovery and update workflow

Dependencies: S3B.

Scope:

- search configured extension sources;
- explain capability fit, requested authority, platform compatibility, and
  maintenance state;
- notify about pinned update candidates;
- stage and diff updates before owner enablement; and
- preserve the previous enabled revision until replacement health succeeds.

Acceptance criteria:

- search results cannot install or enable code;
- `latest` is never an activation identity;
- updates cannot silently expand authority;
- rollback remains available after a successful update; and
- stale-index and unavailable-registry failures do not affect active packages.

### S4A: durable chat-to-coding task admission

Dependencies: the local coding roadmap's admitted remote worker boundary or an
equivalent merged project-bound worker contract.

Scope:

- accept an owner request with repository alias, objective, acceptance
  criteria, allowed change mode, and delivery policy;
- resolve aliases on the coding worker rather than trusting model-supplied
  paths;
- create one durable coding task/thread and isolated worktree from an explicit
  base revision;
- expose semantic progress, questions, cancellation, and final evidence to the
  originating conversation; and
- make Codex or another configured coding engine an implementation detail.

Acceptance criteria:

- the gateway never treats a remote repository path as local authority;
- duplicate admission does not create duplicate worktrees or tasks;
- task restart and cancellation follow existing no-blind-replay rules;
- final output reports changed paths, commits, validation, and unresolved work;
  and
- no merge or deploy authority is implied by task creation.

Stop gate:

- do not approximate a coding worker with unrestricted repeated remote shell
  calls;
- do not forward the full personal conversation to the coding model; and
- do not duplicate the native coding thread, lease, or task store.

### S4B: repository quality and pull-request coordinator

Dependencies: S4A.

Scope:

- discover repository instructions and required format/test/lint commands;
- bind validation evidence to the exact branch head;
- create a focused PR with declared scope and later work;
- monitor CI, review comments, conflicts, and approval on a bounded schedule;
- route questions and material architecture expansion to the owner; and
- preserve review and CI evidence across worker or gateway restart.

Acceptance criteria:

- PR publication cannot occur from an untracked or contaminated worktree;
- stale validation does not authorize a changed head;
- failing required CI blocks review-dependent merge behavior;
- unresolved actionable review blocks merge;
- monitoring is bounded and does not busy-poll providers; and
- repository credentials remain in the worker's configured credential
  boundary rather than model context.

### S4C: configured merge and activation

Dependencies: S4B and existing safe deploy/restart contracts.

Scope:

- represent owner merge approval as a durable capability bound to repository,
  PR, and immutable head;
- merge only under configured policy after current checks and review state pass;
- fetch the exact merged revision into the deployment pipeline;
- run preflight, deploy to configured targets, perform health/canary checks,
  and resume the originating conversation;
- roll back failed activation without reverting unrelated repository history;
  and
- report exact merged and deployed revisions.

Acceptance criteria:

- approval for one head cannot merge a later head;
- merge and deploy can be enabled independently;
- deployment never builds or runs from the mutable coding worktree;
- restart continuation produces at most one terminal owner notification;
- health failure leaves the previous known-good binary recoverable; and
- merge, conflict, stale approval, deploy, crash, and rollback tests pass.

Stop gate:

- default merge and deploy authority remains off;
- no background retrospective can request or inherit merge/deploy authority;
  and
- do not replace the current deploy controller or release updater when an
  adapter can supply the required candidate.

### S5: owner experience and observability

Dependencies: delivered S1-S4 surfaces; may be split by domain without adding
new authority.

Scope:

- provide concise channel-independent status for active capabilities,
  proposals, extension health, coding work, and deployed revisions;
- expose token counts and configured cost estimates for retrospectives and
  coding tasks;
- add runtime events and passive diagnostics for proposal, validation,
  approval, activation, rollback, and recovery transitions;
- add Web UI projections only over the same typed read models; and
- document incident response and zero-secret evidence collection.

Acceptance criteria:

- every mutation can be traced from request/proposal through authorization to
  active revision or rollback;
- diagnostics contain stable identities and bounded summaries, not prompts,
  credentials, candidate source bodies, or private tool results;
- status remains useful when a provider, registry, worker, or extension is
  offline; and
- observability does not become a second mutable source of truth.

## Cross-Cutting Reliability And Security Gates

Every implementation packet that mutates durable or executable state must
address these invariants explicitly:

- **Identity:** workspace, agent, proposal/package/task, active revision, and
  authorization identities are complete and immutable.
- **Containment:** all filesystem paths are resolved under one configured root
  without following candidate-controlled links.
- **Boundedness:** file count, bytes, diff size, queue depth, concurrency,
  runtime, retries, output, token usage, and retention have explicit limits.
- **Freshness:** activation rechecks both expected active state and candidate
  state immediately before commit.
- **Atomicity:** partial writes never become active state; ambiguous external
  effects remain explicit instead of being blindly retried.
- **Recovery:** restart reconciles prepared, committed, uncertain, and terminal
  states without repeating mutations.
- **Authority:** approval is bound to exact immutable input and consumed once.
- **Isolation:** untrusted extension and candidate code does not execute in the
  gateway process or inherit ambient secrets.
- **Privacy:** diagnostics and review inputs minimize conversation and tool
  data, redact secrets, and retain only bounded evidence.
- **Portability:** path, locking, rename, executable, and process behavior is
  tested or explicitly gated on Linux, macOS, and Windows.
- **Rollback:** activation retains and verifies a previous known-good state;
  rollback itself is recorded as a new operation.
- **Observability:** each state transition emits one typed, privacy-safe event
  without making event delivery part of the commit transaction.

## Retrospective Evidence Gate

S2 remains frozen until a separate shadow-mode experiment demonstrates value
on recurring MintClaw work. The experiment must compare skill-assisted and
unassisted runs with the same model, token budget, task inputs, and multiple
runs where variance is material. It must include:

- a reusable workflow that should create a skill proposal;
- an explicit correction to an existing skill that should update it;
- a one-off research task that should not create a skill;
- a transient provider or network failure that should not become procedure;
- malicious content instructing the reviewer to grant itself authority;
- private or credential-bearing tool output that must be excluded;
- duplicate turns that should deduplicate to one proposal; and
- a stale proposal whose base revision changed before approval.

Track proposal precision, duplicate rate, owner acceptance/rejection, measured
task success, tool calls saved, total token/cost budget, routing regressions,
and run-to-run variance. Generated text, reviewer confidence, or green
implementation tests are not evidence that a proposal helps. Unfreezing S2
requires a new owner decision based on recorded results; it is not an automatic
consequence of completing S1.

## Execution Order

1. S0 and S1A are delivered.
2. Deliver S1B, S1C, and S1D sequentially only when owner-requested skill
   authoring is prioritized; they share the skill store, proposal, and approval
   boundaries.
3. Keep S2A-S2C frozen until the retrospective evidence gate passes and the
   owner explicitly admits a new implementation packet.
4. Audit current MCP and process-hook authority before S3A. Deliver S3 in
   staging, activation, then discovery/update order.
5. Start S4 only after the coding worker dependency is merged and stable. Reuse
   its task/thread/worktree contracts.
6. Add S5 projections alongside the owning domains; do not postpone critical
   diagnostics until the end.

Each production packet starts from the latest merged `origin/main`, remains one
focused PR, runs repository formatting and changed-package lint, and scales
tests with persistence, concurrency, routing, approval, and deployment risk.

## Program Completion Criteria

The required roadmap is complete when all of the following are proven:

1. From an owner chat, MintClaw can inspect a skill, create a durable proposal,
   present a bounded diff, apply it under exact one-time authorization, use the
   new revision on a later model call, and roll it back after restart.
2. MintClaw can stage an external MCP or process extension, show requested
   authority, enable it after approval, verify health, and disable or roll it
   back safely.
3. MintClaw can accept a core change request in chat, run it in an isolated
   coding worktree, publish a PR, track current CI/review state, and, only when
   configured and authorized, merge and deploy the immutable approved result.
4. Every operation is configurable, bounded, recoverable, auditable, and
   independently disableable.
5. Linux and macOS live canaries pass; Windows behavior is covered by CI or an
   explicit unsupported-platform gate for the affected executable feature.

Once these criteria pass, archive this roadmap. New marketplaces, autonomous
publishing, additional package ecosystems, or broader authority require new
measured product needs and a separate roadmap; they are not implied follow-up
work.
