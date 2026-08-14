# Browser Capability BF1 Protected Fill Parity Admission

Status: admitted for implementation

This slice adds typed semantic `fill` parity to gateway-hosted and
companion-hosted browser sessions. It reuses the first-party browser broker,
fresh element authority, private Playwright adapter, companion command, and
no-replay boundaries. It also closes the remaining persistence gap between a
model-produced form value and the existing companion `EphemeralInput`
transport.

The slice does not expose raw Playwright tools, selectors, coordinates,
JavaScript, browser endpoints, host paths, credentials, or a generic secret
transport. It admits synthetic and ordinary non-secret form input only.
Password, payment, one-time-code, and operator-designated sensitive fields
remain unavailable until a separate credential policy is admitted.

## Admitted contract

`browser_act` may accept exactly one fill action containing:

- `kind: fill`;
- a fresh broker-issued semantic element reference; and
- one non-empty UTF-8 value bounded by the selected profile's
  `text_input_bytes` limit.

The broker revalidates the reference as an editable text control in the
selected tab and frame. The model cannot supply a selector, input type,
autocomplete classification, effect, approval decision, origin, profile,
driver command, or submit behavior. Fill replaces the control's local value;
it never presses Enter, clicks a submit control, accepts a dialog, or otherwise
commits an external effect in the same driver operation.

Trusted code classifies an admitted fill as `local_edit`. A later action that
submits, sends, publishes, purchases, books, pays, deletes, or confirms state
retains its own independently derived effect and approval boundary.

Success returns a fresh bounded observation. Navigation, popup, dialog, page
close, detached control, stale document authority, timeout, transport loss,
and ambiguous driver completion retain the existing explicit terminal and
quarantine semantics. An accepted fill is never blindly replayed.

## Protected value lifecycle

The form value may exist only in the current provider response, the in-memory
tool call being executed, one bounded transport envelope, the live broker
input slot, and the private driver request. It must not enter durable or
diagnostic state.

Before the assistant tool-call intent is persisted, the trusted tool registry
projects fill arguments to a schema-valid redacted form. The durable
projection keeps only the tool-call identity, action kind, semantic reference,
and bounded non-plaintext metadata required to pair the later tool result and
repair a turn. The original value remains in the current in-memory execution
request only. This projection is selected by trusted tool and action type; the
model cannot request it for another tool or disable it.

The same projection applies to every copy derived from the assistant response,
including canonical session history, Seahorse input, ingress or turn recovery,
pending interaction state, diagnostic traces, runtime events, tool feedback,
stagnation records, approval records, and model-visible retained artifacts.
Logs expose only fixed redaction markers and bounded counts. Errors never echo
the value or raw driver payload.

The browser broker stores the live value only in its existing per-worker
ephemeral input map. Preparation and durable action records contain no
plaintext value. Close, expiry, cancellation, quarantine, worker loss, and
completed action cleanup erase the live slot. Restart cannot reconstruct the
value and therefore fails closed instead of redispatching the action.

## Companion transport and ledger boundary

For a companion action, the durable `browser.act.v1` plan contains an empty
`action.value`, a bounded byte length, and a domain-separated input digest
bound through the prepared action hash to the owner, actor, session, tab,
frame, snapshot generation, origin, policy, profile, catalog, semantic role,
accessible name, and action invocation ID.

The initial gateway-to-node dispatch carries the value once in the existing
bounded `InvocationDispatch.EphemeralInput` envelope. The gateway commits the
redacted durable invocation plan at the transport boundary before writing the
request. The companion:

1. decodes the envelope only for `browser.act.v1` with `kind: fill`;
2. requires the durable action value to be empty;
3. verifies the exact byte length and input digest;
4. independently revalidates owner, actor, session, tab, frame, document,
   snapshot, origin, policy, profile, catalog, role, and name authority;
5. accepts the redacted invocation record immediately before driver dispatch;
   and
6. passes the value only to the private typed Playwright fill operation.

The companion invocation ledger stores the redacted plan and a bounded
terminal receipt. It never stores the ephemeral envelope, fresh observation,
page-rendered value, or driver request. A duplicate, reconnect, restart, or
recovery attempt without the initial envelope fails closed. A response lost
after acceptance produces an unknown terminal outcome and quarantines the
session; it does not replay the fill.

Gateway-local sessions use the same prepared-action and cleanup semantics but
do not serialize a node dispatch. Their durable action record and all agent
state use the same redacted projection as companion sessions.

## Sensitive-field deny policy

The private browser host classifies the revalidated control before accepting
the value. This slice denies at least:

- password controls;
- payment-card number, security-code, and expiration controls;
- one-time-code and recovery-code controls;
- controls whose trusted autocomplete metadata identifies a credential or
  payment field; and
