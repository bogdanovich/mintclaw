# Manual Agent Evals: Target Usage V1 Pilot

## Outcome

One new Linux general-suite run on 2026-10-18 passed **4/4 trials** in **10,601 ms**, with two declared repetitions
of each unchanged scripted case. One judge-only regrade completed with **1 pass / 3 error**, not another four
target executions. Comparison preserved every trial and error. Neither command was retried to obtain a pass.

The new returned-final usage observer retained four correlated finals but **zero returned usage blocks**. Its
coverage stayed `unknown`, not observed zero or complete all-call usage. Passive native outbox inspection then
confirmed a non-streaming channel omission: four native final messages carry counters that `sendText` does not
copy into the wire payload. That diagnosis does not backfill or upgrade the original eval receipt.

This exercises general repeats, error accounting and honest missing-usage reporting. It does not qualify positive
target-token collection, adaptive users, remaining PDF/browser cases or the [full E0-E5 goal](../architecture/agent-evals-goal.md).

## Reviewed Code And Runtime

[PR #1766](https://github.com/bogdanovich/mintclaw/pull/1766), reviewed head
`8383f0c4035be325446a212e833a698c2dbeb8e3`, merged as `5a9492528b3508249e0eec192a26ac9b28ae5279`
at `2026-10-18T22:48:26Z`. CI run `39171484553` passed ten checks; frontend and PDFium jobs were policy-selected
skips. Exact-head reviewer writeback `6255994811`, id `dff5bbb909706d43c7fd`, reported no high-confidence issues
at `22:33:56Z`; only the documentation file was excluded by repository review rules. Owner PR-description rocket
`578117973` followed at `22:33:58Z`. Current green checks, clean mergeability and no new/unresolved feedback were
verified at `22:48:20Z` before matching-head merge.

The isolated binary was built from that final merge tree, including other agents' already-merged PDF changes, not
an earlier candidate. Linux amd64, Go 1.26.9, CGO enabled, `goolm,stdjson`, version `eval-target-usage-v1`, build time
`2026-10-18T22:49:12Z`. With `-buildvcs=false`, revision/version were injected by the operator; the source archive
and checksums identify the build but do not constitute independent VCS attestation.

- Binary SHA-256: `c4e35b1eeef521b6623e47e1c3b0f52533e32698c311042a076bce87377f5e96`.
- Source archive SHA-256: `901b6fcb0b415eeb52cae84518c7b1dc5ee356c1fcdab9287975a1e97b68f51e`.
- Prepared public-config SHA-256: `e67ce97b4f587e1355025e04904a0860e90f8e51df0af1512b8f7efc62f69cd1`.
- Admitted runtime-config SHA-256: `a118419a05eaae0ba8765583cc46683ee987ff2482aceb9df40cee556de4b2eb`.

All 14 manifests validated offline. Native `eval prepare` copied one allowlisted synthetic instruction file into
a new UUID home/channel identity. The profile permitted only the ordinary question tool and loopback channel;
PDF/browser/exec/MCP, nodes, hooks, deployment and trace capture were disabled. Only one previously authorized
deployed-main credential and selected public model settings were reused, not production sessions/memory/state.
Configured target: `gemini-3.5-flash-lite`; separate tools-disabled judge: `gemini-3.6-flash`. These are request
identities, not complete backend attempt or billing evidence.

An operator checksum preflight initially used a relative path from the wrong directory. It failed before seed,
profile, credentials or model work. Changing to the validated private root corrected only that script. The actual
agent run below occurred once, with a twelve-minute suite limit and unchanged case/turn limits.

## Run And Usage

Run `e890e064-b610-4566-b69f-21da1acb0d11`, CLI exit 0, complete, phase `22:51:20Z` to `22:51:31Z`.
Reported duration starts after owned admission; it excludes cold build, preparation and gateway startup.

| Case | Repetition | Trial | Verdict | Duration Including Judge |
| --- | --- | --- | --- | --- |
| `general.context.followup` | 1 | `704676f1-412e-4351-9957-960247ebe64d` | pass | 2,754 ms |
| `general.context.followup` | 2 | `bb2919a0-2483-43e2-86f5-1709434f9028` | pass | 2,672 ms |
| `general.interaction.navigation` | 1 | `d10c7551-7031-4d44-9469-7fa51030778f` | pass | 2,759 ms |
| `general.interaction.navigation` | 2 | `1fbba1ce-5616-460d-acd5-2a5277074153` | pass | 2,343 ms |

Case hashes remain `4703ef12118060ecbb7c4edfb2ee19cf4950f91e13b52ac29e01851c8797b69d` and
`d5e6ab4c8732f27ef726ea3407e181d595b05ca8ac035236a35c2dfd2f7332e4`, respectively. The context checks retained
the marker in each session; question cancellation used native prompt-bound controls and did not claim a preference.

Four original judge calls returned **2,349 input / 332 output / 2,681 total tokens** (`2.3k in, 332 out`), with
`returned_calls_only` coverage. There were no LLM-user calls. Target `returned_final_events_v1` recorded four
finals, zero usage-bearing finals and `coverage: unknown`; top-level/all-call coverage remained `unknown`.
Numeric zero fields in that unknown receipt are not an observed zero-token cost.

Passive inspection found four delivered native outbox final records with **9,417 input / 54 output / 9,471 total**
in aggregate. The native messages and source both demonstrate the missing non-streaming projection; the streamed
finalization path already calls `setTurnUsagePayload`. This is a scoped diagnosis, not all-call accounting or a
substitute for the missing authenticated wire blocks. Do not modify old JSON, infer interrupted-turn usage or
promote this run into positive token-collection qualification. A narrow channel fix needs a native-channel
regression test, review/merge and a separately identified experiment.

## Regrade And Compare

Regrade `d332d018-3987-4d8c-8a15-f196998b8bb3`, CLI exit 1, complete in **2,622 ms**: one context repetition passed;
the other context repetition and both interaction repetitions recorded `judge_call_failed`, role code
`provider_unavailable`. Failed call durations were 1,326 / 109 / 76 ms. No specific HTTP/quota/timeout cause is
retained, so none is inferred. No judge retry or alternate model was used.

Four judge calls were attempted; one returned usage: **584 input / 65 output / 649 total tokens**, explicitly
`partial_returned_calls`. Target usage, native lifecycle, file observations and all four source trial identities
were preserved as historical evidence. Regrade performed no target, gateway or tool work.

Comparison exited 0 with `compatible_cases: true`: context **2 pass** versus **1 pass / 1 error**, interaction
**2 pass** versus **2 error**. Only `grading_mode` changed; effective inputs/all-call models remain unknown and
quality attribution is not established. These are rejudgements of the same executions, not independent samples
of agent regression. Original passes and all earlier failed/inconclusive reports remain unchanged.

## Cleanup And Limits

Owned PID `3320985` stopped and exited 0, not forced, with untruncated capture. Independent process absence was
checked after execution. One profile served four fresh conversation sessions; this is not automatic per-trial
profile isolation or whole-domain cleanup/privacy qualification.

The bounded literal copy guard inspected **91 files / 425,676 bytes**, found exactly five authorized credential
copies and no literal leakage elsewhere in scope. All five copies were removed; seed/prepared secret directories
were independently empty. Original production config/security hashes were unchanged. This guard is not a raw
encoded/derived-secret or whole-runtime privacy proof.

All ten production units stayed active, with no failed units or error-level records in the observed ten-minute
windows. Core stayed `82178cbd7156ff1ea77cd6c37002ec71fa6938e8` during this run; another agent had advanced it
since the earlier browser experiment. This operation deployed/restarted/changed no production runtime or config.

Private evidence remains under `/home/server/mintclaw-eval-target-usage-v1-20261018.DIvxECKy`; the used profiles now
have unresolved credential references and must not be restarted. Before/after workspace hashes matched, but they
remain file samples, not actual loaded prompt/catalog identities. Available all-call usage and remaining E3-E5
qualification/exit work remain mandatory.

## Validation

Root formatting, affected lint and documentation checks passed. Initial-head full macOS eval race tests passed in
497.816 seconds; focused conversation/run/grade/compare race controls passed in 21.697 seconds. CLI/client race
controls passed separately. Final-head Linux controls passed in 55.340 seconds.

CI caught a 32-bit ARM overflow in an untyped test fixture; explicit `int64` fixed it. Local ARMv7 cross-compilation
and final CI succeeded; this is not ARM execution. Two follow-up macOS binaries were sampled at `_dyld_start` before
Go main and terminated after 299.301 / 341.809 seconds, not counted as passes. Final-head macOS CI passed. No test,
deadline, assertion, approval or CI gate was weakened.

## Subsequent Follow-Up

The [v2 pilot](manual-agent-evals-target-usage-v2-pilot-20261018.md) after merged #1772 separately qualified actual
returned-final wire collection for one new context trial, plus judge-only regrade and comparison. The original v1
result, its unknown target counts and three judge errors remain unchanged; no old receipt was backfilled or rejudged again.
