# Credential approval modes and login preflight

The operator clarified on 2026-10-25 that the model and host are trusted and
requested optional phone approval, disabled for the configured trusted agent.
The objective remains private credential use: values stay outside provider
requests, ordinary logs, diagnostic traces, transcripts and browser artifacts.

## Operator configuration

`credentials.approval_mode` accepts `phone` or `automatic`. Omission keeps
`phone`, preserving existing installations' approval requirements. Unknown
modes are rejected. This is trusted startup configuration, never a model tool
argument, per-call flag or phone-page setting. Apply changes through normal
gateway validation and restart.

```json
{
  "credentials": {
    "approval_mode": "automatic"
  }
}
```

This fragment modifies the existing configuration; it is not a replacement for
the dedicated vault, caller policy, private state directory or browser policy.
Only existing configured owner/actor/agent/host/account scopes are admitted.
Website matching and live origin/frame/field checks still apply. No password,
service token, TOTP seed or code is added to configuration or tool arguments.

`phone` uses the existing passkey ceremonies and
[durable original approval window](credential-durable-approval.md), at most
20 hours with expiry, revocation and policy-change retirement.

`automatic` creates bounded, runtime-only scoped leases under the operator's
explicit policy. No phone enrollment, notification or approval is required.
Active leases are reused, canceled on shutdown/policy change and never saved as
phone consent. Switching to automatic mode clears any durable phone grants;
switching back requires fresh phone approval. To stop automatic access, disable
credential access or select phone mode and restart. The phone's individual
grant-revocation workflow does not represent revocation of automatic policy.

The service-account token remains in the existing private runtime store and
its SDK client. Approval mode does not change the private value-consumption
path or known-value output guard. Read-only access to the dedicated 1Password
vault remains the scope boundary enforced by 1Password.

## Authorization before manual work

`browser_login discover` continues to return only website-matching Login
metadata. `browser_login authorize` selects a matching item and checks access
using the current admitted page identity. It takes no form fields, retrieves
no password/TOTP, performs no private browser submission and creates no login
receipt. No match, ambiguous matches and unknown item references remain safe
fixed outcomes without guessing.

In phone mode, preflight can suspend through the existing external wait; its
idle browser closes before the owner spends time solving a CAPTCHA. After
approval the agent opens a fresh browser. In automatic mode, preflight returns
`authorized` without a phone ceremony. The owner then resolves any challenge
in the browser that will continue through credential submission and fresh
verification. The authorization result alone is never proof of login.

The bundled browser workflow starts at the public HTTPS homepage and follows
observed sign-in links. HTTP success alone is not readiness. One bounded safe
GET retry or homepage recovery is permitted before manual help, honoring an
explicit image-first request. Credential submissions and unknown external
effects are not replayed. No website is added to Go-core navigation logic.

Public-only checks on the deployed Linux Chrome found that the eBay homepage
could open while both direct sign-in and the homepage's observed Sign in link
led to a Security Measure/CAPTCHA page with HTTP 200. These observations do not
guarantee future availability or certify a real account login; no credentials
were read or submitted by those checks.
