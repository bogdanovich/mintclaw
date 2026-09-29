# Runtime Capability And Turn-Engine Convergence Roadmap

Status: active

MintClaw baseline: `origin/main` at `dc3df949c`, 2026-09-27

OpenClaw comparison baseline: `openclaw/openclaw` at `389e035c`, 2026-09-27

Implementation progress:

- C0 is complete: deterministic gateway/coding capability baselines and
  incompatibility reasons are frozen in tests.
- C1 is complete: trusted composition roots now provide a runtime-neutral
  context and every admitted turn binds a validated principal before tool
  execution. Existing tool registration remains unchanged.
- C2 is complete. The first packet introduced the atomic composition plan and
  migrated the base coding catalog. The second routes gateway core, shared,
  runtime-injected, and MCP tools plus late-bound coding tools through a stable
  per-agent composer. The third makes the final admitted tool/capability report
  the live skill-compatibility source and exposes structured policy,
  dependency, and runtime diagnostics while retaining legacy tool
  requirements.
- C3 is complete. Coding now admits the shared read-only document tool for
  exact workspace paths and canonical thread attachments, derives ownership
  from the coding runtime principal, and keeps rendered pages in bounded
  turn-scoped media storage. Integration coverage proves inspect, extract,
  render, path confinement, cleanup, and the unchanged gateway PDF workflow;
  forms, retention, approvals, verification, and delivery remain unavailable
  in coding.

## Purpose

Make PDF, browser, and later first-party capabilities available to both the
always-on gateway and the local coding agent without creating two independent
implementations, two policy languages, or one oversized registry that gives
every runtime the same authority.

This roadmap also records the longer-term decision about turn-loop
convergence. The desired architecture is one shared native turn engine behind
separate gateway and coding hosts. It is not a gateway built on the coding TUI
or coding controller.

## Executive Decision

1. Do not make the gateway depend on `pkg/coding`, the coding controller, or
   terminal frontend.
2. Preserve one native LLM/tool progression engine. MintClaw already does most
   of this: coding constructs `NewCodingAgentLoop`, enters through
   `processCodingDirect`, and reaches the same `AgentLoop`, `turnRunner`, and
   `Pipeline` used by gateway turns.
3. Treat gateway and coding as runtime hosts with different admission,
   identity, storage, scheduling, interaction, and presentation concerns.
4. Add one shared capability-composition plan so both hosts can construct tools
   from the same feature implementations while retaining different policies.
5. Do not introduce a general pluggable harness framework until MintClaw has a
   second real turn-engine implementation to justify it.
6. If an external native harness such as Codex is admitted later, place it
   behind an explicit turn-runtime boundary. Do not replace gateway routing,
   delivery, task, interaction, or channel ownership with coding-agent state.

The immediate value comes from shared capability construction, not from moving
gateway lifecycle code into the coding agent.

## Current Architecture Truth

The two products already share the lower turn loop:

```text
gateway channels                         mintclaw code
       |                                      |
inbound routing, claims, recovery       TUI/controller, thread lease
       |                                      |
       |                               native coding adapter
       |                                      |
       +---------- AgentLoop / turnRunner ----+
                              |
                           Pipeline
                              |
             prompt -> model -> tools -> model -> final
```

The shared engine already owns provider execution, context assembly, the model
and tool loop, retries, steering inside a turn, compaction integration, and
finalization. The coding profile changes construction, storage roots, prompt
facts, tool admission, and direct-turn identity; it does not run a separate
model/tool algorithm.

The genuinely different host responsibilities should remain different:

| Gateway host | Coding host |
| --- | --- |
| channel ingress and routing | project and working-directory identity |
| concurrent session claims and recovery | one thread lease and controller serialization |
| durable outbound delivery | TUI and headless coding presentation |
| chat sender and route authority | trusted local execution profile |
| reload generations | restart-bound coding profile |
| channel interactions and media delivery | repository evidence and coding artifacts |

The largest current divergence is tool construction. Gateway shared tools are
registered through gateway-oriented wiring, while the coding registry is an
explicit narrow catalog. Skill compatibility then reflects those separate
registries. This makes PDF and browser appear gateway-only even though their
domain packages are largely reusable.

## OpenClaw Comparison

OpenClaw's current design is not a simple replacement of its original runtime
with Codex. It has an explicit agent-runtime or harness boundary. Its built-in
runtime remains available, while compatible turns may be executed by a Codex
app-server harness.

