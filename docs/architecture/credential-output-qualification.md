# Integrated Credential Output Qualification

This records evidence for the [trusted-runtime contract](credential-trusted-runtime-design.md).
It is an inventory of exercised boundaries, not permission to enable personal
account use. All values and passkeys in these fixtures are synthetic.

## Actual integrated paths

The gateway fixture uses the production alias tool, private runtime consumer,
first-party Chromium driver and phone UI with a new virtual authenticator.
Private provider retrieval remains zero until passkey approval. One 20-hour
grant authorizes sequential browser sessions without another notification;
revocation retires authenticated access. The one-live-session limit is retained.

The provider fixture additionally uses the real personal agent loop, real
OpenAI-compatible HTTP adapter or production Codex OAuth/Responses adapter,
canonical session history and Seahorse context manager. A local HTTP peer
scripts ordinary tool choices and captures the actual serialized requests,
including headers. The Codex variant uses synthetic authentication and
intercepts the SDK's exact Responses destination locally; it does not load an
OAuth store or add a production endpoint override. Neither variant contacts an
LLM provider. The website is intercepted inside Chromium and has no real account.

Each scenario runs two separate user-requested turns that open a browser,
observe login fields, request the account alias and return a final response.
Scenarios cover confirmed login, a configured MFA handoff and submission
without confirmed success. Confirmed/handoff observations are inspected before
closing the session; an unconfirmed result retires the browser. Each browser
permits exactly one login submission, so an uncertain effect cannot silently
replay. A second explicit user turn reuses the same signed approval.

The fixture deliberately echoes the synthetic username, password and cookie in
its result page/title. The MFA page also contains a synthetic one-time-code
control value. All four values must be absent from every captured public
output, including after browser cleanup releases session guards.

## Evidence inventory

| Boundary | Evidence | Qualification limits |
| --- | --- | --- |
| Provider request | Actual local HTTP body/header capture through the production OpenAI-compatible and Codex OAuth/Responses adapters, including streaming Responses | Other provider/fallback protocols need equivalent wire qualification or exclusion from the pilot |
| Tool arguments/results | Strict alias/ref input; fixed outcome; actual serialized tool results and authenticated observations | No model-facing value retrieval API |
| Canonical history | Read back and serialize durable history after each completed turn; require history growth | Protected browser observations retain existing durable projection |
| Seahorse/context state | Run the real context manager and inspect workspace state files | Does not establish arbitrary external store/export behavior |
| Browser ledger/catalog | Inspect actual first-party file-store state; project cookie-bearing metadata before persistence | Companion credentials remain denied |
| Local logger | Inspect actual file logging; separately qualify real subprocess stderr discard before logging | No SDK/Go/browser complete-memory-zeroization claim |
| Diagnostic traces | Enable `redacted_content` capture, validate and inspect both actual turn traces | Recovery paths still require explicit inventory |
| Support bundles | Query both actual traces and generate metadata-only and private redacted-content bundles; scan every generated file | Export evidence covers the current typed diagnostics exporter |
| Opaque artifacts | Credential-bearing screenshot/download/unrestricted-execution admission denies | Enabling an artifact path requires its own qualification |
| Phone/grant lifetime | Actual virtual passkey approval/revoke and signed lease/component tests | Physical Android enrollment, notification delivery and personal-account pilot remain required |
| MFA/unconfirmed submission | Actual provider-backed turns, fixed outcomes, single-submit assertion and browser cleanup; two actual traces and four bundles per scenario | MFA continuation/private operator interaction remains unqualified |
| Credential runtime recreation | Two actual provider-backed browser turns for each qualified adapter, with the credential runtime closed/recreated from its owner store between turns; retained passkey, denied old lease/grant, fresh notification/assertion before another retrieval; inspect both traces/history/exports | Exercises component recreation inside the fixture, not an OS process crash |
| Cancel/expiry | Existing focused component tests | The complete serialized-output scenario matrix remains incomplete |

The tests report fixed stages and boolean assertions. They do not print a
detected canary, HTTP request body, vault error or child-process output.
State-directory contents intentionally used for private token storage are not
ordinary model/diagnostic outputs and must remain outside workspaces.

## Remaining rollout gates

Keep `credentials.browser_login_enabled` false until the enabled deployment's
provider, tracing, canonical/context, recovery/export, companion and artifact
paths have either passed their relevant qualification or been explicitly
denied. Qualify the intended browser placement and selected site's login/MFA
flow. Complete protected real-token setup and actual Android approval, login
and revocation for one selected account before expanding to additional sites.

The synthetic pipeline evidence does not establish Facebook, Craigslist,
eBay, SSO or multi-step form support. Reuse the existing application and vault;
none of these remaining checks requires reviving the retired appliance.
