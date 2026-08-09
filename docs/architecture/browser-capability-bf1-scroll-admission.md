# Browser Capability BF1 Scroll Parity Admission

## Status And Decision

The first BF1 ordinary-interaction parity slice, **Shared Scroll**, is admitted
as the dependency-ordered change in this document.

The slice extends the existing first-party `browser_act` contract so one
bounded semantic scroll works with the same authority and result semantics on
both gateway-hosted and companion-hosted sessions. It also makes
`browser_targets` report the action set actually supported by each target
instead of advertising gateway-only actions on a companion.

No new model-visible tool, selector language, coordinate input, raw MCP call,
CDP endpoint, or arbitrary page code is admitted.

## Why Scroll Is First

The deployed B3 companion slice proves only
`open -> observe -> navigate -> observe -> close`. Gateway-hosted sessions
already support the ordinary B1 actions, but companion discovery currently
does not distinguish that narrower implementation.

Scroll is the smallest useful BF1 action that exercises a non-navigation
driver dispatch without adding a new approval, artifact, or secret-retention
boundary:

- its effect is `read`;
- its arguments are a bounded direction and amount;
- it does not use a model-provided selector or element reference;
- it does not carry form text through the durable node invocation ledger; and
- it can be proven against one deterministic document on both placements.

`fill` is intentionally not included in this slice. B1 keeps form values out
of persistent broker records, while the current node invocation contract
durably retains command input. A later fill-parity admission must define an
ephemeral or protected value-delivery contract before form text crosses the
companion boundary.

## Admitted Operator Workflow

The vertical slice is one dry-run document interaction performed separately
on `gateway/managed` and `companion/managed`:

1. the browser specialist calls `browser_targets` and confirms that the
   selected target advertises `navigate` and `scroll`;
2. it opens one broker-owned managed session;
3. it observes `about:blank` and navigates to an operator-controlled HTTP(S)
   document with enough content to scroll;
4. it observes a fresh snapshot;
5. it performs one downward scroll with amount `1`;
6. it observes the resulting fresh snapshot and unchanged origin; and
7. it closes the session and proves that the same profile can open again.

The two runs use the same model-visible tool arguments and safe-result shape.
Placement changes only the private worker transport.

## Shared First-Party Contract

The existing `browser_act` input admits this additional companion action:

```json
{
  "browser_session_id": "opaque",
  "tab_id": "opaque",
  "snapshot_id": "opaque",
  "snapshot_generation": 4,
  "action": {
    "kind": "scroll",
    "direction": "down",
    "amount": 1
  }
}
```

The contract is fixed as follows:

- `direction` is exactly `up` or `down`;
- `amount` is an integer from `1` through `5`;
- one call performs one scroll;
- the gateway derives the `read` effect; the model cannot provide or broaden
  it;
- scroll requires a ready agent-controlled session and a fresh tab, snapshot,
  generation, origin, policy revision, and catalog revision;
- scroll does not require approval and remains available in dry-run mode;
- success invalidates the prior snapshot and returns or permits only a fresh
  observation; and
- malformed, stale, unsupported, disconnected, terminal, and ambiguous
  outcomes use the existing bounded browser safe errors.

The first-party broker remains the sole model-facing authority. Playwright MCP
remains a private driver implementation behind the worker seam.

## Companion Wire Contract

The internal `browser.act.v1` action union may add one `scroll` branch with
only `kind`, `direction`, and `amount`. Its bound effect is exactly `read`, its
approval digest is absent, and its existing session, tab, snapshot generation,
current origin, action invocation ID, prepared-action hash, browser policy
revision, and profile revision remain required.

Before driver dispatch, the companion must:

1. authenticate the existing routed owner;
2. verify that the selected local profile explicitly allows `scroll`;
3. verify the session, tab, generation, policy, and profile revisions;
4. re-observe and match the gateway-bound current origin without advancing
   gateway snapshot authority; and
5. durably reserve the stable action invocation ID immediately before the
   driver call.

After dispatch, the companion observes the resulting document, increments the
snapshot generation exactly once, and returns the bounded observation. An
accepted action whose terminal result cannot be proved becomes `unknown`, is
never replayed under a new ID, and triggers the deployed session quarantine
and profile cleanup behavior.

## Capability Discovery

`browser_targets` must fail capabilities closed and report target-specific
actions from one passive runtime generation:

- a gateway target advertises the ordinary actions implemented by its local
  worker;
- a companion target advertises only actions present in the approved remote
  profile catalog and supported by the gateway adapter;
- a target with multiple enabled profiles reports only actions supported by
  every reported profile;
- unavailable, disconnected, mismatched, or unapproved companion authority
  does not advertise actions; and
- upload, download, screenshots, headed view, and handoff remain governed by
  their existing independent feature diagnostics.

Opening a session must revalidate the same authority. Discovery is not an
authorization token and cannot make a stale catalog executable.

## Profile And Catalog Rollout

The companion profile may add `scroll` only after the new binary is active.
The rollout order is:

1. merge and install compatible gateway and companion binaries;
2. add `scroll` to the local profile's `allowed_actions` and increment its
   profile revision;
3. reconnect the companion and inspect the new catalog;
4. explicitly approve that exact catalog revision;
5. verify passive discovery before opening a browser; and
6. run the same scroll canary on gateway and companion targets.

There is no automatic catalog approval and no fallback from companion to
gateway when the companion action is absent.

## Required Validation

Implementation is complete only with all of this evidence:

- strict schema and normalization tests accept only the admitted scroll shape;
- profile descriptors reject unlisted, duplicate, or excessive action sets;
- broker tests preserve fresh snapshot, origin, effect, dry-run, and
  no-blind-replay invariants;
- target discovery tests prove exact gateway and companion action lists and
  fail closed on unavailable diagnostics;
- companion-host tests cover success, stale generation, policy mismatch,
  unsupported profile action, driver failure, timeout, and cleanup;
- the production WSS integration test performs
  `open -> observe -> navigate -> observe -> scroll -> observe -> close` and
  preserves reconnect behavior before acceptance and `unknown` behavior after
  acceptance;
- race tests cover action, close, expiry, and disconnect concurrency; and
- live real-browser canaries complete through only first-party tools on both
  deployed targets, followed by immediate profile reuse and a zero-orphan
  process check.

Model-visible output, events, traces, and persistent records must not expose
driver arguments, endpoints, profile paths, cookies, storage state, raw MCP
payloads, or unbounded page content.

## Explicit Non-Goals

This slice does not admit:

- click, fill, select, press, dialog, hover, check, drag, or file chooser
  parity on the companion;
- tabs, windows, popups, frames, or document selection;
- screenshots, uploads, downloads, PDF, trace, HAR, video, or console parity;
- arbitrary Playwright code, raw MCP tools, CDP, selectors, or coordinates;
- attached-user profiles, credentials, or non-dry-run external commits; or
- managed Playwright runtime distribution.

Those remain later BF1, BF2, BF3, B4, or deferred BF4 work.

## Mandatory Stop Conditions

Stop implementation if:

- gateway and companion require different model-visible scroll semantics;
- capability discovery cannot omit unsupported companion actions;
- scroll cannot remain bound to a fresh document and exact current origin;
- an accepted scroll can be blindly replayed after an ambiguous outcome;
- the change requires raw MCP, CDP, selectors, coordinates, or arbitrary code
  in the model contract; or
- live completion leaves a session, driver, browser process, or profile lock
  behind on either placement.
