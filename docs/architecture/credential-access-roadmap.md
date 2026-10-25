# Credential Access Roadmap

The fixed-binding implementation below was delivered. On 2026-10-18 the
operator requested a replacement: discover Login items from the live website,
use generic browser forms and privately fill a stored one-time code. The active
follow-up design and delivery plan are
[Website-discovered browser credentials](credential-discovery-redesign.md).
Its milestones are not complete merely because the earlier TR milestones are.

The 2026-10-25 follow-up is
[durable approval and browser continuity](credential-durable-approval.md):
persist the original approval window across restart, repair continuation image
delivery, then acquire approval before manual browser challenges.

## Current decision and intended outcome

Revised on 2026-10-11 following the operator's explicit design clarification:
trust the model to follow instructions and trust the MintClaw runtime and host.
Keep secret values out of all model-provider requests, tool arguments and
results, transcripts, local traces, logs, error messages, and browser artifacts.
Secret names/slugs and fixed operation outcomes may be visible. The operator
uses Android and a personal 1Password account, and has created a service
account/token. The operator has provisioned the token through the protected
terminal setup path; its value was not supplied through chat or agent tools.

The authoritative contract is the
[trusted-runtime design](credential-trusted-runtime-design.md). It replaces the
[historical isolated-runtime roadmap](credential-isolated-runtime-history.md).
A separate credential host, VM, Root browser helper, isolated Linux process
forest, custom egress appliance, and cross-host credential protocol are no
longer required. Do not continue implementing those prerequisites.

Deliver one integrated MintClaw feature, using the existing gateway, first-party
browser, and paired companion where applicable. Reuse 1Password as the vault;
do not build another password manager. Android passkey approval grants access
to selected aliases for at most 20 hours, with revocation, expiry, policy-change
invalidation, and durable reuse of the remaining approval window after restart.
A credential grant does not approve external
effects such as purchases, posts, messages, or account changes.

The operator clarified on 2026-10-18 that GitHub access is not needed for the
requested browser logins. TR5's scoped connector implementation and synthetic
qualification are delivered, but activating that optional integration is
separate work. A GitHub token, repository selection and real-token API pilot
are not prerequisites for completing or using the browser credential feature.

## Delivery sequence

| Milestone | Reviewable change | Completion evidence |
| --- | --- | --- |
| TR0 | Replace the design and roadmap; label old isolation documents as historical | Consistent current trust/output contract, preserved history, valid documentation links |
| TR1 | Reuse the vault/approval primitives in the integrated runtime; remove unused appliance scaffolding | No VM or privileged-helper deployment dependency; normal runtime can own private credential handling; affected builds and tests pass |
| TR2 | Alias-only 1Password configuration, private operator setup, and safe credential-use coordinator | Dedicated-vault read scope, no model-facing value retrieval, fixed safe errors, value-free audit records, synthetic provider tests |
| TR3 | Integrated Android approval/revoke page and generic notifications | Owner enrollment, exact scope and HTTPS origin, user-verifying passkey ceremonies, 20-hour maximum, immediate revoke/expiry/restart behavior, physical Android qualification |
| TR4 | Private login operation in the existing first-party browser | Exact-origin checks, current field readiness, fill/submit without model round trips, observation suppression and failure cleanup, safe MFA outcome, protected session lifetime |
| TR5 | One explicitly scoped non-browser connector; live activation optional | Private credential injection at a fixed endpoint/action, independent effect policy, allowed result projection, no raw authentication headers/body in diagnostics |
| TR6 | End-to-end output qualification and one personal login pilot | Synthetic canaries absent from every enabled model/durable sink; protected real-token setup; actual Android approval/login/revoke; one operator-selected account |

Keep PRs focused on these complete behaviors. Do not split each OS primitive or
helper into another prerequisite PR. A prerequisite is justified only when a
specific failure blocks the integrated feature. Preserve useful tests; remove
tests of deleted appliance behavior as part of that explicit retirement rather
than weakening the current privacy contract to obtain green CI.

## Reuse and retirement

- Reuse `pkg/credentialbroker`: immutable alias/origin/principal policy,
  canonical revisions, bounded grants, and live lease checks.
- Reuse the official 1Password adapter's exact item reads, bounded retrieval,
  private value consumption, buffer cleanup, and fixed error projection. Move
  useful code out of command-private packages when needed for gateway use.
- Reuse the reviewed WebAuthn verifier and mobile approval/revoke UI. Replace
  the old Linux service-role, Root ancestry, and owner-console deployment
  assumptions with ordinary private operator setup in the trusted runtime.
- Reuse existing browser ownership, policy, ephemeral input, companion
  transport, and diagnostic redaction machinery. Extend their credential
  admission explicitly; ordinary model-supplied fill arguments remain
  unsuitable for passwords.
- Retire unused Mac VM packaging, immutable appliance images, Root launch and
  resource services, process/network forest helpers, private browser appliance
  transport, and standalone qualification scripts. Do not install them.
