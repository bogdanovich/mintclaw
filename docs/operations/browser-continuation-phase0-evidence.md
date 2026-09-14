# Browser Continuation Phase 0 Evidence

## Status

Browser continuation Phase 0, **B4 Closeout and Smoke Baseline**, is merged,
deployed, live-validated, and complete. Phase 1, **Driver/Provider Seam and
Conformance**, is the next dependency-ordered phase.

The final canonical matrix used smoke-runner source from merged `main` commit
`5d1c8ab22ad8c9dfbaf46b654ac8a2a3cfd48cc9`. The gateway and Darwin companion
runtime binaries were built from merged `main` commit
`7ce75adc72896ba80263ffe6a13a4c5fc6c75a53`, the last Phase 0 commit that
changed runtime code. The final smoke-runner commit changed scripts and tests
only, so it did not require a runtime restart.

This record closes Phase 0 in
[Browser Capability Continuation Execution Goal](../architecture/browser-continuation-execution-goal.md).
The superseded B4 goal already records managed and ephemeral profiles as
complete and attached-user work as deferred.

## Merged Work

The baseline and its live closeout fixes were delivered as focused autonomous
pull requests:

| Pull request | Merge commit | Outcome |
| --- | --- | --- |
| [#1176](https://github.com/bogdanovich/mintclaw/pull/1176) | `a1f93658` | Added the canonical operator smoke runner and deterministic fixture |
| [#1187](https://github.com/bogdanovich/mintclaw/pull/1187) | `c34b5ec8` | Bound execution evidence to the admitted browser child |
| [#1190](https://github.com/bogdanovich/mintclaw/pull/1190) | `ee68806b` | Split multi-session suites into deterministic stages |
| [#1194](https://github.com/bogdanovich/mintclaw/pull/1194) | `cd510bd0` | Required one synchronous browser delegation |
| [#1198](https://github.com/bogdanovich/mintclaw/pull/1198) | `5207e8fc` | Validated structured result output against the live trace |
| [#1217](https://github.com/bogdanovich/mintclaw/pull/1217) | `575c8e54` | Admitted read-only context inspection in smoke evidence |
| [#1220](https://github.com/bogdanovich/mintclaw/pull/1220) | `fd0969a8` | Required fresh, non-invented browser action authority |
| [#1222](https://github.com/bogdanovich/mintclaw/pull/1222) | `394c19a7` | Removed non-browser delegation alternatives from the live request |
| [#1223](https://github.com/bogdanovich/mintclaw/pull/1223) | `a850ffa5` | Added bounded recovery for invalid model action projections |
| [#1225](https://github.com/bogdanovich/mintclaw/pull/1225) | `55fcff6e` | Stabilized the boolean predicate result contract |
| [#1226](https://github.com/bogdanovich/mintclaw/pull/1226) | `7ce75adc` | Migrated the companion invocation ledger without losing terminal history |
| [#1229](https://github.com/bogdanovich/mintclaw/pull/1229) | `5d1c8ab2` | Made strict navigate actions deterministic across Darwin and Linux shells |

The final code pull request passed all 10 CI jobs. Automated review ran against
the exact head, reported no high-confidence issue, produced no review thread,
and supplied the owner rocket approval before merge.

## Stable Contract

The operator entry point is:

```text
scripts/browser-capability-smoke.sh \
  --target <gateway|companion|cloud> \
  --profile <safe-alias> \
  --suite <suite-name> \
  --json-output <path>
```

Phase 0 admits `core`, `managed-reuse`, and `ephemeral-cleanup` for gateway and
companion. Each live stage asks the main agent for exactly one synchronous
delegation to the browser specialist. The child uses only the first-party
browser tools, while companion runs additionally cross the authenticated
gateway broker and node worker boundary.

Reports use schema `mintclaw.browser_smoke.v1`. The runner validates both the
child's exact structured predicate record and passive execution evidence. It
fails closed on a missing or renamed field, non-boolean predicate, missing
trace, wrong child, wrong placement, unexpected mutating tool, failed cleanup,
or surviving process. Reports contain capability and predicate facts, call
counts, cleanup state, duration, and a bounded safe error; they omit page
content, storage values, credentials, cookies, endpoints, native identifiers,
and private runtime paths.

## Live Matrix

All six canonical cases passed on 2026-09-14:

| Target | Profile | Suite | Checks | Execution | Cleanup | Process audit | Duration |
| --- | --- | --- | ---: | --- | --- | --- | ---: |
| Gateway | `managed` | `core` | 4/4 | Passed | Clean | Passed | 90,396 ms |
| Gateway | `managed` | `managed-reuse` | 4/4 | Passed | Clean | Passed | 158,557 ms |
| Gateway | `ephemeral` | `ephemeral-cleanup` | 9/9 | Passed | Clean | Passed | 141,526 ms |
| Companion | `managed` | `core` | 4/4 | Passed | Clean | Passed | 184,501 ms |
| Companion | `managed` | `managed-reuse` | 4/4 | Passed | Clean | Passed | 276,470 ms |
| Companion | `ephemeral` | `ephemeral-cleanup` | 9/9 | Passed | Clean | Passed | 245,268 ms |

Every report advertised `observe`, `navigate`, and `click`; had no safe error;
verified the expected browser tool counts; closed every opened session; stopped
the fixture; and proved immediate process reuse. Managed reuse started clean,
persisted the harmless marker across sessions, then removed it. Ephemeral
cleanup started clean, seeded cookie, local-storage, cache, and service-worker
markers, then proved that a new session could observe none of them.

## Failure And Cleanup Coverage

`make test-browser-smoke` passed locally and in the final Linux CI run. Its
real runner/fixture process tests cover successful suites, a failed predicate,
a failed first stage, malformed capabilities, missing or invalid structured
output, missing or mismatched execution evidence, a hard timeout whose child
ignores termination, and termination of an active runner. Timeout and signal
cases verify that both the live client and fixture process are gone. Invalid
cloud billing and fixture-origin requests fail before work starts.

The live matrix then exercised the same cleanup contract against real
Playwright workers on both placements. No live suite required an unbounded
retry or manual cleanup.

## Deployment And Discovery

Production discovery contains exactly `managed` and `ephemeral` for both the
gateway and companion targets. It does not advertise `attached_user`. The
dormant attached implementation remains in the repository and can be
re-admitted later; no extension or attached browser runtime was installed.

The safe deployment identities are:

| Artifact | Revision or SHA-256 |
| --- | --- |
| Smoke-runner source | `5d1c8ab22ad8c9dfbaf46b654ac8a2a3cfd48cc9` |
| Smoke-runner file | `f168b99ccdd3cfeec34107c2a8d4b5c3b13d0af2e1ad86813931abfbf385c6ed` |
| Gateway runtime source | `7ce75adc72896ba80263ffe6a13a4c5fc6c75a53` |
| Gateway binary | `42f3e486f1b8adf1e2e153d5ee7ee12e7f5f0b940b9ff295619b4ab5603fab03` |
| Darwin companion binary | `0d28fd8af80f79194c5af656ed0d4248b0ebf7aabe1d04354f5c692ed68d5b45` |
| Gateway configuration | `a9c85e91b9abba8391e624c7f9ade6cd119735c001811c3bc52f6876af75d0d8` |
| Darwin companion configuration | `8d22738931cb9f6365799c7058a6a16d2f7482530938f431396aac6166655261` |

The gateway service and Darwin companion were active after the matrix, with
zero failed user units. The gateway invocation ledger contained 314 dispatched
and zero prepared records. The bounded companion ledger contained 256 terminal
records and zero nonterminal records. This confirms that the smoke runs left no
in-flight invocation authority.

## Rollback And Retention

The checksum-verified immediate runtime recovery sets remain:

```text
/home/server/mintclaw-recovery-browser-p0-final-pre-20260914T111700Z
/Users/ab/mintclaw-recovery-browser-p0-companion-pre-20260914T093239Z
```

The companion's pre-migration v1 ledger is retained separately in the
owner-only, checksum-verified recovery set:

```text
/Users/ab/mintclaw-recovery-node-ledger-v1-pre-20260914T111500Z
```

The final smoke-runner change is independently reversible by restoring the
previous repository revision; it did not alter configuration, profile data, or
runtime binaries.

## Phase Conclusion

Every Phase 0 acceptance criterion is satisfied. The deployment exposes only
the selected managed and ephemeral modes, the canonical smoke runner proves the
same first-party flows on gateway and companion, structured evidence is bounded
and private, failure paths clean up, and both invocation ledgers are settled.
Phase 1 may now introduce the minimum provider/runtime and driver/control seam
around the existing Playwright MCP implementation without changing the
model-facing contract.
