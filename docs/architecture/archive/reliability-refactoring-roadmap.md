# Reliability and Refactoring Roadmap

Status: completed and archived
Archived: 2026-08-09
Audit baseline: `origin/main` at `d796943c`, 2026-08-01

## Purpose

This roadmap records the reliability and architecture work identified by a static review of the MintClaw runtime,
its persistence and delivery paths, package boundaries, tests, and pull-request CI.

It is not a rewrite proposal. MintClaw already has several strong foundations:

- durable inbound spooling and explicit steering ownership;
- canonical JSONL history with Seahorse as reconciled derived state;
- typed LLM and tool-loop outcomes;
- durable task, interaction, and node-operation records;
- strict node protocol schemas and fail-closed node-side enforcement;
- runtime events and passive diagnostic traces;
- substantial automated coverage, with test code roughly equal in size to production code.

The main concern is consistency at the boundaries between those foundations. In particular, an admitted inbound
message, a durable turn record, an external side effect, and a user-visible final reply do not yet form one explicit
reliability contract.

This roadmap supersedes the priority order in
[Current Refactoring Audit](../current-refactoring-audit.md), while retaining that document as the history of already
completed refactors and narrower implementation notes.

## Current Assessment

The codebase is a mature, feature-rich system in active architectural transition. Newer subsystems such as node
operations, tasks, human interactions, and Seahorse reconciliation generally have explicit ownership and durable
state machines. Older core paths in `pkg/agent`, `pkg/channels`, and `pkg/providers` still combine compatibility
contracts, mutable phase state, and best-effort error handling.

The architecture is suitable for continued incremental development. It is not yet production-hard against process
failure or storage failure during the interval between inbound admission, tool side effects, and outbound delivery.
Those correctness gaps should be addressed before broad structural cleanup.

## Required Reliability Invariants

The roadmap is complete when the following invariants are enforced and tested:

1. An inbound spool item is acknowledged only after the turn has transferred its final outcome to a durable owner or
   completed a synchronous delivery with a known result.
2. A turn cannot invoke an LLM or side-effecting tool unless its root user input has been durably appended to the
   canonical journal.
3. Every externally visible tool side effect has a durable operation identity and an explicit persisted outcome:
   succeeded, failed, or unknown.
4. A process restart reconciles pending outbound work without silently dropping it or blindly retrying an ambiguous
   remote acceptance.
5. Empty channel authorization configuration is fail-closed. Public access requires an explicit wildcard.
6. Failover decisions use structured provider errors whenever the provider protocol exposes structured status.
7. Turn finalization does not infer terminal behavior from unrelated mutable fields.
8. Supported build targets receive at least compile-level CI coverage, and concurrency-sensitive runtime packages
   receive race coverage.

These are system invariants, not implementation prescriptions. Individual milestones may choose smaller internal
types as long as the resulting behavior is explicit and testable.

## Priority Findings

### R0: Final Reply Admission Is Not Part of Inbound Acknowledgement

Status: implemented. Final reply admission is part of the inbound acknowledgement contract, and rejected or
cancelled outbound admission cannot acknowledge durable ingress work.

The aggregated final response path calls `MessageBus.PublishOutbound`, ignores its returned error, and then reports
the turn as handled. The inbound coordinator subsequently acknowledges the durable ingress item. The outbound bus is
an in-memory buffered channel, and shutdown drains queued outbound values without persisting or delivering them.

This permits the inbound item to be acknowledged even when bus admission fails. A separate loss window remains after
successful in-memory admission and is owned by R2.

#### Direction

- Return a typed final-delivery admission result from the agent outbound boundary.
- Do not acknowledge inbound work after rejected or cancelled outbound admission.
- Record a runtime event for rejected, durable, delivered, definitely failed, and ambiguous outcomes.
- Keep media-only final responses outside footer requirements; media delivery still needs an explicit delivery
  outcome where it participates in inbound acknowledgement.

#### Acceptance Criteria

