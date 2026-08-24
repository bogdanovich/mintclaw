# Local Coding Agent P0.2 Pre-construction Runtime Profile

Status: implemented

Roadmap packet: P0.2 in
[`local-coding-agent-roadmap.md`](local-coding-agent-roadmap.md)

## Decision

The coding frontend admits an immutable `CodingRuntimeProfile` before
`AgentRegistry` and `AgentInstance` construction. A profile binds each
configured coding agent ID to one already validated coding-thread
`CodingRuntimeLayout`. The configured agent ID and coding-thread UUID are
independent.

Every configured agent binding is preflighted before the first session store,
context builder, registry member, or tool is created. Missing and duplicate
bindings therefore fail without a split registry or filesystem side effects.
The binding set must exactly equal the configured registry: extra bindings and
duplicate canonical configured IDs are rejected.

The coding layout type cannot express a personal-agent owner. Personal gateway
and CLI loops use their current config-owned workspace lifecycle; they do not
carry coding thread roots, trusted-local tools, or restart-only profile
semantics.

## Construction Boundary

`NewCodingAgentLoop` is the pre-construction entry point. It:

1. validates that the profile covers the complete configured registry;
2. constructs each `AgentInstance` from its bound layout;
3. uses `ExecutionRoot` for the existing workspace-facing read and execution
   APIs;
4. opens the current canonical session backend under
   `StatePaths().SessionsRoot`;
5. places prompt-memory construction under `StatePaths().MemoryRoot`;
6. closes already opened agent resources if a later instance fails; and
7. closes the registry and context manager when loop construction fails.

Coding-profile loops require restart for config or provider changes. P0.2 does
not add a live registry-generation and resource-retirement protocol to the
pre-construction admission boundary. Gateway loops retain their current
hot-reload behavior.

The existing `NewAgentLoop` and `NewAgentLoopChecked` gateway entry points are
unchanged. `NewCodingAgentLoop` is the only profile-based entry point; internal
instance and registry construction helpers remain implementation details.

## Coding Bootstrap Handoff

P0.2 initially constructed coding threads with an empty core registry.
P0.4 replaces that temporary guard with the exact trusted-local catalogue
recorded in
[`local-coding-agent-p0-tool-profiles.md`](local-coding-agent-p0-tool-profiles.md).

Isolated tool bootstrap also makes deferred MCP initialization a no-op across
startup, direct turns, and commands. Isolated skill bootstrap retains
the execution root only for skill discovery while prompt memory continues to
use the external state owner.

The coding profile does not mutate persisted personal configuration.

Coding profiles had to select the `none` context manager when P0.2 landed.
P0.3 removed that temporary restriction by routing derived context through the
external context root. Retrieval tools remain the only context-owned additions
to the fixed P0.4 coding catalogue.

## P0.3 Handoff

P0.2 routes canonical JSONL session initialization through the layout to
establish the no-source-pollution construction invariant. Coding threads
start directly on canonical JSONL. P0.3 replaces that concrete call with
injected, rollback-aware store factories and routes Seahorse through
`StatePaths().ContextRoot`.

Derived coding context is owner-scoped: each coding thread gets its own
Seahorse SQLite file. There is no global, ever-growing coding-context database.
Canonical JSONL remains authoritative and every Seahorse database remains
disposable and rebuildable.

## Done Evidence

Focused tests prove that:

- a configured agent can be bound to a distinct coding thread;
- execution and state roots remain distinct through full loop construction;
- sessions and prompt memory are created under the external state root;
- the source execution root is not created;
- coding tools are admitted only through the exact P0.4 catalogue;
- a missing later-agent binding fails before the first owner's roots are
  created;
- unusable state targets on a later binding fail before an earlier owner's
  state is created;
- physical root isolation is revalidated immediately before construction, and
  missing state targets are probed for creatability without persistent files;
- duplicate bindings, overlapping state roots for distinct threads, duplicate
  configured IDs, extra bindings, and incomplete coding layouts are rejected;
- before P0.3, a coding profile using Seahorse was rejected before either root
  was created;
- coding-profile reload is rejected without replacing the registry or
  creating the execution root;
- enabled configured MCP cannot start or register tools before or after a
  rejected reload; and
- isolated skill bootstrap and rejected reload preserve external prompt-memory
  ownership.
