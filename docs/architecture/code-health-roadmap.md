# Code Health Roadmap

Status: active.

Audit baseline: `origin/main` at `e8953566`, 2026-09-03.

## Objective

Reduce the change amplification and hidden ownership that make new MintClaw
features expensive, while preserving current behavior and the repository's
existing durability and security boundaries. Execute the work as small,
reviewable packets. A packet should remove duplicated state, branching, or
implicit dependencies before it adds a new abstraction.

This roadmap is complete only when every packet below is either merged and
validated or explicitly removed from scope with recorded evidence that the
underlying problem no longer exists.

## Guardrails

1. Do not turn this program into a repository-wide rewrite.
2. Preserve the current `ChannelLifecycle`, `DeliveryRuntime`, and
   `StreamCoordinator` ownership model. Do not introduce a generic supervisor.
3. Do not split files only because they are large. Split after state and
   lifecycle ownership become clear.
4. Do not introduce a general dependency-injection or workflow framework.
5. Keep security policy checks explicit at every authority boundary.
6. Treat persisted-data and node-wire changes as coordinated cutovers with
   inventory, rollback, and compatibility evidence.
7. Start each implementation packet from the latest `origin/main`. Keep each
   packet independently reviewable and run the narrowest relevant tests plus
   the repository-required formatting and lint checks.

## Audit Summary

The repository is not uniformly over-engineered. Previous simplification work
established useful ownership boundaries for channel lifecycle, durable ingress
and outbound delivery, task state, and several node/browser contracts. The
remaining high-impact debt is concentrated in a few vertical slices:

- turn execution and finalization;
- durable human-interaction orchestration;
- configuration projection and secret serialization;
- web-launcher gateway process ownership;
- node JSON number canonicalization;
- frontend validation and duplicated model forms;
- broad dependence on the root `config.Config` object.

The audit baseline contains about 290,000 lines of production Go, 314,000 lines
of Go tests, and 31,000 lines of frontend source. Ninety-eight production Go
files refer directly to `*config.Config`. These figures are navigation signals,
not targets by themselves.

## Execution Order

| Packet | Scope | Depends on | Status |
| --- | --- | --- | --- |
| H0 | Restore a trustworthy development baseline | None | Completed |
| H1 | Make config secret projection explicit | H0 | In progress |
| H2 | Give the web gateway process one instance owner | H0 | In progress |
| H3 | Add frontend contracts and remove model-form duplication | H0 | Completed |
| H4 | Extract human-interaction application orchestration | H0 | Completed |
| H5 | Consolidate turn input, runtime state, outcomes, and finalization | H0, H4 characterization tests | In progress |
| H6 | Reduce root-config coupling at high-change boundaries | H1, H2, H5 | Not started |
| H7 | Simplify canonical node JSON numbers through a protocol cutover | H0 | Not started |
| H8 | Remove confirmed legacy and close the program | H1-H7 | Not started |

Packets H1-H4 may proceed independently after H0. H5 should remain a sequence
of small pull requests, not one large branch. H7 is isolated because it changes
hash-bearing protocol representation.

## H0: Trustworthy Development Baseline

### H0.1 Fix concurrent gateway-invocation SQLite initialization

The existing
`TestGatewayInvocationSQLiteConcurrentInitialization` fails repeatedly on the
audit baseline. Parallel constructors sometimes fail to create or open the
anchored initialization lock and fewer stores open than requested.

Deliverables:

- make directory anchoring and initialization-lock acquisition safe for
  concurrent constructors;
- preserve symlink, hard-link, and replacement rejection;
- make the existing concurrency regression pass repeatedly on macOS and Linux;
- run the adjacent SQLite identity, rollback, capacity, and lifecycle tests.

Completion gate:

```text
go test -count=20 -tags goolm,stdjson \
  -run '^TestGatewayInvocationSQLiteConcurrentInitialization$' ./pkg/nodes
```

### H0.2 Stop the Makefile from defeating Go toolchain selection

The Makefile forces `GOTOOLCHAIN=local`. On the audit host, `go.mod` requires Go
1.26.6 while the host-local toolchain is 1.26.5; normal automatic toolchain
selection can supply the required version, but `make lint` disables it.

Deliverables:

- remove the forced local default or fail early with a precise documented host
  prerequisite;
- prove that `make lint` and the documented developer workflow use the same
  toolchain policy as CI.

### H0.3 Add frontend pull-request validation

