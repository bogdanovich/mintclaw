# Session System

> Back to [README](../README.md)

This document describes the runtime session system used by MintClaw to:

- map inbound messages onto stable conversation scopes
- persist message history and summaries
- expose current frontend session references without making them storage aliases

This document covers the core runtime path in `pkg/session`, `pkg/memory`, and `pkg/agent`.
It does not describe launcher login cookies or dashboard authentication sessions in `web/backend/middleware`.

## Responsibilities

The session system has four jobs:

1. Decide which messages should share the same conversation context.
2. Persist that context durably across turns and restarts.
3. Expose a small `SessionStore` interface to the agent loop.
4. Keep routing identity, lifecycle identity, and frontend presentation identity separate.

## Main Components

| Layer | Files | Responsibility |
| --- | --- | --- |
| Session contract | `pkg/session/session_store.go` | Defines the `SessionStore` interface used by the agent loop. |
| Ephemeral store | `pkg/session/memory_store.go` | Supplies a non-persistent store for tests, benchmarks, and explicit ephemeral use. |
| Session adapter | `pkg/session/jsonl_backend.go` | Adapts `pkg/memory.Store` to `SessionStore` and persists structured scope metadata. |
| Durable storage | `pkg/memory/jsonl.go` | Append-only JSONL storage plus `.meta.json` sidecar metadata. |
| Scope and key building | `pkg/session/scope.go`, `pkg/session/key.go`, `pkg/session/allocator.go` | Builds structured scopes and opaque canonical keys from routing results. |
| Runtime integration | `pkg/agent/instance.go`, `pkg/agent/agent.go`, `pkg/agent/agent_message.go` | Initializes the store, allocates session scope, and persists metadata before turns run. |

## Session Data Model

The structured session identity is represented by `session.SessionScope`:

| Field | Meaning |
| --- | --- |
| `Version` | Schema version. Current value is `ScopeVersion`. |
| `AgentID` | Routed agent handling the turn. |
| `Channel` | Normalized inbound channel name. |
| `Account` | Normalized account or bot identifier. |
| `Dimensions` | Ordered list of active partition dimensions such as `chat` or `sender`. |
| `Values` | Concrete normalized values for each selected dimension. |
| `RouteScopeKey` | Stable trusted identity shared by all lifecycle epochs of one routed conversation. |
| `ClientSessionID` | Current MintClaw frontend provenance; never part of canonical storage identity. |
| `Epoch` | Optional lifecycle partition that selects the current history epoch. |

Only four dimensions are currently recognized by the allocator:

- `space`
- `chat`
- `topic`
- `sender`

The default config uses:

```json
{
  "session": {
    "dimensions": ["chat"]
  }
}
```

That means one shared conversation per chat unless a dispatch rule overrides it.

## Canonical Keys

The runtime now prefers opaque canonical keys:

```text
sk_v1_<sha256>
```

These keys are built from a canonical scope signature in `pkg/session/key.go`.
Only exact `sk_v1_` keys are accepted as explicit incoming session keys.
Textual keys, metadata aliases, and alias-based lookup are not runtime formats.

MintClaw frontend session IDs are recorded in `SessionMeta.ClientSessionIDs`.
They let the web API present the newest usable canonical history for a current
frontend ID, but they cannot redirect session-store reads or writes.

## Allocation Flow

The end-to-end flow for a normal inbound message is:

```text
InboundMessage
  -> RouteResolver.ResolveRoute(...)
  -> session.AllocateRouteSession(...)
  -> resolveScopeKey(...)
  -> ensureSessionMetadata(...)
  -> AgentLoop turn execution
  -> SessionStore read/write operations
```

More concretely:

1. `pkg/agent/agent_message.go` resolves the agent route from normalized inbound context.
2. `session.AllocateRouteSession` converts the route's `SessionPolicy` plus inbound context into a structured `SessionScope`.
3. The allocator builds:
   - `SessionKey`: canonical routed session key
   - `RouteScopeKey`: stable route identity before lifecycle epoch selection
4. `runAgentLoop` persists current scope and frontend provenance through `ensureSessionMetadata`.
5. Later reads and writes address that exact opaque key.

