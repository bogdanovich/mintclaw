# Browser Capability BF1 Click Parity Admission

## Status And Decision

The second BF1 ordinary-interaction slice, **Shared Click**, is admitted as
Phase 1 of the
[Browser Functional Parity Execution Goal](browser-functional-parity-execution-goal.md).

The slice extends the existing first-party `browser_act` click contract to
companion-hosted sessions without creating a placement-specific tool. It also
admits an explicitly configured approved-action mode so an operator-controlled
profile can execute a click after the existing durable approval boundary.
Dry-run remains the default and fails closed.

No raw Playwright MCP tool, selector, coordinate, arbitrary page code, CDP
endpoint, generic node command, approval token, or retry flag becomes
model-visible.

## Current Gap

Gateway-hosted B1 sessions already prepare a click from a fresh semantic
element reference. The broker resolves the driver-local target, binds its role
and accessible name into an immutable prepared action, derives a conservative
effect, requires approval for `external_commit` and `unknown`, invalidates the
snapshot before dispatch, and never blindly replays an accepted invocation.

Companion-hosted sessions currently advertise and execute only `navigate` and
`scroll`. Their typed `browser.act.v1` union cannot carry a click, the
companion host cannot revalidate its semantic target, and the remote worker
rejects click preparation.

There is also a shared execution prerequisite. Enabled browser profiles are
currently required to use `dry_run=true`, while a click is conservatively
classified as `external_commit` for a button and `unknown` for every other
role. Dry-run correctly denies both effects. Merely adding `click` to the
companion schema would therefore advertise an action that can never complete
in the deployed configuration and would not satisfy the execution goal.

## Admitted Operator Outcomes

The slice admits two explicit profile modes on both placements:

- **dry-run** remains the default. Clicks may be prepared and may request
  approval, but `external_commit` and `unknown` clicks are denied before
  worker dispatch regardless of approval;
- **approved-action mode** is enabled only by an explicit operator setting on
  the exact target/profile. A click still dispatches only after the broker
  consumes an exact unexpired approval bound to the current prepared action.

Omitting the new setting, setting inconsistent mode fields, using a stale
profile or catalog, or presenting no valid approval fails closed. Enabling
approved-action mode does not enable raw browser tools, arbitrary actions,
additional origins, private-network access, credential access, or automatic
approval.

The deployed vertical slice uses an operator-controlled fixture with no real
external consequence. It proves one approved click separately on
`gateway/managed` and `companion/managed`, then restores the intended operator
profile configuration if the canary used a temporary exact-origin policy.

## Shared Model-Visible Contract

The existing tool input remains unchanged:

```json
{
  "browser_session_id": "opaque",
  "tab_id": "opaque",
  "snapshot_id": "opaque",
  "snapshot_generation": 7,
  "action": {
    "kind": "click",
    "ref": "opaque"
  }
}
```

The reference is a broker-issued identity from the most recent bounded
observation. It is not a Playwright ref, CSS/XPath selector, coordinate, DOM
path, or reusable locator. One call performs one ordinary left click; double
click, right or middle click, modifiers, force, position, timeout overrides,
and repeated clicks are not admitted.

The result preserves the existing first-party action envelope: stable
invocation identity, trusted effect, terminal state, safe error when present,
and a fresh bounded observation only when the terminal outcome is known and
the document can be observed safely. Popup and dialog detail remains bounded
by the existing B1 observation contract; multi-document selection belongs to
Phase 3.

## Freshness And Semantic Binding

Preparation and dispatch bind all existing authority:

- owner actor and browser specialist;
- target, placement, profile, and controller generation;
- session, tab, snapshot ID, and snapshot generation;
- current origin, policy revision, profile revision, and catalog revision;
- normalized click kind;
- resolved driver target, semantic role, and bounded accessible name;
- derived effect and dry-run or approved-action mode;
- prepared-action ID, canonical action hash, creation time, and expiry; and
- approval binding when the effect requires it.

The driver-local target remains private and live. It is not persisted in the
gateway prepared action or exposed to the model. The companion host mints a
session- and generation-scoped opaque wire reference and retains the raw
driver target only in memory. Only that opaque reference may cross the
authenticated companion wire or enter an invocation ledger; the raw target
must never appear in model-visible output, approval text, events, persisted
state, or diagnostic traces.