- Tests prove that a closed bus, cancelled context, and rejected channel queue cannot produce an inbound ack.
- Successful acknowledgement requires intentional no-output or suppression, synchronous delivery success, or
  successful transfer to the current outbound owner. R2 upgrades that ownership transfer from memory to durable state.
- Existing final reply, streaming, steering, and message-tool suppression behavior remains covered.

### R1: Canonical Session Writes Fail Open

Status: implemented. Contextual, error-returning turn-journal writes make the root user append a mandatory admission
barrier before model or tool execution.

`SessionStore` retains fire-and-forget write methods, while error-aware writes are an optional interface. The root
user message path logs or forwards a canonical write error to the context manager but continues into model and tool
execution. JSONL compatibility methods also use `context.Background()` internally.

On storage failure, MintClaw can therefore perform an external side effect and acknowledge the inbound message even
though the causal user input and tool transcript are absent from canonical history.

#### Direction

- Introduce a mandatory, contextual, error-returning turn journal for turn-critical writes.
- Treat the root user append as a turn admission barrier.
- Define explicit policies for assistant tool-call, tool-result, suspension, and final-response writes.
- Preserve best-effort writes only for passive observations where loss does not invalidate a turn.
- Use bounded recovery contexts only after ownership has intentionally moved out of the request context.

#### Acceptance Criteria

- Root append failure prevents all LLM and tool execution.
- A post-side-effect persistence failure leaves a durable `unknown` or `degraded` outcome rather than success.
- Fault-injection tests cover append, flush, rename, and fsync failures.
- Request cancellation and shutdown behavior are deterministic and do not depend on hidden background contexts.

### R2: Durable Outbound Delivery Ownership

Status: implemented. Final text and media responses transfer to one canonical
outbox before inbound acknowledgement. Typed adapter outcomes persist remote
acceptance, platform IDs, and retry deadlines; startup replays only `pending`
and `definitely_failed` records while interrupted attempts become `ambiguous`.
See [Durable Outbound Delivery](../durable-outbound-delivery.md).

Fixing error propagation in R0 closes immediate fail-open behavior but does not protect a reply already accepted by
the in-memory bus. A durable outbound outbox is needed to survive the process boundary.

#### Direction

- Persist an outbound intent keyed by a stable turn and message identity before acknowledging ingress.
- Track `pending`, `delivered`, `definitely_failed`, and `ambiguous` states.
- Persist platform message IDs and retry-after metadata where channel APIs provide them.
- Reconcile pending records on startup.
- Retry only outcomes known to be unaccepted. Preserve ambiguous outcomes for explicit reconciliation or operator
  policy.

#### Acceptance Criteria

- Crash tests cover every boundary between outbox persistence, ingress ack, channel send, and outcome persistence.
- Restart resumes definitely pending messages.
- Ambiguous acceptance is never blindly retried by default.
- Documentation promises at-least-once delivery with explicit ambiguity, not universal exactly-once delivery that
  arbitrary chat APIs cannot provide.

### R3: Empty Channel Allowlists Deny by Default

Status: implemented. `BaseChannel` treats omitted, empty, and blank-only `allow_from` lists as deny-all. Explicit
`["*"]` configuration remains available for intentionally public channels, and doctor distinguishes blocked channels
from public ones.

#### Direction

- Make omitted or empty `allow_from` deny all senders.
- Require `allow_from: ["*"]` for a public channel.
- Audit and update deployed configurations before rolling out the new runtime.
- Make onboarding require an owner identity or explicit public policy before enabling an inbound channel.

#### Acceptance Criteria

- Every channel factory inherits the same deny-empty rule through contract tests.
- Startup refuses an enabled, empty-allowlist channel in strict mode.
- Public access remains possible only through explicit configuration.
- Migration guidance identifies affected configuration keys and recovery steps.

## Structural Refactoring

Structural work follows the reliability milestones unless a small extraction is required to implement them safely.

### R4: Narrow Turn Phase State

Status: implemented. Finalization consumes a typed `FinalizationContext`, each model call owns a short-lived
`LLMIterationState`, and typed dispositions replace shared terminal-control booleans.