- Preserve earlier work as historical evidence. Pending PRs whose only purpose
  is the retired appliance transport can be superseded rather than merged.
  Useful policy/approval changes may be retained after reviewing their fit.

## Required output coverage

Primary protection is prevention: retrieve/use a value inside trusted local
code, generate a safe typed result there, and never send a raw value to a
logging, tracing, persistence, artifact, or model interface. Redaction of known
values and ordinary encodings is an additional guard for accidental echoes.
It is not the basis for an adversarial-agent or malicious-site guarantee.

Inventory and qualify enabled sinks across gateway and coding paths: provider
requests; tool arguments/results; Seahorse/session state; browser ledgers;
interaction records; node/companion messages and traces; logger fields; passive
diagnostic traces and support bundles; recovery records; screenshots; DOM and
accessibility observations; console/network output; and browser artifacts.
Do not export cookies, storage state, Authorization headers, service tokens,
passwords, or OTP values through any of those surfaces.

Exercise successful login, provider/driver failure, form submission failure,
cancellation, MFA handoff, expiry, revocation, and restart with synthetic
canaries. Inspect the actual serialized outputs and resulting files rather
than only checking a helper's return value. Diagnostics report sink names and
boolean pass/fail; they must not print the canaries they detect.

## Setup and rollout

Store the service-account token using an ordinary owner-only file or supported
OS credential facility, outside model-visible configuration and workspaces.
Use a private operator prompt with no echo; never ask for a token in chat or
include it in argv, tool calls, traces, or shell history. Host administrators
and the trusted runtime may access it under the chosen trust model.

Configure stable HTTPS access for the Android approval page in the existing
application. Generic notifications may reveal that access was requested but
must contain no secrets or bearer approval. Initial owner enrollment and real
token entry still require the operator; prepare and test those paths before
asking the operator to use them.

Begin with synthetic credentials and one test site. Actual passwords are gated
on TR6 output qualification and physical phone approval, not on the retired
host-isolation requirements. Pilot one selected alias, then add Facebook,
Craigslist, eBay, or another site after validating that site's login and MFA
behavior. Retain normal policy for subsequent authenticated actions.

## Current progress

Acceptance status checked on 2026-10-18 UTC. Merged implementation, synthetic
qualification and physical-device acceptance are recorded separately below.

