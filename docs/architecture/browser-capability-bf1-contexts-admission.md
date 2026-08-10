# Browser Capability BF1 Tabs, Frames, And Popups Admission

Status: admitted for implementation

This slice adds bounded browser document-context discovery and selection to
the shared first-party browser contract. It lets an agent work with multiple
tabs, action-created popups, and nested frames on gateway and companion
targets without exposing Playwright page objects, frame handles, page indexes,
selectors, CDP target IDs, driver endpoints, or arbitrary page code.

The slice extends the existing broker and private Playwright adapter. It does
not add another browser engine, raw MCP forwarding, coordinate input, form
value transport, artifacts, or privileged execution. Protected fill and the
remaining ordinary interactions stay in later phases of the
[Browser Functional Parity Execution Goal](browser-functional-parity-execution-goal.md).

## Admitted model contract

Add one typed first-party tool, `browser_contexts`, with exactly four
operations:

- `list` returns the current bounded context catalog for one owned session;
- `open` creates one new `about:blank` tab and selects it;
- `select` selects one fresh catalog-issued tab and optional frame and returns
  a fresh observation from that context; and
- `close` closes one fresh catalog-issued tab and returns the resulting
  catalog and deterministic selected context.

Every request includes `browser_session_id`. `select` and `close` also include
`context_catalog_id`, `context_generation`, and `tab_id`; `select` may include
one `frame_id`. The model cannot provide a tab index, window name, URL match,
frame name, frame path, selector, driver identifier, or fallback rule.

`browser_observe` and `browser_act` gain an optional `frame_id` and continue to
require explicit `tab_id`, snapshot identity, and snapshot generation. The
requested tab and frame must equal the session's selected context. Element
references are minted only for that selected context and bind the context
catalog generation in addition to their existing owner, session, profile,
controller, runtime, origin, policy, document, snapshot, and catalog
authority. There is no implicit fallback to the primary tab, main frame,
newest popup, or another same-origin context.

`browser_targets` advertises context support and limits only when the selected
target and profile support the complete admitted contract. The capability
view includes booleans for tabs, popups, and frames plus the effective tab,
frame, nesting-depth, and returned-metadata ceilings. Partial support is
omitted rather than discovered by a failed action.

## Context identities and bounded metadata

The broker owns every model-visible identity:

- `tab_id` identifies one page for the lifetime of that page;
- `frame_id` identifies one attached frame within one tab document;
- `context_catalog_id` identifies one catalog snapshot; and
- `context_generation` advances for every relevant page, popup, frame,
  selection, navigation, close, attach, detach, or runtime transition.

Raw Playwright, browser, CDP, and node identifiers remain private runtime
state. Opaque identities are scoped to one actor, agent, session, profile,
target, controller generation, worker runtime generation, policy revision,
and effective command catalog. They cannot be reused across sessions,
placements, reconnect generations, or ownership transfers.

The catalog returns deterministic bounded metadata:

- tabs are ordered by broker creation sequence, with the initial tab first;
- frames are ordered in bounded parent-before-child attachment order;
- each entry reports only its opaque identity, kind, selected state, opener
  relationship when known, bounded URL and origin, bounded title or frame
  label, document generation, and availability state; and
- omitted entries and metadata are represented by explicit counts and
  `truncated: true`, never by unbounded continuation controlled by the model.

The implementation starts with hard ceilings of 16 live tabs, 64 frames per
tab, frame depth 8, 2,048 URL bytes, 512 title or label bytes, and 64 KiB for
one serialized catalog. Configuration may reduce but not increase those
ceilings in this slice. Reaching a creation ceiling prevents the new context
without evicting an existing one.

A tab identity survives navigation, but its document generation advances and
all frame, snapshot, and element authority beneath the old document becomes
stale. Frame identities do not survive detach, parent navigation, tab close,
worker replacement, or any attempt to reconstruct a context from URL, name,
position, or markup similarity.

## Selection and observation

