# Active Tool Fixture

The internal eval fixture is a prerequisite for C2, not a completed steering
eval, a public tool or permission to enable arbitrary MCP servers. Ordinary eval
profile admission still refuses MCP. No live/default case is added by the
fixture alone.

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
- A 256-bit bearer token, exact Host/path checks, no Origin, 16 KiB request
  bodies and bounded HTTP timeouts protect the loopback endpoint. The host and
  token cannot be JSON-serialized into a report. Token provisioning belongs to
  the later owned-profile integration, not a public corpus manifest.
- In-memory observations retain trial/session/slot, state and ordered entry,
  release and settlement sequence numbers, never model/tool payloads or tokens.
- A missing call, cancellation, shutdown or timeout is not successful release.
  Shutdown unblocks owned calls; it does not assert native gateway settlement.

The fixture observes a local MCP handler, **not** native AgentLoop ownership or
steering injection. A later C2 observer must join this activity with the native
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
single-use identity, missing calls, cancellation, the concurrency limit,
shutdown, HTTP admission and refusal to serialize private host state. These
controls are not live model qualification.
