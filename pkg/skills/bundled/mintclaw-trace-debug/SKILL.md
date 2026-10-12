---
name: mintclaw-trace-debug
description: Diagnose MintClaw reliability incidents from passive traces, metadata-only diagnostics and authoritative owner snapshots. Use for missing or duplicate responses, wrong tools, loops, provider failures, compaction, recovery, remote tasks, interactions or delivery, and to identify a focused regression without replaying effects.
---

# MintClaw Trace Debug

Diagnostics are private, passive evidence, never execution authority. Durable
threads, tasks, interactions, invocations and delivery stores remain the
source of truth. Do not invoke the retired `mintclaw eval` workflow.

## Locate and validate

Record the affected profile, approximate time, visible symptom and exact known
thread, task, trace, turn, model-attempt, tool-call, invocation, interaction,
completion or delivery ID. Do not assume the newest trace is the incident.

Prefer the read-only operator commands on the host that owns the state:

```sh
mintclaw diagnostics list --since 2h --json
mintclaw diagnostics show <trace-id> --json
mintclaw diagnostics correlate --task <task-id> --json
mintclaw diagnostics correlate --thread <thread-id> --json
```

Set the active `MINTCLAW_HOME`/`MINTCLAW_CONFIG` for the affected profile, or
use `--config` without printing its contents. On the deployed stack, prefer
batch-mode SSH to `server@oc`, falling back to `server@oc-ts`; profiles live at
`/home/server/.mintclaw/<profile>`. Inspect rich evidence remotely unless a
local copy is explicitly needed. For an offline trace store use
`--trace-root`; it is mutually exclusive with `--thread` and deliberately does
not inspect live owners.

Selectors combine with AND; repeated `--trace` values are the OR set. Exact
trace queries avoid decoding unrelated traces. `correlate` and export require
an exact identity, not just a time window. Validate source commit/schema,
capture policy, retention, scan/match bounds, corruption, truncation and drops
before interpreting absence. Disabled capture, missing/expired/corrupt traces
or unavailable owners are evidence limits, not proof execution did not occur.

## Find the first divergence

Use the safe ordered timeline and content-free authoritative snapshots:

- distinguish model completion from final channel delivery;
- join model request/retry/fallback by model-attempt ID, tool call/result/loop
  decisions by call ID plus generation, and task completion by task plus
  generation rather than time or prose;
- for remote uncertainty, inspect the same invocation/task/worker generation
  before any retry; a disconnected worker might still have changed state;
- compare canonical history revision/counts with the separately owned
  presentation checkpoint when output differs; unavailable presentation is
  not a zero count; and
- compare compaction/reconciliation events with the canonical history owner,
  never treating a derived summary as repository or transcript authority.

`correlate` and support export include bounded owner lookups for tasks,
interactions, outbox deliveries, gateway invocations and coding threads. They
do not initialize registries, reconcile history, acquire a thread writer,
repair state or replay work. Check each lookup's found/not-found/unavailable
status before drawing conclusions. Companion ledgers and service logs remain
external and require exact-ID, bounded read-only inspection when needed.

Read [trace-correlations.md](references/trace-correlations.md) for raw schema
fields, continuation joins, task/worker generations, remote invocation and
interaction semantics, historical missing fields and selected private-preview
inspection. Do not infer a missing generation, alias or parent turn.

## Preserve safe evidence

Export only the smallest exact evidence set to a new directory:

```sh
mintclaw diagnostics export --task <task-id> --output ./support-new-directory
```

A completed bundle has `manifest.json`, `health.json`, `owners.json` and safe
trace summaries, with no `INCOMPLETE` marker. Recompute every manifest-listed
size/SHA-256 and retain the receipt's manifest digest. Verify privacy and
destination policy before sharing; the command never uploads automatically.

Default export omits prompts/responses, rich previews, source/diff content,
tool arguments/results, commands/output, environment values, credentials,
absolute paths, channel destinations and unrelated traces. An explicitly
needed local rich export uses one exact `--trace` plus
`--include-redacted-content`; it remains private, with `upload_allowed=false`.
Redaction is not permission to publish. Preserve original evidence unchanged;
never execute historical commands or interpret a preview as approval.

## Close the loop

Report the symptom, exact IDs and cited record sequence/kind/offset, first
divergence, user-visible effect, missing evidence, and confidence:
**proven**, **likely** or **unknown**. Chronology alone does not prove causation.

Propose the smallest ordinary unit, integration, restart, concurrency,
real-process or race regression for that boundary. Implement a narrow fix only
when requested, validate it, and verify a fresh harmless trace afterward; do
not replay the historical side effect or build a universal evaluator. A
coding-agent handoff contains relevant IDs, cited records/log facts and the
regression invariant, not unrelated rich content or credentials.
