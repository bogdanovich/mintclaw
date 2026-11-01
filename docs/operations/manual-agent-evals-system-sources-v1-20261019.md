# Effective System Sources V1 Qualification

## Changes And Merge Gates

[Native producer #1826](https://github.com/bogdanovich/mintclaw/pull/1826) preserves internal prompt-source byte
ranges through cache assembly and unchanged hooks, then emits bounded filtered source fingerprints on actual
requests. [Eval consumer #1830](https://github.com/bogdanovich/mintclaw/pull/1830) retains the shared typed witness,
validates frozen reports and compares source groups without reading current workspace files. See the
[producer contract](native-system-source-fingerprints.md) and [consumer scope](manual-agent-evals-native-models.md).

| Gate | Producer #1826 | Consumer #1830 |
| --- | --- | --- |
| Reviewed head | `0eafd9e35c5225ef04f8441c257464292eb1add0` | `fc1172ffdb804d8e4166abcb005fc5f2485aa553` |
| Exact-head CI run | `39213024872`: 13 successful jobs, one policy skip | `39218903045`: ten successful jobs, two policy skips |
| Actual reviewer comment | `6262080343`, 07:32:11 UTC, no must-fix findings | `6262653806`, 08:08:22 UTC, no high-confidence findings |
| SHA-bound writeback | `a8e36f6144af1ddee390` | `00302d6073bf20c1c067` |
| Owner PR-description rocket | `578812587`, 07:32:13 UTC | `578877023`, 08:08:24 UTC |
| Consolidated ready/mergeable/green/no-feedback gate | 07:40:18 UTC | 08:09:22 UTC |
| Confirmed merge | `7644a64c63b2bffbbd055d10114f442cb88e8d5d`, 07:40:29 UTC | `dd117238c3c9199920b1f6d0bc8017c7b4293c46`, 08:09:34 UTC |

All timestamps are 2026-10-19 UTC. Neither packet required a review-driven code fix. Producer integration CI
initially failed before tests because the Ubuntu snapshot mirror returned HTTP 500/502/503 while installing
dependencies. A failed-job rerun passed; no code, test gate or dependency policy was weakened. The consumer's
Frontend and PDFium WASM jobs were policy skips, not new PDF/backend qualification.

The consumer changed only `internal/eval` plus tests/docs: production four files, +78/-12; tests three files,
+209/-3; docs three files, +35/-4. Formatting accounts for some production alignment churn. It adds no runtime
authority, provider wrapper, approval/delivery path, secret store or per-trial daemon.

## Source Validation

| Observation | Result |
| --- | --- |
| Producer full local macOS agent race suite | Pass, 852.386 seconds |
| Producer native trace/protocol and focused cache/hook/source controls | Pass; exact-head CI additionally passed Linux/macOS, integration and platform builds |
| Consumer exact-head Linux native/source/run-grade-compare race controls | Pass, 54.978 seconds |
| Consumer full Linux eval race suite | Pass, 605.401 seconds |
| Same full Linux run: gateway client / CLI eval | Pass, 1.418 / 19.101 seconds |
| Root formatting, affected lint and post-commit pre-push validation | Pass; zero lint findings |
| Documentation lint and diff checks | Pass |

Consumer tests retain legacy absence, unavailable sources, observed empty groups, separate group drift, case
bindings, multiplicity, order independence and malformed hash/count/scope refusal. An owned two-repeat run deletes
runtime trace files before offline regrade, verifies the frozen projection and exercises public comparison of a
changed catalog without inventing aggregate request drift. Malformed frozen source metadata refuses judging and
comparison before model work. These use stubs, not paid models or production credentials.

The Linux validation used exact head `fc1172ffdb804d8e4166abcb005fc5f2485aa553`, Go 1.26.9, native CGO and
`-race -tags goolm,stdjson`, a cleared environment and private HOME/tmp. Root:
`/home/server/mintclaw-eval-system-sources-validation-20261019.nS46GMtZ`. Source archive SHA-256:
`d263366d673be644341b222dc23c6f13df160595c1ea2cf8b23d623cdaa53474`.

Earlier consumer Mac source controls passed in 3.304 seconds and broader native/run-grade-compare controls in
22.080 seconds, before the final extra public comparison assertion. Subsequent broad/new-assertion invocations
were blocked before test `main`, with 12 KiB footprint, zero CPU and sampled `_dyld_start` stacks. They were
interrupted after 248.968 and 159.154 seconds, not counted as passes. A first broad CLI invocation also omitted
repository build tags and lacked libolm headers. The corrected CLI invocation waited on a dyld-blocked native
linker; only the identified owned command group was terminated and joined. Samples remain outside Git. Passing
Linux and CI Mac results do not relabel those local non-passes or qualify a live Mac eval.

## One Merged-Source Experiment

One fresh owned Linux profile ran the unchanged `general.context.followup`, repeat one, at 08:13:38-08:13:44 UTC.
This qualifies the newly retained source metadata, not a retry/tuning loop for the earlier partial native-model
experiment. Historical reports remain unchanged. The task, independent tools-disabled judge, one offline regrade
and comparison all passed.

| Observation | Retained result |
| --- | --- |
| Source run / trial | `84850860-c28c-4e02-8a60-cb763a02c215` / `2b4e88f1-2307-4741-80e2-c16161ea241a` |
| Case SHA-256 | `4703ef12118060ecbb7c4edfb2ee19cf4950f91e13b52ac29e01851c8797b69d` |
| Target requested / actual model | `gemini-3.5-flash-lite` / `gemini-3.5-flash-lite` on both returned responses |
| Independent judge | `gemini-3.6-flash`; initial call 1,151 ms |
| Hard checks | Marker retained, same session, final outcome: all pass |
| Run / trial duration after admission | 3,448 / 3,283 ms; not cold build/startup time |
| Native records | Observed: two requests, two responses, two usage responses, no retry/fallback records |
| Effective sources on each request | Observed: one system message/four parts; workspace one part, catalog one part, active skills zero parts |
| Cross-turn source identity | Both requests have identical workspace/catalog hashes and the canonical empty active-skill hash |
| Returned-final usage | Two finals: 4,645 input / 28 output / 4,673 returned total; compact input `4.6k` |
| Native response usage | 4,645 input / 28 output; no native provider-total field or invented billing total |
| Initial judge usage | 585 input / 80 output / 665 total, separate from target |
| Regrade | `1f506670-ec38-4022-8337-19c0a1a44896`, pass, 1,286 ms; no target/tool work |
| Regrade judge usage | 585 input / 75 output / 660 total, not added to target |
| Comparison | Compatible, pass/pass; only `grading_mode` changed |
| Frozen native projection | Independently equal in original stdout, canonical run, grade stdout and canonical grade |
| Owned lifecycle | PID `132116`, native exit 0, no force/log truncation; independently absent afterward |
| Scoped raw-profile privacy | Observed, 49 files / 318,538 bytes, zero opaque files/leaks; native SQLite witness observed |
| Credential-copy guard | Observed, 74 files / 364,343 bytes, five authorized copies, zero known-literal leaks |
| Credential/temp cleanup | Five copies removed; independently zero remain; owned `eval-tmp` empty |
| Production safety | Config/security byte hashes independently unchanged; no deploy/restart/config write by this packet |

Native trace byte identities retained by the observer:

- `trace-turn-6a42059236ff9f3fa2d9b021`:
  `9b83f17d2b839eda89cfc9d49841049050116b8a28f33805876d3596cf879b9c`.
- `trace-turn-d4dde5701a25a2714c0cfd12`:
  `9e4d1f2962d408bbc538173e94ba751ef3d73cc63f3579851fb4f0f544d0e86a`.

No source text, secret or conversation transcript is committed. Native provider names remain absent/unknown;
they are not inferred from the model spelling. Comparison retains request/response-provider unknowns and the
broader `instructions_and_skills` / `target_all_call_models` unknowns. The observed empty active group is not
positive live qualification of loading nonempty skill instructions. Group-specific changes are tested offline,
not a second live experiment that edits source instructions until a desired result appears.

## Evidence And Remaining Work

Private packet root: `/home/server/mintclaw-eval-system-sources-v1-20261019.rwIntyGu`. Used prepared home:
`profiles/eval-f058dbdf-7221-4daa-8411-b26799910d3b`. Keep original traces/reports/operator scripts and receipts;
never restart this profile. Only the already authorized deployed-main scoped model credential was copied through
the checksum-verified operator helper. No production workspace/session history was copied.

- Merged-source archive SHA-256: `8d37ea8ee437f3efa8c9fc53ab2cdf6b1eb540b01ac09a2430c32bc976b32575`.
- Isolated binary SHA-256: `d3650dd5c4a016621af3c96b91129b6aacc0bbf29b1136cb36ed0bde0df35878`.
- Build: `eval-system-sources-v1`, full merge SHA above, Go 1.26.9, native CGO, `goolm,stdjson`,
  timestamp 08:10:35 UTC. Operator-supplied archive/build metadata is not independent VCS attestation.
- Original canonical `run.json` SHA-256: `224a7d04b4372889b61e01aa56e0e0f8c6fee60b8663db193036b1d31ff9fa92`.
- Regrade canonical `run.json` SHA-256: `4f3f74bbb9015c3eb3886726d1e088698190d76e25caa6bccb487e599ac5ac25`.

Before/after health checks found all ten production services active, no failed units and no ten-minute errors.
Production core advanced externally to `72cfc9c7896df3c95232057ef14982dc66664e46`; this work did not deploy it.
About 5.2 GiB remained free afterward. Do not delete shared caches or another agent's evidence to admit later work.
No production rollback is needed; future isolated profiles can omit metadata capture.

This closes the producer/consumer's routed system-content qualification only. Complete per-trial resource
evidence, remaining PDF/browser/private/adaptive scenarios, domain repeats and the frozen E0-E5 exit are still
mandatory. Content hashes do not attest every individual skill/helper version, all instruction roots, wire bytes
or all billable calls. The privacy guard is bounded known-value/surface coverage, not a whole-runtime or
provider-internal certificate. Preserve earlier failures/unknowns; this passing context trial does not upgrade them.
