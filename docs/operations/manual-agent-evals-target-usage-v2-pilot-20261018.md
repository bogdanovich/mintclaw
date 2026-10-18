# Manual Agent Evals: Target Usage V2 Pilot

## Outcome

One new isolated Linux `general.context.followup` trial on 2026-10-18 passed in **3,031 ms**. Both correlated
final messages carried actual native wire usage: **4,770 input / 27 output / 4,797 total tokens**, displayed as
`4.7k in, 27 out`. One judge-only regrade passed in **1,031 ms**, preserving those historical target counts;
comparison completed with compatible cases and pass/pass coverage.

This qualifies bounded non-streaming returned-final collection for this scripted Linux context case, **not
all-call accounting**, cost, tool-loop/paused-turn coverage, macOS live or completion of the
[E0-E5 goal](../architecture/agent-evals-goal.md). Earlier missing-usage receipts and three regrade errors remain
unchanged in the [v1 report](manual-agent-evals-target-usage-v1-pilot-20261018.md).

## Reviewed Code

[PR #1772](https://github.com/bogdanovich/mintclaw/pull/1772), head
`dee6ef87afa50b00e2c77e2b29856faa9a106dbf`, merged as `dfc95d00a9c70bbad91401cf310ddb74c4a418a9`
at `2026-10-18T23:18:38Z`. Three production lines reuse the existing usage helper for text replies marked both
`final_reply` and `final`. Streaming, media, provider behavior and approval authority are unchanged.

CI run `39174627261` passed ten checks, with frontend/PDFium policy skips. Reviewer writeback `6256378798`,
id `ba801920bbf29405a37d`, reported no actionable findings at `23:14:55Z` on that exact head. Owner PR-description
rocket `578165841` followed at `23:14:56Z`. Current CI, review, reactions and absence of unresolved/new feedback
were checked at `23:17:52Z`. Main's new computer changes did not overlap and local merge-tree validation was clean.
Normal matching-head server merge succeeded without an admin override and was independently confirmed.

The binary used the final merge tree, not a pre-merge candidate. Linux amd64, Go 1.26.9, CGO enabled,
`goolm,stdjson`, version `eval-target-usage-v2`, build time `2026-10-18T23:20:16Z`. Revision/version were injected
with `-buildvcs=false`; archive/checksums identify the build, not an independent VCS attestation.

- Binary SHA-256: `fae968b73124ee5e85bb7318318145459903648e0a7f5e2de308bfa18b539d3b`.
- Archive SHA-256: `377c261b847833f07df499b18f6a1973bcb198c5fb0f7896d8422f0e3deceb7a`.
- Prepared public-config SHA-256: `cdda510ded267db9d408c147cd1c7d1fc50774473f1e2d77e8b408f47968f205`.
- Runtime-config SHA-256: `1c1997f082a7ea7b70c25aabdad92e232210bbe113c0cef465ac96e69403a99a`.
- Unchanged case SHA-256: `4703ef12118060ecbb7c4edfb2ee19cf4950f91e13b52ac29e01851c8797b69d`.

All 14 manifests validated offline. A fresh seed/native `eval prepare` copied one allowlisted synthetic
instruction file into a new private home/channel identity. Only the authorized deployed-main credential and
selected public model settings were reused, not production sessions/memory/state. Only ordinary questions and
the loopback channel were permitted; PDF/browser/exec/MCP, nodes, hooks, deploy and trace capture stayed disabled.
Requested target/judge remained `gemini-3.5-flash-lite` / `gemini-3.6-flash`, not proof of every backend attempt.
Case/turn limits were unchanged; the suite limit was twelve minutes.

## Run And Regrade

Run `60b0047a-a3eb-472d-94ed-f9de0fdcd549`, trial `878d51a4-7e70-439a-91b4-68310302672d`, one declared repetition,
CLI exit 0, complete. Execution phase: `23:22:35Z` to `23:22:40Z`. Reported duration excludes cold build,
preparation and gateway startup before admission; trial duration including its judge was **2,977 ms**.

The two-turn marker/context hard checks and independent judge passed. There were no LLM-user calls. Actual final
events retained integer counts, not context-window estimates or reconstructed native-store totals:

| Final Event | Input Tokens | Output Tokens | Total Tokens |
| --- | --- | --- | --- |
| `event-000002` | 2,306 | 17 | 2,323 |
| `event-000004` | 2,464 | 10 | 2,474 |

`target_usage.scope` is `returned_final_events_v1`, coverage `returned_finals_only`, with 2/2 usage-bearing finals.
Top-level `usage` and `all_call_coverage` remain `unknown`: these totals do not establish all call/attempt
completeness, missing provider usage or interrupted-turn usage.

The original judge returned **584 input / 69 output / 653 total tokens**. The single regrade,
`6c8de735-6376-4afa-80de-80a31fa25a11`, returned **584 / 71 / 655**, passed and preserved the source trial,
events, target counters and lifecycle. It performed no target/gateway/tool work. Neither judge call was retried.

Comparison exited 0 with `compatible_cases: true`, one pass versus one pass, only `grading_mode` changed.
Effective inputs/all-call models remain unknown; quality attribution is `not_established`. Rejudging the same
execution is not an independent agent-quality sample. This does not repair v1's judge errors or treat the older
four-trial cohort as this one-trial cohort.

## Cleanup And Limits

Owned PID `3374958` stopped and exited 0 without force/log truncation; independent process absence was checked.
The literal copy guard inspected **71 files / 329,034 bytes**, found five authorized copies and no literal leak
elsewhere in scope. All five copies were removed; seed/prepared secret directories were independently empty.
Production config/security hashes stayed unchanged. This is bounded literal evidence, not encoded/derived-secret
or whole-runtime privacy proof.

All ten production units remained active, with zero failed units and zero error-level records in the observed
ten-minute windows. Core stayed `80f00e37a0a915444fe1a8d65af7f65a402d0adf` during this experiment; another agent
had advanced it beforehand. This operation deployed/restarted/changed no production runtime or configuration.

Private evidence remains under `/home/server/mintclaw-eval-target-usage-v2-20261018.4s39IA4F`. Used homes have
unresolved credential references and must not be restarted. Matching workspace file samples are not actual loaded
prompt/catalog identities. Remaining PDF/browser/adaptive cases, per-trial profile isolation, effective inputs,
available all-call usage, domain repeats and final exit work remain open.

## Validation

The native real-WebSocket regression failed before the fix for three positive variants, then passed with nine
variants including zero/unknown, thought, tool-call and interim/unmarked replies. Full macOS channel race tests
passed in 4.130 seconds, shared client in 2.007 seconds and related eval controls in 20.998 seconds. Root formatting,
affected lint and committed-head pre-push checks passed with zero issues. Final exact-head Linux/macOS/Windows CI
was green. No tests, deadlines, assertions or approval gates were weakened. These controls and one live Linux
context case do not qualify the other required domains or platforms.
