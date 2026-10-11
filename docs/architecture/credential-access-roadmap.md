# Credential Access Roadmap

## Status and outcome

Active implementation roadmap, requested on 2026-10-10. The operator uses
Android and a personal 1Password account, has created a service account, and
has a token. The token has not been provisioned to this implementation.

The intended flow is: ask MintClaw to use an account alias, approve a bounded
grant on the phone, and let a trusted local service log in through the
first-party browser. The model receives account aliases and safe outcomes;
passwords, service-account tokens, MFA seeds, and session credentials stay
outside model requests, tool results, transcripts, traces, and artifacts.

An approval lasts at most 20 hours, can be shorter, and can be revoked. It
authorizes named accounts for one owner, actor, agent, and execution host under
one exact policy revision. It does not authorize arbitrary secret retrieval
or automatically approve purchases, messages, posts, or account changes.

Each milestone is a separate reviewable change. CA0 records the contract;
CA1 starts its implementation. Later milestones must satisfy their admission
and qualification gates before real credentials become accessible. Until CA6,
the capability stays unavailable to production agents.

## Existing foundations

Baseline: `origin/main` at `72a236cbbfee21fc2fc10de6a6a8754b3bc58263`.

- Reuse the first-party browser broker, typed actions, current element
  authority, exact-origin policy, private Playwright adapters, and existing
  gateway/companion identity boundaries.
- Reuse the ephemeral input and durable redaction mechanics admitted by
  [protected fill](browser-capability-bf1-protected-fill-admission.md).
  That admission explicitly excludes credential injection. Ordinary fill
  remains separate: credential values must never pass through model-provided
  arguments, even transiently.
- Accessibility snapshots can contain values entered earlier. Protected fill
  already avoids durable storage of those observations, but this is not a
  guarantee that the model never sees a password. Credential sessions need a
  stronger observation boundary.
- Coordinate with the [Android companion roadmap](android-companion-roadmap.md).
  A small passkey-authenticated phone approval page can ship without waiting
  for the full native operator application. It must not create a second
  general chat, node, or interaction protocol.

## Trust and deployment boundaries

1Password remains the encrypted vault. Use a dedicated MintClaw vault and a
service account with read access only to that vault. Personal/Private vaults
cannot be granted to service accounts; copy only selected login items into
the dedicated vault. Validate the existing account's permissions during
protected setup rather than assuming the token is suitably scoped.

The service-account token is an unattended credential. A phone approval is
our service's authorization policy; it is not a native 1Password phone-unlock
operation. Compromise of the service or its token can bypass that policy.

Run the credential service under an identity separate from the agent runtime.
Keep its token, private IPC, browser profile, and process memory inaccessible
to agent shell, file, debugging, and process-control capabilities. Merely
moving retrieval into a tool in the same unrestricted process is insufficient.
The gateway routes authenticated, typed requests; it receives no raw secrets.

The first credential browser runs locally on an explicitly trusted gateway
host or companion host, under that isolation boundary. Disable credential
login for cloud browser providers, attached user profiles, generic JavaScript,
raw CDP/driver access, arbitrary selectors, cookie exports, and storage-state
exports. Admit any later expansion through a separate trust decision.

Approved websites necessarily receive their own passwords. Browser scripts,
the browser host, and 1Password are within the necessary trust boundary.
Account content the operator asks the agent to read may reach the model; the
goal protects authentication material, not all authenticated page content.
Do not claim that substring redaction protects against a malicious site
encoding a credential into arbitrary page content.

## Authorization contract

- Operator configuration maps aliases to exact 1Password vault/item IDs,
  exact HTTPS login and credential-entry origins, permitted principals, and
  explicit login field/flow constraints. No wildcard domains, model-selected
  secret references, or model-supplied destination endpoints.
- Reuse `browserpolicy.NormalizePublicOrigin`, require HTTPS, and retain live
  browser DNS/network enforcement. Origin syntax checks alone do not prevent
  DNS rebinding, redirects, frame substitution, or stale-element attacks.
- Derive a revision from the entire normalized authority policy. A change of
  account binding, origin, principal, host, or login constraints invalidates
  approvals rather than silently widening an existing grant.
- Verify actor, agent, host, owner, grant, revision, account, live origin,
  frame, and element authority before retrieval and immediately before fill.
  Fail closed on cancellation, expiry, revocation, stale authority, or unknown
  execution outcome. Do not automatically replay a login after restart.
- Use cryptographically random grant IDs. IDs alone are not authorization;
  they remain bound to the authenticated principal and approved account set.
  Bounded in-memory grants disappear on restart in the first release.
