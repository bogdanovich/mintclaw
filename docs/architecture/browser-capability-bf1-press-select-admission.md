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
its current observation immediately before dispatch. Every accepted
observation is bracketed by a private driver query for the Chromium main-frame
identity and a monotonic navigation generation. The private Playwright adapter
advances that generation for committed-document and same-document main-frame
navigation events; a transition during observation fails stale. The
host-private HMAC binds that driver-sourced navigation identity together with
the entire observation that minted the current authority. Therefore even a
same-origin replacement or same-document history transition that reproduces
byte-identical URL, title, snapshot, element, and dialog fields fails stale
before dispatch. Any other observable mutation also fails stale. A timeout,
transport loss, or ambiguous driver result quarantines the session and never
replays an accepted action. The raw driver identity and generation are not
exposed in tool output or persisted in either invocation ledger.

Gateway-local Playwright sessions use the same driver-owned identity: the
broker brackets the observation that mints snapshot authority, retains only
its opaque identity in the live worker slot, and routes click, select, press,
and scroll through the same final navigation check. Snapshot invalidation and
session cleanup erase that identity. A companion select returns its fresh
observation only in the initial transient response; the companion ledger
stores a bounded success receipt without the observation so page-rendered
option text cannot persist the ephemeral option identity. The gateway browser
ledger likewise stores only its existing bounded completion receipt.

The final action is issued through a private navigation-checked Playwright
callback. That callback refreshes the CDP main-frame state, compares the exact
expected frame, loader, and monotonic generation, and only then issues the
fixed typed action. The check is the authority linearization point: a
transition observed before it returns stale without issuing input. Playwright
and CDP native input primitives do not accept a document-generation
precondition, so the following native input is not atomic with the check. A
navigation beginning after the linearization point is a concurrent browser
event, not a condition this slice claims to reject before input. Successful
dispatch is followed by a fresh observation; timeout, transport loss, or an
ambiguous result quarantines the session and never replays the accepted
invocation. This boundary applies to click, select, press, and scroll.

## Acceptance evidence

Implementation is complete only when focused broker, schema, runtime, host,
real-driver, and production-WSS tests prove:

- allowed document keys execute only after exact approval;
- privileged or malformed chords never reach the driver;
- select resolves only a fresh semantic combobox reference;
- stale references, invalid options, disabled controls, navigation, dialogs,
  popups, timeout, close races, expiry, disconnect, and ambiguous results fail
  closed with bounded placement-equivalent outcomes;
- byte-identical observations from different main documents or different
  same-document navigation generations fail stale before either press or
  select reaches the driver;
- a navigation observed after host revalidation but before the private final
  check fails stale with zero input dispatch;
- tests do not claim atomicity between that final check and native input, and
  ambiguous concurrent outcomes quarantine without replay;
- gateway and companion catalogs advertise the same admitted action shapes;
- reconnect cannot revive stale authority or replay an accepted action; and
- live gateway and companion canaries execute `press` and `select`, observe a
  fresh state, close, and immediately reuse the managed profile.