Immediately before acceptance, the gateway revalidates the prepared action
against its live worker slot. Immediately before driver dispatch, the
companion host re-observes the current document and proves that the private
target still exists with the same semantic role and accessible name at the
same origin and snapshot generation. Missing, detached, changed, duplicated,
or ambiguous targets fail stale before the accepted node-side dispatch
boundary.

## Effect, Dry-Run, And Approval Contract

This slice preserves the conservative B1 classification:

- a resolved `button` click is `external_commit`;
- every other click is `unknown` unless a later admission adds stronger
  driver-proven semantics; and
- page labels, accessible names, model text, skill instructions, and MCP
  annotations cannot lower the effect.

Both effects require MintClaw's existing durable human-interaction approval.
Approval is bound to the prepared-action ID, action hash, policy revision, and
expiry. Resume consumes that exact authority and revalidates the current
session, origin, document, element semantics, profile, catalog, and policy; it
never reconstructs or refreshes an expired action.

Approved-action mode is explicit and fail-closed:

- the existing `dry_run` value remains part of target/profile authority;
- a new positive operator setting is required before `dry_run=false` is
  accepted, so an omitted boolean cannot silently enable commits;
- gateway and companion configuration, discovery, open-session input, and
  runtime state must agree on the exact mode;
- dry-run rejects commit and unknown clicks before dispatch even if an
  approval exists; and
- approved-action mode never bypasses approval for commit or unknown effects.

The companion invocation carries a non-secret `approval_digest` only after the
gateway has consumed the exact approval. It is the SHA-256 digest of a
domain-separated canonical encoding of the complete normalized click input
except the digest itself, including session, tab, generation, opaque host reference,
expected semantics, origin, effect, action invocation ID, prepared-action
hash, and policy and profile revisions. The companion recomputes it and
rejects an absent, malformed, unexpected, or mismatched value. The digest does
not claim to protect the companion from its authenticated gateway; it lets the
companion enforce that the trusted gateway made an approval-gated dispatch
decision for exactly the input in the signed execution plan. The companion
does not receive a human approval token or independently broaden gateway
policy.

## Companion Wire Contract

The internal `browser.act.v1` action union adds exactly one click branch:

```json
{
  "kind": "click",
  "ref": "driver-private-target"
}
```

The invocation retains the existing session, tab, snapshot generation,
current origin, action invocation ID, prepared-action hash, browser policy
revision, profile revision, routed owner, timeout, and output bounds. It adds
the bounded expected semantic role and accessible name needed for node-local
revalidation and requires the approval attestation for `external_commit` or
`unknown` click effects.

The schema admits only effect values appropriate to the selected profile and
action. It must not use a broad effect enum that lets a caller label a click
as `read`, `navigation`, or `local_edit`. The companion runtime independently
checks the action kind, effect, exact profile mode, attestation presence,
semantic binding, and allowed-action list before dispatch.

The companion host then:

1. authenticates the existing routed owner and exact approved catalog;
2. verifies session, tab, generation, policy, profile, mode, and current
   origin;
3. resolves the ephemeral host reference, then re-observes and matches the
   private target, role, and accessible name;
4. durably reserves the stable action invocation ID immediately before the
   driver call;
5. performs one ordinary left click;
6. obtains one bounded post-action observation and advances the generation
   exactly once; and
7. returns the existing typed terminal result.

Failure before reservation is definitely unaccepted. Cancellation, timeout,
disconnect, driver loss, or missing terminal proof after reservation becomes
`unknown`, quarantines the session, and is never replayed with a new ID.

## Capability Discovery And Rolling Compatibility

`browser_targets` advertises `click` for a target only when every reported
enabled profile supports the shared click contract in the same passive runtime
generation. Companion discovery additionally requires:

- a connected approved node catalog generated by a click-capable companion;
- an exact profile revision whose `allowed_actions` contains `click`;
- a gateway adapter compatible with the click wire schema and approval
  attestation; and
- matching dry-run or approved-action mode authority.

Unavailable, stale, mixed-generation, unapproved, or old-schema companions do
not advertise click and cannot open a click-capable session. The action-limit
bound may increase only enough to carry the admitted action set.

