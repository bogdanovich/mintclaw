# Durable credential approval and browser continuity

The operator requested this revision on 2026-10-25 after the eBay human
handoff pilot. The trusted runtime and host remain the security boundary;
passwords, TOTP values and seeds remain in 1Password and outside model output,
ordinary logs and diagnostic traces.

## Delivery plan

1. Persist verified phone grants in the existing private owner store. Restore
   only the original scope and remaining lifetime after a gateway restart.
   Qualify expiry, revocation, policy changes, storage failures and private
   output. This is the first implementation change.
2. Restore durable image delivery during an agent continuation after phone
   approval. Exercise screenshot delivery after an external wait, in addition
   to the already qualified initial delegated screenshot.
3. Acquire account authorization before manual browser challenges. Then use
   one browser for the owner's challenge and subsequent login. For blocked
   entry pages, prefer a working public entry page and its observed sign-in
   link; allow bounded safe navigation retries without replaying an uncertain
   login submission. Qualify the combined lifecycle and deploy merged changes.

Continuation image delivery is implemented in
[PR #1879](https://github.com/bogdanovich/mintclaw/pull/1879). The preflight and
the operator's subsequent choice of optional approval are described in
[approval modes](credential-approval-modes.md). Deployment and an owner pilot
remain separate evidence; a real eBay login is not certified by synthetic tests.

## Durable grant contract

The existing `owner-key.json` transaction now also contains bounded approval
metadata: random grant reference, authenticated owner/actor/agent/host,
approved aliases, policy revision, issue time and absolute expiry. This stores
no password, token, TOTP, browser cookies, pending phone link or passkey private
key. The existing directory mode `0700`, file mode `0600`, owner/ACL checks,
exclusive lock, no-follow open, atomic rename and file/directory sync apply.
Protection comes from this private trusted-host store; an unkeyed hash would
not authenticate an editable approval, and encryption with a nearby key would
not establish an additional host trust boundary.

Phone approval is a consent checkpoint for the supported credential path. It
does not cryptographically unlock the service-account token: the gateway loads
that token into its private SDK client at startup, before a phone grant exists.
The WebAuthn assertion authenticates the owner's approval to MintClaw;
1Password's service-account authentication uses the service token independently.
See the [1Password service-account security model](https://www.1password.dev/service-accounts/security)
and [WebAuthn assertion verification](https://www.w3.org/TR/webauthn-3/#sctn-verifying-assertion).

An agent with arbitrary same-user filesystem/shell access or administration
over this runtime could bypass the approval controller rather than forge a
passkey signature. Private file modes protect other unprivileged OS users;
they do not separate an agent running as the credential owner. A hash, or
encryption with a key readable by that same principal, does not establish that
separation. Keeping secret values out of providers and logs depends on the
private consumption/output path under the explicitly trusted-model contract.
Protecting against a deliberately bypassing agent requires an independently
enforced access boundary for the token and credential runtime, including no
agent administration over that boundary. This PR adds no such isolation.

Only a verified passkey assertion creates a new grant, for at most 20 hours.
Use and restart never renew it. Startup validates the entire matching-policy
snapshot before installing any grants, discards expired or future-issued
grants, and recreates bounded lease cancellation timers for the remaining
time. Shutdown cancels old process leases and browser resources; it does not
delete the durable approval or adopt old browser sessions.

Successful revocation durably removes the grant before acknowledgement and
cancels its live leases. Policy replacement durably clears grants. A different
policy at startup clears the stored snapshot before serving requests, so a
later A-B-A policy change cannot restore it. Equivalent policies preserve
approval. The stored passkey owner and HTTPS origin must still match.

Any failed or uncertain durable transition disables the current controller
and its consumers. A subsequent startup reads the actual durable record; an
authenticated approval whose commit outcome was uncertain may be present,
but its original deadline is never extended. An unsuccessful revocation is
not reported as completed. Missing or unsafe store state grants no access.
Pending requests and ceremonies remain process-local and are not resumed
merely because an unrelated active grant survived.

The first upgrade cannot recover approvals that existed only in the old
process's memory. One subsequent phone approval will establish the new
durable record. Older binaries reject the added record fields: downgrade
requires a deliberate forward conversion that removes only grant metadata
and requires approval again, preserving the latest authenticator counter.
Do not restore an older passkey record over newer mutable state.
