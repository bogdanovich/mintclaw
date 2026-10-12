# P8 Streaming Failure Qualification

H6's isolated loopback authentication canary exposed a supported coding-path
defect on merged source `3d1afa5573e7448a0d46169143e03be0bf4e182b`: a streaming
HTTP 401 before visible output automatically issued a second non-streaming
POST to the same endpoint. Changing transport cannot repair authentication.
The metadata-only trace retained one model request and a generic LLM-stage
error, but no safe provider classification for that direct single-route error.

The synthetic failing thread is `c94fa523-0a2e-403b-9df7-260370d12830`, trace
`trace-turn-be80721c49ef59311ae8c447`. Its canonical history revision/count
was `1/1`; the presentation checkpoint was revision 11 with failed activity,
one completed user item and two failed error items. Project state did not
change. A fresh second fixture recorded two `POST /chat/completions` requests
to the loopback server, confirming the boundary without replaying an external
effect or sending production credentials.

## Narrow correction

- Authentication failures and caller cancellation/deadline do not trigger the
  stream-to-Chat transport fallback. Existing eligible pre-output fallback and
  post-visible-output no-replay behavior remain intact.
- A structured authentication classification takes precedence over incidental
  timeout wording when deciding the outer LLM retry.
- An actual stream-to-Chat fallback emits the existing `model.retry` record
  with reason `stream_transport_fallback` and the same logical model-attempt ID.
- LLM terminal errors carry the known model-attempt ID plus bounded error code,
  classification source, provider-error kind, HTTP status and filtered request
  ID. Metadata-only traces/support summaries omit the provider message.

These are existing runtime-event/schema extensions, not a new state owner,
provider retry engine, tracing dependency, replay mechanism or permission.
Ordinary regressions verify no duplicate auth/cancel call, eligible transport
fallback, exact model/error joins and configured-secret exclusion. H6 requires
the corrected production canary and exact-head gates before declaring P8 stable.