Fresh catalogs use the new exact schema. Persisted pre-click catalogs remain
acceptable only through an exact historical schema matcher and only when they
do not claim click authority. A legacy schema paired with a profile that lists
`click` fails closed. There is no automatic catalog approval and no fallback
from companion to gateway.

## Required Validation

Implementation is complete only with all of this evidence:

- strict config tests prove dry-run is the default and approved-action mode
  requires its separate explicit positive setting on gateway and companion;
- action and descriptor schemas accept only the admitted click fields,
  effects, semantic binding, mode, and approval-attestation combinations;
- legacy catalog tests accept only the exact historical no-click schemas and
  reject old schemas that claim click;
- broker tests cover preparation, semantic binding, effect derivation,
  approval suspension/resume, expiry, dry-run denial, snapshot invalidation,
  and no blind replay;
- gateway adapter tests cover exact capability intersection, private target
  handling, attestation construction, and result validation;
- companion policy and host tests cover unsupported action, wrong mode,
  missing or mismatched attestation, stale generation, origin drift, semantic
  target drift, duplicate invocation, driver failure, timeout, quarantine,
  close, expiry, and cleanup;
- production-WSS tests exercise approved success, dry-run denial, failure
  before acceptance, disconnect before and after acceptance, terminal
  recovery, and no replay;
- race tests cover click against close, expiry, reconnect, policy change, and
  profile revision change;
- model-visible outputs, approvals, events, traces, ledgers, and persisted
  broker state contain no driver target, endpoint, raw MCP payload, approval
  token, cookie, storage state, credential, or unbounded page content; and
- equivalent real-browser canaries on gateway and companion complete
  `open -> observe -> click -> observe -> close -> reopen -> close`, followed
  by profile-lock and zero-orphan process checks.

The live matrix also proves that the same click is denied without approval,
denied under dry-run even after approval, and never dispatched twice after an
ambiguous accepted outcome.

## Dependency-Ordered Delivery

1. Merge this architecture-only admission.
2. Add the explicit approved-action profile mode and exact discovery/open
   authority without yet advertising companion click.
3. Extend the typed node action, effect, semantic-binding, attestation, and
   historical-schema contracts with focused protocol tests.
4. Add companion policy, host execution, reservation, recovery, and cleanup.
5. Add gateway capability intersection and prepared click dispatch over the
   production WSS path.
6. Update the version-controlled browser specialist guidance only if click
   selection or cleanup instructions require a first-party contract change.
7. Roll out compatible binaries, update and explicitly approve the exact
   companion catalog, then run the gateway and companion live matrix.
8. Record merged revisions, production versions, live results, privacy and
   cleanup evidence, and residual limits before completing Phase 1.

These may be separate focused pull requests if combining them would cross a
reviewable authority or lifecycle boundary. Dependent work starts only after
its prerequisite merges.

## Explicit Non-Goals

This slice does not admit:

- double, right, middle, modifier, coordinate, forced, repeated, or scripted
  clicks;
- model-supplied selectors, driver refs, effects, approval attestations, or
  retry controls;
- trusted classification of ordinary links as navigation without a later
  driver-proven destination contract;
- tabs, windows, popup selection, iframe selection, or cross-document
  authority from Phase 3;
- press, select, fill, dialog, check, uncheck, hover, drag-and-drop, or file
  chooser parity;
- new screenshot, upload, download, diagnostic, or large-snapshot behavior;
- credentials, attached-user profiles, raw MCP/CDP access, arbitrary page
  code, or unrestricted computer control; or
- weaker origin, network, artifact, approval, recovery, or cleanup policy.

## Mandatory Stop Conditions

Stop the implementation and revise the admission before proceeding if:

- gateway and companion require different model-visible click semantics;
- approved-action mode can become active through an omitted or false-default
  configuration value;
- a commit or unknown click can dispatch without exact consumed approval;
- node-local code cannot revalidate the semantic target immediately before
  dispatch;
- click authority requires persisting or exposing a driver target outside the
  private authenticated invocation;
- an accepted click can be blindly replayed after an ambiguous outcome;
- old catalog compatibility can accidentally grant click authority;
- the change requires raw MCP, selectors, coordinates, CDP, or arbitrary page
  code in the model contract; or
- live completion leaves a session, driver, browser process, artifact,
  prepared action, invocation, or profile lock behind on either placement.