`select` is a broker state transition, not a driver-handle lookup delegated to
the model. Trusted code resolves the opaque IDs against the current live
context graph, verifies ownership and effective policy, asks the private
driver to prove the same page, frame, and document generations, records the
new selection, invalidates prior snapshot authority, and returns a fresh
observation.

The main frame has no model-supplied alias. Selecting a tab without
`frame_id` selects its broker-known main frame. Selecting a child frame
requires an ID from the same fresh catalog. A nested-frame observation returns
only references owned by that frame. A reference from a parent, sibling,
detached frame, old document, or old catalog fails stale before driver input.

Each frame origin is revalidated through the session network policy when it is
selected and immediately before observation or action. A frame that cannot be
selected safely remains visible only as bounded unavailable metadata with a
safe reason. It does not make another context selectable, leak driver errors,
or silently broaden exact-origin, public-web, or any-HTTP policy.

Selection has read effect and does not require human approval. `open` creates
only `about:blank`, has local context effect, and does not navigate. `close`
has unknown effect because it may discard unsent page state or trigger a
before-unload dialog, so it requires the existing exact one-time approval and
is denied in dry-run mode. Closing the final tab is rejected; callers use
`browser_session close` to end the session. After another tab closes, the
selected context becomes its live opener when available, otherwise the
oldest remaining tab, always reported explicitly.

## Popup correlation and action outcomes

The private driver establishes a popup watch for the exact initiating page
inside the accepted action boundary. A popup can be returned as that action's
result only when the driver proves it was emitted by the initiating page after
the action watch began and before the bounded action result settled. The
broker then mints the popup `tab_id`, records its opener and originating
invocation, advances the context generation, and returns a fresh bounded
catalog view.

An unrelated page created concurrently may be admitted to the session catalog
when policy and limits allow it, but it is never reported as the initiating
action's popup. Multiple correlated popups, a popup after the result deadline,
or a transport loss before correlation becomes durable produces an explicit
ambiguous or bounded-multiple-popup outcome. The accepted action is never
replayed to rediscover a popup.

Click and later typed actions use explicit outcomes for:

- no context change;
- same-tab document or same-document navigation;
- one correlated popup;
- dialog before or during popup creation;
- initiating tab close;
- policy-blocked popup;
- multiple or ambiguous popup creation; and
- timeout, disconnect, or worker loss.

The popup's destination is checked with the same network and origin policy as
ordinary navigation. A denied popup is closed when that can be proved safe;
otherwise the affected session is quarantined. No denied popup becomes
selectable.

## Gateway and companion implementation boundary

The broker gains a durable bounded context projection and a live private map
from opaque IDs to driver contexts. The gateway Playwright worker and the
companion browser host implement the same internal context operations. The
existing node browser commands remain the transport boundary; their schemas
gain the exact bounded context fields and results, and the catalog hash change
requires explicit command-surface renewal before companion context support is
advertised.

Driver discovery uses Playwright page, popup, and frame lifecycle events plus
the existing private navigation-generation checks. The implementation may use
the pinned Playwright MCP adapter or its private unsafe-code seam to maintain
those maps, but raw output is parsed and validated inside the worker. It never
crosses the model contract or becomes durable authority.

The gateway and node host independently validate:

- owner, session, profile, controller, runtime, policy, and catalog bindings;
- current catalog, tab, frame, document, and snapshot generations;
- tab and frame membership and current attachment;
- network and origin policy for the selected context;
- popup correlation for an action result; and
- all count, depth, string, response-size, and deadline limits.

The companion ledger retains only opaque IDs, generation bindings, operation
digests, bounded terminal receipts, and safe failure codes. It does not retain
raw handles, full snapshots, page content, selectors, cookies, storage,
credentials, response bodies, or driver diagnostics. A successful observation
is returned only in the transient response, following the existing browser
observation boundary.

## Lifecycle, concurrency, and recovery

All context mutations serialize with action, observation, handoff, expiry,
and session close under the existing session authority. The implementation
uses stable invocation IDs for tab open and close and for any action that may
create, navigate, or close a context.

After acceptance:

- a stored terminal result is returned without re-execution;
- an accepted operation without a provable terminal result becomes unknown;
- reconnect never retries an open, close, click, or popup-producing action;
- no context is rebound by index, URL, origin, name, title, or apparent page
  equality; and
