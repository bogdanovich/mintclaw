# Raw Trace Correlation Reference

Read this reference when a metadata-only timeline needs field-level
interpretation, an incident crosses continuation/remote boundaries, or the
operator explicitly needs one private raw trace. Use `diagnostics` and
`owners.json` first; this reference does not authorize rich collection or
execution. Raw files use `.data`; safe command projections use allowlisted
`.facts` and omit previews.

## Build The Timeline

Read records in `sequence` order. `offset_nanos` is relative to `created_at`.
Start with kinds and correlations, then inspect content only around the likely
divergence.

```bash
jq -r '
  .records | sort_by(.sequence)[] |
  [
    (.sequence | tostring),
    ("+" + (((.offset_nanos / 1000000) | floor) | tostring) + "ms"),
    .kind,
    (.scope.turn_id // "-"),
    (.scope.target_hash // "-"),
    (.scope.target_alias // "-"),
    (.scope.scope_alias // "-"),
    (.scope.profile_alias // "-"),
    (.correlation.model_attempt_id // "-"),
    (.correlation.tool_call_id // "-"),
    (.correlation.tool_call_generation // "-"),
    (.scope.task_id // "-"),
    (.correlation.task_generation_id // "-"),
    (.correlation.completion_id // "-"),
    (.correlation.invocation_id // "-"),
    (.correlation.policy_revision // "-"),
    (.correlation.interaction_id // "-"),
    ((.correlation.interaction_revision // 0) | tostring),
    (.correlation.interaction_event_id // "-"),
    (.correlation.delivery_id // "-"),
    (.correlation.event_id // "-")
  ] | @tsv
' TRACE.json

jq '
  .records[]
  | select(
      .kind == "runtime.error"
      or .kind == "model.retry"
      or .kind == "model.fallback_attempt"
      or .kind == "tool.loop_decision"
      or .kind == "tool.steering_decision"
      or .kind == "delivery.outcome"
      or (.data.is_error? == true)
    )
  | {sequence, offset_nanos, kind, scope, correlation, data}
' TRACE.json
```

Correlate:

- `model.request` with `model.response`, retries, and fallbacks by
  `correlation.model_attempt_id`; one ID covers candidate-level fallbacks and
  the retry decision for one logical attempt, while each outer retry gets a
  new ID;
- `tool.call`, `tool.result`, skipped calls, and `tool.loop_decision` by the
  pair `correlation.tool_call_id` and
  `correlation.tool_call_generation`; the generation disambiguates a provider
  call ID reused by a later model attempt;
- async delivery decisions with the authoritative task registry by
  `scope.task_id` plus `correlation.task_generation_id`, and completion
  deduplication by `correlation.completion_id`; a completion normally uses a
  separate bounded continuation trace whose `metadata.parent_turn_id` names
  the originating turn, so inspect both files instead of expecting a late
  decision in the finalized parent trace;
- delivery attempts and outcomes by `correlation.delivery_id`; use their exact
  continuation trace scope to relate them to the retained async decision, then
  verify the current outcome in the durable outbox rather than inferring it
  from timing; direct user-only delivery has a 30-second settlement window,
  while parent synthesis is bounded by its 120-second deadline plus that
  delivery window; in user-and-parent mode the early direct delivery is
  correlated but non-settling and only the final synthesized outbound settles
  the continuation;
- remote `coding_task` admission with its authoritative task projection by
  `scope.task_id` plus `correlation.task_generation_id` on the parent
  `tool.result`. Its later terminal delivery uses a bounded
  `coding-completion-*` continuation whose decision repeats those identities
  and adds `correlation.worker_generation_id`,
  `correlation.remote_thread_id`, safe `scope.scope_alias` and
  `scope.profile_alias`, and bounded `task_status`, `remote_state`, and
  `failure_code` fields in `.data`. Join the final channel receipt by
  `correlation.delivery_id` and the deduplication transaction by
  `correlation.completion_id`. A recovered monitor may not retain the original
  turn identity, so use task plus generation rather than time matching or an
  inferred `metadata.parent_turn_id`; verify current state in the task
  registry, companion worker ledger, and remote thread without replaying the
  task;
- `node.invocation.observed` tool results with the gateway invocation store and
  companion invocation ledger by exact `correlation.invocation_id`. Use
  `scope.target_alias`, optional applicable `scope.scope_alias` and
  `scope.profile_alias`, and `correlation.policy_revision` to verify the
  admitted authority. Read `.data.action`, `.data.status`, and
  `.data.decision_code` to distinguish prepared/dispatched/completed/status,
  success/failure/cancellation/timeout, and disconnect/uncertainty. A missing
  optional scope or profile means that capability has no such configured
  alias; never infer one from command text, paths, or timestamps. Inspect the
  authoritative ledgers read-only and never replay an uncertain mutation;
- `interaction.lifecycle` records with the authoritative interaction registry
  by `correlation.interaction_id` and exact
  `correlation.interaction_revision`; use
  `correlation.interaction_event_id` for the event log, `scope.task_id` for task
  ownership, and `correlation.delivery_id` for a bound prompt/final outbox
  intent. Late answer, resume, delivery, cancel, timeout, failure, and terminal
  events use immediate one-record continuation traces whose
  `metadata.parent_turn_id` names the originating turn; no question, answer,
  approval, or failure-detail content is copied into the trace;
- steering injection or interrupt with the next model/tool decision;
- context compaction with the model behavior immediately before and after it;
- turn start/end and the final outcome.

Provider-controlled request IDs remain optional and separate from the
runtime-generated model-attempt ID. Historical traces may lack
`scope.task_id`, `correlation.task_generation_id`,
`correlation.completion_id`, `correlation.delivery_id`,
`correlation.interaction_id`, `correlation.interaction_revision`,
`correlation.interaction_event_id`, `correlation.model_attempt_id`,
`correlation.tool_call_generation`, `correlation.worker_generation_id`, or
`correlation.remote_thread_id`, `correlation.invocation_id`,
`correlation.policy_revision`, `scope.target_alias`, `scope.scope_alias`, or
`scope.profile_alias`; for
those traces, correlate records conservatively by turn and sequence and state
the limitation. Older loop-decision records may also lack
`correlation.tool_call_id`; use their tool and argument hash only as bounded
supporting evidence, not as an exact join. An empty task generation on a new
trace means the completion producer did not carry an exact durable generation;
do not substitute the task ID alone when task reuse is possible.

In rich mode, inspect only the relevant normalized preview fields in `.data`,
including `input_preview`, `final_preview`, `messages_preview`,
`response_preview`, `reasoning_preview`, `tool_calls_preview`,
`arguments_preview`, `result_preview`, `content_preview`, `error_preview`, and
`message_preview`.

## Correlate External Evidence

Inspect the affected profile's service logs around `created_at` when the trace
is incomplete or the failure crosses process boundaries:

```bash
journalctl --user -u mintclaw-PROFILE.service \
  --since 'START_TIME' --until 'END_TIME' --lines 200 --no-pager
```

Use durable task or interaction registry state only to verify current
authoritative state. Do not edit it. Prefer exact IDs from the trace over text
matching.
