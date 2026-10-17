# Craigslist Credential Pilot

The operator selected Craigslist as the first account. This is the setup and
acceptance sequence for the [integrated alias login](credential-alias-browser-login.md).
It is not a record of a completed personal-account login. Keep
`credentials.browser_login_enabled` false until the applicable rollout gates
below have passed.

## Prepare the existing application

The administrator prepares the reference-only configuration and existing HTTPS
route. This uses the current gateway, browser, outbound chat and 1Password vault;
it needs no credential daemon or additional machine.

- Use one read-only 1Password service account restricted to a dedicated vault,
  with the selected Craigslist Login item in that vault. Configure its exact
  vault/item identifiers under the alias `craigslist`.
- Bind the exact owner, authenticated actor, agent and stable host label. Select
  the qualified direct local Playwright library and an ephemeral profile. Use
  the qualified model adapter; exclude unqualified fallback routes and browser
  placements from this pilot.
- Keep the private state directory outside every agent workspace, owned by the
  runtime user and mode `0700`. Prepare normal workspace directories first.
- Serve `<approval-origin>/credentials/` through the existing stable HTTPS
  endpoint, reachable from Android. Verify exact Host/Origin forwarding and the
  explicit loopback TLS-proxy admission when applicable. The existing forwarding
  process alone does not establish that the new route is configured correctly.

The public form was inspected in fresh Chromium without submitting credentials
on 2026-10-17. Its entry/navigation URL and password-submit destination are
`https://accounts.craigslist.org/login`. It has a text username field, a password
field, a `Log in` submit button and no form HTML ID. A separate submit button
requests an emailed login link; the password pilot selects `Log in`. These
observations establish the public form shape, not authentication success.
See the [public login page](https://accounts.craigslist.org/login).

Privately qualify the exact successful-login URL and visible success marker
before completing the account's flow configuration. Do not guess those values
from the public page. Use your own already signed-in browser to identify this
metadata privately; keep its page content and credentials out of agent tools.
Account-specific challenges and unsupported outcome
shapes must be resolved before enabling that account.

## Private operator setup

After the implementation is reviewed/merged and configuration is prepared,
enable the credential component with browser login still disabled. Run these
commands yourself in an unrecorded private terminal on the gateway host. Use
the same profile home as its running gateway so enrollment finds the correct
PID file; `MINTCLAW_CONFIG` selects its configuration file.

```sh
export MINTCLAW_HOME=/home/operator/.mintclaw/main
export MINTCLAW_CONFIG="$MINTCLAW_HOME/config.json"
mintclaw credentials init
mintclaw credentials token
```

The first command prepares state while the credential component is stopped;
skip it if the state is already initialized. The token command prompts with
echo disabled and stores the token privately. It accepts no token argument,
environment value or stdin loader. Do not enter it in chat or an agent terminal.

Restart only the affected gateway through its normal service workflow, then
run this in the same private terminal:

```sh
mintclaw credentials enroll
```

Open the private five-minute link on Android and create the approval passkey.
Keep that setup link out of chat and logs. Enrollment creates no access grant
and does not retrieve the Craigslist password.

## First approved login and acceptance

First verify the enabled deployment's [output qualification inventory](credential-output-qualification.md),
selected site flow, phone route and notification destination. Enable browser
login only for the selected account/principal/profile and restart the affected
gateway. A restart restores the phone passkey but no grants.

Ask MintClaw to log into Craigslist using `craigslist`. Open the generic
approval link delivered through the existing chat notification on Android and
approve with the passkey. Approval lasts at most 20 hours. If the tool returns
`approval_required` before phone approval finishes, a later invocation can use
the completed grant. An `outcome_unknown` must not silently replay submission.

Confirm the real account is logged in through the qualified private flow. A
subsequent explicit login within the same scope should reuse the grant without
another approval. Use **Revoke access** on the phone page and confirm that the
grant ends and MintClaw retires its authenticated browser. A new request then
requires fresh approval. Complete any MFA only through a qualified private
interaction; never send passwords, one-time codes or seeds through chat.

Record fixed outcomes and qualification results, without capturing credentials,
cookies, raw page dumps or network bodies. Complete the actual Android remote
notification, approval, login and revocation checks before expanding beyond this
one account. None of these physical-device or personal-account checks has been
claimed complete by the synthetic fixtures.
