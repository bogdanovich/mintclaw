# Hook Tool Extension Boundary

MintClaw does not admit provider tool schemas injected by `before_llm` hooks.
The system prompt, provider tool definitions, frozen turn envelopes, and
completed transcript prefix are runtime-owned inputs. A hook response that
changes any of them is accepted only for its unrelated fields; MintClaw keeps
the prior protected messages or tools and logs the rejected mutation.

This restriction keeps capability discovery, turn-profile filtering, approval,
execution, and prompt-cache lineage on one typed tool contract. A model must
never see a callable schema that the runtime did not register and admit.

External integrations have two supported paths:

1. Register a typed MintClaw tool through the capability registry, then use
   `before_tool` or `after_tool` to enforce policy or transform its execution.
2. Use `before_tool` with `respond` for an already registered and admitted tool
   call when an external hook owns the result.

`before_llm` may still choose an allowed model, adjust allowed request options,
append current-tail context, or rewrite the current dynamic tail. It cannot
rewrite completed history or create a new callable capability by modifying the
`tools` array.

See [Hooks](README.md) and the [Hook JSON Protocol](hook-json-protocol.md) for
the current interceptor contract.