- Use server time with monotonic elapsed-time enforcement. Approval cannot
  silently renew a grant. An additional account or changed policy requires a
  new phone approval. Expired/revoked grants block every operation on their
  authenticated browser sessions, including reads using existing cookies.
- Quarantine or destroy credential browser sessions at grant expiry or
  revocation. Lease checks must occur inside the trusted browser service;
  gateway-only checks permit a bypass through lower-level browser tools.
- Remote approval requires an enrolled operator passkey, user verification,
  an expiring single-use challenge, an exact relying-party origin, and binding
  to the complete requested scope. A notification link is a locator, never a
  bearer approval or decryption key. The page displays accounts, host, agent,
  duration, and a revoke control.
- A grant approves credential use. Login submission, MFA, external effects,
  and account changes still obey their respective browser policies. MFA and
  CAPTCHA may require a human handoff; never export a TOTP seed to the model.

## Delivery sequence

| Milestone | Scope | Depends on | Completion evidence |
| --- | --- | --- | --- |
| CA0 | Roadmap and indexed authorization contract | None | Local docs validation; merged documentation PR |
| CA1 | Immutable account/origin/principal policy and bounded in-memory grants | CA0 | Tested default denial, complete scope binding, exact HTTPS origins, deterministic revisions, expiry, revocation, policy invalidation, concurrent access, restart denial, capacity bounds, and fixed safe errors |
| CA2 | Isolated credential service, private typed IPC, 1Password adapter, protected provisioning | CA1 | Fake-vault integration tests; token absent from agent environment/files/output; pinned provider dependency; process isolation and deployment checks; safe provider failure handling |
| CA3 | Android passkey enrollment, approval/revoke page, generic notifications | CA2 | Reviewed WebAuthn library; challenge replay/expiry/origin/user-verification tests; approval bound to exact scope; successful Android remote approval and immediate revocation |
| CA4 | First-party credential browser login and authenticated-session lease enforcement | CA2, CA3 | Real local browser fixture verifies origin/frame/element freshness, private injection, output restrictions, isolation, no bypass through ordinary browser tools, expiry/revocation of existing sessions, and unknown-outcome quarantine |
| CA5 | Explicitly scoped non-browser connector operations | CA2, CA3 | One concrete connector operation with host/endpoint/action constraints, private credential injection, bounded safe results, independent effect policy, and adversarial egress tests |
| CA6 | End-to-end qualification and protected personal pilot | CA4; CA5 only for enabled connectors | Canary-secrets suite passes; phone and isolated-host setup documented and verified; dedicated-vault token provisioned privately; one operator-selected login succeeds without secret material in provider requests or durable outputs |

### CA1: policy and grant foundation

Introduce a small `pkg/credentialbroker` package separate from the existing
provider API-key encryption store. It contains no password fields, provider
calls, public endpoints, or model tools. Construct and defensively copy
operator policy; compute a canonical revision; issue grants only from a
trusted internal approval entry point; check and revoke them under one
concurrency boundary. Approval verification is added by CA3, not simulated
with a model-generated approval boolean.

Keep this merge unit bounded. The package is a policy foundation, not a
claim that 1Password login or phone notifications already work. Security
tests cover authority and lifecycle invariants rather than mirroring helper
implementations.

### CA2: service and 1Password access

Qualify the official Go SDK against MintClaw's build/deployment targets before
choosing it. If its native dependencies are unsuitable, use an operator-pinned
`op` executable with bounded private pipes, strict structured parsing,
timeouts, a controlled environment, and fixed errors. No arbitrary command
wrapper, free-form `op` arguments, or provider stdout in model results.

Provision the token through an owner-only secret file or OS service credential
facility using a protected local command. Never request it in chat, put it in
shell history/argv, commit it, or copy it into the agent environment. Enforce
file ownership and permissions, document rotation/revocation, and test with
synthetic credentials before accessing the operator's vault.

The IPC API admits operation intents and aliases, not `get_secret`. Its
authentication binds caller identity to an operator grant; the caller cannot
choose another actor or host. Credential-bearing byte buffers stay private
and short-lived. Avoid claiming guaranteed memory zeroization in Go or
Playwright. Bound retention and access instead.

### CA3: phone approval

Use an established WebAuthn verifier rather than implementing authentication
cryptography. Serve a minimal mobile approval/revoke page over HTTPS reachable
from the Android phone; do not require Bluetooth proximity. Enrollment itself
requires an authenticated owner setup action and excludes arbitrary visitors.

