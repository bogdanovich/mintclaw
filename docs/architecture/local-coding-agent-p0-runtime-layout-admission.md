# Local Coding Agent P0.1 Runtime Layout Admission

Status: admitted for implementation

Admission baseline: `origin/main` at `d7b7a36b`, 2026-08-08

Roadmap packet: P0.1 in
[`local-coding-agent-roadmap.md`](local-coding-agent-roadmap.md)

## Decision

MintClaw admits one explicit, side-effect-free `RuntimeLayout` contract before
adding a coding frontend or changing live storage. The contract names:

- a stable runtime owner;
- the execution root used by tools and subprocesses;
- the external state root used by MintClaw-owned persistent data;
- ordered instruction roots; and
- the owned state paths derived from the state root.

There are no external MintClaw installations that require disk-layout
compatibility. P0 therefore targets a clean cutover instead of permanent
dual-read, fallback-path, or legacy constructor behavior. The deployed
installation will receive a one-time, backed-up migration before a binary that
uses the new roots is restarted.

P0.1 only admits the contract and migration inventory. P0.2 resolves a runtime
layout before construction, and P0.3 switches canonical and derived stores.
Until those packets merge, current production code continues to use
`AgentInstance.Workspace` and its existing paths.

## Required Invariants

1. A personal agent has owner kind `personal_agent`; construction normalizes
   its owner ID with the same canonical agent-ID rules used by routing.
2. A coding thread has owner kind `coding_thread` and its stable thread ID is
   the owner ID.
3. `ExecutionRoot` is the cwd/project authority for filesystem tools,
   subprocesses, and intentional user-requested outputs.
4. `StateRoot` owns sessions, derived context, memory, operational state,
   diagnostics, media, locks, and other MintClaw-managed files.
5. Construction stores the same trimmed, absolute, symlink-resolved execution,
   state, and instruction paths that passed validation. Later cwd changes
   cannot reinterpret a layout.
6. `StateRoot` cannot equal or descend from `ExecutionRoot` for any runtime
   owner. Path resolution uses the nearest existing ancestor and fails closed
   on dangling symlinks, permission errors, or other ambiguous ancestors.
   Containment also compares filesystem identities so case-only aliases on a
   case-insensitive volume cannot bypass the rule.
7. Layout construction and validation are read-only. They do not create the
   execution root, state root, or any derived directory.
8. Instruction roots are explicit and ordered. Discovering instructions never
   makes their parent an implicit state root.
9. A deployment migration is copy/verify/switch/rollback capable. Runtime code
   does not indefinitely support both the old and new locations.
10. Personal behavior—sessions, memory content, routing, tools, and approvals—is
   preserved across migration even though path compatibility is not.

## Target State-Path Ownership

Each owner receives its own selected `StateRoot`. The exact default owner-root
catalogue is admitted in P0.2; paths below that root are fixed here:

| State producer | Layout-owned path | Authority |
| --- | --- | --- |
| Canonical session JSONL and metadata | `sessions/` | Canonical conversation history |
| Seahorse SQLite and rebuild metadata | `context/` | Derived, rebuildable context state |
| Personal or coding memory selected by profile | `memory/` | Durable profile memory |
| Session goals and route/model overrides | `runtime/state.json` | Runtime operational state |
| Async task registry | `runtime/task_registry.json` | Durable task and delivery state |
| Human interaction registry | `runtime/interaction_registry.json` | Durable interaction state |
| Interaction argument-binding key | `runtime/interaction_hmac.key` | Restart-stable private binding key |
| Diagnostic traces | `diagnostics/` | Bounded diagnostic state |
| Persistent media and attachments | `media/` | Owner-scoped media state |

Canonical sessions and derived context intentionally receive different roots.
Seahorse can be deleted and rebuilt without touching JSONL. A coding profile
does not inherit personal media, memory, or private tools merely because the
same layout type can name those paths.

## Current `Workspace` Consumer Classification

The implementation audit classifies production consumers as follows. This is
the migration inventory for P0.2 through P0.4; P0.1 does not partially redirect
live consumers before construction and deployment migration are ready.

