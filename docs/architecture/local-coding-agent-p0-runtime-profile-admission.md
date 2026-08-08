# Local Coding Agent P0.2 Pre-construction Runtime Profile

Status: implemented

Roadmap packet: P0.2 in
[`local-coding-agent-roadmap.md`](local-coding-agent-roadmap.md)

## Decision

MintClaw admits an immutable `RuntimeProfile` before `AgentRegistry` and
`AgentInstance` construction. A profile binds each configured runtime agent ID
to one already validated `RuntimeLayout`.

The binding is intentionally explicit because the two owner domains have
different identity rules:

- a personal agent's owner ID must equal its canonical configured agent ID;
- a coding runtime may bind configured agent `main` (or another selected model
  configuration) to an independent coding-thread owner UUID.

Every configured agent binding is preflighted before the first session store,
context builder, registry member, or tool is created. Missing and duplicate
bindings therefore fail without a split registry or filesystem side effects.

## Construction Boundary

`NewAgentLoopWithRuntimeProfile` is the pre-construction entry point. It:

1. validates that the profile covers the complete configured registry;
2. constructs each `AgentInstance` from its bound layout;
3. uses `ExecutionRoot` for the existing workspace-facing read and execution
   APIs;
4. opens the current canonical session backend under
   `StatePaths().SessionsRoot`;
5. places prompt-memory construction under `StatePaths().MemoryRoot`;
6. closes already opened agent resources if a later instance fails; and
7. closes the registry and context manager when loop construction fails.

The existing `NewAgentLoop` and `NewAgentLoopChecked` gateway entry points are
unchanged in P0.2. The deployed personal runtime switches to the new entry
point only with the P0.3 storage migration.

## Fail-closed Coding Bootstrap

P0.2 does not admit a coding tool catalogue. A coding-thread owner is therefore
constructed with an empty core tool registry and isolated shared-tool
bootstrap. This prevents legacy constructors—including exec's workspace-local
scratch initialization, personal memory tools, messaging, and MCP—from writing
to or becoming visible in a source checkout before P0.4 defines the exact
trusted-local coding profile.

P0.4 replaces this temporary empty catalogue with an explicit tested coding
tool profile. It does not mutate persisted personal configuration.

## P0.3 Handoff

P0.2 routes the existing concrete JSONL session initialization through the
layout only to establish the no-source-pollution construction invariant. P0.3
replaces that concrete call with injected, rollback-aware store factories and
routes Seahorse through `StatePaths().ContextRoot`.

Derived coding context is owner-scoped: each coding thread gets its own
Seahorse SQLite file. Personal agents retain one derived database per personal
agent, which may index that agent's routed sessions. There is no global,
ever-growing coding-context database. Canonical JSONL remains authoritative and
every Seahorse database remains disposable and rebuildable.

## Done Evidence

Focused tests prove that:

- a configured agent can be bound to a distinct coding-thread owner;
- execution and state roots remain distinct through full loop construction;
- sessions and prompt memory are created under the external state root;
- the source execution root is not created;
- coding tools fail closed to an empty registry until P0.4;
- a missing later-agent binding fails before the first owner's roots are
  created; and
- duplicate bindings and mismatched personal identities are rejected.

