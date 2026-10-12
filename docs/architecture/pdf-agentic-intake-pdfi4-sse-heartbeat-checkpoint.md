# PDFI4 Provider Heartbeat Checkpoint

PDFI4 is not complete. This prerequisite repairs stream framing, not form interpretation or value authority.

## Evidence

On merged runtime `10fa246de67f67fed8099d97587c32e5a9116121`, protected interpretation sometimes fails before a model
answer is available. A separate tools-disabled probe using only the blank public I-134 schema and a value-free
instruction reproduces `*json.SyntaxError`, `unexpected end of JSON input`, offset zero. The context is neither
cancelled nor expired. The shared classifier labels this transient failure `timeout`; that label does not establish
an elapsed-time limit.

A bounded, in-memory HTTP framing probe records only event counts, not bodies, headers, credentials or input values.
Successful requests contain 107 or 121 events, with no comments or empty data events. Both failed requests contain
three events, one comment line and one empty data event. The pinned OpenAI Go SDK's SSE decoder dispatches a
comment-only event; its stream layer then tries to unmarshal the empty data as JSON. Retrying can reproduce the same
heartbeat failure and does not repair the protocol boundary.

## Minimal Change And Gates

Keep the existing SDK request/authentication/error handling, response union decoder and bounded request lifecycle.
At the Codex response stream boundary, skip only unnamed events with empty or whitespace-only data before JSON
decoding. Preserve all nonempty payloads, named malformed events, provider errors, `[DONE]`, cancellation, completion,
usage, text/reasoning/tool-call accumulation and final-response requirements. Do not accept a partial response as
success, raise deadlines, add retries/fallbacks, change global SDK decoders or expose sensitive diagnostics.

Use one focused provider PR independent of the guidance-accounting PR. Reproduce failure first with deterministic
HTTP/SSE tests; cover heartbeats before and between events, streaming callbacks, malformed and named-empty events,
heartbeat-only EOF, cancellation and request/auth/cache options. Run provider, shared adapter and intake integration
regressions, race tests, formatting and scoped lint. Require exact-head CI/review/authorized merge, then backed-up
merged deployment and same-job continuation. A public probe pass alone is not form or Telegram qualification.
