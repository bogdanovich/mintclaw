# Integrated Alias Browser Login

This joins the existing [private vault setup](credential-private-vault-setup.md),
[phone approval](credential-gateway-approval.md),
[browser transaction](credential-private-browser-login.md), and
[output boundary](credential-browser-output-privacy.md) in the gateway.
It adds no service, listener or password store.

## Admission and public contract

`browser_login` accepts an account alias, browser session/tab identity, current
snapshot identity/generation and username/password/submit references. Its schema
accepts no password, token, vault/item ID, principal, grant, URL, selector or
executable code. Caller identity comes from authenticated tool context. The
gateway derives owner/actor/agent/host and current policy revision, checks the
live browser origin and uses immutable configured outcome evidence.

Only the selected alias and a fixed outcome reach the model. Unexpected source
outcomes are replaced with a fixed denial. Retrieval remains lazy: an approved
lease and the browser's live form/document preparation precede SDK access.
Revocation, expiry, policy changes and browser retirement retain their existing
authority. A login grant approves no later purchase, post, message or account
change.

## Phone request and grant reuse

When no current matching signed grant exists, the controller issues a bounded
request for the alias and the existing outbound chat channel carries a generic
phone-approval link. Its destination comes from authenticated inbound context;
the model cannot select the recipient. The notice contains no login value or
provider binding. The link locates a request; the owner's user-verifying passkey
is still required to approve it.

The operation waits up to 45 seconds, bounded by its caller's context, without
holding a browser lock or credential value. Approval can complete the same tool
call. A timeout returns `approval_required`; a later invocation may reuse a
subsequently approved grant. A valid grant is reused across matching sessions
for its approved lifetime, at most 20 hours. Opening the link grants nothing.
Notification queuing is not evidence that an Android push was delivered.

## Rollout settings and limits

`credentials.browser_login_enabled` defaults to false, independently of phone
enrollment/approval setup. Enabling login requires the credential runtime and
an explicit stable `credentials.host_id` matching operator policy. Credential
configuration changes require restart. These are trusted execution-host labels,
not hardware attestation or a requirement for a separate machine.

This initial admission uses the direct local Playwright library, an ephemeral
profile and top-frame single-page form flow. Unsupported placements, child
frames and other login patterns deny before credential use. Facebook, eBay,
Craigslist, SSO, multi-step login, MFA continuation and companion placement each
need their own flow qualification before claiming support.

Keep real-account login disabled until the roadmap's full serialized-output
audit and protected token/physical Android/one-account pilot gates pass. The
synthetic Chromium fixture checks actual tool/gateway/driver and virtual
passkey integration, grant reuse, revocation, serialized tool outputs, browser
state and local logging. It does not establish a physical device, real site or
every provider/companion/artifact sink as qualified.