The current PR workflow has no frontend job. The audit baseline builds and
lints, but `pnpm format` reports eight files and there are no frontend tests.

Deliverables:

- fix the current formatting drift;
- add frozen dependency installation, formatting, lint, and production build
  checks to the PR workflow;
- add a `test` script and a minimal Vitest setup used by H3;
- keep browser end-to-end testing out of this packet unless a concrete browser
  regression cannot be covered below that layer.

### H0.4 Add characterization gates before structural changes

Before H1, H4, or H5 changes production ownership, add focused contract tests
for:

- public config output never containing secret values;
- security config round-tripping every supported secret form;
- every terminal turn path producing exactly one outcome and one finalization;
- interaction answer, cancellation, replay, and restart ordering.

Prefer public or boundary-level tests for these invariants. Do not mass-convert
the existing same-package test suite.

## H1: Explicit Config And Secret Projection

`SecureString.IsZero` currently detects YAML callers through `runtime.Caller`
and a package-path substring. JSON redaction uses a magic `"[NOT_HERE]"` value,
and credential resolution is stored in package-global mutable state.

Deliverables:

1. Introduce explicit public and security document projections at the config
   repository boundary.
2. Remove serializer-stack inspection from `SecureString` and `SecureStrings`.
3. Make credential resolution repository- or loader-owned rather than
   package-global.
4. Preserve encrypted references, file references, plaintext migration policy,
   atomic commit behavior, and revision calculation.
5. Add invariant tests that enumerate every secret-bearing config field.

Completion gate: public output is safe by construction rather than by custom
JSON behavior, and security output round-trips without relying on the calling
serializer's implementation.

## H2: Instance-Owned Web Gateway Process

The web backend keeps process, PID, status, boot signature, logs, deadline, and
token data in a package-global `gateway` structure. HTTP `Handler` instances do
not own that lifecycle.

Deliverables:

1. Introduce one concrete `GatewayProcessManager` owned by `Handler`.
2. Move start, attach, readiness, restart, stop, PID, log, and cached-token state
   behind that owner.
3. Inject only narrow operating-system seams: command runner, clock, process
   inspection, and health request.
4. Migrate config, version, and MintClaw proxy endpoints away from global state.
5. Prove that two Handler instances can be constructed and tested without
   shared resets or cross-instance process state.

Do not create a repository-wide service container as part of this work.

## H3: Frontend Contracts And Shared Model Forms

The frontend config API currently exposes broad `Record<string, unknown>`
objects. Add-model and edit-model sheets duplicate a large form workflow, while
large config sections have no unit-test boundary.

Deliverables:

1. Extract a shared model form schema, state, validation, and submit projection.
2. Keep add and edit containers responsible only for their distinct loading and
   mutation behavior.
3. Add unit tests for config-to-form mapping, form-to-patch projection, secret
   placeholders, provider/model changes, and error preservation.
4. Split config sections by domain when doing so establishes a testable boundary;
   do not split presentation components solely by line count.
5. Replace broad API records with the smallest stable TypeScript contracts that
   cover the endpoints being changed.

OpenAPI or generated clients require separate evidence of contract drift and
are not a prerequisite for this packet.

## H4: Human-Interaction Application Orchestration

The interaction domain registry has a durable state machine, but agent inbound
code also owns route authorization, claim retries, cancellation fences,
persistence ordering, continuation resume, task completion, and delivery. This
forms a second orchestration engine inside the agent package.

Deliverables:

1. Define explicit application commands and results for answer, cancel, and
   resume operations.
2. Move claim/fence/persistence ordering into one `InteractionService` while
   retaining `interactions.Registry` as the domain state owner.
3. Make externally visible effects explicit and idempotent: persisted answer,
   continuation scheduling, task transition, delivery, and cleanup.
4. Reduce agent ingress to classification, route resolution, authorization
   context construction, and service invocation.
5. Preserve restart, duplicate-answer, stale-choice, and cancellation-race
   behavior with characterization and race tests.

Persisted approval cleanup belongs to H8 after deployed-state inventory; it
must not be mixed into this ownership extraction.

## H5: Turn Runtime And Finalization

Turn identity, policy, mutable execution state, model binding, final content,
usage, cancellation, persistence rollback, and subturn coordination currently
overlap across `turnSpec`, `turnState`, `turnExecution`, iteration state, local
loop variables, and finalization results. Several control branches repeat the
same pending-result, rendering, repair, and finalization sequence.

Execute H5 in the following order:

### H5.1 Immutable turn input

