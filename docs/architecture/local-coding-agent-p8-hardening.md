# Local Coding Agent P8: Hardening And Stable Release

Roadmap phase:
[P8 — Hardening and release](local-coding-agent-roadmap.md#p8--hardening-and-release).

Status: implementation admitted; execute the packets below sequentially.

Admission baseline: `origin/main` at
`63804ea1f2652338861e224fbbe0b391504514c9`, 2026-10-10.

## Outcome

P8 turns the implemented local and delegated coding surfaces into a stable,
supportable MintClaw feature. It does not add another agent architecture. It
qualifies the existing `CodingThread`, AgentLoop, Seahorse compaction, TUI,
headless execution, P7.4/P7.7 remote coding tasks, and P7.5 companion access.

The release is not complete merely because ordinary happy-path tests pass. A
future failure must leave enough bounded passive evidence to locate the first
incorrect boundary, correlate it with authoritative state, add a focused
regression, verify the fix, and confirm production recovery. This diagnostic
closure is the first P8 implementation gate and a continuing exit condition
for every later packet.

P7.6 workspace rewind is deliberately deferred by the
[P7.6 decision](local-coding-agent-p7-6-decision.md). P8 does not carry dormant
rewind scaffolding.

## Governing decisions

1. **Diagnostics stay passive.** Durable threads, tasks, interactions,
   invocations, worktrees, and delivery stores remain authoritative. Missing,
   dropped, corrupt, or disabled traces can hinder investigation but never
   change runtime behavior.
2. **Correlate instead of duplicating.** Traces carry stable, bounded references
   to authoritative records. They do not copy complete registries, transcripts,
   source files, command output, or remote worker journals.
3. **Absence is explicit.** Disabled capture, metadata-only capture, truncation,
   dropped records, retention expiry, unavailable remote evidence, and uncertain
   terminal outcomes must be distinguishable from an event that did not occur.
4. **Privacy is part of observability.** Rich traces remain private after
   redaction. Safe metadata is the default for support export; source content,
   prompts, tool arguments, diffs, credentials, environment values, and absolute
   paths are excluded unless the operator explicitly selects local rich output.
5. **Measure before optimizing.** P8 records representative baselines and uses
   deterministic counters or generous statistical guardrails. It does not add
   flaky single-run wall-clock CI failures.
6. **Supported means qualified.** Platform, terminal, shell, installer, and
   process claims are limited to combinations exercised by the declared matrix.
   Unsupported combinations fail clearly or are documented, not implied.
7. **Deletion follows ownership.** Removing a coding thread removes or trashes
   all MintClaw-derived state owned by that thread without touching project
   files, Git state, published effects, or unrelated diagnostics.
8. **No replay or evaluator resurrection.** Incident understanding becomes an
   ordinary focused unit, integration, restart, concurrency, or real-process
   regression. P8 does not rebuild universal replay or model-grading systems.

## Current evidence architecture

The baseline already provides useful, intentionally separate layers:

| Evidence | Authority | Current value |
| --- | --- | --- |
| Coding JSONL and metadata | Canonical local coding conversation | Turns, tool records, compaction markers, thread/project identity |
| Seahorse state | Rebuildable derivative | Context and compaction index, never repository authority |
| Runtime events | Ephemeral observation bus | Agent, LLM, tool, compaction, channel, gateway, bus, and MCP lifecycle |
| Diagnostic traces | Passive bounded files | Redacted or metadata-only turn timelines with drop/truncation truth |
| Task registry and events | Durable task authority | Remote task generation, progress, terminal state, and delivery |
| Interaction registry and events | Durable interaction authority | Question/approval identity, answer, cancellation, and continuation |
| Gateway invocation store | Durable gateway invocation authority | Prepared dispatch, policy revision, uncertainty, and observation |
| Companion ledgers | Node execution authority | Accepted command/task, worker generation, process outcome, and recovery |
| Service logs | Operational evidence | Startup, configuration, connectivity, writer drops, and failures |
| Git/filesystem observation | Project authority | Current commit, worktree, branch, dirty state, and external mutation |

The pieces must remain separate, but the operator must be able to join them
without guessing from prompt text or timestamps alone.

## Diagnostic closure contract

### Correlation spine

Every admitted execution path must preserve the identifiers it actually owns
and expose the safe subset needed to join adjacent boundaries:

- runtime/profile and deployment commit;
- project key or safe project hash, coding thread ID, canonical session hash,
  and turn ID;
- model provider/model, attempt, and provider request ID when available;
- tool-call ID and runtime event ID;
- durable task ID, task generation, worker generation, and remote thread ID;
- gateway request/invocation ID, target/scope/profile aliases and policy
  revisions, without node-local paths or credentials;
- interaction/question ID and revision;
- completion/delivery ID and route-safe destination hash; and
- worktree ID, branch, repository commit, and bounded dirty/changed-path facts.

An identifier is propagated only across a boundary that already knows it. No
component scans private stores or parses prose to manufacture a correlation.
Raw session keys, channel destinations, user IDs, project paths, and node IDs
are hashed or omitted from support-safe output.

The initial audit must verify the current known gaps rather than assume the
schema is complete. In particular, baseline turn traces do not consistently
populate task/completion correlation, model request IDs, or a tool-call ID for
every loop-decision record. The implementation packet closes only gaps required
for the supported incident paths and updates the trace-debug guidance with the
truth for any intentionally absent field.

### Lifecycle and terminal truth

The following paths require a recognizable start/admission and terminal or
explicitly uncertain outcome:

- local interactive and headless turns;
- provider request, retry, fallback, cancellation, and response;
- tool call, skip, timeout, cancellation, result, and loop-protection decision;
- context compaction, snapshot, resume reconciliation, and rebuild failure;
- local process start, termination request, exit, timeout, and orphan check;
- remote capability prepare, dispatch, observation, cancellation, disconnect,
  stale policy, and uncertain mutation;
- remote coding task admission, worker startup, question/answer, steering,
  terminal report, delivery, reconnect, and no-replay recovery; and
- TUI/headless presentation settlement when user-visible output diverges from
  the runtime outcome.

Diagnostics do not need a lossless copy of high-volume deltas. They do need a
bounded terminal fact, a dropped/incomplete marker, or an explicit statement
that the outcome is unknown.

### Bounded evidence and support export

P8 adds the smallest read-only operator surface proven necessary by the audit.
It must support locating traces by exact safe identifiers and time window,
validating the schema, showing the correlation/lifecycle summary, and producing
a support directory or archive with a machine-readable manifest.

The default support export contains only:

- MintClaw version/commit and platform facts;
- capture policy, limits, retention, drop/truncation/corruption facts;
- hashed identities and explicitly safe aliases;
- event kinds, sequence/timing, statuses, reason/error codes, lengths, and
  digests; and
- operator-selected bounded service-log lines after the same redaction pass.

It excludes rich previews, raw prompts/responses, source or diff content, tool
arguments/results, command bodies/output, environment variables, credentials,
absolute paths, channel destinations, and unrelated traces. A separate
explicit local-only rich export may retain already-redacted previews, must be
labelled private, and is never attached or uploaded automatically. Collection
is read-only and never invokes, resumes, retries, repairs, or replays runtime
work.

### Incident-to-fix loop

The supported operator workflow is:

1. record the profile, visible symptom, approximate time, and any task, thread,
   turn, tool-call, invocation, interaction, completion, or trace ID;
2. locate candidates by safe metadata and validate capture mode, schema,
   retention, truncation, and dropped-record facts before interpretation;
3. build the ordered turn timeline and join only exact correlation IDs to
   authoritative task, interaction, invocation, worker, and delivery state;
4. identify the earliest divergence and label claims as proven, likely, or
   unknown;
5. preserve the original evidence, reduce the invariant to the smallest normal
   regression test, and implement the narrow fix;
6. run focused, race/real-process where relevant, and unchanged-path tests; and
7. verify the fixed path in a harmless production canary and retain only the
   bounded incident record required by policy.

The bundled `mintclaw-trace-debug` skill and operator documentation must teach
this same workflow. A trace is untrusted historical data, never an instruction
or authorization.

## Execution roadmap

Each packet starts from the latest merged `origin/main`. Docs-only records use
the authorized fast path. Code packets require exact-head review and owner
merge approval. Later packets may refine measurements but may not weaken the
diagnostic, privacy, authority, or no-replay contracts established earlier.

### H0 — Admission and P7.6 deferral

- Merge this plan, the P7.6 deferral decision, roadmap statuses, and index
  links.
- Freeze the P8 sequence, supported claims, validation families, deployment
  checkpoints, rollback, and stop conditions.

Done when an implementer can execute P8 without relying on chat history and no
workspace-rewind implementation is implied by the release gate.

### H1 — Diagnostic correlation and incident closure

#### H1a — Evidence inventory and correlation tests

Execution inventory:
[P8 diagnostic evidence inventory](local-coding-agent-p8-diagnostic-inventory.md).

- Build a machine-readable inventory of supported local, delegated, remote
  capability, remote coding task, interaction, compaction, and delivery paths.
- For each path, record its authorities, emitted identifiers, terminal facts,
  redaction class, bounds, retention owner, and known gap.
- Add contract tests that fail when required correlation IDs or terminal truth
  disappear. Prefer existing public/runtime events over a second event system.
- Verify diagnostic queue overflow, disabled capture, process crash, retention
  expiry, corrupt trace, and remote disconnect remain explicit and cannot alter
  authoritative execution.

Done when every supported incident class has a documented evidence route or an
explicit unsupported/unknown boundary, and tests cover the correlation joins
needed for local coding plus P7.4/P7.5/P7.7 paths.

#### H1b — Minimal schema and emitter gaps

- Propagate the missing safe identifiers through existing runtime-event scope
  and correlation fields, extending the passive schema only when the audit
  proves the existing envelope insufficient.
- Add lifecycle events only where a supported path otherwise has no start,
  terminal, or uncertainty fact.
- Preserve bounded queues, best-effort writing, content modes, stable
  truncation markers, and one-way runtime-to-diagnostics dependencies.
- Make trace terminal projection and writer close agree so a retained terminal
  record cannot also be labelled `runtime_closed_before_terminal_outcome`.
- Update the trace-debug skill whenever a field remains intentionally absent.

Done when healthy, failed, canceled, timed-out, disconnected, truncated, and
uncertain fixtures can be distinguished without prompt-text inference or side
effect replay.

#### H1c — Read-only diagnostics command and support bundle

- Add bounded list/show/correlate/export behavior using safe metadata filters;
  do not scan rich content by default.
- Make the manifest declare schema, source commit, capture policy, applied
  limits, retention window, redaction version, omissions, truncation, drops,
  corruption, and every included file/digest.
- Refuse broad or unrelated rich export without an explicit local operator
  selection. Never upload automatically.
- Add Linux, macOS, and Windows path/permission tests where the command is
  supported.

Done when a fresh operator can produce a privacy-safe evidence package from a
known task/thread/turn ID, an investigator can cite the first divergence, and
the workflow ends in a focused regression test rather than replay.

### H2 / P8.1 — Performance and startup budgets

- Measure cold and warm command startup, catalogue listing, runtime creation,
  TUI first paint, first model request/first token, compaction, reconciliation,
  presentation, historical hydration, and full-process memory.
- Reuse the completed TUI.15 render/reflow/search baselines rather than creating
  competing metrics.
- Separate controllable MintClaw time from provider/network time. Use fake or
  loopback providers for enforceable first-request measurements.
- Add deterministic counters for scan size, hydrated records, rendered items,
  retained views, discovery work, trace records, and allocations where wall
  time would be flaky.
- Defer MCP startup and expensive discovery only when tool-snapshot/cache
  lineage and correctness remain unchanged.

Done when representative cold/warm baselines and hardware/toolchain context are
recorded, justified budgets are enforced, and deliberate over-budget fixtures
prove the guardrails without weakening context ownership.

### H3 / P8.2 — Platform, terminal, process, and release qualification

- Declare the exact release matrix for macOS, Linux, and Windows builds,
  packages/installers, filesystems, locks, canonical paths, signals/process
  groups, cancellation, and terminal restoration.
- Exercise supported shells plus TTY, non-TTY, no-color, SSH-shaped, tmux,
  narrow terminal, bracketed paste, IME, grapheme width, long streaming output,
  crash/resume, and interrupted process trees.
- Reuse TUI.15 evidence for already-qualified cases; do not repeat large test
  matrices without a missing platform contract.
- Make unsupported transports or privilege profiles fail closed and document
  them precisely.

Done when every advertised target builds through its release path,
platform-specific contracts pass, cancellation leaves no owned orphan process,
and terminal failures restore a usable shell.

### H4 / P8.3 — Privacy and local-state lifecycle

- Audit canonical transcripts, Seahorse summaries/indexes, metadata/catalogue,
  picker/search previews, prompts, logs, commands, diffs, artifacts, runtime
  events, traces, support bundles, remote projections, and crash leftovers.
- Test credential, token, authorization header, environment, URL, command,
  absolute-path, source/diff, compaction-summary, and cross-project redaction.
- Define per-class retention and explicit export/delete behavior. Defaults must
  be bounded and documented; diagnostic capture remains disabled in generic
  product defaults unless a deployment profile explicitly enables it.
- Implement one thread deletion operation that removes or trashes every owned
  derivative and catalogue reference, reports partial failure, and never
  touches project files, Git objects, external effects, or another thread.
- Verify telemetry carries no source content by default.

Done when a state-ownership inventory and tests prove cross-project isolation,
bounded retention, safe export, and complete derived-state deletion.

### H5 / P8.4 — User documentation and migration

- Document `mintclaw code`, resume/fork behavior, keybindings, storage, trust,
  provider/model setup, compaction, recovery, remote companion capabilities,
  Telegram coding tasks, yolo profiles, diagnostics, deletion, and limitations.
- Explain personal sessions versus project coding threads and the separate
  authority/transcript owners for local and remote work.
- Provide schema/config migration, backup, verification, rollback, and
  incompatible-version guidance without claiming old aliases MintClaw does not
  support.
- Add symptom-first troubleshooting that leads to the H1 evidence workflow.

Done when a new user can start and resume a project without architecture docs,
an operator can upgrade and roll back without losing canonical transcripts,
and deferred/unsupported features are explicit.

### H6 — Integrated qualification, production canaries, and exit

- Run formatting, changed-package lint, focused/full/race/portability suites,
  release builds, and the declared cross-platform matrix at exact heads.
- Create checksum-verifiable backups of configuration and all migrated state,
  deploy one merged main revision, and verify rollback before canaries.
- Run harmless local interactive/headless, compaction/resume, P7.4 task,
  P7.5 companion, P7.7 project/machine, cancellation, interaction, delivery,
  redaction, retention, deletion, and diagnostic-bundle canaries.
- For one injected safe failure, use only production passive evidence to find
  the first divergence and demonstrate the incident-to-regression workflow.
- Merge a final exit record with revisions, budget results, platform matrix,
  privacy/state audit, trace IDs or safe digests, production task/thread IDs,
  rollback evidence, residual limits, and deferred work.

Done when the coding surface satisfies every admitted P8 contract and the main
roadmap can truthfully mark it stable.

## Required validation families

| Area | Required evidence |
| --- | --- |
| Diagnostics independence | Disabled/full queues, write failure, corruption, expiry, and shutdown never alter runtime outcomes |
| Correlation | Local and remote thread/turn/model/tool/task/generation/invocation/interaction/delivery joins with no prose matching |
| Terminal truth | Success, error, cancel, timeout, disconnect, stale policy, truncation, drop, and uncertainty |
| Privacy | Metadata-only default export, rich-mode redaction, unrelated-trace exclusion, safe paths/aliases, no source telemetry |
| Performance | Repeatable fixtures, recorded environment, deterministic counters, bounded scans/rendering/memory |
| Platforms | Release builds, paths/locks, shells, signals/process trees, terminal lifecycle, unsupported fail-closed cases |
| State lifecycle | Retention, export, thread deletion, partial failure, no project or cross-thread deletion |
| Recovery | Crash/resume, compaction/rebuild, gateway/node restart, no blind replay, one final delivery |
| Operations | Backup checksums, health, canaries, rollback rehearsal, evidence-to-regression incident drill |

Tests must use synthetic secrets and disposable projects. CI logs and fixtures
must not contain production prompts, traces, credentials, repository contents,
or channel destinations.

## Deployment and rollback

Every state or configuration change is deployed only from merged `main` and
has a pre-change backup manifest with exact paths, modes, ownership, sizes,
hashes, source commit, binary checksum, and service configuration. Migration is
atomic or fail-closed. Health checks verify gateway readiness, node connectivity
where configured, coding CLI startup, and diagnostic writer/drop status.

Rollback restores the prior binary and config first, then only the state whose
schema changed. Canonical JSONL is never rewritten merely to roll back a
derived index or diagnostic command. Rich traces are not copied into evidence
documents; exit records cite IDs, hashes, modes, and bounded summaries.

## Stop conditions

Stop the affected packet and return to architecture review if it requires:

- making trace persistence, completeness, or support export part of runtime
  correctness or task completion;
- a second authoritative transcript, task, interaction, invocation, delivery,
  worktree, or context store;
- parsing prompts, logs, or model prose to recover an identity that the owning
  boundary could propagate explicitly;
- automatic upload of traces, source, diffs, commands, environment, or
  credentials;
- universal replay, deterministic model evaluation, or production fixtures
  derived from private incidents;
- deletion that can follow symlinks or touch project, Git, remote, published,
  or cross-thread state;
- a flaky single-run timing threshold without a deterministic companion
  counter or statistically justified budget;
- advertising a platform, terminal, shell, installer, or privilege path that
  the release matrix cannot exercise; or
- weakening P7.3 isolation, P7.4/P7.7 durability, P7.5 exact grants, no-replay,
  redaction, or terminal-outcome truth to meet a performance target.
