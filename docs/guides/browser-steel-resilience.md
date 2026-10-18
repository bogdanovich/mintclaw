# Steel fallback and spending controls

MintClaw can switch an ordinary top-level `browser_act` navigation from a local
target to an explicitly authorized Steel profile. The source navigation must
have completed and a fresh observation must contain specific anti-bot evidence.
HTTP status alone, missing inventory, login pages, and worker loss do not qualify.

## Configuration

Keep the existing cloud target, its private API key in `.security.yml`, and its
managed provider state/lock files. Add these public settings to its `steel`
object:

```json
{
  "concurrency": 1,
  "session_timeout_seconds": 300,
  "inactivity_timeout_seconds": 60,
  "max_billable_seconds": 180,
  "use_proxy": false,
  "proxy_on_block": true,
  "proxy_country": "US",
  "solve_captcha": true,
  "budget": {
    "max_sessions_per_day": 3,
    "max_billable_seconds_per_day": 900,
    "max_session_cost_usd": 0.25,
    "max_daily_cost_usd": 0.75
  }
}
```

Add this object to each local target that may recover to that cloud profile:

```json
{
  "fallback": {
    "target": "cloud",
    "profile": "personal",
    "remember_seconds": 86400
  }
}
```

These are object fragments, not complete replacement configurations. Keep
existing actor/agent grants, network policy, driver configuration, and profiles.
Changing Steel options requires a new managed profile revision on a live reload.
An API key alone does not grant access to a cloud profile: both source and
destination must authorize the requesting actor and agent.

With `proxy_on_block: true`, Steel starts without a proxy. Only a fresh,
confirmed anti-bot block on that direct Steel navigation permits one new Steel
session with a residential proxy. Supported CAPTCHA solving gets its bounded
passive wait before this transition. The direct session closes and its profile
must finish persisting before the proxied session starts. Both modes share the
same cookie profile, exclusive lease, daily ledger, and cost thresholds; they
consume two session reservations when both are used. A remembered successful
domain still tries direct Steel first in a later task.

`browser_targets` exposes `provider: "steel"` and an explicit `steel` object
with `use_proxy` and `proxy_on_block`, including false values. For example,
`{"use_proxy": false, "proxy_on_block": true}` means direct first, proxy only
after confirmed blocking. Open the configured target/profile normally; these
settings are not per-call toggles. The profile's `network_boundary` describes
policy enforcement (`remote_request_routes` for Steel), and `readiness.proxy`
reports policy readiness. Neither field indicates a paid Steel proxy.

`use_proxy: true` selects always-on proxies instead. These two modes are mutually
exclusive. `proxy_country` applies to whichever proxy mode is enabled. Without
either mode, Steel uses its datacenter IP, not the local machine's home IP.
The recovery result reports `use_proxy: true` when the final session uses a
proxy. A proxy requires a new session; a live session's IP is not changed.

The initial recovery implementation admits full-access profiles only and
excludes attached user browsers. A restricted profile must continue using its
explicit policy workflow. Recovery prepares a new navigation under the Steel
profile's policy; it does not carry an old approval into the new session.

Companion targets also carry this evidence over the authenticated node browser
protocol. Upgrade their node runtime and direct Playwright sidecar before
expecting challenge detection. Legacy peers can still return ordinary snapshots
without navigation evidence.

## Cost limits

Budgets apply **per configured Steel profile**, using UTC calendar days. Session
count and reserved maximum browser seconds are hard admission limits. Before
creation, MintClaw reserves one session, its entire configured maximum duration,
and the session's USD threshold against the daily allowance. Reservations are
never refunded, including failed or interrupted creations. The private ledger
is stored beside provider state as `<provider_state_file>.usage.json`. Corruption
or unavailable storage refuses new paid sessions.

USD settings are **reported-cost stop and admission thresholds**, not hard Steel
billing caps. MintClaw checks Steel's exact session cost every five seconds,
stops a session when the threshold is reached, and stops on missing cost
evidence. Reporting delay, in-flight traffic, and remote release latency can
cause overshoot. Failed release retains cleanup ownership and is retried.
Steel's provider timeout remains an independent hard lifetime limit.
If a budget stop cannot capture the live profile revision, billing is stopped
but the profile remains fenced for explicit recovery; a stale READY snapshot
cannot prove that the latest cookies were persisted.

For the initial trial, use a small prepaid Steel balance and disable automatic
top-up. On Launch, Steel requires a paid deposit to enable its managed proxies
and CAPTCHA service; free credits alone do not enable those features. See
[Steel pricing and limits](https://docs.steel.dev/overview/pricinglimits).

## Recovery and reuse

- The direct Playwright sidecar recognizes specific Cloudflare, DataDome,
  PerimeterX, and Imperva response/DOM evidence. Detection does not imply that
  Steel can solve every challenge. See [Steel CAPTCHA
  support](https://docs.steel.dev/overview/stealth/captcha-solving).
- Only `browser_act` with `action.kind=navigate` participates. Clicks, forms,
  uploads, downloads, privileged `browser_execute` scripts, and uncertain
  outcomes are never replayed. Prefer `browser_act` for initial site access.
- The broker records each attempt before closing its source session and creates
  at most one direct Steel recovery, followed by at most one proxied recovery
  when conditional proxies are configured and direct Steel is still blocked.
  Its result includes `recovery` and a fresh
  observation with a new `browser_session_id`. Use those new identities.
- A domain preference is recorded only after a successful HTTP navigation with
  no detected challenge. It is scoped to the exact hostname, actor, agent,
  target/profile, and policy/profile revisions. It is retained in the existing
  bounded browser journal; at most 16 successes are recorded per session.
- On a later task, the first local navigation still completes before MintClaw
  applies a remembered preference. Subsequent work uses the live Steel session.
  Set `remember_seconds` to zero to recover only from current challenge evidence.
- Steel profiles preserve provider-managed cookies across sessions. Profiles
  remain exclusive; local cookies are not copied to Steel. Keep the same live
  provider session throughout a task. Steel ordinarily assigns a new proxy IP
  for a new session, so profile reuse does not promise the same IP across tasks.
  See [Steel proxy behavior](https://docs.steel.dev/overview/stealth/proxies).

## Qualification

Run the focused broker/config tests and the real-browser evidence fixtures:

```sh
go test -tags goolm,stdjson ./pkg/config ./pkg/browser ./pkg/tools ./pkg/gateway
make install-browser-playwright-library
MINTCLAW_BROWSER_TEST_EXECUTABLE=/path/to/chrome \
  node --test runtime/browser/playwright-library/navigation-evidence.test.cjs
```

The evidence test opens only localhost fixtures and creates no Steel session.
After review and deployment, qualify ordinary local access, one confirmed
challenge, successful Steel recovery, profile reuse, and exhausted-budget
behavior together with the account owner. Password onboarding is separate.
