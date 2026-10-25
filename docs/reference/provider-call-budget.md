# Owned Provider Call Budgets

`providers.NewCallBudget` creates an opt-in, caller-owned limit shared by
explicitly wrapped provider instances. It is a prerequisite for bounded manual
core evals, not an enabled runtime policy or a completed eval integration.

## Contract

- One `Chat` or declared `ChatStreamEvents` invocation consumes one logical call,
  including a failed invocation. A non-streaming fallback probe consumes none.
- Admission requires caller-mediated tool capabilities, a positive integer
  `max_tokens` within the requested-output ceiling, and bounded serialized
  messages, tool definitions and options. Media/attachments are unsupported;
  references do not establish a bound on the data an adapter might load.
- The wrapper forces provider-native search off on a detached options map and
  does not expose image generation. It preserves default model, supported
  streaming and stateful cleanup. It adds no retries or queues.
- Limits permit at most 256 calls, 2 MiB of serialized input per call, 32,768
  requested output tokens per call, a 2,000,000 reported-token threshold and two
  simultaneous calls. The caller must choose its own smaller limits/deadline.
- Any admission refusal is sticky. `Seal` prevents new work but does not cancel
  or adopt work already admitted. The caller owns cancellation and settlement.
- Missing, invalid, overflowing usage or any failed adapter call makes usage
  unknown and stops further calls. Known zero usage is distinct from missing
  usage. No successful response is returned for an unknown or exhausted call.
- The reported-token count uses the larger of provider total and input + output
  counts. It does not treat cache dimensions as separately billed tokens.
  Reaching/exceeding the threshold stops work and returns a budget error.

## Evidence And Boundaries

Snapshots retain only configured limits, counters, a fixed stop reason and the
sealed flag. They contain no prompts, tools, outputs, model names or credentials.
`UsageKnown` means all **wrapped logical calls** have completed with usable
provider reports. A final consumer must additionally require `Sealed`, zero
`InFlight`, a nonzero expected call count and no stop reason. It must prove that
all relevant provider construction paths used the shared budget; a snapshot
alone does not prove that coverage.

The reported-token threshold is not a hard billing cap. Calls already admitted
can exceed it, bounded by the concurrency/request limits. Adapter-internal HTTP
retries, server-side reasoning and omitted provider work are not independent
logical calls in this API. Input size is serialized adapter input, not actual
wire bytes or provider-tokenizer output. Requested output limits are requests to
the provider, not an attestation that its implementation honored them.

The existing eval native-model observation still declares `AllCalls: unknown`.
This primitive does not relabel that historical evidence, activate a case,
permit live model access, or claim complete billing telemetry. Owned gateway
wiring, frozen-run validation and real qualification remain separate work.
