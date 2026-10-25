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

The latter two steps remain pending until their own implementation and
qualification. A real eBay login is not certified by synthetic tests.

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
