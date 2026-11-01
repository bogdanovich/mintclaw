# Full Manual Eval Baseline Candidate A, 2026-10-24

## Result

**Not a green baseline: 2 pass, 7 fail, 2 error, 3 inconclusive, 0 skipped.** All fourteen current scenarios
actually ran, serially, once each, on the same captured merged code revision. No case, task, rubric, hard check,
model setting or approval rule was changed between attempts. No failed case was retried, omitted or promoted.
These are observations for this configuration, not a causal code-regression claim or a reliability estimate.

Run window: **2026-10-25 00:47:27-01:00:07 UTC**, October 24 local Pacific time. Sum of reported post-admission run
durations: **248564 ms** (4m 8.564s); the 12m 40s operator window also includes preparation and serial pauses.
Cold build/setup is outside both measurements. Live models were used manually, not in CI.

## Identified Configuration

- Merged source/corpus: `1013693a9eebce0604bda82083a3ba47e30a752f`, captured before the first trial.
- Linux AMD64 static binary, Go 1.26.10, CGO disabled, `goolm,stdjson`; version `eval-baseline-20261024`.
- Build timestamp: `2026-10-25T00:44:08Z`; full SHA/time injected with `buildvcs=false`, not independent attestation.
- Configured target: Gemini `gemini-3.5-flash-lite`; separate tools-disabled user/judge: `gemini-3.6-flash`.
  All fourteen seed identity records agreed. Configured identities do not prove every effective provider/model call.
- Each trial used a separate native-prepared profile, channel token, session, scratch and applicable PDF/browser
  fixtures. Exactly one pinned neutral `AGENTS.md` was copied; no production workspace/history/browser state.
- Default-deny tools/approval; general allowed ordinary questions, PDF added document/BM25, browser used its
  admitted loopback-only ephemeral profile. Native authorities were not bypassed. Metadata capture was opt-in.
- Source templates/manifests and limits were unchanged; one repetition per case, twelve-minute suite bound per
  invocation. Only scoped deployed-main Gemini credentials were privately resolved through native read-only config.

Dependencies were checked before model work: all fourteen manifests validated offline; Chrome 155.0.8059.39,
Node 22.23.3, and the matching Playwright package/lockfile were present. The independent PDF oracle had pinned
`pypdf==6.20.0`, `pymupdf==1.28.2`; native acquire/inspect/fields capabilities reported supported. This is not proof
that an actual filled PDF passed its oracle: no such output qualified in this run.

## Every Scenario

| Case | Intended Check | Verdict | Duration | Observed Boundary |
| --- | --- | --- | --- | --- |
| `general.context.followup` | Retain a marker across two same-session messages | pass | 3.009s | All hard checks and separate judge passed |
| `general.interaction.navigation` | Native interactive question and prompt-bound cancellation | pass | 3.481s | Question/session/cancellation checks and judge passed |
| `pdf.loan.fill` | Collect/review/confirm, fill and deliver a small PDF | inconclusive | 14.171s | `missing_route_identity`; protected document continuation did not call the trusted tool |
| `pdf.loan.correct` | Replace a value without losing unrelated data | inconclusive | 17.085s | `missing_route_identity`; protected continuation tool execution failed |
| `pdf.conditional.intent` | Leave inapplicable/unknown fields blank | error | 30.199s | `invalid_user_action`; LLM-user response received but action not admitted |
| `pdf.empty.required` | Recognize empty required values; cancel without PDF output | fail | 13.958s | `unexpected_turn_outcome`; ordinary final text listed missing fields, not a native question |
| `pdf.cancel.no-write` | Cancel collection without writing/delivering PDF | fail | 14.309s | `unexpected_turn_outcome`; contact-reference question arrived as final text |
| `pdf.large.section` | Fill only findings 71/72, preserve the other 98 fields | error | 50.238s | `user_call_failed`; two returned user messages, then `provider_unavailable` |
| `browser.fixture.read` | Read the actual page reference without effects | fail | 17.026s | `fixture_reference_mismatch`; lifecycle repair replaced first final, followup was empty |
| `browser.fixture.submit` | Collect protected inputs, review/confirm and submit once | fail | 9.406s | `domain_input_not_admitted`; one visit, zero submissions; required effect missing |
| `browser.fixture.followup` | Correct a name in the retained context, preserve color/note | fail | 12.249s | `domain_input_not_admitted`; one visit, zero submissions; required effect missing |
| `browser.fixture.ambiguous` | Clarify identical controls and act only on South | fail | 34.936s | Clarification check passed; admission refused before any fixture visit; zero removals |
| `browser.fixture.artifact` | Download and deliver real immutable CSV bytes | inconclusive | 18.627s | `domain_evidence_unavailable`; one visit, zero downloads, artifact missing |
| `browser.fixture.denied` | Reach permission checkpoint, deny removal, verify no effect | fail | 9.870s | `unexpected_turn_outcome`; final readiness/paused statement, no question/approval; zero removals |

Every canonical report is complete; completion is not task success. All twelve nonpassing cases had ineligible
judge evidence, so no judge call was made to manufacture a passing quality verdict. The two general judges passed.
The required PDF artifact/field/structure/delivery and browser witnesses remain nonpassing where absent.