- controls matched by an operator-configured sensitive-field policy.

The classification is derived from the current private DOM/accessibility
state, never from model arguments. Unknown or conflicting classification fails
closed. Denial happens before action acceptance and before the private driver
receives the value. The safe error identifies only the bounded policy class,
not the value, selector, page markup, or protected attribute.

No credential alias, password manager, payment vault, cookie, storage-state
import, or origin-bound secret injection is admitted here. Such behavior
requires a separately reviewed credential policy and must not be simulated by
broadening ordinary fill.

## Capability discovery

A target and profile advertise `fill` only when all of the following are true:

- the private driver supports typed semantic fill;
- the broker supports fresh editable-control authority;
- the agent runtime supports trusted durable-argument projection;
- the selected placement supports protected transient value delivery and
  cleanup;
- the target's approved catalog and profile action allowlist contain fill; and
- the bounded text-input limit is non-zero.

The gateway intersects those facts in one runtime generation. A target must
omit fill instead of advertising it and failing only after the model supplies
a value. Gateway and companion profiles expose the same model-visible action
shape, effect, freshness, terminal outcomes, and safe errors.

## Approval, freshness, and no-replay rules

Fill itself is a local edit and does not require an external-commit approval.
The value is excluded from approval prompts and durable approval arguments.
If future site policy requires approval for a particular local edit, the
approval may bind the redacted prepared action identity and field description,
but never plaintext or a reversible derivative of it.

Immediately before driver dispatch, the broker or companion host revalidates:

- routed owner and actor;
- session and profile lease;
- selected tab and frame;
- driver-owned document identity and navigation generation;
- fresh snapshot and semantic element authority;
- current origin and network policy;
- browser policy, profile, and approved catalog revisions; and
- editable and non-sensitive field classification.

Any mismatch fails stale with zero input dispatch. A navigation beginning
after the private final check is a concurrent browser event; an ambiguous
result quarantines the session and is not retried.

## Acceptance evidence

Implementation is complete only when focused agent, broker, schema, transport,
runtime, host, real-driver, and production-WSS tests prove:

- an admitted editable control fills successfully and returns a fresh
  observation on both placements;
- empty, malformed, oversized, invalid-UTF-8, stale, detached, hidden,
  disabled, read-only, and non-editable targets fail before driver dispatch;
- password, payment, one-time-code, configured-sensitive, and ambiguous fields
  fail closed without revealing the value;
- the assistant tool-call intent is redacted before every durable write while
  the same in-memory call still executes with the original value;
- the companion receives the value only in the initial ephemeral dispatch and
  rejects missing, duplicate, malformed, digest-mismatched, or length-mismatched
  envelopes;
- gateway recovery state, canonical history, Seahorse state, companion and
  browser ledgers, events, traces, logs, errors, approvals, and retained
  artifacts contain no plaintext canary value after success and every failure
  path;
- crash, disconnect, timeout, cancellation, close race, expiry, and restart
  preserve terminal no-replay behavior and erase live value slots;
- catalogs omit fill whenever protected projection or transient delivery is
  unavailable; and
- live gateway and companion canaries fill and observe a synthetic non-secret
  form, close, immediately reuse the profile, and pass durable-store, trace,
  lock, and process cleanup audits.

Tests use unique high-entropy canary strings and scan raw persisted files and
captured logs, not merely structured projections. Production evidence records
only digests of the test fixture files and fixed redaction assertions; it does
not reproduce the canary value.

## Rollout order

1. Add trusted durable tool-argument projection and prove crash/recovery
   pairing with redacted browser fill intents.
2. Extend the shared prepared-action and companion ephemeral-input contract
   for fill, keeping capability discovery disabled.
3. Add private host revalidation, sensitive-field denial, and typed Playwright
   dispatch on both placements.
4. Add adversarial persistence, failure-injection, real-driver, and
   production-WSS coverage.
5. Enable fill in controlled gateway and companion profiles, renew the exact
   approved companion catalog, deploy, and run the live privacy canaries.

## Mandatory stop conditions

Stop implementation and keep fill unadvertised if:

- a plaintext value must be written to durable agent, gateway, node, browser,
  approval, trace, log, or artifact state;
- the current in-memory tool call cannot be separated from its durable
  redacted projection without breaking recovery or tool-call pairing;
- companion recovery requires retaining or replaying the ephemeral envelope;
- a field's sensitive classification must be trusted from model input;
- a driver error can echo the value or raw page payload; or
- gateway and companion cannot expose equivalent freshness, cleanup, and
  terminal no-replay behavior.

Screenshots, uploads, downloads, diagnostics, file chooser, dialog prompt
values, credentials, and the remaining ordinary interactions stay in later
phases of the
[Browser Functional Parity Execution Goal](browser-functional-parity-execution-goal.md).
