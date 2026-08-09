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
The binding set must exactly equal the configured registry: extra bindings and
duplicate canonical configured IDs are rejected.

P0.2 admits homogeneous loops only. Mixing personal-agent and coding-thread
owners in one `AgentLoop` is rejected instead of applying coding isolation to
personal agents. The product architecture already uses separate personal and
local coding frontends; an owner-specific mixed bootstrap can be admitted later
if a concrete use case requires one.

## Construction Boundary

`NewAgentLoopWithRuntimeProfile` is the pre-construction entry point. It:

1. validates that the profile covers the complete configured registry;
2. constructs each `AgentInstance` from its bound layout;
3. uses `ExecutionRoot` for the existing workspace-facing read and execution
   APIs;
4. opens the current canonical session backend under
   `StatePaths().SessionsRoot`;
5. places prompt-memory construction under `StatePaths().MemoryRoot`;
6. fails construction when the admitted session or prompt-memory path is
   unusable instead of falling back to a legacy store;
7. closes already opened agent resources if a later instance fails;
8. closes the registry and context manager when loop construction fails; and
9. retains the same profile-aware registry strategy across supported config and
   provider reloads.

Each instance owns the stateful primary, light, and fallback providers created
internally for it. The caller-injected provider remains caller-owned, and
aliases of one internal provider are closed only once. Registry replacement
retires old instances: complete turns and their background compaction work hold
resource leases, so session stores and providers are finalized only after the
last old-instance user releases. Reload never force-closes an in-use registry
on a timeout, and a registry that finishes construction after cancellation is
closed by the construction result owner.

The existing `NewAgentLoop` and `NewAgentLoopChecked` gateway entry points are
unchanged in P0.2. The deployed personal runtime switches to the new entry
point only with the P0.3 storage migration. The public profile-aware loop entry
therefore rejects personal-only profiles during P0.2; internal instance and
registry construction helpers are not exposed as partially safe APIs.

## Fail-closed Coding Bootstrap

P0.2 does not admit a coding tool catalogue. A coding-thread owner is therefore
constructed with an empty core tool registry and isolated shared-tool
bootstrap. This prevents legacy constructors—including exec's workspace-local
scratch initialization, personal memory tools, messaging, and MCP—from writing
to or becoming visible in a source checkout before P0.4 defines the exact
trusted-local coding profile.

Isolated tool bootstrap also makes deferred MCP initialization a no-op across
startup, direct turns, commands, and reload. Isolated skill bootstrap retains
the execution root only for skill discovery while prompt memory continues to
use the external state owner.

P0.4 replaces this temporary empty catalogue with an explicit tested coding
tool profile. It does not mutate persisted personal configuration.

Coding profiles must select the `none` context manager during P0.2. The
constructor rejects the default Seahorse manager before registry construction,
because its legacy database path still points below `AgentInstance.Workspace`
and it registers retrieval tools. P0.3 removes this temporary restriction by
routing derived context through the external context root.

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
- duplicate bindings, duplicate configured IDs, extra bindings, mixed owner
  kinds, and mismatched personal identities are rejected;
- a coding profile using Seahorse is rejected before either root is created;
  and
- provider/config reload reconstructs the registry with the same owner and
  roots without adding coding tools or creating the execution root;
- enabled configured MCP cannot start or register tools before or after reload;
  and
- isolated skill bootstrap and reload preserve external prompt-memory ownership;
- unusable external session and memory paths fail admission without creating
  the execution root;
- canceled reload construction closes a registry that completes late; and
- reload leaves an old registry open while a turn is blocked outside its LLM
  call, then finalizes it after that complete turn releases.
