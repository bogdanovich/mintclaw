# Browser Capability And Approval Modes

This document is the implementation contract for the two browser-policy pull
requests that restore first-party browser parity without discarding the
existing policy and durable approval infrastructure.

## Product invariants

- Capability policy answers whether a browser operation may execute.
- Approval policy answers whether an otherwise allowed operation must pause
  for the owner.
- Effect classification records what an operation is expected to do. It does
  not grant or remove authority and does not implicitly require approval.
- `full_access` must not use field-name, button-name, site, origin, or action
  allowlists. It retains only protocol validity, fresh element authority,
  session ownership, resource limits, and configured network reachability.
- Gateway and companion placements expose the same model-facing behavior.
- Restricted policy is opt-in. It must be expressed by configuration or an
  operator-owned process hook rather than compiled-in semantic lists.
- A model may request confirmation when the user asks it to do so, even when
  the profile otherwise runs unattended.

## Profile configuration

The owner profile uses this P0 configuration:

```json
{
  "mode": "managed",
  "network_mode": "any_http",
  "capability_mode": "full_access",
  "approval_mode": "model_requested",
  "dry_run": false,
  "allow_approved_actions": true
}
```

For a fully unattended profile that never pauses for model-requested or
effect-based confirmation, use the same capability mode with approvals
disabled explicitly:

```json
{
  "mode": "managed",
  "network_mode": "any_http",
  "capability_mode": "full_access",
  "approval_mode": "none",
  "dry_run": false,
  "allow_approved_actions": true
}
```

This changes only confirmation behavior. It does not bypass protocol
validation, stale-element checks, session ownership, resource limits, or the
configured network policy.

`capability_mode` accepts:

- `full_access`: allow every driver-supported page operation that passes the
  protocol, freshness, ownership, resource, and network checks.
- `restricted`: apply the P1 policy object before dispatch.
- `legacy_strict`: preserve the current compiled policy during migration only.

`approval_mode` accepts:

- `none`: never pause automatically and ignore model-requested confirmation.
- `model_requested`: pause only when the model explicitly requests
  confirmation for the exact prepared action.
- `always_commit`: pause for every `external_commit` or `unknown` effect, and
  also honor an explicit model request.
- `policy`: use the P1 policy decision (`allow`, `deny`, or `ask`).

The browser action contract gains a separate optional confirmation value:

```json
{
  "effect": "external_commit",
  "confirmation": "request"
}
```

`effect` remains audit and recovery metadata. `confirmation` is the only
model-authored request to pause. Operator policy may require a pause but the
model cannot relax one required by configuration or a hook.

Restricted policy does not consume that model-declared audit effect. It derives
a separate conservative policy effect from the typed action and freshly
resolved accessibility identity. The companion derives the same value again
at final dispatch, so declaring a button click as `read` cannot bypass an
`external_commit` rule.

## P0: full access and explicit confirmation

P0 implements the profile enums, prepared-action binding, gateway and
companion propagation, and tool schema described above. In `full_access` it
removes semantic field admission and admits all driver-supported editable
controls, including price, password, one-time-code, payment, and unnamed
controls. The runtime does not expose cookie values, saved credentials, or
other private browser state to the model.

P0 keeps exact action hashes, one-time execution, stale-reference rejection,
network policy, session ownership, resource limits, durable receipts, and
crash recovery. Those are protocol and lifecycle guarantees, not semantic
browser restrictions.

P0 must not delete the existing strict policy. It moves that behavior behind
the explicit `legacy_strict` compatibility mode for removal after the P1
restricted policy has replaced it.

## P1: configurable restricted policy

P1 adds this opt-in configuration shape:

```json
{
  "capability_mode": "restricted",
  "approval_mode": "policy",
  "policy": {
    "default_decision": "deny",
    "rules": [
      {
        "id": "allow-form-edits",
        "match": {
          "actions": ["fill", "select", "check", "uncheck"]
        },
        "decision": "allow"
      },
      {
        "id": "confirm-purchases",
        "match": {
          "actions": ["click"],
          "effects": ["external_commit"],
          "origins": ["https://tickets.example"]
        },
        "decision": "ask"
      }
    ],
    "hook": {
      "command": ["/opt/mintclaw/bin/browser-policy"],
      "timeout_ms": 1000
    }
  }
}
```

