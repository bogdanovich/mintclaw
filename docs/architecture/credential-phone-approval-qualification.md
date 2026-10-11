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
the gateway does not import it. The separate credential executable imports the
private approval package for protected enrollment setup.

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

## Phone page and private enrollment console

The embedded mobile page implements explicit passkey enrollment, approval,
and revocation actions. Approval displays account aliases, owner, actor, agent,
host, and requested duration; provider bindings and passwords are absent.
The ordinary `PhoneHandler` holds only the controller and cannot begin or
finish enrollment. A separate `Enrollment` handler holds the initial setup
capability, admitted only by a 256-bit, five-minute private console token.
There is no HTTP route to issue or renew that token.

The service borrows a startup-qualified Unix sequenced-packet console listener
and admits only live root socket creators whose per-packet identity matches.
SO_PEERPIDFD is required; numeric-PID fallback and descriptor handoff are
denied. The protected Linux `enroll` command disables core dumps, marks itself
non-dumpable, checks the service-owned private socket directory and dedicated
service identity, and writes a setup URL only to `/dev/tty`. Root access itself
is not human approval: agent root/sudo/service-control access must be excluded
on this host before either listener is activated.

The setup URL carries its nonce in a fragment, removed from browser history
before page operations. Registration begin and finish both require it; the
ceremony locator alone is insufficient. Reissuing invalidates the previous
token and ceremony. Reopen returns an independent copy of the original
challenge without renewing either deadline. A valid finish attempt consumes
the token, and persistence must finish within the console invite's original
deadline. Enrollment cannot replace an existing key.

HTTP handlers require TLS 1.3, exact SNI, Host and POST Origin, no query/fragment
or encoded-path alternate, same-origin fetches, strict bounded JSON, and
deadline-capable response writers. They bound concurrent POSTs to eight per
handler and request work to ten seconds, including body reads and writes.
Assets are embedded and use a restrictive same-origin CSP, no cookies, no
external resources, no-referrer, and no-store. Failures contain fixed outcomes,
never request bodies, setup tokens, assertions or verifier diagnostics.

`scripts/qualify-credential-phone.sh` combines a disposable real-Linux console
fixture with a new headless Chromium profile and virtual UV authenticator.
The browser uses actual `navigator.credentials.create/get` against the mobile
JavaScript and real verifier, verifies the displayed 20-hour scope, and exercises
approval, revocation and replay denial over synthetic TLS. See
[operator setup](../security/credential-broker-setup.md#reproducible-qualification)
for reproduction. This is software evidence, not physical Android evidence.

No production HTTP/console listener, notification, SDK call, token access or
model tool is activated. CA3 still requires protected startup wiring, authorized
notifications and actual Android remote approval/revocation. Credential-browser
lease enforcement remains CA4.

## Primary sources

- [Official v0.18.2 release](https://github.com/go-webauthn/webauthn/releases/tag/v0.18.2)
- [Configuration and timeout behavior](https://github.com/go-webauthn/webauthn/blob/v0.18.2/webauthn/types.go)
- [Assertion creation and verification](https://github.com/go-webauthn/webauthn/blob/v0.18.2/webauthn/login.go)
- [Registration verification](https://github.com/go-webauthn/webauthn/blob/v0.18.2/webauthn/registration.go)
