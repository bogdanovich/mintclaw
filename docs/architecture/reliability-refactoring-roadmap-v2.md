# Reliability and Refactoring Roadmap V2

Status: proposed
Audit baseline: `origin/main` at `f71ed1a0`, 2026-08-09
Previous baseline: `d796943c`, 2026-08-01

## Purpose

This is the second architecture and reliability pass after the original
[Reliability and Refactoring Roadmap](archive/reliability-refactoring-roadmap.md) was completed.

It is deliberately not a general cleanup mandate. A large file, repeated vocabulary, or a high package fan-out is
not enough to justify a refactor. Work qualifies for this roadmap only when the current code demonstrates at least
one of these conditions:

- a correctness or durability invariant can fail;
- one runtime resource or state transition has ambiguous ownership;
- failure recovery can leave a mixed or partially active generation;
- concurrent writers can silently lose accepted state;
- repeated defects show that an existing boundary is not holding.

Every admitted item below has a bounded behavioral outcome and a stop condition. When those items are complete, this
roadmap is complete. Further structural work requires new evidence and a new decision.

## Audit Scope

The baseline is 661 commits after the first audit and includes 833 changed files, about 111,000 additions, and about
12,000 deletions. The current tree contains about 245,000 production Go lines and 247,000 test lines across 1,440 Go
files, including 574 test files.

The second pass reviewed:

- agent turn admission, LLM retries, tool persistence, finalization, and session recovery;
- gateway startup, shutdown, service ownership, and config reload;
- config loading, migration, public/security persistence, web mutations, and CLI writers;
- channel delivery, streaming, lifecycle, and durable outbox ownership;
- task, interaction, Seahorse, browser, node invocation, job, update, and file-transfer state machines;
- package dependency fan-in and fan-out, large production and test files, goroutine ownership, ignored errors, panic
  recovery, and `context.Background()` use;
- pull-request CI, cross-platform compilation, race coverage, static analysis, and current product roadmaps.

The following tagged baseline tests pass:

```sh
go test -tags goolm,stdjson \
  ./pkg/session ./pkg/agent ./pkg/gateway ./pkg/config ./web/backend/api
```

## Current Assessment

MintClaw is in substantially better architectural condition than at the first audit. It is suitable for continued
incremental development and does not need a broad rewrite.

The strongest parts are now explicit rather than conventional:

- durable ingress and outbound delivery transfer ownership before acknowledgement;
- the turn pipeline uses typed LLM, tool-loop, and finalization outcomes;
- Seahorse has a documented canonical-versus-derived-state contract;
- provider failures and capabilities are normalized at provider boundaries;
- channel lifecycle, delivery, and streaming have distinct state owners;
- tasks, interactions, node operations, browser sessions, updates, and file transfers use bounded durable state
  machines;
- strict node decoding and authority checks are backed by vertical and recovery tests;
- PR CI includes production lint, vulnerability checks, full tagged tests, targeted race suites, Darwin and Windows
  compilation, native macOS portability, and integration suites.

The remaining concerns are narrower. Three transaction boundaries can still admit split state under rare failures:

1. one LLM retry path replaces canonical history through a fire-and-forget compatibility API;
2. gateway startup and reload mutate live resources without one generation-level commit and rollback owner;
3. configuration writers perform independent load-modify-save sequences without versioned, cross-writer
   serialization, while public and secret data are committed separately.

These are reliability defects, not reasons to reorganize unrelated packages.

## R0: Close the Turn-Critical Session Mutation Escape Hatch

Priority: high
Estimated shape: one focused code PR

### Evidence

`session.SessionStore` correctly states that turn-critical writes must use the contextual, error-returning
`TurnJournal` contract. Most turn execution now follows that rule.

The vision-unsupported retry in `pkg/agent/pipeline_llm.go` is the remaining exception. When media came from stored
history rather than the current turn, it:

1. strips media from the in-memory call history;
2. replaces `exec.history`;
3. calls `Sessions.SetHistory`, whose compatibility contract cannot return a persistence error;
4. refreshes the canonical restore point;
5. retries the provider call.

A failed canonical replacement can therefore leave the retry and restore state different from durable history while
the second provider call still runs. Existing vision tests cover current-turn media preservation, but not a failed
historical-media replacement.

The other production `SetHistory` and `SetSummary` calls are administrative clear operations. They should become
error-aware, but they do not justify a broad session backend rewrite.

### Direction

- Add a contextual, error-returning contract for canonical history replacement and session clearing.
- Keep append and replacement semantics distinct; a replacement is not an append disguised behind a generic method.
- Make the vision retry stop before its second provider call when the canonical replacement is not known durable.
- Reconcile or restore in-memory turn state deterministically after a failed replacement.
- Narrow the interface exposed to active turn execution so compatibility `Add*`, `Set*`, and `Truncate*` methods
  cannot be selected accidentally.
- Retain compatibility methods for passive, administrative, benchmark, and test consumers until they have a concrete
  reason to migrate.

### Acceptance Criteria