The main session key is separate from routed chat sessions.
It is used for agent-level flows that need one stable per-agent conversation.
Async task completions use typed `AsyncCompletionInput` delivery instead of
synthetic inbound messages.

## Seahorse Retrieval Boundaries

Seahorse stores each lifecycle epoch as a conversation row. The row also carries nullable trusted provenance:

- `route_scope_key`, copied from canonical `SessionScope.RouteScopeKey`
- `agent_id`, copied from canonical `SessionScope.AgentID`

The context manager records provenance during assemble, compact, and ingest. Existing rows are upgraded on access only
when canonical session metadata proves their identity. Conflicting provenance is rejected rather than overwritten.

Retrieval tools expose only an enum, not identity values:

- `current_epoch` resolves the active session key to one Seahorse conversation
- `conversation` resolves all rows with the same route scope and agent
- `workspace` resolves all rows for the current agent in the workspace-local database

The tool boundary reads the session key and scope from trusted execution context, then passes an explicit conversation
ID set into every FTS and LIKE query. An empty set matches nothing. Missing provenance is excluded, and
`short_expand` checks the same resolved ID set so a message ID from `short_grep` cannot be expanded across scope.

An operator maximum is applied before those IDs are resolved. It defaults to `conversation`; `workspace` requires an
explicit single-user deployment opt-in. The tools expose only scopes at or below that maximum in their schemas and
still reject a manually supplied broader value, so model behavior cannot widen the configured privacy boundary.

Both retrieval tools bound serialized output. Truncation is reported explicitly with advice and omitted-result counts;
callers should narrow the pattern or expand message IDs in smaller batches.

## Absolute Context Budgets

The Pipeline owns model-request budgeting because it is the only layer that sees the complete dynamic request: system
prompt, active skills, visible tool definitions, media, and output reserve. It estimates those mandatory costs before
calling the context manager and passes only the remaining capacity to Seahorse.

When configured, Seahorse applies three independent controls:

- `historyMaxTokens` as the target for raw messages
- `summaryMaxTokens` for the rendered summary and its guidance
- `recentTailTurns` for the minimum number of newest complete user turns kept raw

The model context remainder is always the outer hard limit. Selection prioritizes the requested recent turns, then
adds older complete turns up to the history target, then adds the newest summaries that fit. A recent tail may exceed
`historyMaxTokens` when it still fits the outer limit. If the requested tail itself exceeds that hard limit, selection
drops its oldest complete turns until the remainder fits. It never splits a user turn or a tool-call/result sequence.
Stored token metadata is treated as a lower bound; assembly re-estimates full messages and rendered summary text so
stale counts cannot bypass a limit.

If non-history plus output reserves consume the model window, assembly returns an error and the Pipeline does not call
the provider. Otherwise, source data over a configured target produces a structured budget report, a context-pressure
event, and deduplicated background compaction. Reports distinguish requested and retained recent turns and expose tail
overflow or hard-limit degradation. Degradation does not schedule compaction because compaction preserves the configured
raw tail and cannot make progress until that protected window advances. Forced compaction may bypass the configured
message-count tail, but never splits an explicit recent turn.

## Tool Result Projection

Tool-result retention is a prompt projection, not destructive history cleanup. The agent records an explicit persisted
result status when execution resolves: successful, failed, or unresolved. Canonical JSONL and Seahorse message rows keep
the full result and status across reconciliation and restart. Missing status is unknown and therefore conservative.

The tools layer owns exact-name workspace rules under `tools.result_retention`; context-manager configuration contains
only context-manager-specific controls. The agent composition root passes the resolved policy to Seahorse. At future
assembly and before leaf-summary generation, Seahorse joins each result to its assistant tool call and applies that
policy. Successful results may remain full, become a bounded receipt, or be omitted. Omitting a result also removes only
its matching tool call; other calls and results in the same assistant batch remain paired. A receipt keeps the matching
call so provider history remains valid.

Errors, unresolved operations, unknown status, multiple or ambiguous result parts, and media-bearing output bypass
projection. `durable` is an explicit operator assertion that the configured tool writes to an external source of truth;
the receipt should tell the model how to query that source again. Runtime logs report preserved, safety-preserved,
receipted, durable, and transient counts without recording result content.

