# Credential Access Roadmap

## Current decision and intended outcome

Revised on 2026-10-11 following the operator's explicit design clarification:
trust the model to follow instructions and trust the MintClaw runtime and host.
Keep secret values out of all model-provider requests, tool arguments and
results, transcripts, local traces, logs, error messages, and browser artifacts.
Secret names/slugs and fixed operation outcomes may be visible. The operator
uses Android and a personal 1Password account, and has created a service
account/token. No real token has been provisioned to this implementation.

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
invalidation, and restart denial. A credential grant does not approve external
effects such as purchases, posts, messages, or account changes.

## Delivery sequence

| Milestone | Reviewable change | Completion evidence |
| --- | --- | --- |
| TR0 | Replace the design and roadmap; label old isolation documents as historical | Consistent current trust/output contract, preserved history, valid documentation links |
| TR1 | Reuse the vault/approval primitives in the integrated runtime; remove unused appliance scaffolding | No VM or privileged-helper deployment dependency; normal runtime can own private credential handling; affected builds and tests pass |
| TR2 | Alias-only 1Password configuration, private operator setup, and safe credential-use coordinator | Dedicated-vault read scope, no model-facing value retrieval, fixed safe errors, value-free audit records, synthetic provider tests |
| TR3 | Integrated Android approval/revoke page and generic notifications | Owner enrollment, exact scope and HTTPS origin, user-verifying passkey ceremonies, 20-hour maximum, immediate revoke/expiry/restart behavior, physical Android qualification |
| TR4 | Private login operation in the existing first-party browser | Exact-origin checks, current field readiness, fill/submit without model round trips, observation suppression and failure cleanup, safe MFA outcome, protected session lifetime |
| TR5 | One explicitly scoped non-browser connector | Private credential injection at a fixed endpoint/action, independent effect policy, allowed result projection, no raw authentication headers/body in diagnostics |
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

- TR0: the revised design/roadmap is merged in
  [#1619](https://github.com/bogdanovich/mintclaw/pull/1619). The same active
  goal retains all original requested outcomes.
- TR1: unused Mac VM packaging is removed in
  [#1620](https://github.com/bogdanovich/mintclaw/pull/1620), and the private
  vault adapter is shared with the runtime in
  [#1625](https://github.com/bogdanovich/mintclaw/pull/1625). Other unused
  appliance commands/helpers and their obsolete qualification remain to retire.
- TR2: the dynamic model/log/trace output guard is merged in
  [#1623](https://github.com/bogdanovich/mintclaw/pull/1623). Private token entry,
  storage, SDK startup and lazy scoped consumption are merged in
  [#1643](https://github.com/bogdanovich/mintclaw/pull/1643). Alias-only tool
  wiring and complete end-to-end output qualification remain incomplete.
- TR3: gateway-owned enrollment/approval/revocation is merged in
  [#1631](https://github.com/bogdanovich/mintclaw/pull/1631). Synthetic signed and
  virtual-authenticator tests cover the component; actual Android enrollment
  and generic notifications remain required.
- TR4: the existing browser's private credential transaction is merged in
  [#1637](https://github.com/bogdanovich/mintclaw/pull/1637), including grant
  checks around preparation/retrieval and authenticated session retirement.
  Authenticated cookie rotation, native control-value suppression and browser
  catalog/diagnostic/artifact projections are described in the
  [browser output boundary](credential-browser-output-privacy.md). User-facing
  tool wiring, site/placement admission and complete companion/output
  qualification remain rollout requirements.
- TR5/TR6: the scoped connector, full serialized-output qualification and
  personal-account pilot are incomplete. No real token, login, or physical
  phone approval is claimed. Passing component tests do not prove the complete
  feature or absence of secrets from every enabled output.
- Earlier PR and qualification records are preserved in the
  [historical roadmap](credential-isolated-runtime-history.md).