- An injected replacement failure prevents the media-free retry provider call.
- After every injected pre-commit, committed-with-error, and cancellation outcome, durable history and the turn's
  restore point agree on the retained media boundary.
- Context cancellation is propagated; the retry path does not introduce a hidden unbounded background write.
- Context-manager clear operations report persistence failures instead of silently claiming success.
- A focused static or compile-time contract prevents active turn code from calling fire-and-forget history mutation.
- Existing stateless turns, current-turn media behavior, historical-media retry behavior, and recovery tests remain
  green.

### Stop Condition

Stop when all active turn mutations use the error-returning contract and the failure tests pass. Do not remove the
legacy `SessionManager`, rewrite JSONL storage, or migrate every test fixture solely to make the interface smaller.

## R1: Make Gateway Startup and Reload Transactional

Priority: high
Estimated shape: two or three focused code PRs

### Evidence

`setupAndStartServices` starts cron and heartbeat before later fallible media, outbox, channel, node, tool, and browser
setup. Its late error paths clean up different subsets of those resources. For example, some paths stop media or the
browser but leave cron and heartbeat running. `Run` has already started the agent loop before this function and does
not have one owner that closes the agent, message bus, provider, and all partially started services on startup error.

Reload has the same problem at a larger boundary:

- `handleConfigReload` stops reload-scoped services before all restart-required conditions are preflighted;
- `ReloadProviderAndConfig` publishes the new agent registry and config, resets hook and MCP runtimes, and closes the
  old provider before gateway services have restarted successfully;
- `restartServices` replaces fields on the live `services` value step by step;
- a late channel, node, media, or browser failure can leave a mixture of old and new runtime state;
- the current test suite proves browser lease retention and several component rollbacks, but not a full failed
  startup or reload transaction across all owned resources.

Seahorse is intentionally restart-required and hot reload is disabled by default. R1 does not change that product
decision. It requires the rejection to happen before healthy services are stopped.

### Direction

- Introduce an explicit gateway runtime generation that owns its generation-scoped services and cleanup stack.
- Classify resources as process-lifetime, reload-persistent, or generation-lifetime. Keep channels and node admission
  reload-persistent only where their existing durability contracts require it.
- Prepare fallible resources before publication where ports and external leases allow it.
- Give non-parallelizable reconciliation an explicit snapshot, commit point, and reverse-order rollback path.
- Preflight restart-required config changes before quiescing the active generation.
- Publish the provider, agent config/registry, tool registry, and gateway service generation as one logical commit, or
  retain the old generation until every required new component is ready.
- Use bounded cleanup contexts and aggregate cleanup failures instead of dropping them.
- Keep this abstraction gateway-specific. It is a lifecycle transaction, not a generic service framework.

### Acceptance Criteria

- Fault-injection tests cover every fallible startup stage after cron begins and every reload stage after quiescing
  begins.
- Failed startup closes all listeners, providers, stores, workers, timers, the message bus, and the agent loop that
  were created for that attempt.
- Failed reload leaves either the complete old generation or the complete new generation active, never a mixed
  provider/config/service set.
- The old generation remains serviceable after a failed reload unless rollback itself reports a terminal degraded
  state that requires process restart.
- A Seahorse or workspace restart-required change is rejected without stopping cron, heartbeat, channels, media,
  browser, or device services.
- Goroutine, listener, browser lease, outbox, media-store, channel, and node-admission ownership is asserted after
  rollback and shutdown.
- Existing conservative channel reload and durable outbox recovery semantics remain unchanged.

### Stop Condition

Stop when startup has complete reverse-order cleanup, reload has an explicit preflight and commit boundary, and
fault-injection tests prove old-or-new generation behavior. Do not add general supervision, automatic process restart,
or Seahorse hot reload as part of R1.

## R2: Add a Versioned Single-Writer Configuration Boundary

Priority: medium-high
Estimated shape: two focused code PRs plus endpoint migration in bounded batches

### Evidence

The web backend contains 13 production `SaveConfig(h.configPath, ...)` calls and 36 mutable
`LoadConfig(h.configPath)` calls. The handler has locks for OAuth and QR flows, but no lock or revision owner for
configuration. Multiple endpoints independently load, mutate, validate, and save the same document.

Atomic file replacement prevents a torn JSON file, but it does not prevent this sequence:

1. request A and request B load the same revision;
2. A changes models while B changes a tool or channel;
3. both writes succeed;
4. the last writer silently removes the other accepted change.

The problem also crosses process boundaries because CLI commands write the same config. An in-process web mutex alone
would not be sufficient.

`SaveConfig` first commits `.security.yml` and then commits `config.json`. A failure between those writes can expose a
secret document from one revision with a public document from another. Migration makes this less observable by
ignoring the `SaveConfig` error and logging migration success. Most web reads also call the migration-capable
`LoadConfig` even though `LoadConfigReadOnly` exists.

### Direction

- Introduce one config repository API with separate read-only and mutation operations.
- Serialize a complete load, mutate, validate, and commit transaction per canonical config path.
- Add a persisted revision or content identity and reject stale full replacements instead of silently overwriting a
  newer revision.