`turnExecution` mixes history, model selection, streaming, tool execution, persistence errors, usage, and final
rendering. Typed `LLMCallOutcome` and `ToolLoopOutcome` improved terminal control flow, but `CallLLM` and
`ExecuteTools` still mutate a large shared state object and have high cyclomatic complexity.

#### Direction

- Introduce `FinalizationContext` containing only final content, media, model and usage metadata, stream disposition,
  and history commit information.
- Split LLM work into request preparation, provider invocation/failover, and response normalization.
- Split tool work into admission, approval, execution, result persistence, and batch outcome.
- Move per-iteration data into a short-lived `LLMIterationState`; keep only turn aggregates in turn-owned state.
- Replace booleans such as `allResponsesHandled` with typed dispositions.

#### Acceptance Criteria

- Finalization consumes a typed input and does not read unrelated iteration state.
- Usage aggregation, retained terminal content, streaming completion, compaction, and persistence-failure invariants
  have focused tests.
- Each extraction preserves behavior and is reviewable as a focused PR; no all-at-once pipeline rewrite is required.

### R5: Normalize Provider Contracts

Provider failover classification currently combines structured HTTP errors with broad string and regular-expression
matching. Provider capabilities are discovered through several optional interfaces and duplicated catalog metadata.

Status: implemented. Provider adapters normalize protocol failures into `ProviderError` at their boundaries, with
structured metadata taking precedence over the isolated legacy classification fallback. Built-in providers publish a
normalized `ProviderCapabilities` descriptor, while centralized streaming and image-generation compatibility edges
preserve external providers without spreading optional-interface assertions through the agent pipeline. Cross-family
contract tests cover the required failure taxonomy and capability invariants.

#### Direction

- Normalize provider failures at adapter boundaries into `ProviderError` with kind, HTTP status, retry-after,
  request ID, safe message, and wrapped cause.
- Add a `ProviderCapabilities` descriptor for streaming, thinking, native search, image generation, and schema limits.
- Retain text classification only as a compatibility fallback for providers that cannot expose structured errors.

#### Acceptance Criteria

- Contract tests cover authentication, billing, rate limiting, context overflow, timeout, cancellation, and transient
  server failures for each provider family.
- Structured provider status always takes precedence over message matching.
- Adding a provider does not require new optional-interface assertions throughout the agent pipeline.

### R6: Complete Channel Runtime Ownership

Status: implemented. `channels.Manager` is now a composition facade over `ChannelLifecycle`, `DeliveryRuntime`, and
`StreamCoordinator`, plus routing dependencies such as the bus, runtime events, and durable outbox. It has no
promoted worker, queue, retry, listener, stream, placeholder, or interaction maps.

`DeliveryRuntime` owns delivery registration, dispatcher lifetime, workers, queue admission, retry decisions, and
delivery outcomes. `StreamCoordinator` owns stream lifecycle, final-stream metadata, placeholders, typing/reaction
state, and the `ToolFeedbackCoordinator`. `ChannelLifecycle` owns the channel/config registry, shared HTTP runtime,
restart-required hashes, and serialized start, stop, reload, registration, and shutdown transitions.

The extraction landed incrementally in PRs 595, 599, 605, 607, 609, 610, 612, 613, and 615. Focused tests cover
owner registration, queue admission and drain, retry outcomes, stream finalization and cancellation, placeholder and
tool-feedback cleanup, restart-required reload, concurrent/repeated startup and shutdown, reload-versus-shutdown
serialization, and one HTTP serve loop per lifecycle generation. The final merged-main audit passed channel race
tests, affected package tests, tagged tests, and repository lint.

#### Completion Audit (2026-08-09)

1. **Manager is a facade.** Its fields are limited to the message bus, runtime events, durable outbox, and references
   to the three owners. Static searches find no promoted or duplicate mutable delivery, stream, interaction, HTTP, or
   lifecycle state.
2. **Tests use owner boundaries.** Channel tests install and inspect state through package-private owner operations and
   narrow fixtures. Static searches find no direct mutation of Manager-owned or promoted registry, stream,
   interaction, hash, or restart-required maps.
