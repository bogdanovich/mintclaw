# Login by live website discovery

The browser agent uses `browser_login` to find and use Login items in the
configured dedicated 1Password vault. A new website needs its URL saved on the
Login in 1Password, not an item UUID, site flow or success URL in MintClaw.

This implements the [replacement design](credential-discovery-redesign.md).
The trusted-runtime/output contract and qualified browser placement still apply:
gateway-local Playwright library, full-access ephemeral profile. Other browser
placements remain denied. Existing Android enrollment, token storage and HTTPS
approval route are reused.

## Configuration and matching

Configure one `credentials.accounts` rule with `discover_logins: true`, its
dedicated vault ID and allowed principals, as shown in the replacement design.
Do not supply its `item_id`, `origins`, `login` or connector fields. This rule
approves website-matched Login use in the vault; ordinary fixed rules can still
serve existing explicitly configured consumers. At most one discovery rule is
accepted by gateway configuration.

Matching uses the live top-level HTTPS snapshot origin, never a model-supplied
destination. The SDK lists active Login overviews and compares registrable
website domains with the Public Suffix List, including private suffixes. A
title filter may narrow domain matches. Names alone do not authorize sending
values to an unrelated website. IP-literal, insecure and non-public origins
are excluded from discovery. Full-item retrieval rechecks its website metadata
before any value reaches the private browser callback.

The gateway reuses one private SDK client. Initialization has a bounded
45-second allowance, separate from ten-second item/list calls: real metadata
qualification observed roughly 24 seconds for a cold SDK start. CLI discovery
uses the same separate initialization allowance instead of spending the list
deadline on authentication. This does not extend phone approval or grant times.

No inventory is cached. Newly added/renamed items appear in the next lookup
without restarting or editing configuration. Moving/deleting/archiving an item
prevents a later matching lookup; it does not itself revoke an already issued
grant or an authenticated cookie session. Use the existing phone revocation to
retire that access immediately.

## Browser operations

Every operation supplies the current `browser_session_id`, `tab_id`,
`snapshot_id` and `snapshot_generation`. Element parameters are opaque refs from
that observation. No operation accepts a password, code, seed, username, provider
binding, grant ID or caller principal.

| Operation | Additional input | Safe result |
| --- | --- | --- |
| `discover` | Optional `login_name` title filter | `found` with names, domains and `credential_id` references, or `not_found` |
| `credentials` | `username_ref`, `password_ref`, `submit_ref` | Fill and submit a combined form privately |
| `username` | `username_ref`, `submit_ref` | Private username step |
| `password` | `password_ref`, `submit_ref` | Private password step |
| `totp` | `otp_ref`, `submit_ref` | Private computed authenticator-code step |
| `verify` | `authenticated_ref` | Confirm a live visible Log out/Sign out control without retrieving or submitting values |

Value-using operations may supply the chosen `credential_id` returned by
discovery. Without it, exactly one website match is selected automatically;
multiple matches return `choice_required` and metadata without reading values
or requesting approval. No match returns `not_found`. Omit `account_alias` for
the generic path; it is retained for existing configured fixed consumers.

For example, navigate to the intended login page, observe it, discover the Login,
then submit its current form refs. The runtime prepares exact native elements,
checks the grant again after retrieval and fills/submits without an intervening
model turn. Each field is filled and its input/change events finish before
the next field is filled; live targets and the destination are rechecked in
between. This supports sites that clear a dependent password when the username
changes without rebinding replaced controls. Native submit forms require
same-origin HTTPS POST with no target override. JavaScript buttons are admitted
on the trusted same-origin document.
An initially disabled submit control may become enabled by the site's own input
validation. Private filling requires visible, writable inputs and valid native
targets/destination beforehand, then waits within the bounded private operation
for that same submit control to become enabled. It never removes `disabled` or
`aria-disabled`. Changed controls, hidden/disabled inputs, changed destinations
or a control that stays disabled prevent submission and retire the attempted
session; the operation is not automatically retried.
After submission, HTTPS redirects may stay within the matched registrable
website domain. The Go broker derives that domain using the Public Suffix List;
model arguments cannot widen it. An unrelated redirect retires the session.
Child-frame requests remain denied. If native form preparation finds missing
or detached controls, it returns `observe_required` before retrieving any
values or creating a private submission capsule. The agent must observe the
current page and retry preparation once with newly observed refs; repeated
readiness failure requires handoff. It must never privately rediscover a
replacement control by its label or reuse the stale refs. Revocation,
cancellation, destination/method/target policy failures and other denials do
not become permission to retry.

