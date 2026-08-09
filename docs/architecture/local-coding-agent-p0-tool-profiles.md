# Local Coding Agent P0.4 Tool Profiles

Roadmap packet: P0.4 in
[`local-coding-agent-roadmap.md`](local-coding-agent-roadmap.md)

## Decision

Runtime owner admission now selects an immutable tool profile before agent
construction. A homogeneous personal-agent profile selects `personal`; a
homogeneous coding-thread profile selects `coding`. Persisted configuration is
an input to construction and is never rewritten to express this choice.

The personal profile preserves the existing config-driven gateway catalogue.
Its canonical transcript, derived context, memory, runtime state, task and
interaction registries, approval key, and exec scratch are rooted in the
admitted state layout rather than the execution workspace.

Strict personal admission currently accepts one owner per loop. The legacy
gateway retains its existing multi-agent behavior; a future owner-aware
operational-state service is required before a strict loop can safely combine
multiple personal owners. Coding profiles may contain multiple owners because
they do not mount personal operational-state or goal services, and duplicate
execution roots are rejected.

The coding profile exposes exactly these core tools:

- `read_file`
- `write_file`
- `list_dir`
- `search_files`
- `exec`
- `apply_patch`
- `update_plan`

When Seahorse is selected, its owner-scoped `short_grep` and `short_expand`
retrieval tools are the only additional tools. JSONL remains authoritative and
each coding thread retains its own disposable Seahorse database.

## Trust and isolation

Coding tools are trusted-local: filesystem paths and subprocess working
directories resolve from the execution root, and tool execution bypasses the
gateway approval path. Exec receives a transient configuration with deny
patterns and remote invocation disabled; the user's stored configuration and
gateway approval behavior remain unchanged. Exec scratch is placed at
`<state-root>/runtime/tmp`, so merely constructing a coding runtime does not
create the source root.

Coding profiles do not initialize MCP, configured hooks, or process hooks and
reject dynamic hook and runtime-tool mounting. Repository agent frontmatter
cannot broaden the coding catalogue or admit MCP servers. Project-local
executable extensions therefore require a future explicit trust design.

After Seahorse registers its deliberate retrieval tools, each coding tool
registry is sealed against direct registration, replacement, removal,
allowlist changes, and hidden-tool visibility changes. Each coding exec tool
also owns a separate in-process session manager, so background process handles
cannot cross personal-agent or coding-thread boundaries.

Strict construction preflights every operational leaf and rejects symlinks,
wrong file types, unreadable files, malformed state/registry snapshots, and
invalid approval keys instead of silently starting from empty state.

## Verification contract

Focused tests enumerate the complete coding catalogue with MCP and hooks
enabled in configuration, verify Seahorse's two deliberate additions, reject
dynamic tool and hook expansion, and assert trusted execution without config
mutation. A paired legacy/strict personal test verifies identical personal
tool catalogues while all MintClaw-owned writes stay under the state root.

This completes the P0 runtime-construction sequence. Later packets can build a
terminal frontend and coding session lifecycle on this boundary without
mixing personal gateway capabilities into repository-scoped turns.
