# Configured Credential Login Scope

The private approval controller's `LeaseConfiguredLogin` entry binds an
authenticated caller principal, account alias and grant locator to the actual
immutable browser configuration. It derives the initial HTTPS entry origin
and current policy revision under the controller's existing lock, then borrows
the original signed grant generation. It issues no approval and initializes
no provider, browser or listener.

An intent cannot select a URL, provider binding, policy revision or caller
identity. Caller authentication must supply the complete owner/actor/agent/host
principal independently of model input. Even a principal or account allowed
by the current policy must belong to this actual grant.

The controller checks the bounded configured origin list against the immutable
policy's explicit login flow. List order is not a destination choice; only
the configured canonical entry succeeds. A legacy origin-only account cannot
become a browser login. The policy, binding, flow and grant generation are
snapshotted within one concurrency boundary.

The returned request is private consumer scope metadata. It supplies the
initial configuration, not current document, frame or element authority.
Consumers must retain the original caller lifetime, watch the lease's `Done`,
and recheck complete scope immediately before retrieval, injection and private
session use. The browser supplies its independently verified live origin for
those operation checks. No original grant can be revived by restoring an
earlier policy revision.

Signed synthetic WebAuthn fixtures check canonical entry selection when a
different approved origin appears first, immutable scope/flow/binding,
another policy-allowed principal or account outside the grant, malformed and
unknown grant locators, absent/canceled context, legacy flow denial and a
policy A–B–A transition. Existing lease retirement and owner-persistence
fixtures retain their responsibilities.

This is a private composition prerequisite. Broker registration, remote
authenticated intents, protected host/browser qualification and physical
Android ceremonies remain separate gates. No real token or account login
is used by this implementation or its fixtures.
