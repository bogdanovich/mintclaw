# Provider Output Limit Capability

`ProviderCapabilities.OutputTokenLimit` declares whether a positive integer
`max_tokens` option reaches the provider request as an explicit output-token
ceiling. It covers `Chat` and every streaming operation that the same instance
advertises. It does not attest backend enforcement, hidden reasoning, internal
HTTP retries or billed-token accounting, and grants no tool authority.

The default is conservative `false`. Initial verified adapters are:

- OpenAI-compatible HTTP: automatic `max_tokens`/`max_completion_tokens` selection
  or either explicitly configured known field; no extra-body override.
- Anthropic SDK/OAuth and Messages HTTP: request `max_tokens`.
- Gemini: `generationConfig.maxOutputTokens`; no extra-body override.

Unknown/custom output field names and payload-overridden HTTP/Gemini instances
do not advertise support. Tool-schema wrappers preserve the underlying
descriptor. OAuth Codex remains unsupported: its backend rejects the field and
the adapter deliberately omits it. Unwrapped requests and existing Codex
behavior are unchanged; no new admission policy is enabled by this metadata.

Model-free HTTP tests inspect actual request bodies for Chat and supported
streaming operations. The fixture returns a synthetic HTTP error after capture;
these are transport-contract checks, not live qualification or usage evidence.
Consumers must explicitly require the capability when an output ceiling is a
boundary. The bounded core-eval consumer is tracked in
[#1882](https://github.com/bogdanovich/mintclaw/pull/1882).