- session close owns cleanup for every tab, popup, frame, watcher, and private
  driver mapping created by the session.

A companion reconnect may preserve context authority only when both sides
prove the same node identity, approved catalog, browser worker runtime,
session, context graph, and terminal invocation ledger. Otherwise the session
becomes lost and every prior context ID is stale. Gateway restart continues to
make its in-process worker session lost. Close, expiry, disconnect, handoff,
controller change, and operator revocation invalidate pending watchers and all
affected authority.

Concurrent close, navigation, detach, selection, popup, and action completion
have one durable winner. Losing callers return stale, closed, detached,
unknown, or conflict as appropriate; they never continue against a different
context.

## Safe errors

Gateway and companion return the same bounded safe codes for malformed,
unavailable, denied, stale, detached, closed, ambiguous, limit, timeout,
disconnect, and incompatible-driver outcomes. Errors contain opaque IDs only
when they are already authorized for the caller. They do not contain raw page
or frame handles, indexes, selectors, transport endpoints, stack traces,
profile paths, page content, or private policy values.

At minimum the implementation distinguishes:

- `context_unsupported`;
- `context_catalog_stale`;
- `tab_not_found`, `tab_closed`, and `tab_limit_reached`;
- `frame_not_found`, `frame_detached`, `frame_depth_exceeded`, and
  `frame_policy_denied`;
- `popup_policy_denied`, `popup_multiple`, and `popup_ambiguous`; and
- the existing `driver_incompatible`, `worker_lost`, `timeout`, `unknown`,
  and `session_quarantined` outcomes.

## Delivery sequence

Implementation should remain reviewable in dependency order:

1. add broker context types, identity and generation rules, durable store
   validation, capability projection, and fake-worker contract tests;
2. add gateway Playwright discovery, selection, popup correlation, frame-bound
   observation, context open and close, and real-driver tests;
3. add companion schemas and host behavior, node catalog gating, ledger and
   reconnect rules, production-WSS tests, and parity tests; and
4. deploy the exact merge, renew only the intended companion command surface,
   and run equivalent live canaries and cleanup audits.

These may be separate implementation pull requests, but no later phase may
depend on Phase 3 until all four steps are merged, deployed, and validated.

## Acceptance evidence

Phase 3 is complete only when focused unit, schema, store, lifecycle, race,
real-driver, and production-WSS tests plus deployed canaries prove:

- deterministic bounded discovery of multiple tabs, one correlated popup,
  nested frames, and unavailable policy-denied frames;
- opaque identity isolation across owners, sessions, profiles, placements,
  runtime generations, controller generations, policies, and catalogs;
- fresh selection and observation of a secondary tab or popup and a nested
  frame with no raw selector or driver handle;
- old tab, frame, context catalog, snapshot, and element authority fails stale
  after navigation, attach, detach, close, reconnect, handoff, expiry, and
  worker replacement;
- an unrelated concurrent page cannot be claimed as an action popup;
- popup, navigation, dialog, close, timeout, disconnect, multiple-popup, and
  ambiguous outcomes are explicit and no accepted operation is blindly
  replayed;
- context and response ceilings fail closed without eviction, unbounded
  memory, durable ledger growth, or oversized WSS results;
- gateway and companion advertise and enforce equivalent supported shapes,
  effects, approval rules, freshness, terminal receipts, and safe errors;
- session close and failure cleanup remove every phase-owned page, popup,
  frame watcher, lock, and process while preserving unrelated sessions; and
- fresh live gateway and companion workflows create or discover and select a
  secondary tab or correlated popup, select and observe a nested frame, close
  all owned contexts, close the session, and immediately reuse the managed
  profile.

Stop implementation and return to architecture if Playwright cannot provide
stable page/frame lifecycle evidence, the worker cannot correlate a popup to
one accepted action, raw handles must cross the node transport, a frame must
be selected by model-provided selector or index, reconnect requires heuristic
rebinding, or a bounded catalog cannot fit within production WSS limits.