Send generic notifications through an explicitly configured, authorized
channel or Web Push. Include no password, service token, unlock key, or bearer
approval. Notification providers may learn that an approval is pending.
Reopening a notification cannot replay approval or extend the grant.

### CA4 and CA5: consumers

Define a separately reviewed credential-browser admission before modifying
the protected fill contract. Prefer a typed login operation using a fresh
semantic password-field reference and trusted account mapping. Return only
status, approved alias, safe handoff state, and allowed page observations.

Review all live and durable output paths: accessibility/DOM projections,
screenshots, console/network diagnostics, browser artifacts, session history,
coding context, interaction records, provider requests, Seahorse state,
recovery, and traces. Password reveal controls, hidden inputs, page echoes,
cookie APIs, and encoded output must exercise the boundary. Do not enable
general browser execution within a credential-bearing session.

Connector support is operation-specific. Add no generic HTTP request or shell
operation with secret interpolation. Keep independent external-effect
approval and scope each connector to its concrete endpoint and action.

### CA6: rollout gate

Begin with a dedicated test account and synthetic canary credentials. Capture
model-facing payloads and durable stores during success, failure, cancellation,
restart, and malicious-page cases. Tests must detect raw credentials, common
encodings, tokens, cookies, and MFA secrets without logging their actual values.

Only after these checks pass, provision the real token privately and ask the
operator to select the first account alias. Verify Android approval, login,
expiry, revocation, and reapproval on the actual isolated host. Document
site-specific MFA/CAPTCHA limits. Add Facebook, Craigslist, or eBay only after
their individual flow and exact origin rules are qualified. No rollout to the
entire personal vault.

## Progress record

- CA0: merged in [PR #1511](https://github.com/bogdanovich/mintclaw/pull/1511);
  `make lint-docs` passed.
- CA1: merged in [PR #1512](https://github.com/bogdanovich/mintclaw/pull/1512).
  Targeted policy/grant tests, package race tests, changed-package lint, and all
  16 GitHub checks passed. The exact-origin review finding was fixed, tested,
  and cleared on the merged head.
- CA2: the private provider adapter is merged in
  [PR #1515](https://github.com/bogdanovich/mintclaw/pull/1515). Full local lint,
  targeted normal/race tests, and all 16 GitHub checks passed; review found no
  actionable findings. The agent gateway has no SDK dependency.
  Process/provisioning qualification is recorded in
  [PR #1514](https://github.com/bogdanovich/mintclaw/pull/1514), with browser
  startup requirements in [PR #1519](https://github.com/bogdanovich/mintclaw/pull/1519).
  The protected Linux provisioning command and disposable real-OS qualification
  are merged in [PR #1525](https://github.com/bogdanovich/mintclaw/pull/1525);
  [operator setup](../security/credential-broker-setup.md) records the boundary
  and remaining gates. [PR #1526](https://github.com/bogdanovich/mintclaw/pull/1526)
  records the observed host boundary and why that host is not yet qualified.
  The private intent transport implements bounded, process-pinned local IPC
  with kernel peer and per-message identity checks. Linux normal/race fixtures
  exercise identity substitution, descriptor handoff, cancellation, capacity,
  and fixed response projection. It creates no listener or model tool by
  itself. Isolated startup, trusted process-role admission, and service wiring
  remain pending. Provisioning and transport alone enable no agent access.
- CA3: [verifier candidate qualification](credential-phone-approval-qualification.md)
  records build and upstream fixture evidence; control-plane and Android
  end-to-end implementation remain pending.
- CA4–CA6: pending their dependencies and admission/qualification gates.
  Runtime credential access remains disabled and no real token is provisioned.

Update this record with PRs, validation, and any narrowed admissions as work
lands. A milestone completes on demonstrated evidence, not on scaffolding.

## Primary references

- [1Password service-account setup](https://www.1password.dev/service-accounts/get-started)
  and [security](https://www.1password.dev/service-accounts/security): vault
  scope, permissions, unattended token handling, rotation, and revocation.
- [1Password service-account rate limits](https://www.1password.dev/service-accounts/rate-limits):
  bound retrieval and avoid polling credentials for every browser observation.
- [Official 1Password Go SDK](https://github.com/1Password/onepassword-sdk-go)
  and [CLI item operations](https://www.1password.dev/cli/reference/management-commands/item):
  provider-adapter candidates.
- [go-webauthn](https://github.com/go-webauthn/webauthn) and
  [Android passkey environments](https://developers.google.com/identity/passkeys/supported-environments):
  phone approval implementation candidates and platform constraints.
