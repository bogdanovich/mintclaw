# Phone approval without a short response race

Status: source implementation and qualification in progress for the owner's
2026-10-18 two-hour preference. Production rollout remains a separate gate. The
previous deployed behavior is recorded in the
[rollout evidence](credential-website-login-rollout.md).

## User behavior

The owner explicitly chooses a two-hour approval window because they switch
between tasks. A website login that needs phone approval must become a durable
waiting task, not fail after the current forty-five-second tool wait. The phone
link remains usable for two hours. After a verified passkey approval, the same
task continues automatically, without asking for another chat message.

The twenty-hour vault grant begins at approval, not at request creation. The
short lifetime of an individual WebAuthn ceremony and private operator setup
remains separate; extending a pending request must not extend either.

## Ownership and continuation

Reuse the existing interaction registry, task suspension, five-second recovery
loop and continuation service. Add a trusted externally completed wait rather
than another service, scheduler, password database or detached login worker.
The original task/route and an opaque non-authorizing condition reference are
the durable state. Do not persist the phone-link capability, vault values or
native form references as authority to execute after the wait.

The originating registered tool owns the condition resolver. The credential
resolver reports readiness only for a verified, currently active phone grant
matching the authenticated principal and scope. Ordinary chat answers cannot
approve this wait. The existing registry revision and continuation single-flight
rules own duplicate claims and execution.

Close disposable browser resources with native cleanup evidence before waiting.
Do not keep an idle authenticated browser or retrieve any vault value while
waiting. On readiness, resume the model with a bounded status result; do not
replay the original credential tool call. Open/observe fresh browser state,
match the Login again and use the normal grant/origin/item/form checks.

Cancellation or expiry ends the waiting task; late callbacks cannot revive it.
Policy replacement, restart or a missing originating resolver cannot supply
approval authority. A lost in-memory phone request needs a fresh request, not
an invented successful grant. An unknown actual submission is never an approval
wait and remains ineligible for automatic replay.

## Delivery sequence

1. Add externally completed waits to the existing interaction lifecycle and
   registered-tool boundary. Qualify delayed completion, duplicate polls,
   cancellation, expiry, unavailable resolvers, ordinary-answer rejection and
   continuation without journaled tool replay.
2. Wire browser credential approval to that mechanism. Separate the two-hour
   request lifetime from short WebAuthn/setup lifetimes, expose only an opaque
   condition reference, and retain private password/TOTP handling. Qualify a
   delayed phone approval through actual provider adapter requests and native
   browser cleanup/reopen before deployment.
3. Deploy the reviewed composition, update the explainer and perform the owner's
   fresh Facebook pilot. Do not require another real-account attempt while the
   approval waiting behavior is being replaced.

This remains part of the existing credential discovery goal. The physical
Facebook login/TOTP gate is still open; a plan or waiting task does not certify
an authenticated account.

## Credential composition

The approval controller retains its single bounded in-memory request map.
`RequestLifetime` is two hours; `PendingLifetime` remains five minutes for
WebAuthn ceremonies and private setup. Reopening an unattempted expired ceremony
creates a fresh challenge without renewing the original request deadline.
The phone page refreshes that challenge on the approval click as well, so a page
left open past five minutes can still approve its live two-hour request.
Finishing a challenge checks that deadline again after durable owner persistence.

The browser source returns immediately with a JSON-excluded wait descriptor.
Its durable reference is SHA-256 of the private phone locator, not that locator
or a grant ID. The registered `browser_login` resolver restores the original
browser owner and checks current policy and a verified active grant for exactly
that principal and covered account scope. A verified matching grant from another
request can release the wait; checking readiness never issues or renews a grant.

The model receives a safe readiness result after the normal recovery loop claims
the interaction. Native cleanup receipts survive the pause. A fresh browser and
Website lookup are required before consuming any username, password or stored
code. Timeout and lost approval finish without another model call or automatic
request creation. This change wires browser login; connector consumers retain
their existing bounded inline wait.
