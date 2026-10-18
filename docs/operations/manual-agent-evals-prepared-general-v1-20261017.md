# Manual Agent Evals: Prepared General V1 Pilot

## Scope And Identity

On 2026-10-17, merged [PR #1665](https://github.com/bogdanovich/mintclaw/pull/1665) was exercised with an
allowlisted seed, native `eval prepare`, an owned general run with two repetitions, judge-only regrading and
comparison. All 17 CI checks passed on head `f77ee77eca91dfbd9d90464248aeb498c5ee1506`; the reviewer found no
high-confidence issues, no unresolved threads remained, and owner PR-level rocket `576438406` was checked before
merge `d6ce78976236b4e81809b689f909bfbf8ee44229` at `2026-10-17T21:18:59Z`.

The identified binary used that reviewed code head, now merged, not an assertion that it included every other
concurrent main change. Platform: Linux amd64, Go 1.26.9, CGO enabled, `goolm,stdjson`. Build identity was explicitly
injected with `-buildvcs=false`, not a VCS attestation; version `eval-prepared-general-v1-f77ee77ec`, build timestamp
`2026-10-17T20:30:07Z`. The tracked source was clean and a source archive was retained privately.

- Binary SHA-256: `ab71cf92f392c39b29f5f00870261e9b476effc007ea6706e35a43ab7fc593b4`.
- Source archive SHA-256: `77d92978c4660a01fceca1e040c18d14047d969621fbb8f589c09026184290a9`.
- Prepared public-config SHA-256: `c1c2083df584991d041d0d61c4f78ee24fd668f0eac167b8da9d7ac86ac2304e`.
- Admitted runtime-config SHA-256: `e495153af09e63c94e061a59534b957175d8661ef7704de5c209edc10e184fd7`.

An isolated seed reused only one authorized API credential and selected public request settings from deployed
main. The new command copied exactly one pinned synthetic `AGENTS.md`, created a fresh UUID home and channel
token, and returned `prepared / runtime_run: false`. It did not import production configuration, sessions, memory,
browser/OAuth state or ledgers. The seed had only the native loopback channel and ordinary question tool, with
default-deny tool/approval policy and PDF/browser/exec/MCP, nodes, cron, hooks, reload and deployment disabled.
Production services/configuration/binaries were not changed by this operation.

Target request: `gemini-3.5-flash-lite`; separate tools-disabled judge: `gemini-3.6-flash`, `eval-judge-v2`,
`eval-role-generation-v1`, configuration hash
`462e40289560e45241a3f35e702e78f7187cf5b3c115ed325abdad1f79521787`, matching the
[previous calibrated configuration](manual-agent-evals-judge-v2-calibration-20261011.md).
These are configured request identities, not provider-confirmed all-call identities or billing evidence.

## Original Run

Report `56ddf6ee-b919-402c-b846-6992419629b2`: **4/4 pass**, complete, CLI exit 0, `repeat: 2`.
Reported duration **10,099 ms** includes scenario work, judging and owned shutdown, not gateway startup before
admission, preparation or cold build time. Each repetition used a fresh conversation; all four used one profile.
No target trial was retried. There were no LLM-user calls.

| Case | Repetition | Trial | Verdict | Duration Including Judge |
| --- | --- | --- | --- | --- |
| `general.context.followup` | 1 | `8a9d4338-9af0-4fc2-89ba-55c089e4c4e7` | pass | 2,790 ms |
| `general.context.followup` | 2 | `41b211f4-9f91-4cdd-b56e-33ffcfc4f9b9` | pass | 2,346 ms |
| `general.interaction.navigation` | 1 | `f663ac7d-ea9e-4896-aeca-71d31b07096e` | pass | 2,425 ms |
| `general.interaction.navigation` | 2 | `a896a3b8-4625-4767-9555-2f7f5baa599f` | pass | 2,464 ms |

The context case checked same-session marker retention; the interaction case checked prompt-bound native
question/cancellation and coherent controls. Case hashes remained
`4703ef12118060ecbb7c4edfb2ee19cf4950f91e13b52ac29e01851c8797b69d` and
`d5e6ab4c8732f27ef726ea3407e181d595b05ca8ac035236a35c2dfd2f7332e4`, respectively.

Four judge calls returned 2,337 input / 364 output / 2,701 total tokens (`2.3k in, 364 out`). Target all-call usage
and provider-internal attempts remain unknown. The runner admitted exact child PID `570743`, requested stop and
reaped exit 0 without force or log truncation. Subsequent process/ownership-marker absence checks passed.

Workspace file observations were `observed` before/after with equal instruction and skill aggregate hashes:
`a7eb032ae98bd58278d1661a0fb83d07ef02bdf8f74520729b1d624ff87b74a0` and
`17ebc71f2342352898acf9fe6b097d4874d6045c9c4e1d065623602e86b87541`.
This is `workspace_files_v1`, not proof of effective loaded prompts/catalogs or stability between samples.

## Regrade And Compare

One judge-only regrade, report `1efb53d6-a46c-4a75-9bc7-e10d3a19cd39`, completed in 2,302 ms with CLI exit 1:
**2 pass / 2 error**, not a successful regrade or four more agent trials. Both context repetitions passed; both
interaction repetitions recorded `judge_call_failed`, with role-call code `provider_unavailable`. Those attempts
took 116 and 189 ms, returned no usage, and have no retained specific HTTP-cause classification. Do not infer
quota, timeout, a target regression or the provider's actual execution from that opaque code. No retry was made.

All four source trial IDs/repetitions, lifecycle and file snapshots were preserved. Four judge calls were attempted;
only two returned usage: 1,168 input / 173 output / 1,341 total (`1.2k in, 173 out`), explicitly partial coverage.
The target stayed stopped; regrading ran no gateway or tool work.

Offline compare returned `compared`, `compatible_cases: true`, retaining both trials of each case. Context coverage
was 2 pass versus 2 pass; interaction coverage was 2 pass versus 2 error. It reported `grading_mode` changed,
effective instructions/skills and target all-call models unknown, and `quality_attribution: not_established`.
This compares rejudging of the same four executions, not two independent target-quality samples. Original results
were not overwritten or promoted using the later judge errors.

## Privacy And Retention

Before sanitization/deletion, a bounded operational inspector scanned 75 raw profile files / 408,427 bytes and
six seed files / 9,916 bytes. Both scans completed within their declared bounds and found no exact credential-file
literals outside the authorized `.security` directories. Private value-free receipts were retained separately.
This literal-only scan is not comprehensive secret detection, escaped/derived-value proof or domain privacy
qualification. The private credential sources were excluded deliberately, not sanitized before inspection.

After confirmed native exit and these observations, exactly five generated credential copies were removed:
the seed's API/channel files and the prepared profile's two model-key files and fresh channel-token file.
Independent absence checks passed; production sources were untouched. Raw reports/logs, comparison, native state,
source archives and identified binaries remain private outside Git. Both retained homes now have unresolved
credential references and must not be treated as fresh, resumable targets. No whole-profile/domain cleanup claim
is made.

## Reproduction And Remaining Scope

Use the [operator guide](manual-agent-evals.md) for a new private seed/preset and `eval prepare`; use its returned
home/config/preset, not the seed environment. From the identified binary/corpus, select the general suite in
`eval run --start-target` with `--repeat 2`, then use `eval grade ORIGINAL_REPORT` and
`eval compare ORIGINAL_REPORT REGRADE_REPORT`.
Retain the first outcome of each command, including errors; never reopen the used target or retry until green.

This exercises one allowlisted preparation, two scripted general cases with repeats, owned process lifecycle,
file observations, all-trial regrade/error accounting and comparison on Linux. It does not qualify adaptive users,
PDF/browser, Telegram, macOS live, automatic per-trial provisioning, effective-input identity, full privacy/cleanup
or the 14-case suite. Earlier passing and inconclusive pilots remain unchanged. The full E0-E5 goal remains active.
