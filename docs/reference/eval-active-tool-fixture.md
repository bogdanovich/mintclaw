# Active Tool Fixture

The internal eval fixture underpins C2, but is not by itself a completed steering
eval, a public tool or permission to enable arbitrary MCP servers. Ordinary eval
profile admission still refuses MCP; the explicit owned `active_tool` profile
below admits only this fixture. No live/default case is added by these primitives.

It uses the existing MCP Go SDK and native MintClaw MCP client, on an owned
numeric loopback listener. It exposes exactly one read-only `eval_barrier` tool.
The driver registers a bounded trial/slot before sending a task. An admitted
call changes that slot from registered to active and blocks until the owning
driver releases it, the request is canceled, the host closes or 45 seconds
elapse. There is no model-facing release endpoint or tool. Arguments cannot
name files, URLs, shell commands or operations with external effects.

## Bounds And Observations

- At most 128 registered slots per host, two slots per trial and two active
  calls across the host. Completed identities cannot be replaced or replayed.
- A unique canonical UUIDv4 path, exact Host/path checks, no Origin, 16 KiB
  request bodies and bounded HTTP timeouts constrain the loopback endpoint.
  Mutable host state cannot be JSON-serialized into a report. The path is a
  routing identity, not a credential or protection from hostile local processes.
  Only pre-registered trial/slot arguments can enter a barrier.
- In-memory observations retain trial/session/slot, state and ordered entry,
  release and settlement sequence numbers, never model/tool payloads or tokens.
- A missing call, cancellation, shutdown or timeout is not successful release.
  Shutdown unblocks owned calls; it does not assert native gateway settlement.

The fixture uses bounded, stateful MCP SDK sessions so the SDK's native
cancellation notification reaches the session owning the held request. A
stateless per-request session cannot perform that correlation. Idle sessions
expire after five minutes; owned shutdown closes SDK sessions around HTTP shutdown
and blocks further HTTP admission. No second cancellation protocol is added.

Canceled observations distinguish `cancellation: context_canceled` from
`cancellation: deadline_exceeded`. A host shutdown is `closed` with no cancellation
cause, even when closing the SDK also cancels a request context. Released receipts
omit this optional field, preserving existing frozen C2 observations. A timeout,
client close or fixture cancellation alone is still not proof that native
AgentLoop `/stop` aborted and settled its owner; C3b needs that separate witness.

The fixture observes a local MCP handler, **not** native AgentLoop ownership or
steering injection, nor does it attest against a hostile local operator. A later
C2 observer must join this activity with the native
tool call/history, active turn/scope and correlated enqueue/injection, then
retain complete emitted-batch results and final delivery. A fluent final alone
cannot satisfy those requirements. Do not promote a scenario on this primitive's
unit/integration controls.

The existing native interrupt summary now retains its already-filtered
`message_hash`, alongside role, length and queue depth. It still omits raw
steering text, diagnostic preview and coding UI receipt IDs/text. This makes
enqueue correlation observable without introducing a new queue or journal;
asynchronous event logging may drop records, so missing evidence stays unknown.

Controls use the real native MCP Manager over HTTP and synthetic tool inputs,
with no paid model or production profile. They cover active-before-release,
single-use identity, missing calls, actual native MCP client cancellation,
deadline/cancellation separation, the concurrency limit, native SDK-session
shutdown, HTTP admission and refusal to serialize private host state. These
controls are not live model qualification.

## Owned Profile Integration

An optional preset `active_tool` object pins a numeric IPv4 loopback address,
canonical UUIDv4 namespace and `one-at-a-time`/`all` steering mode. It requires
the matching capability, `bounded_calls`/`target_call_budget`, metadata capture
and owned `--start-target`. Adaptive model users and PDF/browser combinations
are refused. Ordinary presets keep their existing log/admission rules.

The native config must select exactly the `corefixture` HTTP server and
`mcp_corefixture_eval_barrier` tool, with default-deny server/tool policies.
No headers, stdio commands, environment, lock files, deferred discovery, extra
servers or wildcard tools are allowed. The mode must match native configuration.
Info-level logging admits an exact five-kind metadata event filter, never debug,
raw event payloads or glob filters. Existing global private-surface observation
and target budgets remain mandatory; this does not grant approval bypasses.

`prepare` generates a new path identity and unused port without launching a
server. `run` must bind and retain that exact listener before starting its
fresh child. A port collision, closed host or changed probe refuses further
admission; no listening service or PID is adopted. Host shutdown follows native
child stop and remains distinct from a native tool/turn terminal outcome.

## In-Flight Inputs And Evidence

An internal synchronous `afterSend` boundary runs after the actual initial send
and before waiting for its terminal response. The flow owns each guidance send,
wire identity and transcript event. It enforces the case's total user-input
limit, at most two added inputs per boundary and privacy checks before send.
Failures are sticky even if a domain ignores a sender error. This is transport
support, not permission to substitute a follow-up after final for steering.

The enqueue observer reads only the launched child's bounded private log file.
It scans raw JSONL before projection and rejects duplicate keys. Guidance must
contain its unique registered trial identity. While the fixture remains active
and unreleased, it waits at most two seconds for exactly one matching native
filtered-hash event with turn, session and agent identity. Missing/dropped,
malformed, duplicate, canceled or private evidence stays non-passing. Polling
waits for an actual event; elapsed time alone is never success.

The existing bounded passive session-history reader is shared with C1 answer
observation. Its file anchoring, raw privacy inspection, equal metadata reads
and native JSONL inspection remain unchanged; it never opens a recovering store.

The [C2 case-level contracts](../architecture/agent-evals-core-execution.md)
join native scope/trace, enqueue/injection, ordered emitted-batch results and
final delivery, with explicit mode controls and independent two-session routes.
Their fixed qualification outcomes remain unchanged. The
[native stop/reuse contract](core-stop-eval.md) joins these cancellation primitives
with native abort, history and delivery; neither implementation nor model-free
controls alone qualify the case.
