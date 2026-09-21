# Browser Continuation Phase 3 Evidence

## Status

Browser continuation Phase 3, **Privileged Browser Execution**, is merged,
deployed, live-validated, and complete. Phase 4, **Steel Cloud Provider**, is
the next dependency-ordered phase.

The initial capability was admitted on merge commit
`7fdf5e5bd6cb6bc5240971ca11aeb1b5159bae14`. Seven focused source-protection,
runtime, evidence, artifact, and companion-settlement follow-ups were merged
before closeout. The final source and deployment baseline is
`f9703ca352e459dfa95baa7a538af6897c01681e`.

## Merged Work

| Pull request | Head | Merge commit | Outcome |
| --- | --- | --- | --- |
| [#1259](https://github.com/bogdanovich/mintclaw/pull/1259) | `233f9549` | `7fdf5e5b` | Added opt-in first-party `browser_execute`, durable digest and authority binding, the restricted execution worker, gateway and companion routing, and the canonical smoke suite |
| [#1265](https://github.com/bogdanovich/mintclaw/pull/1265) | `bdcf8c0e` | `5d5ac5b0` | Delimited canonical smoke sources and verified that delegated agents copy exact source without prose punctuation |
| [#1269](https://github.com/bogdanovich/mintclaw/pull/1269) | `8d768532` | `809fd910` | Ran serialized locator callbacks in the page realm and preserved typed runtime timeout semantics across gateway and companion |
| [#1281](https://github.com/bogdanovich/mintclaw/pull/1281) | `a9ef3138` | `084ffeb6` | Retained protected source only in the active in-memory turn while durable state and diagnostics keep only its digest |
| [#1285](https://github.com/bogdanovich/mintclaw/pull/1285) | `c9a20b2a` | `790da5bc` | Accepted typed timeout evidence and terminal lost-session status in the canonical smoke contract |
| [#1287](https://github.com/bogdanovich/mintclaw/pull/1287) | `5f42eadd` | `0bfbb9ea` | Accepted the companion-host-minted opaque artifact transfer binding while preserving independent gateway ownership and all authority checks |
| [#1288](https://github.com/bogdanovich/mintclaw/pull/1288) | `b54b7ec3` | `b52fc62d` | Added query-only terminal settlement when the companion timeout and transport deadline coincide |
| [#1289](https://github.com/bogdanovich/mintclaw/pull/1289) | `12ca0855` | `f9703ca3` | Sized settlement for dispatch skew, polled transient missing terminal records, and proved that ephemeral source is never redispatched |

Every code pull request passed the complete ten-job CI workflow and automated
review against its exact head before merge. The final review found no
high-confidence issue and explicitly confirmed that settlement remains
query-only and bounded by validated runtime authority.

## Shipped Contract

The browser specialist continues to use MintClaw-owned tools. The new escape
hatch is a separate first-party tool rather than raw Playwright, MCP, CDP, or
provider administration:

```text
browser specialist
    |
    v
browser_execute prepare and execute contract
    |
    +-- exact owner, target, profile, policy, document, and source digest
    +-- configured effect, approval mode, and execution budgets
    +-- durable accepted and terminal-or-unknown invocation state
    |
    v
selected Playwright-library sidecar
    |
    v
isolated worker-thread VM -> bounded RPC facade -> selected page
```

Preparation binds the exact actor and agent owner, target and profile revision,
policy and controller revisions, tab and fresh snapshot authority, current
origin and network mode, source digest and byte length, language, declared
effect, approval mode, dry-run state, and every execution limit. Dispatch must
provide source matching that digest and a matching approval binding when the
effective profile policy requires one. A stale or changed binding fails closed.

Source is protected input. It remains available only during the active turn
and crosses the companion boundary as ephemeral invocation input. Durable
browser, gateway, node, audit, and diagnostic records retain the digest and
safe metadata, not source text. Once accepted, an execution is never
automatically replayed. Recovery returns its stored terminal or explicit
unknown outcome.

The Playwright-library sidecar creates a dedicated Node.js worker thread and a
contextified VM with string and WebAssembly code generation disabled. Submitted
JavaScript or TypeScript receives only frozen `page`, `context`, and `artifacts`
facades. Browser operations cross an RPC boundary back to the sidecar, which
enforces effect-specific operations, origin policy, network requests, action
count, output bytes, artifact count and bytes, runtime, memory, and single-
execution concurrency independently of submitted code.

The facade does not expose host `process`, filesystem access, imports, ambient
environment, provider or node credentials, browser endpoints, profile paths,
or raw artifact paths. Screenshots become retained first-party artifacts. A
timeout terminates and quarantines the worker and browser session before
cleanup, so later work cannot reuse uncertain page state.

## Effective Production Authority

The owner-operated `managed` profiles on gateway and companion intentionally
enable the feature. Their safe effective facts at closeout were:

| Setting | Gateway | Companion |
| --- | --- | --- |
| Profile revision | `managed-library-v3` | `managed-library-v3` |
| Driver | Selected direct Playwright-library driver | `playwright_library` |
| Network mode | `any_http` | `any_http` |
| Capability mode | `full_access` | `full_access` |
| Approval mode | `model_requested` | `model_requested` |
| Dry run | `false` | `false` |
| Runtime | 15 seconds | 15 seconds |
| Output | 65,536 bytes | 65,536 bytes |
| Actions | 64 | 64 |
| Memory | 64 MiB | 64 MiB |
| Network requests | 64 | 64 |
| Artifacts | 4, 8 MiB total | 4, 8 MiB total |
| Concurrent executions | 1 | 1 |

These settings are explicit deployment authority, not product defaults. New
profiles do not advertise `browser_execute` unless privileged execution is
enabled for their exact revision. Restricted capability and policy-hook modes
remain available; changing authority requires configuration and revision
revalidation.

## Automated Validation

The admitted sequence added or extended coverage for:

- configuration defaults, maxima, revision changes, capability discovery, and
  approval-mode inheritance;
- source-digest binding, protected-input persistence and trace redaction,
  prepared invocation expiry, idempotent recovery, and no blind replay;
- stale document, context, profile, policy, actor, agent, and target authority;
- JavaScript and TypeScript execution, locators, page-realm evaluation,
  structured values, screenshots, and JSON serialization;
- host-global, constructor-chain, environment, import, filesystem, process,
  endpoint, and credential isolation;
- effect-specific operation denial and runtime, output, action, memory,
  network, artifact, and concurrency limits;
- timeout, cancellation, sidecar death, companion disconnect, durable unknown
  outcomes, quarantine, and cleanup;
- gateway and node policy parity, ephemeral source transport, opaque artifact
  binding, and reverse artifact transfer; and
- canonical smoke self-tests for exact source, exact tool counts, typed timeout,
  terminal cleanup, immediate reuse, malformed output, timeout, signal, and
  private-path leakage.

The final transport-skew regression delays visibility of the companion's
durable `COMMAND_TIMEOUT` beyond the former fixed settlement window. The
gateway polls only the already accepted invocation until the configured remote
runtime plus bounded transport grace expires. The test asserts that source is
invoked exactly once.

## Final Production Matrix

The final `privileged-execute` reports were produced against exact merged
source `f9703ca352e459dfa95baa7a538af6897c01681e`:

| Target | Profile | Checks | Primary execution evidence | Cleanup audit | Immediate reuse | Safe error | Duration | Report SHA-256 |
| --- | --- | ---: | --- | --- | --- | --- | ---: | --- |
| Gateway | `managed` | 8/8 | 1 delegation; `browser_targets` 1, `browser_session` 2, `browser_observe` 2, `browser_act` 1, `browser_execute` 3 | Verified and clean | `true` | `null` | 156,563 ms | `e997f40da9d053db1714f7c416d9e604d85260ced9cb3f0dfcc431ec0a75a665` |
| Companion | `managed` | 8/8 | 1 delegation; `browser_targets` 1, `browser_session` 2, `browser_observe` 2, `browser_act` 1, `browser_execute` 3 | Verified and clean | `true` | `null` | 247,247 ms | `b313073d0e08f9cc040c5c94f528fa17da6e784bd0786d6476415750bcd2a25b` |

Each report passed `initial_blank`, `navigated_fixture`,
`structured_extraction`, `reversible_dom_restored`, `artifact_retained`,
`sandbox_denial`, `runtime_timeout`, and `cleanup_after_timeout`. The third
execution intentionally exceeded the 15-second runtime and returned the typed
timeout outcome without retry. The independent cleanup delegation then opened,
observed, and closed a fresh session, proving immediate profile reuse.

The gateway fixture ran on the gateway host; the companion fixture ran on the
companion host. An operator attempt that advertised a Mac loopback fixture to
the gateway was correctly rejected as `destination_unavailable` and was not
accepted as evidence. Passive traces proved that the failed attempt never
reached `browser_execute`, and its independent cleanup audit closed the opened
session.

The accepted reports are retained in owner-only evidence directories:

```text
/home/server/.mintclaw/main/evidence/browser-continuation-phase3-f9703ca3/
/Users/ab/.mintclaw/evidence/browser-continuation-phase3-f9703ca3/
```

They contain safe predicates, counts, durations, and hashes, not source text,
page content, credentials, cookies, storage state, endpoints, native browser
IDs, or private driver arguments.

## Live Corrections

Production validation exposed four general contract issues:

- protected source needed an active-turn-only value path instead of durable
  tool history;
- companion screenshots used a valid host-minted opaque transfer binding that
  must remain distinct from the gateway's artifact owner invocation;
- a companion could durably record `COMMAND_TIMEOUT` just after the gateway
  transport context expired; and
- real dispatch latency meant a fixed three-second settlement window could end
  before the companion's separately enforced runtime and durable record.

The corrections preserve all authority fields, keep source protected, and use
query-only reconciliation. They do not broaden browser access, relax artifact
ownership, or permit source replay. The accepted live matrix passed only after
all four corrections were merged and deployed.

## Deployment And Health

| Artifact | Revision or SHA-256 |
| --- | --- |
| Gateway and companion source | `f9703ca352e459dfa95baa7a538af6897c01681e` |
| Gateway binary | `ea08dd327f020c887fbdf94c3c21c27af818066650d619fd7f75576b0bd4dc2b` |
| Darwin companion binary | `44e1571946320db7087ea0bda8459f7442aeeb90dcdaeede37c93900b939811e` |
| Playwright-library sidecar | `cfaba2cfe0764b52a3d6e0c1cd73b19107577e452e639f86e5fe36fa9dee8e8e` |
| Privileged execution worker | `1725b923e206ffe78ea4d13402d73fce1046523c2f74ac08bdc1e1045ef546cb` |
| Browser smoke runner | `9d243aae81a295b47d155056bf420bb50a46b998b8be3b0ce5e6e7d2436fd2f8` |
| Smoke self-test | `7db675e74eac3211d20aa2baf4d9458bae4b94e7c54f19b3c25885ec0c5833a7` |
| Gateway configuration | `1c49c2225ff2ae1e6cc93aecd337876aaaaa263ee3cc6d41038cb4bc6a169415` |
| Darwin companion configuration | `99278ff7dba5b4adf1c0c1e06d071179dddcfce18fb507f0d948bd2ccaa49e74` |

After the final smoke matrix, every expected MintClaw service was active, no
product or system unit was failed, the companion LaunchAgent was running, the
gateway reported no error-level journal entry in the deployment window, and
the stack reported no error-level entry in the final ten-minute window. No
fixture, privileged execution worker, sidecar, profile lock, or live smoke
session remained. Gateway and companion report immediate reuse.

## Rollback And Retention

The checksum-verified immediate pre-deployment recovery sets are:

```text
/home/server/mintclaw-recovery-browser-phase3-settlement-pre-20260921T0004Z
/Users/ab/mintclaw-recovery-browser-phase3-settlement-pre-20260921T0004Z
```

The gateway set contains the effective and CLI binaries, main configuration,
run script, service unit, source identity, and service state. The companion set
contains its effective binary, local configuration, LaunchAgent definition,
source identity, and service state. Both sets include verified SHA-256
manifests.

The first rollback is configuration-only: disable `privileged_execution` on
the affected exact profiles, increment their revisions, restart only the
affected gateway or companion service, and verify that `browser_targets` no
longer advertises the capability. A binary rollback must restore the matching
binary and configuration from the recovery set while the affected service is
stopped. It must not overwrite newer mutable browser profile or invocation
state without a separate recovery review.

## Phase Conclusion

Every Phase 3 acceptance criterion is satisfied. `browser_execute` is an
explicit first-party escape hatch with exact profile authority, configurable
approval, source-digest binding, protected ephemeral source, externally
enforced limits, restricted browser-only execution, retained artifacts,
durable terminal-or-unknown outcomes, and no replay. Gateway and companion
independently passed structured extraction, reversible DOM work, sandbox
denial, artifact retention, typed timeout, cleanup, and immediate reuse on the
same final merged revision. Phase 4 may now add the separately configured Steel
cloud provider through the existing provider and driver seams.
