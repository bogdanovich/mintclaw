# Manual Agent Evals First-Release Exit

## Decision

The bounded first-release **harness implementation** is delivered. This exit maps the unchanged
[frozen goal](../architecture/agent-evals-goal.md) to merged code, deterministic controls and actual isolated
experiments. Goal completion is gated on merging this exit record. It does not declare all target features reliable,
fourteen passing scenarios, or positive PDF/private-form qualification.

The stop rule explicitly accepts a correctly recorded target failure as an eval result and excludes an endless
target-repair loop. Harness defects discovered during qualification were fixed through reviewed forward PRs;
negative results were retained, not relabelled or retried on used profiles. The latest
[resource qualification](manual-agent-evals-trial-staging-v1-20261020.md) adds two passing general trials and one
inconclusive PDF trial, not three passes. No known mandatory harness implementation task remains from E0-E5.

## Frozen Acceptance Audit

| Goal Requirement | Implementation And Observed Evidence | Boundary |
| --- | --- | --- |
| 1. Agreed design, roadmap and stop gates before code | Frozen admission, [design](../architecture/agent-evals-design.md), roadmap and focused merged packets; criteria below unchanged | This exit is not a new open-ended roadmap |
| 2. CLI/corpus/preset selection and offline preflight | `eval list/validate/run/grade/compare`, strict versioned parsers, suite/tag/case filters, explicit private roots and prepared presets; parser/selection/CLI tests | Offline commands do not call models; missing prerequisites are not passes |
| 3. Real shared transport and same-session interaction | Existing `internal/gatewayclient` reused with `agent live`; bounded streaming/identity/cancellation/no-replay controls; [general qualification](manual-agent-evals-owned-general-v1-20261017.md) | No second AgentLoop or runtime authority |
| 4. Isolation, owned resources and deny-default approval | Explicit fresh `eval prepare`, chat-inclusive native session policy, per-trial PDF/browser fixtures, native scoped approval, [staging receipts](manual-agent-evals-trial-staging-v1-20261020.md) | Local staging, native resources and logical settlement are distinct; no whole-profile reset claim |
| 5. Scripted/adaptive user and independent judge | Separate tools-disabled existing-provider roles; typed actions/private handles; correction experiment made three returned LLM-user calls; [judge v2 calibration](manual-agent-evals-judge-v2-calibration-20261011.md) agreed 6/6; eligible offline grade exercised | Failed adaptive target workflows remain failed/unqualified; labels/hidden oracles are not driver inputs |
| 6. Honest verdicts, identities, timing and usage | Raw integer role/final/native-response usage; compact display; case/fixture/runtime/config/prompt/input fingerprints and scoped native system-source witnesses; [native observation](manual-agent-evals-native-models.md) | Provider-internal/all-call and broader loaded-skill provenance stay explicitly unknown; no cost cap invented |
| 7. Fourteen minimum cases and independent oracles | All definitions committed and actually attempted; ledger below; pinned independent PDF/artifact predicates and sealed browser fixture state; native authority/delivery joins | Not one simultaneous cohort, not a pass rate or full domain qualification |
| 8. Private presets, repeats, grade, compare and cleanup | Allowlisted workspace inputs; [four-trial preparation experiment](manual-agent-evals-prepared-general-v1-20261017.md); current two-trial regrade/compare retain identical receipts and every trial | Private workflows stay in existing private workspace repo; raw evidence/keys stay outside Git; cleanup uncertainty cannot make suite green |
| 9. Deterministic quality/platform gates | Parser/client/driver/judge/privacy/approval/lifecycle/domain/report/comparison tests; latest full Linux eval race 624.200s; exact-head CI Mac full eval, Linux tests/race, integration/build/lint | No paid/live cases in normal CI; local unstarted Mac attempts are not passes; no Windows live qualification |
| 10. Actual review, green CI and authorized merge | Linked pilot records retain exact heads/reviews/reactions; latest code #1857 checked SHA-bound review, all CI, root rocket, no unresolved/new actionable feedback, then confirmed merge | Docs-only exception for this record; eyes alone never approval |
| 11. Real merged-source runs and retained failures | Identified binaries, fresh synthetic profiles, narrow existing credential references, independent evidence and redacted results throughout ledger; current production hashes/services unchanged | No production mutation/deploy, broad grants or retry-until-green |
| 12. Exit and stop | This record, updated roadmap/status and reproducible [operator guide](manual-agent-evals.md) | Complete goal only after documentation merges; future product work needs a separate bounded goal |

Negative controls cover forged success/artifacts, stale/foreign prompts and approvals, unaccepted mutations,
private values in discarded/raw/typed data, missing/truncated witnesses, changed or opaque files, duplicate
events/trials, source mismatches, provider errors and cleanup failures. The
[SQLite negative experiment](manual-agent-evals-sqlite-probe-20261018.md) and earlier 4/6 and 5/6 judge calibrations
remain retained. Native authority is consumed through existing owners, never replaced by target prose or a judge.
These controls establish scoped harness refusal behavior, not universal detection of every unsafe behavior.