For a Codex-backed turn, Codex owns the native model loop, native thread, tool
continuation, and compaction. OpenClaw continues to own channel routing,
visible delivery, dynamic OpenClaw tools, approvals, media delivery, and a
transcript mirror. See the pinned
[agent-runtime overview](https://github.com/openclaw/openclaw/blob/389e035c52c1d7ea0332365dc3472983e4f3597b/docs/concepts/agent-runtimes.md)
and
[Codex harness runtime contract](https://github.com/openclaw/openclaw/blob/389e035c52c1d7ea0332365dc3472983e4f3597b/docs/plugins/codex-harness-runtime.md).

The useful lesson for MintClaw is ownership separation, not that a coding
frontend should become the gateway. A native harness may own a complete turn
loop while the always-on product retains transport and delivery ownership.
OpenClaw also demonstrates the cost: tools, hooks, compaction, session truth,
approvals, recovery, and fallback behavior all need an explicit compatibility
contract for every harness.

MintClaw does not need that complexity merely to share PDF and browser tools.
Its coding and gateway paths already use the same native turn engine.

## Target Architecture

```text
pkg/document                    pkg/browser
     |                               |
document service              browser client/broker
     +---------------+---------------+
                     |
             ToolContributor factories
                     |
              RuntimeToolPlan
          +----------+----------+
          |                     |
    coding policy         gateway policy
          |                     |
          +------ ToolRegistry--+
                     |
          effective capability report
                     |
              skill compatibility

GatewayHost ---------------- NativeTurnEngine ---------------- CodingHost
routing/delivery             AgentLoop/Pipeline                 thread/TUI
```

The shared layer composes and reports capabilities. It does not erase runtime
policy or make every capability universally available.

An eventual `TurnEngine` or `AgentHarness` interface is justified only when at
least two implementations exist, for example the native MintClaw pipeline and
a Codex app-server adapter. Until then, keep the current concrete
`AgentLoop`/`Pipeline` path and improve its host boundaries without adding an
interface with one implementation.

## Design Contracts

### Runtime-neutral invocation identity

Feature tools should receive a principal that is not defined by Telegram or by
a repository path:

```text
RuntimePrincipal
  runtime
  actor
  agent
  session
  execution
```

The gateway derives it from routed channel identity. Coding derives it from
the authenticated local operator and coding thread. Feature-specific policy
may still require additional gateway or coding facts.

### Shared composition, separate policy

A `ToolContributor` constructs tools from an explicit runtime context and
returns a capability report. A `RuntimeToolPlan` applies trusted operator
configuration and produces the final registry. Project instructions and skills
may explain a capability but may not add one.

Compatibility should be derived from the final admitted capability set rather
than a second hardcoded coding/gateway tool list. Capability identifiers such
as `document.inspect` and `browser.observe` are preferable to assuming that a
single tool name implies every action.

### Separate canonical state

Coding threads and routed personal sessions remain separate namespaces and
canonical histories. Sharing the turn engine does not mean merging their
stores. Gateway delivery state, coding leases, repository snapshots, and TUI
projection also retain their existing owners.

### One owner for external resources

Browser profiles and sessions must have one broker owner. A coding turn should
normally use an authenticated client to the existing local gateway or
companion browser broker. It must not open a competing broker over the same
persistent profile.

## Delivery Packets

Packets are ordered and should be implemented as focused pull requests.

### C0 - Freeze Current Capability Surfaces

Baseline record:
[C0 Runtime Capability Baseline](runtime-capability-c0-baseline.md).

Scope:

- inventory gateway and coding tool registries, capability dependencies, and
  skill requirements;
- record current PDF and browser authority, artifact, delivery, and lifecycle
  dependencies; and
- add tests that freeze both current effective registries before refactoring.

Completion gate:

- no capability is accidentally added to coding or removed from gateway;
- every runtime-incompatible PDF/browser requirement has one recorded reason;
  and
- registry snapshots are deterministic and policy-sensitive.

### C1 - Runtime Context And Capability Report

Scope:

- introduce runtime kind, runtime principal, artifact access, optional
  delivery, and optional browser client as explicit construction inputs;
- define bounded capability identifiers and structured unavailable reasons;
  and
- keep existing tool registration behavior unchanged.

Completion gate:

- gateway and coding identities map without fake channel or repository values;
- missing optional services produce typed capability diagnostics; and
- no project instruction or skill can mutate the runtime context.

### C2 - Shared Tool Composition Plan

Scope:

- introduce feature-owned contributors and one `RuntimeToolPlan` builder;
- move existing gateway and coding registration into contributors without
  changing their effective surfaces;
- derive skill compatibility from the final capability report; and
- retain policy, collisions, sealing, and deferred discovery behavior.

Implementation sequence:

1. introduce the deterministic `RuntimeToolPlan` contract and feature
   contributors, then migrate the base sealed coding catalog without changing
   its effective surface;
2. move gateway and late-bound coding tool registration behind the same plan,
   retaining their different admission policies and lifecycle ownership; and
3. derive skill compatibility and operator diagnostics from the final admitted
   capability report instead of a second configured tool-name approximation.

Completion gate:

- pre- and post-refactor registry fixtures match;
- gateway and coding select different policies over the same contributors;
- disabling a capability removes both its tools and compatible skills; and
- compatibility diagnostics identify the missing capability or dependency.

### C3 - Coding PDF Read And Render

Scope:

- separate document sources and artifacts from channel/chat/outbox identity;
- add a coding-thread document source and artifact store;
- admit `inspect`, `extract`, and `render` for exact prompt paths inside the
  repository and for coding attachments; and
- keep channel delivery and protected multi-turn form workflows disabled.

Implementation sequence:

1. derive document ownership from the runtime principal rather than synthetic
   channel/chat values, introduce a strictly read-only document surface, and
   let a directly admitted core document tool prepare exact turn selectors
   without requiring gateway-only skill discovery;
2. add the coding-thread source/artifact store, admit the read-only tool and
   its capability report through coding composition, and prove local-path and
   attachment flows end to end; and
3. close the packet with lifecycle, boundary, and unchanged-gateway evidence,
   splitting this step only if the integration review identifies a distinct
   ownership concern.

Completion gate:

- a coding turn can inspect, extract, and render a local or attached PDF;
- artifacts are thread-owned, bounded, and cleaned by the coding lifecycle;
- source paths cannot escape admitted coding scope; and
- existing gateway PDF, form, approval, verification, and delivery tests are
  unchanged.

### C4 - Coding Browser Through The Existing Broker

Scope:

- adapt browser ownership to `RuntimePrincipal`;
- define a `BrowserCapabilityClient` behind the existing browser tool source;
- connect coding through authenticated local gateway or companion IPC;
- admit managed or ephemeral observe, act, capture, and download operations;
  and
- place results in the coding artifact store.

Implementation sequence:

1. derive browser ownership from `RuntimePrincipal` for coding turns and add a
   typed coding browser client over the existing authenticated capability IPC,
   while retaining gateway channel identity and single-broker ownership;
2. project the admitted target/profile operations as the native coding browser
   tool surface for session, context, observe, diagnostics, and action flows,
   preserving safe broker receipts in durable history, keeping page/context
   payloads live-only, and retaining no-replay outcomes; and
3. extend the existing owner-bound artifact describe/fetch protocol to browser
   capture and download results, import verified bytes into the coding-thread
   artifact store, and close with lifecycle and unchanged-gateway evidence.

Completion gate:

- coding never creates a second owner for the same persistent browser profile;
- target, profile, and action policy remain explicit;
- interruption and uncertain outcomes do not blindly replay actions;
- gateway and companion browser behavior remains unchanged; and
- an unavailable broker fails once with an actionable diagnostic.

An isolated embedded ephemeral browser may be considered later as an offline
fallback. It must use a separate profile and must not silently downgrade a
request for an operator-owned persistent session.

### C5 - Capability-Adaptive Skills And Configuration

Scope:

- make PDF and agent-browser skills depend on admitted capability identifiers;
- keep one adaptive skill where the workflow remains truthful, otherwise split
  gateway and coding instructions;
- add trusted coding capability configuration independently from gateway
  configuration; and
- expose effective capability and incompatibility diagnostics in CLI status.

Completion gate:

- the same compatible system skill is selectable in both runtimes;
- every instruction branch names only tools/actions visible in that runtime;
- repository skills cannot enable document, browser, MCP, node, or delivery
  authority; and
- config disablement removes the capability cleanly without stale catalog
  advertisement.

### C6 - Advanced Parity

Scope:

- consider coding-local PDF fields, fill, and verify using a durable artifact
  journal;
- admit protected form workflows only after interaction continuation is
  runtime-neutral;
- consider browser handoff, attached-user profiles, and richer companion
  placement; and
- keep channel delivery as an optional gateway adapter.

Completion gate:

- each advanced action has an explicit authority and state owner;
- protected values and browser credentials do not enter ordinary transcripts
  or diagnostics;
- recovery distinguishes completed, failed, cancelled, and uncertain work;
  and
- no second PDF transaction, browser session, or delivery control plane is
  introduced.

## Deferred Turn-Engine Convergence

The capability packets above do not require a new harness framework. A later
runtime-convergence track should begin only when a concrete second engine or a
measured duplicated responsibility exists.

### R0 - Ownership Matrix And Behavioral Corpus

- document native engine, gateway host, and coding host ownership;
- capture equivalent gateway and coding turn fixtures for streaming, tools,
  steering, compaction, cancellation, interaction, and finalization; and
- measure actual duplicated code rather than inferring duplication from two
  product entry points.

### R1 - Stable Host-To-Turn Contract

- define prepared turn input, typed runtime events, cancellation/steering, and
  terminal result without exposing mutable `AgentLoop` internals;
- adapt both existing hosts to that contract over the native pipeline; and
- preserve separate canonical session/thread stores.

R1 is useful only if it removes concrete coupling. It must not become a broad
service bag or generic supervisor with one implementation.

### R2 - Optional Harness Registry

Proceed only when a second implementation is being admitted. Register the
native MintClaw engine and the new harness behind an explicit compatibility
contract covering:

- model and tool-loop ownership;
- canonical thread ownership and transcript projection;
- tool and hook support;
- context and compaction ownership;
- steering, cancellation, recovery, and fallback semantics; and
- final response and media delivery.

Runtime selection must fail closed when the selected harness cannot preserve
the requested authority or behavior. A failed started turn must not be replayed
through another harness merely because it returned an error.

### R3 - Selective External Harness Use

- route only explicitly compatible model/runtime pairs;
- mirror native thread state without pretending it is MintClaw-owned history;
- bridge only admitted MintClaw dynamic tools; and
- expose the effective runtime and capability differences to the operator.

Replacing the native gateway loop globally is not an acceptance criterion.
Keeping the native engine alongside another harness is valid and likely safer.

## Global Completion Criteria

The capability-convergence roadmap is complete when:

- gateway and coding registries are produced by one composition mechanism with
  different explicit policies;
- PDF inspect/extract/render works in coding without fake channel or delivery
  identity;
- browser tools can use the existing broker from coding without competing
  profile ownership;
- skill compatibility is derived from effective capabilities and reports a
  precise missing reason;
- gateway behavior, delivery, approvals, PDF transactions, and browser
  recovery remain regression-tested; and
- disabling coding PDF or browser removes its tools, skill compatibility, and
  runtime advertisement together.

The deferred runtime track has no implied completion date. It becomes active
only after its admission triggers are satisfied.

## Stop Conditions

Stop and reassess if an implementation:

- makes gateway import the coding controller or terminal frontend;
- merges coding threads with routed personal sessions;
- treats a skill or repository instruction as capability authority;
- requires one flat registry with the same tools in every runtime;
- introduces a second persistent browser-profile owner, document transaction
  engine, outbox, or interaction store;
- adds an abstraction with no second implementation and no removed coupling;
  or
- attempts a harness cutover without explicit tool, history, compaction,
  recovery, and delivery compatibility evidence.

## Relationship To Existing Roadmaps

- The [Local Coding Agent Roadmap](local-coding-agent-roadmap.md) remains the
  source of truth for thread, TUI, project, compaction, and remote coding work.
- The [AgentLoop Runtime Host](agentloop-runtime.md) remains the current native
  host/pipeline ownership contract.
- The [Skill Bundling And Portability Contract](skill-bundling.md) remains the
  source of truth for skill packaging and compatibility semantics.
- The [Reliable PDF Support Roadmap](pdf-support-roadmap.md) owns document
  correctness and protected workflows.
- The [Reliable Browser Capability Roadmap](browser-capability-roadmap.md) owns
  broker, profile, action, artifact, handoff, and placement behavior.

This roadmap owns only cross-runtime capability composition and the admission
boundary for any later turn-engine convergence.