- Coordinate web and CLI writers with a cross-platform file lease or an optimistic compare-and-commit protocol.
- Stage public and security documents under one recoverable transaction identity. On crash, recovery must select the
  complete previous or complete next pair.
- Propagate migration persistence failures and only report migration success after the new revision is durable.
- Use read-only loading for GET, status, and discovery paths that must not migrate or write.
- Keep endpoint-specific validation and response shaping in the endpoint packages; the repository owns transaction
  semantics, not every config business rule.

### Acceptance Criteria

- Concurrent disjoint web mutations both survive, or one receives an explicit conflict that can be retried.
- A stale `PUT /api/config` cannot silently erase a newer model, tool, channel, or credential update.
- Concurrent web and CLI mutation tests are deterministic on Linux, macOS, and Windows.
- Fault injection before and after each public/security commit boundary recovers one complete revision pair.
- Config migration returns a persistence error and does not log success when backup, security, or public commit fails.
- Read-only API paths do not write migration files, backups, security documents, or config documents.
- Existing environment overrides, secure-string masking, virtual model filtering, and config watcher behavior remain
  covered.

### Stop Condition

Stop when all production config writers use the transaction boundary and stale writes are explicit. Do not merge all
web API handlers, redesign the config schema, or move secrets back into the public document as part of R2.

## Deliberately Rejected Refactors

The audit found several large or central modules. They are not current roadmap items:

- `pkg/channels/manager.go` is physically large, but its mutable state is already owned by `ChannelLifecycle`,
  `DeliveryRuntime`, and `StreamCoordinator`. A physical file split alone has no demonstrated reliability value.
- `pkg/agent/pipeline_execute.go` is large, but admission, approval, invocation, persistence, batch aggregation, and
  finalization now have typed stages and outcomes. R0 repairs one escaped persistence call instead of reopening R4.
- `pkg/seahorse/store.go`, `pkg/tasks/registry.go`, and `pkg/interactions/registry.go` are cohesive durable repositories
  for different state machines. A generic registry abstraction would hide their distinct transition rules.
- Companion file-transfer state, gateway transfer-spool state, and model-facing delivery state are intentionally
  different ownership layers. Strict decoding and contract tests are more valuable than one shared enum.
- `pkg/gateway` and `pkg/agent` have high internal package fan-out because they are the composition and orchestration
  roots. Fan-out is not evidence that either should become a generic kernel.
- The remaining test-file `errcheck` backlog is maintenance work already isolated from production lint. It is not an
  architecture milestone.
- The local coding agent, browser parity, node companion, and Android plans are product roadmaps. This document does
  not duplicate their admitted feature work.

## Deferred Decisions

### Legacy SessionManager

`SessionManager` remains the fallback for the config-only personal runtime and a convenient in-memory-compatible test
store. Its contextual `AppendTurnMessage` and `RestoreTurnSnapshot` methods do return persistence errors when a
storage path is configured. Deleting it now would remove a recovery path and require broad fixture churn without
fixing R0.

Reconsider removal only after the normal gateway uses a strict, error-returning runtime store constructor, deployed
legacy JSON sessions have a completed migration policy, and no supported recovery mode depends on the fallback.

### Observer Panic Reporting

The provider fallback observer deliberately cannot break model fallback and currently recovers callback panics. A
focused logging and regression-test improvement is reasonable if this path is touched, but it does not justify an
architecture milestone.

## Delivery Order

1. R0: close the single active turn-history mutation escape hatch.
2. R1a: introduce gateway resource ownership and complete startup rollback.
3. R1b: add reload preflight, generation commit, and failed-reload rollback.
4. R2a: add the versioned config repository and recoverable public/security commit.
5. R2b: migrate web and CLI writers in bounded batches, then forbid direct production writes outside the repository.

R0 is independent and small. R1 should land before further expansion of gateway-owned services. R2 can be developed
in parallel after its public mutation and revision contract is admitted, but endpoint migration should not bypass R1
reload semantics.

## Roadmap Stop Conditions

This roadmap is complete when:

- R0, R1, and R2 acceptance criteria are enforced by tests;
- current production lint, tagged tests, affected race suites, cross-platform compile jobs, and documentation lint are
  green;
- no unresolved major review finding remains on the final milestone;
- direct active-turn compatibility writes, partial gateway generations, and unversioned production config writes are
  absent from their stated scopes.

After that point, stop. Do not continue splitting files, moving types, renaming packages, introducing supervisors, or
consolidating state machines without a new concrete defect, ownership ambiguity, measured maintenance cost, or
separately approved product requirement.

## Non-Goals

- Rewriting the agent pipeline, gateway, config schema, or web API.
- Reducing line counts or package fan-out as goals in themselves.
- Supporting in-process Seahorse hot reload.
- Claiming exactly-once behavior across files, processes, providers, or third-party channel APIs without a protocol
  that can provide it.
- Removing compatibility code that still has a documented runtime or migration role.
- Duplicating milestones from active product roadmaps.
