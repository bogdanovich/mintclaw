# Craigslist Credential Pilot

The operator selected Craigslist as the first account. This is the setup and
acceptance sequence for the [integrated alias login](credential-alias-browser-login.md).
The first personal-account login is recorded below; grant reuse and revocation
still need physical acceptance. Keep `credentials.browser_login_enabled` false
until the applicable preparation gates below have passed.

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
Then choose **Verify approval passkey** and confirm that setup is complete
before restarting the gateway. If the key was created in 1Password on a
computer, open the plain `/credentials/` page in Android Chrome to verify the
synced key there; do not reuse the consumed enrollment link.
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
another approval. After the task ends, use **Revoke access** on the phone page
and confirm that another login requires fresh approval. The headless ephemeral
profile must be closed before task finalization; it cannot remain open as a
human handoff. Revocation while a browser is active needs a separate in-progress
task check; leaving a headless browser open after a completed task cannot
establish that behavior. Complete any MFA only through a qualified private
interaction; never send passwords, one-time codes or seeds through chat.

Record fixed outcomes and qualification results, without capturing credentials,
cookies, raw page dumps or network bodies. Complete the actual Android remote
notification, approval, login and revocation checks before expanding beyond this
one account. None of these physical-device or personal-account checks has been
claimed complete by the synthetic fixtures.

## Recorded acceptance status

On 2026-10-18 UTC the operator completed **Verify approval passkey** on a Pixel
using Android Chrome and 1Password. A metadata-only server check confirmed that
the owner record's enrollment is no longer pending. Do not repeat enrollment
for that verified owner. This setup check issued no account grant and retrieved
no Craigslist credential.

The existing HTTPS phone route and merged phone-page update are deployed.
The private CLI's localhost repair and website/name discovery are also installed;
the operator need not copy item UUIDs to create subsequent alias bindings.
All thirteen synthetic browser/provider/connector scenarios passed with the
race detector after adding credential-runtime recreation, actual caller
cancellation and signed-grant expiry. These results establish the listed
synthetic paths, not a personal-account login or OS process-crash recovery.

The operator subsequently provisioned the service-account token through the
private terminal command and confirmed **Read Items** access only to the
dedicated vault. Metadata-only website and name discovery each found the
configured Login item without requesting password fields. The gateway was
restarted to load the protected token and retained the verified owner key.

A read-only inspection of the operator's signed-in browser established the
query-free success URL `https://accounts.craigslist.org/login/home` and visible
logout control within `span#ef`; no password field was visible. This metadata
was saved in the account's local flow configuration. No personal vault/item
identifier or site-specific behavior was added to Go code.

Browser login was enabled for the selected account and principal on qualified
runtime commit `694d716db119e14044faffbe4cd68fb1567cba25`. Only the browser
agent's model fallbacks changed: its existing OpenAI OAuth primary and fallback
use the qualified Codex adapter; unqualified provider fallbacks were excluded.
All five profile configurations loaded with unchanged existing doctor findings.
The restarted gateway retained its passkey, all ten expected services were
active, and a harmless live request produced a completed diagnostic trace.

On 2026-10-18 UTC the owner requested login through the usual MintClaw chat,
received the generic approval link and reported approving it on Android. The
actual `browser_login` result contained only `alias: craigslist` and
`outcome: logged_in`. The agent reported a subsequent authenticated observation
of the configured account page and logout control, then closed its session.
This is the first real account login, not a synthetic website result.

That `gateway/ephemeral` profile runs headless on the gateway host. It creates
no desktop window for RustDesk to show. Closing the temporary browser discards
its local website session state; the separate access grant permits another
scoped login until revocation or its deadline, at most 20 hours. Grant reuse
does not preserve website cookies after the browser is closed.

Actual same-scope grant reuse and phone revocation retiring an open,
authenticated MintClaw browser remain unverified. Keep the overall pilot
incomplete until those checks pass. The synthetic output inventory retains its
stated provider, recovery and MFA limits.