## Fourteen-Case Ledger

These are observations on separately identified historical revisions/configurations, not fourteen executions of the
latest binary or the best result selected for a common baseline. Every earlier failure remains in its linked report.
Changed harness contracts received separately declared experiments, not silent report upgrades.

| Case | Actual Retained Observation | Report |
| --- | --- | --- |
| `general.context.followup` | Pass, including current two independent repetitions and offline regrade | [Staging v1](manual-agent-evals-trial-staging-v1-20261020.md) |
| `general.interaction.navigation` | Pass, native prompt-bound ordinary cancellation | [Owned general](manual-agent-evals-owned-general-v1-20261017.md) |
| `pdf.loan.fill` | Inconclusive; native human continuation missing Gemini signature; no filled artifact | [Output v1](manual-agent-evals-pdf-output-v1-pilot-20261018.md) |
| `pdf.loan.correct` | Inconclusive; three returned LLM-user calls, no eligible PDF owner/output | [Remaining PDF v1](manual-agent-evals-pdf-remaining-v1-pilots-20261020.md) |
| `pdf.conditional.intent` | Inconclusive, missing route identity; no eligible question | [Remaining PDF v1](manual-agent-evals-pdf-remaining-v1-pilots-20261020.md) |
| `pdf.empty.required` | V3 inconclusive at protected continuation guard; v1's frozen pass did not qualify document use | [Empty v3](manual-agent-evals-pdf-empty-v3-pilot-20261019.md) |
| `pdf.cancel.no-write` | Earlier ordinary cancellation/no-write pass; current fresh attempt inconclusive at document guard/route | [No-write v1](manual-agent-evals-no-write-v1-20261018.md), [current staging v1](manual-agent-evals-trial-staging-v1-20261020.md) |
| `pdf.large.section` | Inconclusive, missing route identity; no section/output proof | [Remaining PDF v1](manual-agent-evals-pdf-remaining-v1-pilots-20261020.md) |
| `browser.fixture.read` | Scoped native read, judge and offline grade pass | [Read v4](manual-agent-evals-browser-read-v4-pilot-20261018.md) |
| `browser.fixture.submit` | Fail; ordinary rather than protected question, expected submission absent; no private expansion/grant | [Private v1](manual-agent-evals-browser-private-v1-pilot-20261019.md) |
| `browser.fixture.followup` | Fail; expected submission absent and LLM-user provider unavailable, not attributed to target quality | [Private v1](manual-agent-evals-browser-private-v1-pilot-20261019.md) |
| `browser.fixture.ambiguous` | V3 pass: clarified/approved exactly South, independent effect, judge and grade; v1 fail/v2 inconclusive preserved | [Ambiguity v3](manual-agent-evals-browser-ambiguity-v3-pilot-20261020.md) |
| `browser.fixture.artifact` | Inconclusive; click did not match admitted native download; no grant/artifact | [Artifact v1](manual-agent-evals-browser-artifact-v1-pilot-20261018.md) |
| `browser.fixture.denied` | V2 fail; readiness final before required permission checkpoint; no cancel/grant/effect | [Denial v2](manual-agent-evals-browser-denial-v2-pilot-20261019.md) |

## Limits And Follow-On Decisions

Positive live evidence is Linux/local-owned/native-channel scoped. Mac runner behavior is exercised by deterministic
CI and recorded local controls, not live PDF/browser parity. I-134 provenance/full-form filling, Telegram upload and
callbacks, companion/cloud/browser-provider parity, Windows live behavior, complete provider billing, all encodings,
whole-host privacy and whole-runtime quiescence are not implied. Missing required evidence stays non-passing.

Independent filled-PDF/CSV oracles and native positive-path workflows are implemented/tested, but no live completed
private PDF output or private browser submission is certified by these failed attempts. Regrade admits only eligible
frozen evidence and documented generated-fact contracts; it cannot repair a failed source or reconstruct missing
authority. A historical pass on a narrower contract is not promoted into newer resource/content coverage.

Useful next goals, only after separate admission, are target continuation/error-route handling for PDF, protected
browser input selection and positive artifact dispatch. Preserve natural tasks and guards; first reproduce a focused
target defect rather than tune a task into success. Broader provenance or additional models/backends are optional
expansions, not reasons to extend this completed implementation indefinitely.

For future manual checks use explicit suite/tag/case filters, fresh prepared profiles and declared repetitions.
Keep generic synthetic cases with the runner; keep internal workspace scenarios/fixtures in the existing private
workspace repository using the same parser. Do not commit transcripts, keys or runtime artifacts to either repo.
Retained used profiles are evidence, not resumable clean targets. Cleanup removes only verified owned copies and
settled resources; unknown native state is retained and reported. No production rollback is needed for these
isolated experiments. Disable opt-in capture/omit eval commands to stop using the harness; no daemon was installed.
