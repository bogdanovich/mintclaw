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
accessed. The dependency has not been added to the application.

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

CA3 still requires hostile-input, origin, user-verification, expiration,
single-use, concurrency, policy-transition, owner-substitution, enrollment,
and Android remote-approval tests. The control endpoint, owner-key store,
enrollment path, and notification channel are not implemented.

## Primary sources

- [Official v0.18.2 release](https://github.com/go-webauthn/webauthn/releases/tag/v0.18.2)
- [Configuration and timeout behavior](https://github.com/go-webauthn/webauthn/blob/v0.18.2/webauthn/types.go)
- [Assertion creation and verification](https://github.com/go-webauthn/webauthn/blob/v0.18.2/webauthn/login.go)
- [Registration verification](https://github.com/go-webauthn/webauthn/blob/v0.18.2/webauthn/registration.go)
