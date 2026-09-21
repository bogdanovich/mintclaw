# Browser Continuation Phase 4 Evidence

## Status

Browser continuation Phase 4, **Steel Cloud Provider**, is implemented,
merged, deployed to the production gateway, and live-accepted on the exact
deployed source. The remote-coding v2 configuration and durable state were
migrated through their supported boundary before deployment, so the earlier
rollout gate is closed. Phase 4 is complete, and Phase 5 is the next pending
roadmap phase.

The provider was admitted on merge commit
`23d72c0b21162a23814844625e8910c9f9059ae8`. Nine focused lifecycle, profile,
live-client, timeout, and execution-evidence corrections followed. The current
merged Phase 4 source is `b7293aa01cb29c543db1668034d1484d7727bf59`.

## Merged Work

| Pull request | Head | Merge commit | Outcome |
| --- | --- | --- | --- |
| [#1299](https://github.com/bogdanovich/mintclaw/pull/1299) | `28a2a353` | `23d72c0b` | Added the disabled-by-default Steel provider, opaque cloud profiles, private provider identity, deterministic release, and three billable smoke suites |
| [#1306](https://github.com/bogdanovich/mintclaw/pull/1306) | `e48b9e5c` | `4f4e199d` | Validated the remote Playwright runtime configuration and corrected cloud navigation dispatch |
| [#1307](https://github.com/bogdanovich/mintclaw/pull/1307) | `f8db9b97` | `3b53137c` | Bounded the profile verification prompt and prohibited preparatory cloud sessions and retries |
| [#1309](https://github.com/bogdanovich/mintclaw/pull/1309) | `ba8f7e79` | `02fd5a2f` | Waited for provider profile persistence before reporting a clean close |
| [#1310](https://github.com/bogdanovich/mintclaw/pull/1310) | `1941f378` | `09674aad` | Gated release on the live provider profile revision and failed closed when persistence was uncertain |
| [#1311](https://github.com/bogdanovich/mintclaw/pull/1311) | `6c8be643` | `b1cc3013` | Added deterministic same-profile cookie reuse and cleanup proof |
| [#1315](https://github.com/bogdanovich/mintclaw/pull/1315) | `61298b4c` | `e2743eb1` | Removed the live-client automatic-answer admission race |
| [#1316](https://github.com/bogdanovich/mintclaw/pull/1316) | `05b7fe53` | `dd96db51` | Allowed bounded Steel cold launches by aligning the provider API deadline with the configured session lifecycle |
| [#1317](https://github.com/bogdanovich/mintclaw/pull/1317) | `c9f284fd` | `7c8239e6` | Stitched one fail-closed handoff continuation into execution evidence and rejected ambiguous or incomplete continuations |
| [#1319](https://github.com/bogdanovich/mintclaw/pull/1319) | `662457f0` | `b7293aa0` | Recovered the admitted parent when a resumed `user_only` final carried the child continuation scope and waited for asynchronous trace persistence |

Every code pull request passed its required CI and exact-head automated review
before merge. The final review found no high-confidence issue and confirmed
that child-scoped recovery is uniquely linked, bounded, retry-aware, and still
fail-closed on ambiguity.

## Shipped Contract

The browser specialist continues to see the same MintClaw-owned first-party
tools. Steel is a provider behind the existing target, profile, provider, and
driver seams; it is not a model-facing API or MCP server.

```text
browser specialist
    |
    v
first-party browser tools and existing authority contract
    |
    v
cloud target -> Steel provider -> private CDP connection
    |
    v
Playwright-library driver -> selected cloud browser
```

The provider creates a bounded interactive session, connects the existing
Playwright-library driver, exposes ordinary observe, action, capture, handoff,
resume, and close behavior, and releases provider resources deterministically.
Provider API keys, CDP and live-view URLs, session and profile IDs, cookies,
page content, and driver arguments remain runtime-private.

Persistent cloud identity is selected through an operator-defined opaque
MintClaw profile alias. Visible human login can use the existing handoff and
resume lifecycle; no site password, OTP, cookie, or credential is injected
through the model. Provider failure never falls back to gateway or companion.

## Accepted Test Authority

The production acceptance configuration explicitly enabled one `cloud` target
and one persistent `personal` profile with these safe effective facts:

| Setting | Effective value |
| --- | --- |
| Placement | `cloud` |
| Provider | `steel` |
| Driver | `playwright_library` |
| Profile revision | `steel-personal-v2` |
| Profile mode | `managed` |
| Allowed agent | `browser` |
| Allowed actors | Two exact owner actors |
| Network mode | `any_http` |
| Capability mode | `full_access` |
| Approval mode | `none` |
| Dry run | `false` |
| Provider concurrency | 1 |
| Session timeout | 300 seconds |
| Inactivity timeout | 60 seconds |
| Maximum billable lifetime | 180 seconds |

These values are explicit owner-operated acceptance authority, not product
defaults. Steel remains disabled by omission. The API key was loaded through
the existing owner-readable `SecureString` file reference and never supplied
to a model, command-line argument, report, trace, log excerpt, test fixture, or
repository file.

## Automated Validation

The merged sequence added or extended coverage for:

- disabled-by-default configuration, exact actor and agent grants, safe
  discovery, profile revision, limits, and secret redaction;
- create, connect, observe, action, capture, handoff, resume, close, release,
  timeout, cancellation, provider outage, quota, and profile-not-ready states;
- provider and driver identity separation with no silent local fallback;
- deterministic release after success, connect failure, driver failure,
  cancellation, abandonment, maximum lifetime, and process shutdown;
- provider profile creation, revision binding, persistence readiness, reuse,
  and explicit non-persistence failure;
- safe artifact retention without provider endpoints, IDs, or personal page
  content;
- exact one-answer live interaction behavior and durable same-session resume;
- bounded execution-evidence correlation across a suspended child and one
  completed continuation; and
- billable smoke opt-in, safe report schema, cleanup audit, process audit,
  signal handling, timeout, and malformed or private output rejection.

The final regression models the actual resumed delivery: the authoritative
final carries the browser continuation scope, not the original parent scope.
The collector resolves that exact child trace, requires one preceding suspended
child with the same durable session, follows its parent and child turn IDs to
exactly one admitted delegation, waits for asynchronous persistence, and
rejects missing, multiple, failed, incomplete, or unpaired continuations.

## Live Acceptance Matrix

The accepted reports are retained in:

```text
/home/server/.mintclaw/main/evidence/browser-continuation-phase4-23d72c0b/
```

| Suite | Reviewed source | Checks | Cleanup | Process audit | Execution audit | Artifacts | Safe error | Duration | Report SHA-256 |
| --- | --- | ---: | --- | --- | --- | ---: | --- | ---: | --- |
| `steel-cloud` | `b7293aa0` | 5/5 | Clean | Passed | Passed | 1 screenshot | `null` | 156,440 ms | `41b34b39f62008acecac6f162abcb75090c2892d37a529ece0c2978386070819` |
| `steel-profile-reuse` | `b7293aa0` | 4/4 | Clean | Passed | Passed | 0 | `null` | 259,676 ms | `42d1002a227b269e52ed95d37c4506f423f64bed92e0f03044e9d3668ed2d181` |
| `steel-handoff` | `b7293aa0` | 6/6 | Clean | Passed | Passed | 0 | `null` | 160,451 ms | `f322317d66290dcc5a4abf371c78fcb190e451fabf64fb700ef7370e5a690f72` |

`steel-cloud` proved initial blank state, public fixture navigation, one
reversible action, fresh observation, retained screenshot, and clean release.
`steel-profile-reuse` created one provider profile, persisted a same-origin
cookie marker, released it, reopened the same opaque alias, observed the marker,
cleared it, and released again. This proves the current cookie-based identity
canary; it does not claim that every origin or every browser storage mechanism
has identical provider persistence behavior.

`steel-handoff` opened and navigated one cloud session, paused through the
ordinary durable question interaction, accepted one non-sensitive automatic
test answer, resumed the exact browser session, made a fresh observation, and
closed it. Its independent cleanup delegation then opened, observed, and
closed a new session. The final execution audit verified exactly one admitted
browser delegation and the ordered `open`, `handoff`, `resume`, and `close`
operations.

The first production handoff attempt hit the provider control plane's bounded
close-verification deadline. The explicit close failed closed with
`cleanup_required`, terminal owner cleanup retried the retained runtime, and a
provider audit confirmed that the session was released. The diagnostic report
is retained separately from accepted evidence. A clean rerun produced the
accepted handoff report above.

After the final report, a provider-side audit returned 62 released sessions,
zero live sessions, and zero failed sessions. The host process audit found no
Playwright-library or Steel sidecar. The production gateway remained healthy.

## Live Corrections

Acceptance exposed general lifecycle issues rather than site-specific fixes:

- remote cloud launch needed an explicit Playwright-library runtime and the
  same validated navigation payload as local placements;
- provider profile state needed a readiness barrier before release and reuse;
- a cold provider launch could legitimately exceed a hard-coded 20-second SDK
  deadline while still remaining inside configured lifecycle limits;
- automatic question answers could race durable interaction admission;
- execution evidence initially ignored a suspended child continuation; and
- after that was corrected, the collector still assumed the final trace scope
  belonged to the parent even though resumed `user_only` delivery correctly
  carries the child continuation scope.

The fixes preserve exact authority, deterministic release, bounded billing,
single continuation semantics, and private provider identity. They do not add
fallback, replay, raw provider access, or broader model authority.

## Production Rollout

The production deployment completed the previously documented remote-coding
v2 migration before installing exact merged source
`b7293aa01cb29c543db1668034d1484d7727bf59`. The running gateway binary reports
`b7293aa0`. The cloud target is gateway-hosted, so Phase 4 required no separate
companion provider process or companion credential.

Post-deployment validation confirmed gateway health, the expected enabled
cloud target and opaque profile alias, all relevant user services active, no
failed unit, and no orphaned browser sidecar. The three billable suites in the
matrix then ran through the production gateway. The temporary public fixture
was stopped afterward, and the normal authenticated static service was
restored; its local and externally forwarded paths both reject unauthenticated
requests with HTTP 401.

## Rollback And Temporary Evidence

The immediate feature rollback is configuration-only: remove or disable the
cloud target, increment affected profile revisions, restart the gateway only
after configuration validation, and verify that `browser_targets` no longer
advertises the cloud profile. A binary rollback must restore the matching
binary and configuration together. It must not overwrite newer provider
profile state, browser state, or remote-coding durable state without a separate
recovery review.

The accepted production reports are owner-readable files at the evidence root.
Earlier isolated reports are retained under its owner-only `isolated`
subdirectory, and the first production handoff failure is retained under its
owner-only `failed` subdirectory. Neither is completion evidence. The isolated
gateway and all temporary acceptance resources have been removed.
