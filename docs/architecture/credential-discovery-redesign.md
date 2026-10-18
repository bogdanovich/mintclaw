# Website-discovered browser credentials

Status: implementation plan, 2026-10-18. This replaces the requirement to
preconfigure each browser account's item UUID, allowed origins and site-specific
success markers. The existing implementation remains in service until the new
path is reviewed, qualified and deployed.

## Product contract

The operator configures one dedicated 1Password vault and the authenticated
principals allowed to use it. Adding a Login with its website URL to that vault
must make it discoverable without editing MintClaw configuration or restarting
the gateway. No website, including Craigslist or Facebook, is hard-coded.

The browser agent opens the site's HTTPS login page, requests matching Login
metadata and uses the matching record. Match website registrable domains using
the Public Suffix List, including private suffixes; an unrelated lookalike does
not match. A name filter can disambiguate website matches, but name similarity
alone never sends a password to an unrelated website. Multiple matches expose
only record names, references and website domains so the agent can choose with
the owner when necessary. No match is a normal result, not permission to guess.

The existing Android passkey approves access to website-matching Logins in the
configured vault for at most 20 hours. The phone must state this vault-wide
scope explicitly. This deliberately replaces approval of an individually
preconfigured account. Adding or renaming items does not silently renew a grant.
Revocation, expiry, policy changes and restart invalidate grants and retire
authenticated browser sessions through the existing lifetime mechanism.

The trusted runtime resolves the chosen exact item again against the live
browser origin before retrieving values. A consumer lease narrows the approved
vault access to that item and its matched website domain for that browser
session. Metadata discovery grants no value access. The service account remains
read-only and restricted to the dedicated vault.

## Generic login and second factor

Use current observed element references rather than operator-authored selectors
or success URLs. Support combined username/password forms, separate username
and password steps, and a subsequent one-time-code form. The model chooses
ordinary navigation and non-secret field references. The runtime checks live
document, element and destination readiness before reading any value, then
fills and submits privately with no model turn between retrieval and use.

For TOTP, retrieve the current computed code from a TOTP field in the same
selected Login immediately before use. Neither the code nor its seed enters
model arguments/results or ordinary logs/traces/artifacts. Do not print an item
or use a model-facing `op item get`. Missing or ambiguous TOTP, SMS/email codes,
CAPTCHA, approval on another device and unsupported forms require an explicit
safe handoff; do not bypass them or repeatedly submit uncertain attempts.

A successful submission is not proof of authentication. Return a bounded
submission/continuation outcome and inspect a fresh protected observation to
establish the next step or authentication. Preserve runtime action receipts
without fabricating a confirmed-login receipt from a submit or a redirect.
Unknown execution still retires the browser and is not automatically replayed.

## Deployment and trust

Reuse the existing gateway, first-party browser, protected token file,
1Password SDK, Android enrollment, HTTPS approval route and FRP tunnel. Add no
host, listener, broker process or password database. Trust the model, runtime,
host and intended website. Protect values from providers and ordinary local
output; this is not a malicious-model or compromised-root isolation guarantee.

Existing fixed bindings may remain for explicit connectors or compatibility,
but are unnecessary for the new browser path. The optional GitHub connector
remains disabled. Existing private token and phone enrollment are preserved.

## Focused delivery sequence

| Step | Change | Required evidence |
| --- | --- | --- |
| D0 | Record the replacement design and roadmap | Documentation validation and merged plan |
| D1 | Live vault metadata discovery and explicit vault-scoped approval with item/domain-narrowed consumer leases | Domain/private-suffix matching; zero/one/many results; trusted caller enforcement; bounded safe provider failures; grant reuse/revocation and item-change regressions |
| D2 | Generic private browser steps, TOTP consumption and model tool integration | Real Chromium combined/multi-step/TOTP flows; stale refs and form changes; no fabricated login confirmation; revoke/expiry cleanup; full affected package tests |
| D3 | Qualify output boundaries, deploy merged code and migrate browser configuration | Canary values absent from qualified provider and local sinks; backups and health/smoke checks; token/enrollment unchanged; no per-site browser binding required |
| D4 | Owner's Facebook discovery and Android-approved login pilot | Find the moved Login by website without UUID setup; private password and TOTP use if the site requests it; fresh authentication evidence; close browser; report any external challenge honestly |

Keep each code PR a coherent unit. D1 is a prerequisite because the existing
approval policy accepts only a prebound item and exact configured origins.
D2 must connect actual discovery to private browser use, not stop at a search
command. New privacy sinks/providers remain denied until qualified. Real login
starts in the owner's normal authenticated MintClaw chat; operator CLI identity
must not be substituted. Phone approval and external website challenges may
require the owner's participation; implementation and review proceed first.

## Related evidence

The [previous roadmap](credential-access-roadmap.md) records the delivered
fixed-binding implementation. Its [output qualification](credential-output-qualification.md)
and [personal pilot](credential-craigslist-pilot.md) remain historical evidence;
they do not automatically qualify the new discovery, generic forms or TOTP.
