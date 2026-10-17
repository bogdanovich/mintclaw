# Credential Browser Output Boundary

This extends the existing [private browser transaction](credential-private-browser-login.md)
before model tool activation. It uses the existing driver connection and browser
session owner; it creates no service or additional process.

## Private response envelope

After a credential transaction, ordinary driver operations use a private
envelope containing their result and authentication-cookie values. The Go
client admits cookie output protection before returning the result to the
trusted worker parser. Cookie values are never added to model-facing browser
results. Unwrapped authenticated observations deny at the driver boundary.

The driver collects current cookie-jar values and values from observed
`Set-Cookie` responses, including rotations. Earlier values remain protected
until acknowledged worker cleanup. Registrations deduplicate repeated values
and belong to the session, not each individual action. Each session admits at
most 128 distinct cookie values of at most 4096 bytes; global registry limits
also apply. Capacity, collection, malformed-envelope or opaque-output failure
makes the worker unavailable rather than releasing an unguarded response.
Closed sessions reject late registration. Ordinary close/shutdown retains the
existing process-cleanup authority before releasing output guards.

The direct browser driver uses the existing private subprocess transport.
Its stderr is drained to discard before logger/trace fanout, including startup
and shutdown; diagnostic or dependency output cannot become a local log stream.

This is a bounded accidental-echo guard under the approved trusted-site/runtime
model, not a promise about arbitrary adversarial transformations. JavaScript
cookie values that remain in the jar are collected; full companion and site
flow qualification remain necessary before real credentials are activated.

## Safe browser projections

Authenticated snapshots retain native Playwright semantics and exact references.
They omit text and child values from input, textarea, select and editable
controls, and use explicit labels rather than value-derived names for these
controls. This keeps manual OTP and recovery inputs out of observations while
retaining labels/references. Unavailable sources withhold control text. Registered-value projection still
guards ordinary echoes in URLs, page text, titles and dialog messages.

Context catalogs project registered values from tab/frame URLs and labels
before normalization and persistence. Refresh comparisons use the same
projection; private driver handles, identities and generations are not changed.
Context operations also recheck the live credential lease.

Authenticated diagnostic summaries expose category/count metadata and omit
entries, paths and message hashes. Screenshot and unrestricted execution remain
denied; download preparation/dispatch is now denied too, since opaque artifacts
have no qualified secret-value projection.

## Evidence and rollout

Actual Chromium JSON-lines fixtures cover login, private cookie handoff,
authenticated envelope admission, cookie rotation with earlier-value retention,
and OTP suppression. Go race tests cover registration before returning an
ordinary result, deduplication/lifetime, closed-session denial, malformed/opaque
output denial, catalog persistence projection and download denial. The full
normal browser package suite, formatting and lint validate existing behavior.

This does not activate a login tool, load a real token, qualify physical Android
approval or establish every provider/log/trace/persistence/artifact/companion
sink as complete. Alias-only tool/notification integration, one scoped
connector, site/placement support and the serialized-output pilot audit remain
required. No real credentials are used by these fixtures.