- introduce a normalized immutable `TurnInput`/`TurnIdentity`;
- separate caller-owned observation hooks from execution policy;
- remove writable result pointers from the input contract.

### H5.2 One mutable runtime owner

- define the fields owned for the full turn and for one LLM iteration;
- remove duplicate model, content, usage, and cancellation ownership;
- keep subturn synchronization isolated behind explicit methods.

### H5.3 Explicit step outcomes

- normalize LLM and tool controls into a small typed outcome set such as
  continue, suspend, finalize, and abort;
- preserve hook aborts, loop protection, steering, objective repair, and
  pending-subturn semantics without translating through multiple control enums.

### H5.4 One terminal gateway

- route every non-abort terminal path through one sequence for pending results,
  final rendering, bounded objective repair, persistence, delivery, and outcome
  construction;
- guarantee exactly-once finalization with tests;
- remove recursive re-entry used only to schedule objective repair.

### H5.5 Phase-oriented files

Only after H5.1-H5.4, split setup, model iteration, tool execution, repair, and
finalization into files or collaborators that match the established ownership.

Completion gate: adding a new terminal reason or delivery policy changes one
typed transition and one finalization path, rather than several switch branches
and state holders.

## H6: Reduce Root-Config Coupling

Keep parsing and validation centralized, but stop passing the full mutable root
config where a subsystem uses a small stable subset.

Deliverables:

1. Introduce immutable options at the composition root for the browser runtime,
   node tool policy, turn policy, and other high-change paths touched by H1,
   H2, or H5.
2. Resolve effective values once at the documented lifecycle boundary, such as
   process start, request admission, or turn start.
3. Make reload behavior explicit: immutable snapshot replacement or deliberate
   live lookup, never an incidental mixture.
4. Add import/dependency checks only where they enforce a real architectural
   boundary.

Do not attempt to eliminate all `*config.Config` references in one campaign and
do not create one-field interfaces.

## H7: Canonical Node JSON Number Cutover

The strict canonicalizer emits exponent notation for integral values, for
example `60` as `6e1`. Standard typed Go decoding then requires custom numeric
adapters and `float64` intermediates throughout browser/node contracts.

Deliverables:

1. Specify the canonical numeric representation: mathematical integers use
   plain base-10 integer syntax; fractions remain deterministic and bounded.
2. Add golden vectors proving equality across equivalent input spellings and
   rejection of invalid, overflowing, or ambiguous values.
3. Inventory every hash, signature, receipt, persisted record, and gateway-to-
   companion message affected by the representation change.
4. Assign the required protocol-major or minimum-version boundary and document
   gateway/companion rollout and rollback order.
5. Perform the coordinated cutover, then remove custom integer adapters and
   manual `UnmarshalJSON` code made unnecessary by the new representation.

Duplicate-member rejection, bounded decoding, and authority-bearing schema
validation must remain intact.

## H8: Legacy Removal And Closeout

### H8.1 Persisted approval records

Inventory deployed interaction records before changing readers. Convert,
quarantine, or deliberately discard obsolete approval records that lack the
current execution context or argument hash. After the cutover, remove tolerant
steady-state readers and prove that old records cannot recover authority.

### H8.2 Dead provider modes

Remove the advertised GitHub Copilot `stdio` branch if no committed product
milestone will implement it. If it remains planned, give it an owner, contract,
and milestone instead of retaining an always-failing mode.

### H8.3 Test construction seams

The Go suite is extensive but high-change agent tests frequently construct
private `AgentLoop` and `turnState` representations directly. Add canonical
builders and a small set of black-box turn/interaction contract tests, then
migrate tests only when their production area changes. Do not perform a mass
test rewrite.

### H8.4 Documentation closeout

- move completed execution ledgers out of the primary architecture index when
  they no longer describe current architecture;
- archive the completed architecture-simplification roadmap while retaining
  operational cutover evidence;
- replace this document with a short current-state architecture note after all
  completion gates pass.

## Per-Packet Evidence

Every implementation packet records:

- the exact ownership or coupling problem being removed;
- before/after state and branch counts where meaningful;
- focused unit, race, platform, and integration commands;
- compatibility impact and deployment order, if any;
- rollback procedure for persisted or wire-contract changes;
- remaining follow-up, without claiming completion while temporary adapters or
  duplicate ownership remain.

The program is complete when the source tree, tests, current architecture docs,
and deployed compatibility inventory agree on the same ownership and contract
model.