3. **Every runtime resource has one named owner.** `DeliveryRuntime` and per-channel `deliveryOwner` own dispatch,
   workers, queue admission, retries, and outcomes; `StreamCoordinator` and `ToolFeedbackCoordinator` own streams and
   transient interactions; `ChannelLifecycle` owns listeners, channel/config state, and lifecycle transitions.
4. **Transitions are deterministic.** One lifecycle transition mutex serializes setup, start, stop, reload, register,
   and unregister. Repeated start retries incomplete channels without duplicating dispatch or HTTP serving; repeated
   stop shares one drain; reload actions finish before return; restart-required behavior remains conservative.
5. **Regression coverage is explicit.** Focused tests cover delivery admission, draining, retry and ambiguous outcomes;
   stream update/finalize/cancel and final metadata; placeholder, typing, reaction, and tool-feedback cleanup; partial
   startup recovery; repeated/concurrent start and stop; HTTP serving; reload drain and shutdown ordering.
6. **The PR sequence is merged.** PRs 595, 599, 605, 607, 609, 610, 612, 613, and 615 each landed as merge commits
   after their required local validation, CI, current-head review, feedback resolution, and repository-owner approval.
7. **Merged main is clean.** The code-bearing merged-main tree at `ecc45a1a`, unchanged by the docs-only completion
   merge, passes `make lint`, `go test ./pkg/channels`, `go test -race ./pkg/channels`, and
   `go test -tags goolm,stdjson ./pkg/channels ./pkg/gateway ./pkg/agent` pass. Documentation validation passes with
   `make lint-docs`. No obsolete compatibility ownership path remains. R7 is intentionally out of scope.

#### Direction

- Remove promoted delivery-registry and interaction-state compatibility fields after tests use narrow fixtures.
- Let `DeliveryRuntime` own workers, queues, retry decisions, and delivery outcomes.
- Let `StreamCoordinator` own stream lifecycle and final metadata.
- Let `ChannelLifecycle` own start, stop, reload, and HTTP registration.
- Keep `Manager` as a composition facade; do not add speculative generic supervision or broaden hot reload.

#### Acceptance Criteria

- Manager tests use public or package-private owner operations rather than direct map mutation.
- Shutdown and reload have one owner for every queue, worker, listener, and transient interaction.
- Existing conservative restart-required behavior remains unchanged unless a separate design admits hot replacement.

### R7: Expand Reliability Verification

Status: implemented. Pull-request CI now compiles the full tagged Go test graph for Darwin ARM64 and Windows AMD64,
runs targeted race coverage in a separate Ubuntu job, and executes a focused native macOS portability suite with
package parallelism capped at four. The original default-parallelism macOS timeout is replaced by a bounded command
that has passed locally and in CI without an unexplained timeout.

The implementation landed in PRs 623, 625, and 627. PR 623 also fixed missing Unix build constraints that the new
Windows full-graph compile check exposed in the launchd implementation and tests. PR 625 moved the existing
agent/cron/memory race regressions off the serial test path and added full race suites for bus, events, channels,
tasks, interactions, and node WebSocket transport. PR 627 added native filesystem, process, path, symlink, lifecycle,
atomic-write, updater, and workspace coverage on `macos-latest`.

Observed CI timings confirm that the new jobs remain outside the pull-request critical path. Cross-platform compile
jobs completed in about 2 minutes 36 seconds to 2 minutes 53 seconds while the full Tests job took about 5 minutes 30
seconds. The final Race job completed in about 2 minutes 13 seconds against a 4 minute 9 second Tests job, and macOS
Portability completed in about 1 minute 21 seconds against a 4 minute 8 second Tests job.

Production lint already enables `errcheck`, `errorlint`, and `staticcheck`; the remaining broad test-file `errcheck`
exclusion is a separate maintenance backlog, not an R7 runtime-verification gap. `govulncheck` remains required in
CI. A separately tuned additional security scanner remains deferred until its rules and baseline are admitted; R7
does not add an untuned `gosec` warning flood.

#### Completion Audit (2026-08-09)

