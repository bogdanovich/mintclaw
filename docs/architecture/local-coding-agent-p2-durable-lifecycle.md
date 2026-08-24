# Local Coding Agent P2.5: Durable Turn and Tool Lifecycle

This milestone makes coding-thread recovery conservative at every boundary
where a user request can cause an external side effect. The canonical session
journal remains the source of truth; terminal UI state and provider context are
never recovery authorities.

## Durable boundaries

The existing turn-admission path appends the accepted root user message before
the first provider call. A failure at that boundary rejects the turn, so a
provider never acts on a request that the canonical coding session does not
own.

Assistant tool intent is also appended before tool execution. For every tool
whose loop semantics are not explicitly read-only and idempotent, MintClaw then
atomically replaces canonical history with a start marker on that assistant
message before invoking the tool. Unknown tool semantics fail closed and
receive a marker.

Marker insertion is a store-level atomic history mutation. It holds the same
per-session exclusion used by append, replacement, truncation, and compaction,
so it cannot overwrite a newer canonical snapshot. A coding tool-call batch is
rejected before intent persistence or execution when any call ID is empty or
duplicated.

The marker contains only:

- a SHA-256 correlation hash of the provider call ID;
- a bounded tool name;
- the `started` state and a UTC timestamp.

It contains no arguments or raw provider call ID, permits at most 64 markers on
one assistant message, and is removed from provider-facing history. The hash
associates it with the already-durable assistant intent when diagnostics need
the original call. The normal correlated tool-result message remains the
terminal record and has the raw call ID required by provider protocols.

If the start-marker write fails, MintClaw does not invoke the tool. If terminal
result persistence fails after invocation, the durable assistant intent and
start marker remain unresolved instead of being rewritten as success.

## Startup repair

Strict coding-runtime construction scans its owner-scoped canonical sessions
before returning control to a frontend. A tool call with no terminal result is
repaired exactly once:

| Durable evidence | Recovered result status | Meaning |
| --- | --- | --- |
| Assistant intent, no start marker | `interrupted` | Execution did not cross the durable start boundary |
| Assistant intent plus start marker | `unknown` | Execution may have changed external state |

Both synthetic results state that the call was not replayed. Neither status is
success, and Seahorse's conservative retention rules keep both. Reopening the
same session is idempotent because the synthetic correlated result closes the
pair.

Repair never invokes a tool. In particular, `unknown` means the model or user
must inspect current repository/process state before deciding whether another
operation is appropriate. This prevents duplicate writes when a process exits
during mutation or after mutation but before terminal-result persistence.
Terminal correlation is local to the contiguous result block following each
assistant intent, so providers may safely reuse identifiers such as `call_0`
in later turns.

## Scope and compatibility

Start markers and startup repair are enabled only by the coding tool
profile. Personal-agent session recovery, including unanswered-user replay,
keeps its existing behavior. Read-only idempotent coding tools use the existing
assistant-intent and terminal-result pair without an extra start-marker write.

Deterministic tests cover accepted-turn ordering, marker redaction and bounds,
live marker persistence across reopen, provider-context stripping, a failed
marker write, and four crash fixtures: before start, after start, during a
mutation, and after mutation but before result persistence.
