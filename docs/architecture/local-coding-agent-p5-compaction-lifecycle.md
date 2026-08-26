# Local Coding Agent P5 Compaction Lifecycle

This note records the P5.3 compaction trigger, correlation, and ownership
contract. Canonical coding history remains the per-thread JSONL transcript;
Seahorse SQLite state and summaries remain derived and rebuildable.

## Trigger contract

Persistent coding sessions support all four compaction paths:

- proactive background work after context pressure is observed;
- deduplicated post-turn background work after a final reply;
- synchronous emergency work after a provider context overflow; and
- synchronous manual work owned by the coding controller.

Short-lived one-shot coding commands suppress background compaction because
their runtime closes immediately after the turn. Foreground emergency and
manual compaction remain available wherever their callers need them.

Background work is deduplicated by coding thread, not merely by session-key
spelling. The runner owns a root cancellation context and a worker set. Runtime
shutdown first stops and drains those workers, then closes Seahorse, providers,
and the event bus. A bounded shutdown that cannot drain returns an error without
closing dependencies underneath an active worker.

Manual compaction is serialized with foreground turns by the coding controller.
All Seahorse assemble, ingest, clear, and compact operations for a session use
the same session lock, so emergency or background compaction cannot summarize
across an in-flight transcript mutation.

## Lifecycle and correlation

Every attempted compaction emits a correlated lifecycle:

1. `started`;
2. `progress` when the engine reports tokens saved or summaries created; and
3. exactly one terminal `completed`, `no_progress`, `interrupted`, or `failed`
   observation.

The lifecycle payload carries a unique attempt ID, coding thread ID, source
transcript revision and message count, trigger reason, and tokens saved. The
revision is captured while holding the session lock after canonical-to-derived
reconciliation. Failures that occur before a runtime or revision can be
resolved still receive a paired attempt ID, with unavailable correlation fields
left empty.

Cancellation and deadline expiry are `interrupted`, not generic failures.
No-progress is terminal and is never automatically rescheduled by the same
attempt. Frontends retain the correlation fields in their bounded presentation
view and release foreground compaction ownership for every terminal state.

## Crash and recovery boundary

Compaction never edits or truncates canonical JSONL. Process exit can therefore
leave a derived summary attempt incomplete without losing accepted messages.
P5.4 owns durable `last_compaction` metadata updates, missing/corrupt Seahorse
rebuild, and resume-time recovery. P5.3 deliberately records source revision in
events without making an incomplete derived write authoritative.

## Verification

Regression coverage proves that:

- background jobs deduplicate by coding thread;
- shutdown cancellation drains the worker before dependency teardown and a
  closed runner rejects new work;
- no-progress, failure, and cancellation produce paired terminal lifecycle
  events with one attempt identity;
- progress events preserve thread and transcript correlation;
- the coding frontend projects progress and interrupted states without losing
  foreground activity ownership; and
- persistent and one-shot coding runtimes select the intended background
  compaction policy.
