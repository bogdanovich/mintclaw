# Browser Capability BF1 Press And Select Parity Admission

Status: admitted for implementation

This slice extends the first-party browser action contract with the two
remaining typed BF1 input primitives: document-scoped key presses and semantic
option selection. It reuses the existing broker, Playwright MCP worker,
companion command, approval, and no-replay boundaries. It does not expose raw
Playwright tools, selectors, coordinates, JavaScript, browser endpoints, or
host paths to the model.

## Admitted contract

`press` accepts exactly:

- `kind: press`;
- `target: document`; and
- one normalized key from `Enter`, `Space`, `Escape`, `Tab`, `Shift+Tab`, the
  four arrow keys, `Home`, `End`, `PageUp`, `PageDown`, `Backspace`, or
  `Delete`.

The explicit document target is broker-owned policy, not a selector. Arbitrary
characters and `Control`, `Alt`, or `Meta` chords are rejected before driver
dispatch, preventing browser-chrome and operating-system shortcuts. Trusted
code classifies a document key press as an unknown effect, so it requires an
exact one-time approval and remains denied in dry-run mode.

`select` accepts exactly:

- `kind: select`;
- a fresh broker-issued semantic element reference whose revalidated role is
  `combobox`; and
- one non-empty option identity bounded by the browser text-input ceiling.

The option identity is data for Playwright's typed `selectOption` operation;
it cannot become a selector, executable code, or a driver command. The broker
keeps it out of both gateway and companion durable invocation records. A
transport-only ephemeral envelope carries it for the initial dispatch, while
the durable plan contains only a domain-separated digest and byte length bound
to the session, tab, snapshot generation, origin, policy, profile, catalog,
semantic role, and accessible name. The companion verifies that binding before
ledger acceptance. A restart or redispatch without the transient value fails
closed. Invalid or disabled options fail through the same bounded action
outcome as other driver failures.

## Companion parity

The companion catalog may advertise `press` and `select` only when its local
profile admits them. The gateway intersects that catalog with its configured
target policy. The typed `browser.act.v1` schema then enforces:

- the exact document key allowlist for `press`;
- the semantic control reference and bounded option identity for `select`;
- `unknown` effect plus an exact action approval digest for `press`;
- `local_edit` effect and no approval digest for `select`; and
- the existing session, snapshot, origin, policy, profile, catalog, timeout,
  disconnect, and invocation no-replay bindings.

The companion browser host independently revalidates those invariants against
its current observation immediately before dispatch. It also compares a
host-private HMAC of the entire driver observation with the exact observation
that minted the current authority. Any same-origin reload, replacement, or
observable mutation fails stale before dispatch. A timeout, transport loss, or
ambiguous driver result quarantines the session and never replays an accepted
action.

## Acceptance evidence

Implementation is complete only when focused broker, schema, runtime, host,
real-driver, and production-WSS tests prove:

- allowed document keys execute only after exact approval;
- privileged or malformed chords never reach the driver;
- select resolves only a fresh semantic combobox reference;
- stale references, invalid options, disabled controls, navigation, dialogs,
  popups, timeout, close races, expiry, disconnect, and ambiguous results fail
  closed with bounded placement-equivalent outcomes;
- gateway and companion catalogs advertise the same admitted action shapes;
- reconnect cannot revive stale authority or replay an accepted action; and
- live gateway and companion canaries execute `press` and `select`, observe a
  fresh state, close, and immediately reuse the managed profile.