- TR0: the revised design/roadmap is merged in
  [#1619](https://github.com/bogdanovich/mintclaw/pull/1619). The same active
  goal retains all original requested outcomes.
- TR1: unused Mac VM packaging is removed in
  [#1620](https://github.com/bogdanovich/mintclaw/pull/1620), and the private
  vault adapter is shared with the runtime in
  [#1625](https://github.com/bogdanovich/mintclaw/pull/1625). The remaining unused
  daemon, privileged launch/transport/browser stack and obsolete qualification
  are removed in the [appliance retirement](credential-appliance-retirement.md).
  Current policy, vault, approval, private setup and integrated browser tests
  remain; historical documents preserve the earlier approach.
- TR2: the dynamic model/log/trace output guard is merged in
  [#1623](https://github.com/bogdanovich/mintclaw/pull/1623). Private token entry,
  storage, SDK startup and lazy scoped consumption are merged in
  [#1643](https://github.com/bogdanovich/mintclaw/pull/1643). Alias-only tool
  wiring and generic notifications are implemented by the
  [integrated alias operation](credential-alias-browser-login.md), merged in
  [#1654](https://github.com/bogdanovich/mintclaw/pull/1654). Browser login is now
  enabled for the selected Craigslist account/principal after the preparation
  gates passed; broader rollout still requires the remaining acceptance checks.
- TR2 setup: [private item discovery](credential-item-discovery.md) in
  [#1679](https://github.com/bogdanovich/mintclaw/pull/1679) searches the dedicated
  vault's Login overviews by website or item name. The operator chooses a
  candidate and saves its exact reference for an existing alias, without copying
  UUIDs or retrieving password fields. Website matching does not expand runtime
  origins; site flow and actor scope stay in local configuration. Personal item
  identifiers and site-specific behavior do not belong in production Go code.
  This CLI update is merged and installed, including the localhost enrollment
  repair. The CLI installation preserved the running gateway and owner key;
  the operator subsequently provisioned the token privately. Website and name
  discovery both found the configured Login item without reading passwords.
- TR3: gateway-owned enrollment/approval/revocation is merged in
  [#1631](https://github.com/bogdanovich/mintclaw/pull/1631). Synthetic signed and
  virtual-authenticator tests cover the component. The alias operation queues
  generic approval links through the existing outbound chat channel.
  [#1686](https://github.com/bogdanovich/mintclaw/pull/1686) fixes initial
  notification-link navigation and adds grant-free setup verification. That
  update is deployed through the existing HTTPS route. The operator completed
  verification on a Pixel using Android Chrome and 1Password; server metadata
  confirms enrollment is durably activated. This retrieved no credentials and
  issued no account grant. The first subsequent real account request delivered
  its chat notice, the owner reported Android approval, and the private login
  succeeded. A second same-scope login reused the grant in the same gateway
  lifetime. After the owner reported completing phone revocation, a later
  same-scope request required fresh approval without a gateway restart.
  State-changing API calls retain the exact same-origin boundary.
- TR4: the existing browser's private credential transaction is merged in
  [#1637](https://github.com/bogdanovich/mintclaw/pull/1637), including grant
  checks around preparation/retrieval and authenticated session retirement.
  Authenticated cookie rotation, native control-value suppression and browser
  catalog/diagnostic/artifact projections are described in the
  [browser output boundary](credential-browser-output-privacy.md). The
  [alias login operation](credential-alias-browser-login.md) joins these
  components for synthetic qualification. The initial supported placement is
  the gateway's local Playwright library with an ephemeral profile. Credential
  use through companion, cloud and managed browser profiles remains denied.
  The selected site's success URL and visible logout marker were privately
  qualified and saved in reference-only local flow configuration.
- TR6: synthetic [integrated output qualification](credential-output-qualification.md)
  exercises two real agent turns, provider request serialization, canonical
  history, Seahorse state, browser state, logs, traces and both support-bundle
  modes through the production OpenAI-compatible and Codex OAuth/Responses
  adapters, including MFA handoff and uncertain-submission outcomes. The merged
  phone-fix deployment passed the initial nine combined browser, provider and
  GitHub fixture scenarios. Merged
  [#1700](https://github.com/bogdanovich/mintclaw/pull/1700) adds credential-runtime
  recreation for both qualified adapters;
  [#1704](https://github.com/bogdanovich/mintclaw/pull/1704) adds actual Codex turn
  cancellation and signed-grant expiry during submission. All thirteen cases
  passed with the race detector at the reviewed test head. Expiry must cancel
  the private consumer and retire the browser before provider-scripted cleanup;
  isolated negative controls fail when either mechanism is disabled. These use
  local scripted peers and virtual passkeys, with no personal credentials or
  website traffic. OS process-crash recovery, private MFA continuation and
  other provider protocols retain explicit qualification limits; unqualified
  fallback providers must be excluded from the pilot.
- TR6 pilot selection: the operator selected Craigslist. The
  [pilot sequence](credential-craigslist-pilot.md) records public form evidence,
  completed private setup and the first real Android-approved login on qualified
  runtime `694d716db119e14044faffbe4cd68fb1567cba25`. Its tool returned only the
  alias and `logged_in`; a second login confirmed same-scope grant reuse.
  [#1724](https://github.com/bogdanovich/mintclaw/pull/1724) adds a native receipt
  for confirmed private login without expanding the public result. A fresh
  owner-requested login on deployed runtime
  `5ce0ed6ef919db58820cf716499778a0974479d8` produced that verified receipt and
  a succeeded objective, then closed the temporary browser.
  The owner subsequently reported completing phone revocation; a retry at the
  configured entry URL returned `approval_required` and delivered a fresh link
  under the same actor/agent scope and gateway process. This completes the
  selected account's notification, approval, login, grant-reuse and
  closed-session revocation checks. Active-browser retirement remains
  synthetically qualified, with no physical in-progress-task claim.
  The selected headless ephemeral browser is separate from the remote desktop
  and does not retain a website session after closure.
- TR5: the [GitHub read connector](credential-github-read-connector.md) is an
  optional read-only API integration. It
  reuses phone approval for exact configured repositories and an API Credential
  item. Synthetic actual-HTTP/provider/phone tests cover protected result
  release, grant reuse and cancellation on revocation. The implementation is
  merged in [#1673](https://github.com/bogdanovich/mintclaw/pull/1673) and included
  in the deployed binary. Its implementation and synthetic qualification are
  complete. It remains disabled; repository configuration, a separate API
  Credential item and real-token acceptance are needed only if the operator
  later requests this integration. They do not block browser credential access.
- TR5/TR6: the integrated implementation and selected personal-account pilot
  are delivered for the qualified routes and closed-session revocation
  sequence above. Real GitHub activation is optional and has not been requested.
  Synthetic output qualification covers the explicitly inventoried routes and
  scenarios, not every possible provider or recovery path.
  Dedicated-vault read-only scope is operator-confirmed; protected token entry,
  exact site outcome metadata and the first real Android-approved login are
  recorded, along with actual same-scope grant reuse and a fresh approval
  requirement after the reported phone revocation. These observations do
  not expand the synthetic inventory's provider/recovery limits. Passing
  component tests or one successful login do not prove the complete feature or
  absence of secrets from every possible output.
- Earlier PR and qualification records are preserved in the
  [historical roadmap](credential-isolated-runtime-history.md).
