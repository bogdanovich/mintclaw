# Local Coding Agent P0.1 Coding Runtime Layout

Status: implemented

Roadmap packet: P0.1 in
[`local-coding-agent-roadmap.md`](local-coding-agent-roadmap.md)

## Decision

The coding frontend admits one explicit, side-effect-free
`CodingRuntimeLayout` before constructing a coding `AgentLoop`. The layout
names:

- the stable coding thread ID;
- the execution root used by tools and subprocesses;
- the external state root used by MintClaw-owned data;
- ordered instruction roots; and
- the derived state paths below the state root.

This is a coding-thread contract, not a general abstraction over the personal
gateway. The gateway keeps its config-owned workspaces, channel lifecycle, and
hot reload behavior. The removed personal owner kind was never used by a
production frontend and would have required an unnecessary storage migration
for structural symmetry.

## Invariants

1. The thread ID is non-empty and trimmed.
2. `ExecutionRoot` is the project authority for filesystem tools,
   subprocesses, and intentional user-requested output.
3. `StateRoot` owns coding sessions, derived context, prompt memory,
   operational state, diagnostics, locks, and tool scratch.
4. `StateRoot` cannot equal or descend from `ExecutionRoot`. Resolution uses
   the nearest existing ancestor and fails closed on dangling symlinks,
   permission errors, and case aliases that identify the same directory.
5. Construction stores the same absolute, symlink-resolved paths that passed
   validation. Later working-directory changes cannot reinterpret them.
6. Layout construction is read-only and does not create either root.
7. Instruction roots are explicit, ordered, and immutable to callers.

## State Paths

| Producer | Layout-owned path | Authority |
| --- | --- | --- |
| Canonical session JSONL | `sessions/` | Conversation history |
| Seahorse SQLite | `context/` | Derived, rebuildable context |
| Coding prompt memory | `memory/` | Thread-owned memory |
| Operational state | `runtime/state.json` | Runtime state |
| Async tasks | `runtime/task_registry.json` | Task and delivery state |
| Human interactions | `runtime/interaction_registry.json` | Interaction state |
| Approval binding key | `runtime/interaction_hmac.key` | Private restart-stable key |
| Diagnostics | `diagnostics/` | Bounded diagnostic state |
| Media | `media/` | Thread-owned attachments |

Coding threads start directly on this current layout. Runtime code neither
discovers nor converts a historical personal workspace layout, and no deployed
personal data move is part of this contract.

## Done Evidence

Focused tests cover path canonicalization, immutable instruction roots,
derived paths, external-state containment, symlink and case-alias rejection,
empty contract fields, and absence of filesystem side effects. Profile tests
cover exact agent bindings, distinct threads, preflight, store rollback,
and isolated coding construction.
