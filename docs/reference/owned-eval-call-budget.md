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
and all feature-specific oracles.

## Runner Admission

An optional preset `target_call_budget` uses the five explicit limit fields
shown below and must be paired with capability `bounded_calls`:

```json
{
  "max_calls": 24,
  "max_input_bytes": 131072,
  "max_output_tokens": 2048,
  "max_reported_tokens": 100000,
  "max_in_flight": 2
}
```

These are operator-selected ceilings, not model recommendations or expected
costs. The budget is shared across **the entire run**, including all selected
cases and repetitions. It does not reset per trial. User/judge roles retain
their separate bounded call paths and are outside the target receipt's scope.

Bounded mode requires `--start-target` and the existing fresh private-profile
admission. This initial scope permits only the General conversation/question
policy; document/browser profiles are refused because their independent model
owners are outside the wrapped agent graph. No new tool authority is granted.
Cases declaring `requires: ["conversation", "bounded_calls"]` refuse missing
limits or attach mode before launch, rather than silently running unbounded.
The two pending core candidates declare this requirement; existing defaults,
private corpora without it and historical case identities are unchanged.

After settling the exact owned child, the runner reads a bounded private regular
receipt, inspects raw bytes before projection and retains the canonical proof
and digest in `run.json` as `target_budget`. It binds the fresh launch nonce,
limits and PID to the owned lifecycle and runtime probe. Missing/malformed,
unsettled, zero-call, unclean or unknown-usage evidence is non-passing; hard limit
refusals fail. A passing trial or semantic judge cannot override that run-level
verdict. Native model metadata still reports `all_call_coverage: "unknown"`.
Bounded reports must retain the requested whole-profile privacy observation;
its omission is invalid, not the historical unrequested/nil privacy state.

Offline `grade` checks the manifest's mandatory requirement and all frozen budget
bindings before any judge call. An unknown, failed, omitted or malformed required
budget cannot be regraded to green. It retains the original run-wide receipt,
even when selecting a subset of source cases, rather than attributing costs to
that subset. `compare` validates retained bindings and preserves the run verdict;
it does not consult a current child/home or certify reports against malicious
operator rewriting. Changing budget presence/limits is environment drift, not a
quality attribution. No catalog case is promoted by enabling this mechanism;
bounded live qualification remains required.