| Responsibility | Current consumers | Future layout source |
| --- | --- | --- |
| Agent bootstrap and instructions | `definition.go`, `context.go`, `discovery.go`, `skills/loader.go` | Ordered `InstructionRoots` |
| Filesystem and subprocess execution | core tools in `instance.go`, shared tools in `agent_init.go`, MCP binding in `agent_mcp.go`, model CLI workspace configuration | `ExecutionRoot` |
| Canonical sessions | `NewAgentInstance`, `memory.JSONLStore`, session manager | `StatePaths().SessionsRoot` |
| Derived context | `context_seahorse.go` | `StatePaths().ContextRoot` |
| Personal prompt memory | `ContextBuilder`, `MemoryStore`, memory tool | `StatePaths().MemoryRoot`; excluded from coding by default unless admitted |
| Goals and session operational state | `AgentLoop` initialization and `state.Manager` | `StatePaths().RuntimeStateFile` |
| Tasks and human interactions | `task_registry.go`, `human_interaction.go`, inbound/recovery coordinators | Runtime owner plus task/interaction paths |
| Diagnostics and traces | trace projector, trace scopes, runtime event logging | Runtime owner plus `DiagnosticsRoot` |
| Runtime claims, caches, steering, and recovery | `runtime_identity.go`, `turn_state.go`, recovery, steering, inbound/outbound coordinators | Stable `RuntimeOwner` |
| Provider and model working directory | provider resolution and turn candidate construction | `ExecutionRoot` |
| Intentional tool output | file, image, and shell tools | `ExecutionRoot`; user-requested mutation, not MintClaw state |
| Gateway-shared media and channel state | gateway construction from the configured default workspace | A gateway owner root admitted separately from agent roots |

Installed workspace skills currently combine instruction discovery and writes
under `<workspace>/skills`. P0.4 must keep project-local executable resources
disabled for coding and place any future installed resource under an admitted
owner rather than writing it into a source checkout.

## Deployment Migration Contract

The storage-switch packet must inventory the live installation immediately
before deployment. Expected current sources include:

```text
<workspace>/sessions/
<workspace>/memory/
<workspace>/state/state.json
<workspace>/state/task_registry.json
<workspace>/state/interaction_registry.json
<workspace>/state/interaction_hmac.key
<workspace>/state/diagnostics/
<workspace>/state/media/
```

The deployment procedure must:

1. stop writers and record the deployed binary and source paths;
2. create a recoverable backup;
3. copy or move each source to its owner-scoped target;
4. verify file counts, sizes, permissions, JSONL readability, registry loads,
   and Seahorse rebuildability;
5. start the new binary only after configuration selects the new roots;
6. run session, memory, task, interaction, media, and diagnostic smoke checks;
7. retain the backup until the new runtime has passed an observation window;
   and
8. roll back the binary and directories together if verification fails.

No merged packet may silently start with empty state merely because the old
path still exists. Missing migration evidence is a deployment blocker, not a
reason to add hidden fallback reads.

## P0.1 Implementation Boundary

This packet may:

- add layout, owner, validation, and derived-path types;
- document the complete consumer and migration inventory;
- test exact path ownership, owner/path canonicalization, immutable instruction
  roots, fail-closed symlink handling, and the absence of filesystem side
  effects; and
- update the roadmap from path compatibility to explicit cutover semantics.

This packet must not:

- redirect a live state-producing consumer before P0.2/P0.3;
- create directories while resolving a layout;
- add dual-read or fallback paths;
- change constructor timing, tool catalogues, or approval behavior;
- move deployed state; or
- combine gateway and per-agent state ownership.

## Done Evidence

P0.1 is complete when:

- tests prove canonical owner, execution root, state root, ordered immutable
  instruction roots, and every derived state path;
- all owner kinds reject equal, nested, symlink-hidden, and case-aliased state
  roots where the filesystem treats the alias as the same directory;
- an external state root validates without creating either root;
- the Workspace consumer inventory is accurate for the merged baseline;
- the roadmap and this admission require a one-time verified deployment
  migration and prohibit permanent path compatibility; and
- affected package tests, formatting, lint, and repository-required CI pass.
