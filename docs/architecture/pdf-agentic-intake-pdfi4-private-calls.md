# PDFI4 Protected Model Call Checkpoint

## Scope And Dependencies

Packet 1A (#1484) and 1B (#1488) are merged. Their transport and ledger contracts do not yet implement natural-language
intake. The [PDFI4 goal](pdf-agentic-intake-pdfi4-goal.md) remains incomplete.

Before introducing another value-bearing model call, close the existing private-audit logging boundary. This small
prerequisite reuses the current direct, ephemeral PDF audit call; it does not add a service, a store, a planner, a
provider, a public plaintext argument, or a general protected-input framework. Native proposal application may be
developed independently from merged 1B because it calls no real provider. Live semantic dialogue depends on both.

## Verified Risk And Minimal Boundary

`oauth/codex_provider.go` copies a normalized provider error's `SafeMessage` and `RequestID` into diagnostic logs.
Both originate in untrusted response data; whitespace normalization and truncation do not make an echoed PDF value
safe. An unexpected streamed tool name in `openai_compat` is another untrusted diagnostic field. Catching the error
in the document auditor is too late to protect a provider's earlier log.

Use an in-process context marker for sensitive requests, not an API-body option or a value-scanning redactor. Marked
provider diagnostics retain fixed event names, configured model identity, counts, canonical error kind and HTTP
status, but omit untrusted response text, request IDs and tool-name/error-detail fields. Ordinary requests retain
their existing diagnostics. The marker is not field-assignment, approval, route, or retention authority.

The existing audit adapter, and the later intake adapter, must explicitly disable native search as well as supplying
no tool definitions. Disable ordinary prompt-cache lineage/write intent, use only the bounded private payload and
the dedicated system instruction, and install no ordinary streaming, trace or history callback. These client-side
controls do not imply a provider-side retention guarantee.

Compatibility-provider `extra_body` is an opaque final override: it can restore tools/cache fields, replace the
private messages or redirect the configured model, including through provider-specific extensions. Marked calls
with any such overrides are unsupported and refuse before request construction or HTTP send, for both chat and
streaming entry points. Ordinary overrides remain unchanged; a no-override API configuration/equivalent fallback
can serve the protected lane. Do not silently reinterpret arbitrary vendor extensions as private-safe controls.

Source inspection also confirms that Codex/Claude CLI adapters run autonomous local tools, and the Copilot adapter
shares a persistent session. A nil MintClaw tool list does not disable those facilities. These adapters must refuse
marked requests before building a prompt, spawning a process or sending to the session; ordinary CLI/session calls
are unchanged. A configured equivalent API fallback can still satisfy the existing audit policy. This is a refusal
of an unsupported protected lane, not a new CLI isolation backend or provider-selection allowlist.

## Executable Gates

- An HTTP error that echoes a synthetic private sentinel in both its message and request ID leaves neither in marked
  request logs. Canonical kind/status and the fixed failure event remain visible.
- The same ordinary request retains its useful diagnostics; suppression is scoped, not global.
- An unexpected streamed tool name or malformed argument detail cannot leak through marked compatibility-provider
  logs. The caller still rejects tool-bearing private responses.
- The real PDF audit adapter sets the marker, passes only its two private messages, supplies no tools, and explicitly
  disables native search and ordinary prompt-cache intent. Strict response/error handling remains unchanged.
- Context derivation preserves cancellation/deadlines and unrelated values; it does not mutate the parent or appear
  in serialized API bodies. Existing provider/audit tests, race checks, formatting and lint remain required.
- CLI/session adapters reject before accessing command configuration or execution/send, without copying the synthetic
  sentinel into the refusal. Tests use nil receivers and a counted fake session; they never launch a coding agent.
- Configured search/cache overrides, message/model overrides and unknown vendor extensions cannot reach HTTP on a
  marked request. Ordinary chat/stream requests still send those same configured fields; no-override marked chat
  remains supported. The refusal contains no private value or configured override content.

This checkpoint is not semantic intake or actual Telegram qualification. That later integration must also remove
Telegram's pre-admission content preview and fence ordinary history before protected ingestion. Existing raw ingress
spooling remains a transient transport boundary; a document-owned durable replay guard must prevent a previously
admitted message from falling through to ordinary history after mode close/cancel. No raw spool rewrite or second
fact store is admitted.
