# Credential Phone Approval Verifier Qualification

Candidate qualification dated 2026-10-10 for CA3 of the
[credential access roadmap](credential-access-roadmap.md). This records build
evidence and required caller behavior. It does not admit a control endpoint,
owner enrollment, notification delivery, or real credential access.

## Candidate and evidence

`github.com/go-webauthn/webauthn` tag `v0.18.2` resolves to
`a4c6fc6a4399bcd1ba982c116189f1598d9d5eec`. In a separate temporary checkout,
`GOTOOLCHAIN=go1.26.9 CGO_ENABLED=0 go build ./...` passed for Linux AMD64,
Linux ARM64, Darwin ARM64, and Windows AMD64. The module recommends Go 1.27.1;
these commands explicitly used MintClaw's Go 1.26.9 toolchain.

`go test -count=1 ./webauthn ./protocol` also passed under Go 1.26.9. These are
upstream fixture tests, not tests of MintClaw's future approval integration.
`OP_SERVICE_ACCOUNT_TOKEN` was removed from every build/test environment
without reading it. No real token, vault, passkey, phone, or approval was
accessed. The dependency is now pinned for the private approval controller;
the gateway and the current provision-only executable do not import it.

## Required control-service contract

- Preserve the complete `SessionData` returned by the library's `Begin*`
  functions in the trusted service. Never reconstruct it from client input.
  Bind it to the immutable owner, principal, accounts, policy, and duration
  displayed by the approval page.
- Require user verification for owner registration and grant approval. Enable
  server-enforced ceremony timeouts explicitly: the zero-value `TimeoutConfig`
  does not enforce them. Independently bound pending challenges to five
  minutes with monotonic elapsed-time checks and a single-use claim.
- Consume a challenge atomically; expired, consumed, invalid, or
  policy-transitioned challenges cannot issue grants. Discard every pending
  challenge during policy replacement, including a later return to policy A
  after A changes to B. A canonical policy digest alone does not prevent this
  approval replay.
- Configure one HTTPS browser origin and its relying-party ID for the first
  release. Accept no opaque/native origin, cross-origin ceremony, related-origin
  expansion, or client-selected relying-party setting. Enrollment itself
  requires an authenticated owner setup action.
- Notification URLs locate requests and never approve them. Return fixed
  failures that exclude assertion bodies and raw verifier diagnostics.

## Private controller implementation

`cmd/mintclaw-credential/internal/approval` owns policy, exact provider-binding
snapshots, pending requests, the complete verifier sessions, and grant authority
under one lock. There is no externally mutable Authority pointer or direct
approval boolean. Requests and ceremonies are bounded to 64 records each and
five monotonic minutes. A finish attempt consumes its ceremony before parsing
or verification, including canceled, invalid, and operation-substituted attempts.
Every effective policy transition clears pending requests, ceremonies, and
grants, including A-B-A. Equivalent normalized policy retains authority.

One owner, one exact HTTPS browser origin, and ES256 registration are admitted.
Registration and assertions require user presence and verification. Timeouts
are enforced both by the verifier and by the controller, including after a
durable write. Cross-origin, related-origin, native, and opaque-origin modes
are absent. Browser options are independently encoded public data; the private
SessionData returned by Begin is retained intact by the controller.

The constructor returns an initial `OwnerSetup` capability only to trusted
startup when no key is enrolled. Its zero value denies access. The ordinary
controller cannot begin or finish enrollment. Startup must keep the capability
outside the public approval handler and admit its use only through an
authenticated protected owner action. Possession of a Go object does not
establish human approval or qualify the eventual enrollment channel.

Linux owner storage uses a service-owned mode-0700 directory below root-owned,
non-writable, non-symlink ancestry. Records are service-owned mode-0600 regular
files with one link and no extended access ACL. A lifetime directory lock
prevents concurrent controllers from using stale counters. The complete
library Credential record is atomically replaced and both file and directory
are synced before grant issuance. An uncertain write permanently disables the
controller; restart reloads state but restores no grants or challenges. A clone
warning is persisted and disables authority, including after restart; recovery
needs a separate protected operator procedure. Other platforms deny store use.
These metadata checks do not prove process-role or agent/browser isolation.

Signed synthetic ES256 fixtures exercise enrollment, approval, revocation,
exact scope binding, malicious origin/UV/key/owner/operation substitution,
replay, concurrency, request expiration during persistence, cancellation,
policy transitions, capacity, durable failures, and clone warnings. The
disposable Linux owner-store fixture exercises actual permissions, ACLs,
symlinks, hard links, FIFO denial, store locking, and restart state.

Reproduce the filesystem fixture with Go and Docker available:

```sh
bash scripts/qualify-credential-approval.sh
```

This implements private authorization and persistence. No HTTP listener,
notification, SDK call, token access, public enrollment endpoint, or model tool
is activated. CA3 still requires protected startup/enrollment wiring, HTTPS
approval/revoke UI, authorized notifications, and actual Android remote approval
and revocation evidence. Credential-browser lease enforcement remains CA4.

## Primary sources

- [Official v0.18.2 release](https://github.com/go-webauthn/webauthn/releases/tag/v0.18.2)
- [Configuration and timeout behavior](https://github.com/go-webauthn/webauthn/blob/v0.18.2/webauthn/types.go)
- [Assertion creation and verification](https://github.com/go-webauthn/webauthn/blob/v0.18.2/webauthn/login.go)
- [Registration verification](https://github.com/go-webauthn/webauthn/blob/v0.18.2/webauthn/registration.go)
