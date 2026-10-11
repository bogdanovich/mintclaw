# Manual Browser Evals: Fixture Witness

This guide describes an offline-tested fixture prerequisite for E4, not a qualified browser suite. The fixture is
not yet exposed by the eval CLI. No native browser, model, companion, or denied-action scenario is exercised by
these controls.

## Owned Local Fixture

Each trial creates a fresh UUID path, unpredictable nonce and reference code, and its own numeric loopback TCP
listener. Port zero is appropriate before provisioning an isolated profile; a fixed port is admitted only if it
is unused. The fixture never attaches to an existing HTTP service. DNS names, wildcard and external bindings are
refused. Admission errors do not include supplied identities, addresses, or private values.

The page has an accessible synthetic form, a CSV download, and two account sections with identically named
`Remove account` controls. These removals affect only in-memory synthetic fixture state, not a real account. The
form has no prefilled private answers. There are no external assets, scripts, cookies, redirects, or public sites.

POST requests require the exact host, path, origin and nonce, bounded URL-encoded UTF-8 fields, and a known action.
Unknown/duplicate fields, unknown accounts, extra URL/query forms, oversized bodies and malformed values are
refused without echoing inputs. A repeated removal returns conflict without recording a second removal.

## Independent Evidence

The harness can read a detached, private in-process snapshot of page visits, downloads, exact submitted values,
selected synthetic removals, closure, overflow and unexpected server failure. There is no HTTP oracle/admin
endpoint. These private fields are not JSON-exported and must never be placed directly in a report or model prompt.
The source CSV bytes are immutable and independently parsable; a caption or claimed successful download is not
an artifact witness.

Witnesses retain at most 256 reads and 64 mutations combined. Overflow is explicit and prevents a complete-
evidence claim. Successful fixture shutdown marks the fixture closed, stops its own HTTP server and releases its
listener. Late mutations are refused. Shutdown does not mean native browser resources have been closed.

Future E4 integration must bind each observation to native session, owner, execution, profile, prepared action,
approval and delivery evidence. Fixture state alone cannot prove that a browser (rather than another tool) did
the work, that an action was authorized, that a stale target was avoided, or that the response reached the user.
No-activity witnesses are meaningful only after admitted target work is quiescent, not after a WebSocket closes.

## Offline Controls

```sh
go test -race -tags goolm,stdjson ./internal/eval -run '^TestBrowserFixture' -count=1
```

The controls cover fresh authority, real local HTTP page/CSV/form access, exact server-observed values, scoped
ambiguous controls, malformed/foreign/oversized input refusal, snapshot isolation, bounded concurrent witnesses,
idempotent concurrent closure, late-action refusal and owned-port release. They use no model or native browser.

## Remaining Admission

The six first-release browser cases still need typed case contracts, native observations, approval binding,
artifact verification, raw privacy checks and owned cleanup. A pure read-only native browser state decoder is a
separate prerequisite; parsing state is not authority or page-freshness proof.

The native browser network policy does not treat a loopback fixture as a public `exact_origins` target. Integration
must explicitly admit an isolated configuration with deny-default restricted exact-origin/action approval, or
add a scoped native prerequisite. Do not silently widen SSRF/network policy, enable privileged execute, copy a
live browser profile, or treat this fixture's nonce as native approval.

Real qualification must record the actual platform, browser driver and gateway/companion placement, plus retained
independent state/artifact checks and cleanup results. These offline controls make none of those runtime claims.
