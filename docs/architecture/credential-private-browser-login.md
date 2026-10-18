# Private Browser Credential Login

This document describes the original fixed-flow transaction. The
[website-discovered login extension](credential-website-login.md) adds generic
form steps, TOTP and fresh verification while reusing its private transport,
output protection and grant lifetime. It does not require per-site flow setup.

This is the browser transaction portion of the
[trusted-runtime design](credential-trusted-runtime-design.md). It extends the
existing browser broker and direct Playwright library driver; it creates no
credential listener, Root helper, VM, or separate browser service.

`Broker.LoginCredential` receives trusted caller identity, a live scoped grant,
an account alias, current snapshot references and an exact-item private vault
consumer. Model-facing tools must supply only the alias and current browser
references. They cannot supply credentials, a principal, grant authority,
provider binding, policy revision, or outcome evidence.

## Admission and transaction

The broker checks the actor/agent against existing opaque browser ownership,
requires the current snapshot generation, and validates the original credential
grant. This initial implementation admits direct-library workers in ephemeral
profiles with full-access capability. It denies attached/managed profiles,
child-frame forms, restricted-policy profiles, and remote workers without the
private credential transport. Those placements need explicit privacy/effect
admission before they can use this operation; it does not bypass their policy.

Under the existing broker lock, the driver first classifies the live username,
concealed password and submit references in one same-origin HTTPS POST form.
The private capsule retains the actual document and elements. It produces no
snapshot or page text. Retrieval occurs only after this readiness check, and
the runtime rechecks the grant after retrieval and immediately before injection.

The private driver fills and submits in one operation with no model turn in
between. It rechecks the document, targets and form after input listeners run.
Secret-bearing requests use the existing private JSON-lines channel, outside
ordinary tool arguments, prepared actions and durable invocation records.
No credential operation appears in the model's driver catalogue. While a
capsule is pending, ordinary driver observations/actions are denied; dialogs
are dismissed without publishing their message.

The driver returns a fixed outcome. It reports `logged_in` only when the
operator's configured success URL and marker establish authentication. A
configured MFA marker yields `handoff_required`. Unproven or canceled injection
returns `outcome_unknown`, closes/quarantines the context and retires its broker
session. The operation never retries a possibly executed submission.

## Lifetime and output

The runtime registers username/password values before injection and registers
private cookie values before allowing later observations. The
[output boundary](credential-browser-output-privacy.md) harvests cookie rotations
and registers values before ordinary driver results return. Cookie values remain
inside a private parser/result with safe generic formatting; only an alias and
fixed outcome are returned from the broker API. Output registrations belong to
the browser session and are retained until worker cleanup acknowledges closure.

Later actions and observed origins recheck the original live grant. Expiry,
revocation, authority replacement and shutdown invalidate that authority; a
watcher closes the authenticated browser session. Closing a session stops its
watcher and releases output registrations. Cleanup failures retain the guard
and keep the worker unavailable.

Browser observations project registered values from page URLs, titles,
snapshots, element labels and dialog text. Private/public freshness hashes use
the same projection. Credential-bearing sessions deny screenshot capture and
unrestricted browser code execution because text replacement cannot qualify
opaque artifacts or arbitrary browser APIs.

## Evidence and remaining integration

Synthetic Chromium tests cover one private form submission, stale document
rejection, absence of a success marker, context cleanup, GET-form and submitter
override denial. A separate-process test runs the production JSON-lines loop
and driver against synthetic HTTPS routes, exercising prepare/submit/discard,
password-bearing dialog suppression, private cookie handoff and fixed parse
errors. Invalid-input and expired-request regressions verify capsule disposal.
Broker race regressions cover alias-only results, guard retention until close,
actor mismatch before retrieval and revocation during retrieval/after login.
Existing browser behavior is also exercised by the package suite.

This change does not expose a login tool, read a real vault token, initialize
the SDK, send notifications or qualify all companion/artifact/persistence
outputs. The later runtime coordinator must connect reviewed phone requests,
exact-item vault bindings, private token setup and the model-facing browser
operation. Site-specific outcome configuration, additional login forms and
placements, complete sink qualification, physical
Android approval and one personal login pilot remain required before rollout.