## Scope Construction Rules

`pkg/session/allocator.go` builds scope values from normalized inbound context.
Important rules:

- `space` becomes `<space_type>:<space_id>`
- `chat` becomes `<chat_type>:<chat_id>`
- `topic` becomes `topic:<topic_id>`
- `sender` is canonicalized through `session.identity_links` before being stored

There are two special cases worth calling out.

### Telegram forum isolation

Telegram forum topics must stay isolated even when the configured dimensions only mention `chat`.
To preserve that behavior, the allocator appends `/<topic_id>` to the `chat` value for Telegram forum messages unless `topic` is already an explicit dimension.

Example:

```text
group:-1001234567890/42
group:-1001234567890/99
```

Those produce different session keys.

### Identity links

`session.identity_links` lets multiple sender identifiers collapse into one canonical identity.
Both dispatch matching and session allocation use that mapping so that the same person can keep one conversation even if their raw sender IDs differ across channels or accounts.

## Storage Format

The default runtime backend is `pkg/memory.JSONLStore`, wrapped by `session.JSONLBackend`.

Each session uses two files:

```text
{sanitized_key}.jsonl
{sanitized_key}.meta.json
```

The files store:

- `.jsonl`: one `providers.Message` per line, append-only
- `.meta.json`: summary, timestamps, history revision state, structured scope,
  and current frontend session references

`SessionMeta` currently includes:

- `Key`
- `Summary`
- `Skip`
- `Count`
- `CreatedAt`
- `UpdatedAt`
- `Scope`
- `ClientSessionIDs`
- `HistoryRevision`
- `HistoryDirty` and the fields used to recover an interrupted replacement

## Write And Crash Semantics

The JSONL store is designed around append-first durability and stale-over-loss recovery:

- `AddMessage` and `AddFullMessage` append one JSON line, `fsync`, then update metadata.
- `TruncateHistory` is logical first: it only advances `meta.Skip`.
- `Compact` physically rewrites the JSONL file to remove skipped lines.
- `SetHistory` and `Compact` write metadata before rewriting JSONL so a crash may temporarily expose old data, but should not lose data.
- Corrupt JSONL lines are skipped during reads instead of failing the entire session.

`JSONLBackend.Save` maps onto `store.Compact(...)`.
In other words, `Save` is no longer "flush dirty memory to disk"; it is now "reclaim dead lines after logical truncation".

## Concurrency Model

`pkg/memory.JSONLStore` uses a fixed 64-shard mutex array keyed by session hash.
That gives per-session serialization without keeping an unbounded mutex map in memory.

## Startup And Storage Cutovers

Persistent runtimes create `memory.JSONLStore` and wrap it with
`session.JSONLBackend`. Initialization failure is a startup error: MintClaw
does not switch to a second persistence format or import JSON snapshots while
serving traffic.

Storage migrations are coordinated deployment operations. Once all first-party
processes are upgraded, the runtime reader for the previous format is removed.
This keeps one writer, one current identity format, and one recovery owner in
the running product.

## Other SessionStore Implementations

`pkg/agent/subturn.go` defines an `ephemeralSessionStore`.
It satisfies the same `SessionStore` interface, but keeps data in memory only and is destroyed when the sub-turn ends.

That lets SubTurn reuse the same session-facing APIs without writing child-session history into the parent's durable storage.

`pkg/session.MemoryStore` provides the same deliberately non-persistent
behavior for tests and benchmarks. It does not read or write a historical disk
format.

## Operational Consumers

The session system is consumed by more than the agent loop:

- `web/backend/api/session.go` reads current JSONL metadata to expose session history in the launcher UI.
- `pkg/agent/steering.go` can recover scope metadata for active steering flows.

## Related Files

- `pkg/session/session_store.go`
- `pkg/session/memory_store.go`
- `pkg/session/jsonl_backend.go`
- `pkg/session/scope.go`
- `pkg/session/key.go`
- `pkg/session/allocator.go`
- `pkg/memory/jsonl.go`
- `pkg/agent/instance.go`
- `pkg/agent/agent.go`
- `pkg/agent/agent_message.go`
