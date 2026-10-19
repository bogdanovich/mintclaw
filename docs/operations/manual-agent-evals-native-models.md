# Routed Native Model Observations

## Scope

An explicitly prepared `capture_model_metadata: true` owned profile can retain a `native_models` observation in
`run.json`. The default remains off. The [capture admission contract](manual-agent-evals.md#opt-in-model-metadata-capture)
still applies: no attached target, alternate store, content preview, broad approval, new daemon or provider wrapper.
No native model metadata is supplied to the user driver or judge.

This records native AgentLoop request/response records for turns named by actual delivered wire `trace_scopes`.
It does not claim every billable call. Provider-internal retries, tool/audit/private model calls, compaction,
delegation outside those routed scopes and missing transport outcomes remain outside that coverage.
`all_call_coverage` stays `unknown`; returned-final usage and user/judge usage remain separate observations.

## Collection

Collection runs after owned stop/reap, whole-profile raw inspection and domain finalization. It requires the exact
admitted PID's native exit-zero receipt, observed bounded raw-profile coverage, a full forty-character runtime
revision and matching native source version/revision. Operator-injected build metadata is not independent VCS
attestation. The observer reads only the native workspace-default trace store under the admitted private home.

At most sixteen trace files / routed scopes, 2 MiB serialized bytes per file and 2,048 model requests may qualify.
These are observation bounds, not disk, provider-cost or task-completion guarantees. Direct private files, native
limits/policy/source, full native validation and record digests are checked. Aliases, alternate roots, malformed
documents, unknown fields, duplicate JSON keys, previews, foreign identities and ambiguous records cannot qualify.
Original bytes are scanned before native decoding/projection; an earlier malformed file cannot hide a later
observed leak within the admitted bounds. The shared native byte decoder owns `RawMessage` payloads, and the shared
native filtered-hash helper owns session hashing. No prompts are reconstructed from workspace files or transcripts.

Each row binds the retained trial/case, agent/session, full workspace/turn scope and sorted actual request IDs.
A root cannot be counted in two trials. Matching uses native turn/agent/filtered-session identity, not text, timing
or model-name similarity. Record sequences must be contiguous; native truncation/drop witnesses, absent start/end,
contradictory completion, orphan/duplicate attempts and wrong scopes preserve uncertainty. Native request and actual
response provider/model names remain distinct, including fallback. Retry/fallback record counts are observations,
not a count of every provider attempt.

Native provider names can be absent even when model names and fingerprints are present. Empty provider fields
remain unknown; they are never inferred from configuration or model-name spelling. Comparison retains separate
request/response provider unknowns, compares known model names independently and compares provider identities only
when both sides have complete evidence. A coverage change alone is not an observed provider change.

## Results

- `observed`: every selected routed scope has an admitted native record set. This is not complete billing or a
  general runtime/quiescence certificate.
- `partial`: some expected scopes are missing, invalid or ambiguous; qualified other rows remain separately visible.
- `unavailable`: lifecycle, privacy, source, bounds or store admission could not establish that scope. No model
  counts are invented from a final message, configured model or context-window counter.
- `leak_observed`: bounded raw inspection found a known private value. The run fails without publishing that value.

Per-attempt usage preserves `observed`, `unavailable`, `legacy_unknown` and `not_returned`. Explicit provider presence
includes an observed zero; absent legacy presence does not turn nonzero counters into trustworthy returned usage.
Failed/aborted attempts can lack a response, with no estimated usage. A completed turn may also retain an unanswered
attempt when an actual native `model.retry` binds to that still-unanswered attempt. `retry_observed: true` preserves
that evidence; usage stays `not_returned`, never observed zero. Orphan, duplicate, post-response, wrong-scope and
post-terminal retries cannot explain a missing response; a fallback count alone is not such evidence. A
tool-call-only response can have usage and no response-content hash. Every admitted response is summed, not only
the last LLM call of a tool loop.

Totals retain raw integer input/output tokens and coverage/counts. Display uses the existing compact formatter,
for example `10352` becomes `10.3k`. Overflow clears totals with an explicit `overflow` coverage state. The trace
format does not retain a provider total, so no computed input-plus-output number is labeled as that returned total.
Missing records/usage cannot become a hard monetary cap or zero billable spend.

Actual native provider-neutral prompt, stable/dynamic system, tool-schema, history and dynamic-tail hashes are
retained. They describe the source's effective request/receipt projection before provider-specific serialization,
not raw protected content, provider wire bytes, an individually attested skill catalog or every fallback rewrite.
Fresh absolute paths, time, model replies and tool loops can legitimately change hashes. Comparison reports drift,
not normalized inputs or causal quality attribution; instruction/skill and all-call-model unknowns remain explicit.

## Offline And Retention

Judge-only grade validates the selected frozen trial/route bindings and source identity and preserves their native
projection. It does not reread runtime traces, start a target, replay a task or add judge tokens to target usage.
Comparison validates all retained bindings/totals, distinguishes native model/request drift and preserves legacy
reports without native observations as unknown. Unknown telemetry alone does not rewrite a task verdict; a known
raw leak remains a hard failure and refuses judge admission.

Reports and original metadata-only traces stay private outside Git under the existing evidence policy. Trace IDs
and original byte digests support operator inspection of the retained source files. The frozen projection is not a
cryptographic authenticity certificate for reports edited by their owner. Do not modify source profiles/reports
concurrently or restart a used profile. Native traces may be truncated, pruned or never persisted; keep those gaps
visible rather than retroactively backfilling historical reports.

This observer does not finish automatic per-trial profile isolation, actual loaded skill-version attestation,
remaining PDF/browser/adaptive scenarios, domain repeats or the full E0-E5 exit. Stub controls and an observed
metadata record set are not live domain qualification.