This recovery applies only before retrieval/injection. A failure after private
submission may have had effects and retains `outcome_unknown` plus session
retirement, with no automatic replay.

After a generic form submission, the result is `submitted`, not `logged_in`.
Observe the protected page again: it may show the next form, an error, a
challenge or an authenticated account. Separate username/password/TOTP steps
reuse the original protected session and selected item. Switching records
mid-session is denied. TOTP and verification cannot start an unprotected session.

`verify` produces `logged_in` and a runtime login receipt only when a fresh
native reference identifies a visible logout control with a supported label.
This generic evidence is sufficient under the trusted-site/model contract;
it is not a universal or adversarial authentication oracle. If a site exposes
no supported logout control, report the observed account evidence and the
verification limitation rather than inventing a confirmed receipt. The submit
or redirect alone never produces a login receipt. Explicitly close the browser
when the task ends. Unknown submission execution retires it and must not be
automatically replayed.

## Stored one-time codes

The `totp` step reads the same selected Login immediately before use. The
installed official 1Password SDK supplies the computed code through TOTP field
details. Only that code is lent to the private callback, not the field's seed
URI, notes or other item fields. Six to ten decimal digits are accepted,
including leading zeros. No code is cached between operations.

Missing, invalid or ambiguous TOTP fields return `handoff_required` without
filling the prepared field. SMS/email codes, CAPTCHA and approval on another
device also need an explicit user handoff; this feature does not fetch messages
or bypass challenges. Site-rejected credentials/codes require a fresh observation
and an honest report, not guesses or blind repeated submissions.

A user may explicitly choose to provide an SMS code in ordinary chat for an
approved ordinary browser fill. That code is then provider/history-visible input,
outside the private stored-code guarantee. This does not authorize requesting
vault passwords or service-account tokens in chat. The existing grant, origin,
session, challenge and action checks still apply.

The password, username, computed code and private cookies are registered for
output protection before injection or later observation. Cookie inventories
stay in the private envelope. Their known boolean literals (`0`, `1`, `true`,
`false`) are not registered as process-wide secret substrings: those public
constants would corrupt ordinary references and structural numbers. Other
cookie values, including numeric tokens, retain echo protection. This exception
does not apply to vault credentials or computed codes. Registered values remain
protected until browser cleanup acknowledges closure. Screenshots, unrestricted
browser code and downloads remain unavailable in credential-bearing sessions. Retrieval,
private transport, errors and ordinary model/durable output retain their existing
separation. This does not promise erasure of SDK/browser/string copies in RAM.

## Phone approval and evidence

The phone explicitly displays access to all website-matching Login items and
stored one-time codes in the dedicated vault for up to 20 hours. This is broader
than approving one fixed alias. Consumer leases narrow that authority to the
selected item and website domain for the session; adding an item or taking a
new step does not renew the grant. Original unexpired approval now survives
restart under the [durable approval contract](credential-durable-approval.md).
Existing revoke/expiry behavior is
preserved. Grant duration does not make ephemeral browser cookies persistent.

A browser login needing phone approval becomes a durable external wait with a
two-hour absolute deadline. The runtime closes the idle browser with native
cleanup evidence before waiting; no vault value is retrieved. After approval,
the existing recovery loop resumes the same task with a safe readiness result.
The model opens a fresh session, observes new controls and matches the Website
again. It does not replay the original call with stale form references. See
[phone approval waiting](credential-approval-wait.md) for lifetime separation,
original-caller binding, cancellation and lost-request behavior. Production
rollout of this replacement is recorded separately from source qualification.

Synthetic Chromium, virtual-passkey and real provider-adapter fixtures exercise
discovery, password, TOTP, fresh verification, grant reuse and revocation. Canary
values are checked against model requests, serialized tools, browser state,
canonical history, local logging, diagnostic traces and both export modes for
the qualified OpenAI-compatible and Codex paths. These tests use no real vault,
phone or account. The owner's Facebook pilot and deployment are separate gates;
passing a synthetic flow does not claim that Facebook has been logged into.

The [rollout evidence](credential-website-login-rollout.md) records the merged
deployments and separate real account pilots, including later failures.
