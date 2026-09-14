# Browser Continuation Phase 1 Evidence

## Status

Browser continuation Phase 1, **Driver/Provider Seam and Conformance**, is
merged, deployed, live-validated, and complete. Phase 2, **Direct
Playwright-Library Driver**, is the next dependency-ordered phase.

The driver/provider implementation was validated on merge commit
`0278e5fbfedaca603646960789615bb9437bed60`. A production capacity defect found
during the companion matrix was corrected and the final gateway and companion
runtimes were deployed from merge commit
`2cf963ff729ea1735a593006863a408af3718c81`.

## Merged Work

| Pull request | Head | Merge commit | Outcome |
| --- | --- | --- | --- |
| [#1231](https://github.com/bogdanovich/mintclaw/pull/1231) | `6cba52b8` | `0278e5fb` | Added explicit private Playwright runtime-provider and control-driver seams, independent fakes, and conformance and lifecycle smoke suites without changing production behavior |
| [#1232](https://github.com/bogdanovich/mintclaw/pull/1232) | `586865a2` | `2cf963ff` | Preserved seven-day retained-state headroom and made state exhaustion a distinct bounded operator error |

Both code pull requests passed all ten required CI jobs. Automated review ran
against each exact head, reported no high-confidence issue, and supplied the
owner rocket approval before merge.

## Stable Seam

`PlaywrightWorkerFactory` now delegates two private responsibilities:

- the provider owns local runtime configuration, request proxy startup,
  ephemeral profile leases, and deterministic release; and
- the driver attaches browser control to the opaque runtime and returns the
  existing worker contract.

The opaque runtime handle is intentionally non-serializable and is deep-cloned
at the seam. Provider and driver failure paths preserve one cleanup owner for
creation failure, attach failure, partial startup, close failure, worker loss,
restart, and configuration replacement. The broker remains the only owner of
target, profile, session, policy, receipt, recovery, and freshness authority.

Production still selects `playwright_mcp` for both managed and ephemeral
profiles. No model-facing tool, action, result, approval, handoff, or node
protocol schema changed in this phase. The direct official Playwright-library
implementation remains Phase 2 work.

## Automated Validation

The merged implementation added focused provider lifecycle and driver
conformance tests, including runtime privacy and cleanup ownership. Existing
browser, browser-policy, gateway node-worker, companion browser-host, node
protocol, race, portability, and smoke-runner suites passed in CI.

The canonical smoke runner now admits `driver-conformance` and
`provider-lifecycle` in addition to the Phase 0 suites. Its self-tests verify
the exact result fields, required tool-call evidence, cleanup probes, timeout
and signal handling, and rejection of incomplete execution evidence.

## Live Matrix

All ten required gateway and Darwin companion cases passed on 2026-09-14:

| Target | Profile | Suite | Checks | Execution | Cleanup | Process audit | Duration |
| --- | --- | --- | ---: | --- | --- | --- | ---: |
| Gateway | `managed` | `core` | 4/4 | Passed | Clean | Passed | 90,399 ms |
| Gateway | `managed` | `managed-reuse` | 4/4 | Passed | Clean | Passed | 166,583 ms |
| Gateway | `ephemeral` | `ephemeral-cleanup` | 9/9 | Passed | Clean | Passed | 153,537 ms |
| Gateway | `managed` | `driver-conformance` | 4/4 | Passed | Clean | Passed | 100,435 ms |
| Gateway | `managed` | `provider-lifecycle` | 6/6 | Passed | Clean | Passed | 109,441 ms |
| Companion | `managed` | `core` | 4/4 | Passed | Clean | Passed | 188,275 ms |
| Companion | `managed` | `managed-reuse` | 4/4 | Passed | Clean | Passed | 286,603 ms |
| Companion | `ephemeral` | `ephemeral-cleanup` | 9/9 | Passed | Clean | Passed | 252,800 ms |
| Companion | `managed` | `driver-conformance` | 4/4 | Passed | Clean | Passed | 190,243 ms |
| Companion | `managed` | `provider-lifecycle` | 6/6 | Passed | Clean | Passed | 206,447 ms |

Every accepted report had a null safe error, verified its browser-child trace,
closed every opened session, stopped its fixture, and proved immediate process
reuse. One managed-reuse attempt was correctly rejected because its browser
child skipped the required post-seed observation. Its later verification stage
cleared the harmless marker and all sessions closed. One bounded rerun from
that clean state passed the complete contract; rejected evidence was not used
as proof.

## Production Capacity Correction

The first post-deploy companion attempt exposed a separate retained-state
capacity regression. The browser document had reached exactly 512 records
while all records were still inside the configured seven-day retention window:
168 sessions, 172 prepared actions, and 172 invocations. Discovery could read
the document, but a new session could not be persisted and the generic tool
projection obscured the cause.

The correction increased the bounded default record ceiling from 512 to 4,096
while retaining the existing 8 MiB byte ceiling and normal retention pruning.
It also maps store exhaustion to a secret-free `state_capacity` error with an
operator action. Deployment preserved every existing record. The final live
matrix grew the store to 558 records without an error, proving that the old
ceiling was crossed safely. The final store had zero nonterminal sessions and
zero nonterminal browser invocations.

## Deployment And Health

The final deployed identities are:

| Artifact | Revision or SHA-256 |
| --- | --- |
| Gateway source | `2cf963ff729ea1735a593006863a408af3718c81` |
| Gateway binary | `0a6c4c2bde2f69585a259ab3fa54418d9f7f6523d395bedaa5ce43b04e44a68b` |
| Darwin companion source | `2cf963ff729ea1735a593006863a408af3718c81` |
| Darwin companion binary | `94b7ede31bb2272a063efc1dedddd934355d0f645be2e99cb7ff85b118fc29f3` |
| Smoke runner | `628fb7d281e34e7c7d8d850a706b7dcdce6d44fcd1b7d401400d6bac9a030f3e` |
| Gateway configuration | `a9c85e91b9abba8391e624c7f9ade6cd119735c001811c3bc52f6876af75d0d8` |
| Darwin companion configuration | `8d22738931cb9f6365799c7058a6a16d2f7482530938f431396aac6166655261` |

Gateway and companion managed and ephemeral profile revisions remained
`managed-v1` and `ephemeral-v1`; no configuration cutover occurred. The main
gateway, web launcher, SSH forwarding path, and Darwin node were active after
validation. The companion remained connected with its approved catalog. No
failed user units, post-deploy error-level gateway entries, live Playwright MCP
workers, nonterminal companion invocation records, or prepared gateway node
invocations remained.

Safe JSON reports are retained in the operator evidence directories under the
Phase 1 merge label. They contain predicates and counts only; they omit page
content, cookies, storage values, credentials, endpoints, native identifiers,
and private runtime arguments.

## Rollback And Retention

The checksum-verified immediate recovery sets are:

```text
/home/server/mintclaw-recovery-browser-p1-pre-20260914T125800Z
/Users/ab/mintclaw-recovery-browser-p1-companion-pre-20260914T125800Z
/home/server/mintclaw-recovery-browser-state-capacity-pre-20260914T140500Z
/Users/ab/mintclaw-recovery-browser-state-capacity-companion-pre-20260914T140500Z
```

The later pair contains the exact pre-capacity-fix binaries, configuration,
browser state, node registry or invocation ledger, and service definitions.
The gateway SQLite backup used the database backup API. Rollback must restore
matching binaries and state only after stopping the affected service and must
not overwrite newer mutable browser or invocation state without a forward
recovery review.

## Phase Conclusion

Every Phase 1 acceptance criterion is satisfied. Runtime provisioning and
browser control are separated without a plugin registry or duplicated broker
authority; lifecycle and failure ownership are independently testable; opaque
runtime state remains private; existing Playwright MCP behavior matches the
Phase 0 baseline on gateway and companion; and the discovered production
capacity defect is corrected and live-proven. Phase 2 may now implement the
small MintClaw-owned official Playwright-library sidecar while retaining this
MCP driver as the rollback path.