1. **Supported targets compile before merge.** Native Ubuntu tests plus full-graph `CGO_ENABLED=0` compile checks for
   Darwin ARM64 and Windows AMD64 are required PR jobs. Launchd implementation and tests are constrained to Linux and
   Darwin, while retaining both Linux CI and native Darwin coverage.
2. **Concurrency-critical paths run under the race detector.** The parallel Race job covers the retained
   agent/cron/memory regressions and complete bus, events, channels, tasks, interactions, and node WebSocket suites.
3. **macOS behavior executes natively with bounded fan-out.** The documented command is:

   ```sh
   go test -count=1 -p=4 -tags goolm,stdjson \
     ./pkg/fileutil ./pkg/tools/fs ./pkg/tools/shellguard ./pkg/tools \
     ./cmd/mintclaw-node ./pkg/updater ./pkg/workspace
   ```

   It covers atomic replacement, filesystem and path handling, symlink confinement, process/session termination,
   launchd lifecycle, updater behavior, and workspace paths. It passed locally in about 28 seconds and in CI without
   timeout.
4. **Added verification is parallel and focused.** Compile, Race, and macOS Portability are independent jobs. Their
   observed durations remained below the existing Tests critical path, so R7 increased runner work without increasing
   observed pull-request wall-clock time.
5. **Merged main satisfies the checks.** At `cd70faeb`, both cross-compiles, both race commands, and the documented
   native macOS command pass. PRs 623, 625, and 627 were merged only after green CI, current-head automated review,
   no unresolved feedback, and repository-owner rocket approval.

#### Direction

- Add compile checks for at least Darwin ARM64 and Windows AMD64 to pull requests.
- Add a macOS test job for filesystem, process, path, symlink, and atomic-replacement behavior.
- Run targeted `-race` suites for bus, events, agent, channels, tasks, interactions, and node transport packages.
- Diagnose or cap package-level test fan-out where the macOS suite can exhaust a shared resource.
- Burn down `errcheck`, `errorlint`, and `staticcheck` by package, enabling each cleaned scope rather than accepting a
  repository-wide warning flood.
- Add a separately tuned security static-analysis job while retaining `govulncheck`.

#### Acceptance Criteria

- Every supported release target compiles in CI before merge.
- Concurrency-critical packages pass targeted race tests.
- The documented macOS test command completes reliably without an unexplained timeout.
- New tests are linted, and cleaned packages cannot reintroduce their retired lint backlog.

## Recommended Delivery Order

1. R0: propagate final delivery admission and gate inbound acknowledgement.
2. R1: make canonical root persistence a mandatory turn admission barrier.
3. R3: migrate channel allowlists to explicit public access.
4. R2: add the durable outbound outbox and restart reconciliation.
5. R4: narrow finalization and per-iteration turn state.
6. R5: normalize provider errors and capabilities.
7. R6: complete channel manager ownership extraction.
8. R7: expand CI continuously alongside the affected milestones.

R0 and R1 should be separate, narrowly scoped PRs because they change different failure boundaries. R2 is a larger
design and implementation series; it should begin only after R0 defines the admission result and acknowledgement
contract it will satisfy.

## Stop Conditions

This roadmap is not an open-ended mandate to reduce file size or complexity scores. Work stops when:

- all required reliability invariants are enforced by tests;
- R0 through R3 have no unresolved correctness or security findings;
- R4 through R6 have explicit owners and typed boundaries at the identified hotspots;
- R7 covers supported platforms and concurrency-sensitive packages;
- current CI is green and there are no unresolved major review findings on the final milestone PR.

Large files that retain one coherent responsibility are not automatically refactoring targets. Additional cleanup
requires a new concrete defect, ownership ambiguity, measurable maintenance cost, or separately approved roadmap.

## Non-Goals

- Rewriting the runtime or changing all subsystem boundaries at once.
- Claiming exactly-once delivery across third-party channel APIs.
- Adding a generic supervisor abstraction without an admitted lifecycle requirement.
- Refactoring channel adapters only to make their file sizes uniform.
- Enabling every dormant linter as an immediate repository-wide merge blocker.