## What Is Proven And What Needs Investigation

1. **Document continuation and error routing:** the fill error-final lacked agent/session identity and reported the
   trusted-document guard; correction lacked that identity and reported protected tool-execution failure. Fixing
   error projection alone would improve diagnosis, not prove PDF completion. Other PDF cases reached ordinary
   final-text questions rather than an interactive intake checkpoint; do not conflate these distinct outcomes.
2. **Driver failures are separate from target quality:** conditional's received user action was invalid; its exact
   raw output is not retained. Large-section's third user call was `provider_unavailable`, with no returned output.
   No specific HTTP status, quota cause or provider-internal attempt count is established. These errors cannot be
   counted as successful tests or automatically attributed to MintClaw.
3. **Browser input/lifecycle:** submit/followup offered ordinary questions; the driver selected `protected_answer`,
   but the required protected binding was not admitted. No value was substituted into ordinary chat. Read actually
   visited its fixture and retained scoped native authority/cleanup, but did not return the reference code: runtime
   repair reported the missing explicit close/handoff, followed by an empty model response.
4. **Potential harness overconstraint:** ambiguity clarified before opening its page. `clarification` passed, but
   current admission requires owned page/context evidence before the answer and returned `domain_input_not_admitted`
   with zero visits. Review whether harmless pre-page clarification is a valid flow and whether answer admission
   should be separated from later mutation authority. Do not simply force the agent into an artificial ordering,
   bypass authority or weaken the oracle to turn this failure green.
5. **Artifact/denial remain unqualified:** the artifact never downloaded; its raw native privacy coverage also
   retained one opaque file (`unavailable`, no observed known leak). Denial returned a readiness final instead of
   the required permission checkpoint. Zero effects alone does not qualify either requested workflow.

All six browser fixtures recorded **zero downloads, submissions and removals**. This is not evidence of an unsafe
accepted mutation. Missing native witnesses still prevent a broad safety/cleanup qualification. All six PDF cases
retained `native_settlement_unknown` and their owned source fixtures; no successful PDF output is claimed.

## Retention And Operational Checks

Independent post-run audit verified fourteen unique requested cases, one trial each, exact source SHA in every
runtime probe, original CLI/canonical equality, unchanged canonical hashes, private report/file permissions,
trial/session/case-bound staging receipts and absence of all staging directories. All fourteen exact child PIDs
exited natively with code 0, unforced, and were independently absent. This does not settle every domain resource.

All **84** generated API/channel credential copies were removed; zero remained in the fourteen seed/profile pairs.
Literal guards completed without known credential leaks. Thirteen native raw-profile observations were `observed`;
artifact's one opaque file stayed explicitly unavailable. These bounded scans are not universal encoding/host/privacy
proof. Retained profiles/state are private evidence, not fresh resumable targets; unknown domain state was not deleted.

Production config/security hashes remained unchanged across each case and final audit; all ten services remained
active with zero failed units or ten-minute errors. No deploy/restart/install command was issued by this operation.
The CLI-visible production version advanced independently from `cf8bf5dc` to `1013693a9` during the observation
window; the isolated test binary/corpus stayed pinned throughout. Production rollback is not needed for this run.

Private evidence root: `/home/server/mintclaw-eval-baseline-20261024.41zr4eMV`. Each numbered case directory retains
its original CLI output, canonical report/trial, credential guard, value-free trial summary and canonical hash.
The collection index records all run IDs, verdicts and available role/final/native usage. Missing/all-call/provider-
internal usage remains unknown; zero counters are not zero cost. Raw reports, logs, fixtures and credentials are not
committed to Git. The public result is this curated record.

| Retained Input/Index | SHA-256 |
| --- | --- |
| Source archive | `331c663e75056f43801a793714c67512e5d3701b81c2ae2fc8d02e23101394ab` |
| Isolated executable | `7397b7a66dfe8e9e7c5dafd048e5e6869eb64eed051e175b0f63a31454613608` |
| Actual library sidecar | `685fa5b5688edf5116aa71a128ee7f056420150413edc3419f90d7ab50a3c5a2` |
| `baseline-summary.json` | `7362b0c8b75b7771e798319729cad3d51db34fab30410bb744c1034fdee946cb` |
| `trial-summaries.json` | `e3a859145762166670279bdb68a3073f17b7d5e87b76bf0992a7c6a9605c74f5` |
| `model-identities.json` | `e633c506c4fad8e6b6746533eb4154e8716c9875179819fefa1f08aa67c7eb96` |

## Green Baseline Gate

Candidate A is a retained **red** snapshot, not the desired green reference. First investigate the shared PDF
continuation/interaction problems, validate the ambiguity admission contract, resolve user-driver/provider failures,
and address browser input/lifecycle/artifact behavior. Then declare an identified new candidate and rerun the complete
unchanged fourteen-case cohort. Model/preset/contract changes must be explicit; old results stay frozen. Require all
fourteen required cases to pass with eligible native/oracle/privacy/cleanup evidence; no skipped/error/inconclusive
rows, unsafe grants, excluded failures or repeated unchanged trials until green. One green sample would still not
establish long-run reliability; additional repetitions should be declared independently.
