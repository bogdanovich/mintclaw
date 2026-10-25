# Owned Eval Child Call Budget

The private `owned-eval-call-budget` gateway flag is a parent/child protocol,
not a general profile option. Without it, `gateway` uses its existing startup
path. Its canonical JSON specification contains only a version, fresh UUID
nonce and [call limits](provider-call-budget.md); credentials and prompts must
not be supplied. Debug, truncation overrides and empty-model startup are refused.

The child attaches one shared budget through
`gateway.RunWithProviderCallBudget` and `agent.WithProviderCallBudget`. After
gateway shutdown/rollback returns, it seals the budget and writes one private
terminal `eval-call-budget-<nonce>.json` receipt. The recorder anchors the
private 0700 home before startup, uses exclusive 0600 creation and never adopts,
overwrites or removes an old receipt. Existing Linux/macOS owned-launch support
is unchanged; this protocol does not enable owned launch on Windows.

The receipt contains only version/scope, launch nonce, the actual child PID,
whether gateway shutdown returned cleanly, and budget limits/counters. An error
or unknown observation is still retained when possible. Canonical bounded
decoding validates caller-retained nonce, expected PID, limits and counter
consistency without opening a current session or process. No payload, model,
credential, tool argument or arbitrary error string is retained.

A syntactically valid receipt is **not** a passing eval, ownership attestation
or complete billing record. A parent must independently admit the fresh scoped
profile, own/settle the exact child, reject provider-owning tools outside the
budget graph, capture/inspect the private raw receipt and bind it to frozen run
evidence. It must assess clean lifecycle, nonzero work, stop reasons, known usage
and all feature-specific oracles. The existing runner does not yet pass this
flag or consume the receipt; frozen grading, candidate admission and live
qualification remain subsequent C1q work. No catalog case is promoted here.