Rules are evaluated in order and the first match wins. Match fields are
bounded typed arrays; no rule accepts executable expressions. The initial P1
match surface includes action kind, effect, exact normalized origins,
accessibility roles, and normalized element-name patterns. An unmatched action
uses `default_decision`. Element names and patterns are lowercased and internal
whitespace is collapsed before matching. The only pattern operator is `*`,
which matches zero or more Unicode characters; every other character is
literal, and regular expressions are not executed.

When configured, the hook receives one canonical JSON object on standard
input containing only bounded action metadata, profile and policy revisions,
origin, effect, and resolved accessibility identity. It never receives text
input values, cookies, credentials, page contents, or artifact bytes. It must
return exactly one bounded JSON object:

```json
{"decision":"allow"}
```

```json
{"decision":"deny","reason":"operator policy denied the action"}
```

```json
{"decision":"ask","summary":"Purchase tickets from tickets.example"}
```

Spawn failure, timeout, non-zero exit, malformed JSON, oversized output, an
unknown decision, or a changed policy revision fails closed. The hook command
is an argv array and is never interpreted by a shell. Its executable and
arguments are operator configuration and are never model-controlled. The hook
runs with a minimal non-secret environment rather than inheriting gateway or
companion credentials. It may run once while the action is prepared and again
at final dispatch, so it must be deterministic and free of side effects.

The hook refines the declarative result: `deny` always remains denied, while an
`allow` or `ask` baseline may become `allow`, `ask`, or `deny`. The final
decision and policy revision are bound into the prepared action and verified
at the final gateway or companion dispatch boundary.

When a runtime upgrade changes the typed browser command schema, a persisted
catalog matching the recognized immediately previous generation is
quarantined during registry loading instead of preventing the gateway from
starting. The gateway removes the stale browser command surface, changes the
stored catalog hash, and marks an approved node incompatible. No stale browser
command can execute. Unrecognized schema drift and other catalog corruption
still fail registry loading. A current companion must reconnect with the new
catalog, after which the owner explicitly renews the command-surface approval
before browser commands are available again.

## Validation and smoke matrix

P0 automated coverage must prove:

- gateway and companion fill a named numeric `Price` field and an unnamed
  writable field in `full_access`;
- password, OTP, payment, and ordinary text fields are all mechanically
  writable in `full_access`;
- `effect=external_commit` executes without a prompt under `approval_mode=none`;
- `approval_mode=model_requested` executes without a prompt when confirmation
  is omitted and durably pauses/resumes when `confirmation=request`;
- `always_commit` pauses for external or unknown effects regardless of the
  model confirmation value;
- action hashes, receipts, stale-reference checks, session cleanup, and
  accepted-action no-replay behavior remain unchanged.

P1 automated coverage must prove:

- ordered declarative allow, deny, and ask decisions;
- exact-origin and action/effect matching;
- hook allow, deny, ask, timeout, crash, malformed output, oversized output,
  and policy-revision mismatch behavior;
- no private input value or browser secret reaches the hook;
- a gateway cannot broaden companion policy and a companion cannot execute an
  action whose bound policy decision or revision changed.

After automated validation, each PR receives real smoke coverage on both
gateway and companion placements. P0 smoke tests perform an unrestricted
local form edit, an unattended commit, a model-requested confirmation and
resume, result verification, and clean session shutdown. P1 smoke tests
exercise one configured allow, deny, and ask rule plus a real process-hook
decision on each placement. Smoke actions use controlled local fixtures unless
the owner explicitly authorizes a real external mutation.

## Pull-request sequence

1. P0: full-access capability and decoupled confirmation semantics.
2. P1: declarative restricted policy and the external process hook.

P1 starts from merged P0. Both are non-documentation PRs and follow normal CI,
review, rocket approval, and merge requirements.
