# Local Coding Agent P2 Exit Record

Roadmap gate: [P2 — Native coding runtime profile](local-coding-agent-roadmap.md#p2--native-coding-runtime-profile).

The merge containing this record completes the seven ordered P2 packets. The
native path is suitable for useful coding turns with plain CLI output. P3
still owns the terminal event/control plane and TUI, while P4 owns long-session
compaction and richer resume UX; neither is implied by this gate.

## Completion evidence

| Packet | Merged change | Contract evidence |
| --- | --- | --- |
| P2.1 | [#688](https://github.com/bogdanovich/mintclaw/pull/688) | Coding prompt snapshots and session-isolation tests keep personal routing, persona, and channel context out of coding turns. |
| P2.2 | [#699](https://github.com/bogdanovich/mintclaw/pull/699) | One-file-per-directory `AGENTS.override.md` / `AGENTS.md` / `CLAUDE.md` fallback selection, nested scope barriers, bounds, invalidation, and symlink tests. |
| P2.3 | [#701](https://github.com/bogdanovich/mintclaw/pull/701) | Deterministic bounded Git snapshots cover dirty, detached, unborn, non-Git, and worktree states and refresh after audited writes. |
| P2.4 | [#704](https://github.com/bogdanovich/mintclaw/pull/704) | Native read/search/write/patch/exec tools use the coding root, preserve write audits and journal pairing, cancel process groups, and leave gateway restrictions unchanged. |
| P2.5 | [#705](https://github.com/bogdanovich/mintclaw/pull/705) | Crash fixtures cover accepted turns and side-effect lifecycle boundaries; resume repairs unknown outcomes without replaying tools. |
| P2.6 | [#710](https://github.com/bogdanovich/mintclaw/pull/710) | Deterministic command-level scenarios edit across restart through the native AgentLoop, retain inspectable failures, and require no Codex subprocess. |
| P2.7 | [Coding tool quality gate](../testing/coding-tool-quality-gate.md) | Two provider-facing call forms and small/large repositories measure exact edit/search outcomes, stale patches, output volume, awkward paths, cancellation, recovery, and bounded retained command artifacts. |

The six previously merged packet PRs each passed the repository's nine-check
CI matrix. P2.7 uses the same merge gate and adds deterministic quality metrics
plus an opt-in live-provider smoke.

## Exit decision

P2's exit statement is satisfied:

- native coding turns run through MintClaw with project-scoped identity,
  instructions, repository observations, and tools;
- accepted turns and side-effect lifecycle records remain recoverable across
  restart without automatic replay;
- core tools have deterministic small/large fixture evidence and bounded model
  context, while full oversized coding command output remains available as a
  local artifact;
- the current plain renderer is intentionally sufficient for this phase.

The next dependency is P3.1, the coding event projector. Terminal interaction
and compaction must build on the durable runtime records established here
rather than introducing a second source of truth.
