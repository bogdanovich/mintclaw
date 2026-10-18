# Manual Agent Evals Roadmap

## Status

Approved on 2026-10-10; implementation is active under the [first-release goal](agent-evals-goal.md).
The [design](agent-evals-design.md) records source findings, alternatives, and ownership. Agreement preceded
implementation; an admission document alone is not a functioning eval engine.

The roadmap builds a small manual quality harness, not a replacement runtime or an automatic self-improvement system.

The [target usage v1 pilot](../operations/manual-agent-evals-target-usage-v1-pilot-20261018.md) on merged #1766 passed
four new general trials. One regrade preserved one pass and three provider-unavailable judge errors; comparison
retained every trial. Four correlated target finals had no wire usage blocks despite native outbox counters:
the non-streaming channel projection needs a scoped repair. Original receipts remain unknown, not backfilled;
returned-final observations are not all-call usage or completion of the frozen goal.

The [target usage v2 pilot](../operations/manual-agent-evals-target-usage-v2-pilot-20261018.md) on merged #1772
then passed one new Linux context trial, one judge-only regrade and comparison. Both actual wire finals retained
usage (4,770 input / 27 output); old reports were not backfilled. This qualifies returned-final collection only,
not full all-call coverage, domain completion or the remaining frozen-goal work.

The [first live pilot](../operations/manual-agent-evals-pilot-20261011.md) is preserved with its inconclusive
interaction and failed 4/6 judge calibration. The subsequent
[v2 judge calibration](../operations/manual-agent-evals-judge-v2-calibration-20261011.md) agreed on 6/6 unchanged
labels. After a reviewed native outbox observer fix, the
[general v3 pilot](../operations/manual-agent-evals-general-v3-pilot-20261011.md) passed both required scripted
general cases on an isolated Linux profile. The goal remains active; these results are not adaptive, PDF/browser,
macOS live, complete cleanup or full-suite qualification.

The [owned general v1 pilot](../operations/manual-agent-evals-owned-general-v1-20261017.md) subsequently passed
both scripted general cases on merged #1639, including exact owned child admission, native stop/reap and observed
exit 0. Its reported run time was 6,440 ms after admission, not cold build/startup time. Bounded workspace file
observations (#1648) are merged separately; they are not effective prompt/catalog proof or part of that older run.
The retained [PDF cancellation v2](../operations/manual-agent-evals-pdf-cancel-v2-pilot-20261012.md) and
[adaptive PDF fill v1](../operations/manual-agent-evals-pdf-fill-v1-pilot-20261012.md) remain inconclusive, not erased
by passing general cases.

The [prepared general v1 pilot](../operations/manual-agent-evals-prepared-general-v1-20261017.md) exercised merged
#1665's allowlisted `eval prepare`, then passed four general trials (two repetitions per case) on its identified
code head. A single regrade retained two passes and two provider-unavailable judge errors; comparison preserved all
trials without attributing those errors to target quality. This is not complete E5 or domain qualification.

The [PDF post-stop v1 pilot](../operations/manual-agent-evals-pdf-post-stop-v1-pilot-20261018.md) exercised merged
#1706 once: scoped cancellation/no-write passed after native shutdown, but the trial remained inconclusive because
seven opaque private-profile files prevented privacy qualification and source cleanup. No protected PDF job,
positive artifact delivery or judge call was observed. This does not qualify the six-case PDF suite.

The [no-write v1 live result](../operations/manual-agent-evals-no-write-v1-20261018.md) on merged #1731 subsequently
passed one ordinary cancellation/no-write trial, one judge-only regrade and comparison. Valid scoped privacy,
post-stop/source-removal evidence and a separate judge were observed; the previous inconclusive reports remain
unchanged. This is not protected/private-value/worker/output or complete PDF/browser-suite qualification.

The [PDF output v1 pilot](../operations/manual-agent-evals-pdf-output-v1-pilot-20261018.md) on merged #1741 then
preserved one inconclusive `pdf.loan.fill` attempt. A native ordinary-question continuation recorded HTTP 400 with
a missing Gemini `thought_signature`; no filled artifact or final output assessment was reached. Scoped privacy
also retained one opaque file. The target-defect triage and credential-copy cleanup are recorded separately from
the remaining harness/domain qualification; no task was retried or prior result upgraded.

Live model/tool scenarios never become a mandatory CI workload. Fast deterministic tests for the harness itself
may run in normal CI without credentials, a live gateway, paid models, or installed browser/PDF executables.

The [browser read v1 pilot](../operations/manual-agent-evals-browser-read-v1-pilot-20261018.md) on merged #1746
retained one inconclusive attempt: the script completed, but native driver startup failed and the fixture observed
zero page visits. Model-free long/short temporary-path controls identified a Chrome Unix-socket path-length defect.
The owned gateway/listener stopped and five credential copies were removed; native grading/privacy/cleanup and all
six browser cases remain unqualified. The temporary-path prerequisite must preserve native isolation and recovery.

The [browser read v2 pilot](../operations/manual-agent-evals-browser-read-v2-pilot-20261018.md) on merged #1748
then verified repaired startup but stopped at an unbound native navigation approval, again with zero fixture visits.
Native `navigation` is distinct from the current read/local-edit allow rules. Both attempts remain inconclusive;
exact fixture-navigation admission/action correlation is next, not broad approval or a promoted page-read pass.

Exact owned-fixture navigation admission then merged in #1750. The
[browser read v3 pilot](../operations/manual-agent-evals-browser-read-v3-pilot-20261018.md) visited the page once,
matched its fresh code and completed two scripted turns with no fixture mutations. The overall result remained
inconclusive: native owner/execution/action assessment, privacy and cleanup are not qualified. All earlier outcomes
remain unchanged; complete native correlation and the six-case browser suite are still mandatory.

The [browser read v4 pilot](../operations/manual-agent-evals-browser-read-v4-pilot-20261018.md) on merged #1754
then passed one Linux local-owned read trial after native shutdown, with exact native action/final-delivery binding,
complete scoped raw-profile coverage and an independent judge. One judge-only regrade also passed; comparison
preserved v3's inconclusive result. This qualifies the read path only. The other five browser cases, adaptive work,
remaining PDF cases, effective loaded inputs and available all-call usage remain mandatory.

The [browser artifact v1 pilot](../operations/manual-agent-evals-browser-artifact-v1-pilot-20261018.md) on merged
#1761 retained one inconclusive attempt: the model prepared a click instead of a native deliverable download,
so the runner refused the grant. The fixture recorded zero downloads and mutations; no judge ran. The task was
not retried and policy was not widened. The complete artifact assessment code is not positive live qualification.

## Milestones

| Stage | Outcome | Dependency | Status |
| --- | --- | --- | --- |
| E0 | Agree architecture and freeze first-goal acceptance | Operator review | Approved; admission recorded |
| E1 | Run bounded scripted multi-turn cases and produce honest reports | E0 | Contracts/client/runtime admission merged; both required general scripted cases passed on Linux after #1598, with recorded platform/cleanup limits |
| E2 | Evaluate natural conversations with LLM user and independent judge | E1 | Role foundation/adaptive runner/grade merged (#1531, #1541); judge v2 live calibration passed 6/6; adaptive live qualification pending |
| E3 | Reusable PDF regression suite with independent artifact/privacy checks | E1, E2 for adaptive cases | Fixtures/oracle, preparation, approval contracts, dialogue, write intent, native write observation, metadata settlement and artifact collection merged (#1544, #1546, #1547, #1550, #1552, #1553, #1557, #1558); uniqueness/privacy/cleanup and live qualification pending |
| E4 | Reusable browser regression suite backed by fixture state | E1, E2 for adaptive cases | One Linux owned read trial/regrade passed after #1754; artifact assessment merged in #1761, first live attempt inconclusive; other five cases, adaptive private input, protected action and domain qualification pending |
| E5 | Workspace presets, profile provisioning, and comparable repeated runs | E3 and E4 | Repeats/all-trial regrade/comparison (#1588), owned gateway (#1639), file observations (#1648) and explicit allowlisted seed provisioning (#1665) merged; Linux general repeats/regrade/error comparison exercised; per-trial/effective-input witnesses and domain qualification pending |

E3 and E4 share the runner, not one another's domain logic. Keep implementation PRs focused; split a milestone into
prerequisites when necessary. Do not reimplement protected intake, browser sessions, approval, or delivery in the harness.

E3 preparation uses native media identity (#1536), bounded read-only interaction snapshots (#1538), and owner/integrity-
checked public form-job observations (#1540). These do not authorize an answer or approve a write. The
[PDF fixtures/oracle guide](../operations/manual-agent-evals-pdf.md) records the independent witness and its limits;
its manual controls are not real-model scenario results.

The [browser fixture guide](../operations/manual-agent-evals-browser.md) records independent server-side witnesses,
bounds and unresolved native admission. Fixture controls are not browser task qualification. Prepared eval
admission rejects non-loopback native gateway listener plans (#1562), without changing ordinary gateway binding.

## Next Mandatory Work

1. E5: finish per-trial isolation and actual effective-input identity using the existing native boundaries and
   explicit fresh-profile preparation. The merged one-time conversation/PDF seed provisioner and file samples do
   not complete automatic per-trial/browser provisioning or effective loaded prompt/catalog proof.
2. E3: finish native post-settlement delivery/no-write/privacy/cleanup witnesses and qualify all six PDF cases,
   including adaptive conversation and deterministic denial/cancel. The owned post-stop raw file observation retains
   private facts until scanning and fails known leaks, but is not whole-runtime or per-trial privacy/idle proof.
   PDF preparation supplies only a new empty native write journal; owned child temp stays inside its private home.
   These isolation prerequisites do not grant positive checks or complete the post-settlement work.
   Exact compiled public-system-file witnesses can explain unchanged bundled text, not active loaded catalogs or
   SQLite and other opaque surfaces; known literal guards still run before that identity check.
   A [synthetic SQLite probe](../operations/manual-agent-evals-sqlite-probe-20261018.md) retained a full encoded
   canary after native session clearing despite zero current message/FTS rows. Current-row-only SQL plus literal
   matching cannot replace raw privacy coverage; this negative control must survive the native witness work.
   The bounded native SQLite witness combines current schema/value decoding with original contiguous raw JSON-
   escape matching, never row-only promotion or original-store mutation. Its explicit encoding/overflow limits do
   not establish whole-runtime/all-role privacy or complete E3 qualification.
   Typed LLM-user/judge argument and response guards cover reasoning/discarded tool fields and the run's retained
   earlier-trial facts, with explicit bounded receipts before hashing/projection. Unknowns and leaks survive
   regrade/comparison; this is not raw transport/provider-internal coverage or PDF judge/regrade admission.
   The bounded client-media witness binds actual requestless messages to native settlement, and owned raw-frame
   inspection includes earlier trials' retained facts. Neither grants future delivery uniqueness or full privacy.
   Owned PDF finalization now retains bounded native baselines through shutdown and raw scan, publishes canonical
   trial reports afterward, checks scoped no-write evidence and removes only verified settled source fixtures.
   Full private-surface/temporary-resource coverage, positive delivery qualification and PDF judge/regrade eligibility
   still require completion. A bounded cancellation/no-write contract can now admit post-stop judging
   and offline regrade only with valid raw-profile/typed-role/joined-frame evidence, unchanged source, zero native
   effects and verified source removal. Protected questions additionally bind both canceled native records.
   Private-fact/output cases remain gated; deterministic controls
   are not live PDF qualification. Native cleanup completes for the cohort before suite-budgeted judging begins.
   One ordinary cancellation/no-write live path and offline regrade now passed on merged #1731; the other five PDF
   cases and protected/private-value/worker/output behavior still require actual qualification.
   Preserve earlier failures and unknowns.
3. E4: finish native browser admission, fixture execution, approval/artifact/context correlation and owned
   cleanup; qualify the other five cases, including adaptive and denied-action evidence. Read/artifact tasks can run on
   owned profiles. The read post-stop assessment now binds raw native owner/action/policy records and final outbox
   replies, requires native shutdown, complete scoped privacy and empty owned temporary roots, and admits separate
   judging/offline regrade only after those witnesses. One Linux read trial and judge-only regrade now passed on #1754.
   Artifact now has a bounded native prompt/HMAC-bound allow-once, authenticated actual CSV collection, exact
   post-stop approval/transfer/wire join and separate judge/offline-regrade path; its live qualification is still
   pending. Its first live attempt refused a click that did not match the scoped download grant; investigate actual
   tool exposure/action choice before any new experiment. The other four scenarios remain gated. Submit/followup
   retain private-input gates; ambiguity/denial retain
   native-action evidence gates. No local fixture pass qualifies native browser behavior.
   Artifact finalization can now retain a bounded native download/transfer/media-outbox metadata join, using a
   read-only native transfer inspection API rather than constructing/reconciling a store. This receipt does not
   admit a judge, approve an action or validate actual wire/artifact bytes; the full artifact contract requires
   all of those independent witnesses. Deterministic controls are not a live artifact result.
4. Exit: extend the exercised general repeats/regrade/comparison to qualified domain evidence, available all-call
   usage and remaining negative controls; record platform/transport/oracle limits and merge the exit record. Do not
   turn unavailable
   usage, incomplete native evidence or a two-case smoke into complete-suite success.

These are unfinished parts of the frozen first goal, not new milestones or permission to expand its scope.

## E0: Agree And Admit

The approved first runtime target and deployment arrangement: attach to an operator-prepared isolated
gateway profile via loopback; use an on-host runner for a remote gateway. No production mutations or automatic deploy.

The [goal](agent-evals-goal.md) freezes the first contracts, default budgets, minimum fixtures/cases, PR packets,
required evidence, isolation/approval admission, and exact stop criteria. Two general cases are E1 acceptance evidence.

**Exit:** operator accepts the design and bounded implementation goal. A merged design alone does not mean an eval
engine exists or the PDF/browser suites are qualified.

## E1: Bounded Runner And Scripted Cases

Implement proposed `mintclaw eval list`, `validate`, and `run`, with suite/tag/case selection and plan/preflight before
billable work. Store strict versioned case definitions and non-secret target templates in the repository.

Add only the shared session client needed by the existing `agent live` command and new runner. Preserve current
live CLI behavior. Support ordinary follow-ups, streaming revisions, pending interactions, prompt-bound typed
navigation, terminal outcomes, and bounded evidence collection. Consume events continuously during driver work.
Do not prematurely add a second coding-host or Telegram implementation.

Generate fresh per-trial session and scratch identities. Verify returned route/workspace. Require a prepared safe
target and declared capability/side-effect surface. Default to serial execution and no automatic approval.
Make preflight refuse mutating cases whose isolation cannot be established.

Write private versioned JSON results and a Markdown summary with case/target identity, per-turn evidence references,
mandatory checks, timing, limits, and explicit pass/fail/error/inconclusive/skipped verdicts. Aggregate coverage
without treating unqualified cases as passes. No UI, server, or result database.

Required deterministic runner tests:

- schema validation, duplicate/unknown fields, unsafe paths, selection, and no network calls during validation;
- same-session follow-up with new request IDs and no transcript replay;
- foreign-session/request events, streaming revisions, duplicate events, and final-versus-case completion;
- interaction identity binding, stale/foreign response refusal, admission race, and unsafe approval refusal;
- final text without required task evidence, media-before/after-final, and no false artifact success;
- timeout/disconnect/cancellation with no request replay, bounded settlement, and owned-resource cleanup;
- truncated/missing evidence, private output permissions, non-echoing errors, and accurate result reduction.

**Exit:** one real-model two-turn context case and one controlled interaction/navigation case run against an isolated
gateway, are independently checked, and yield auditable reports. All negative fixtures fail closed. Shared-client
refactoring does not regress `agent live`. Offline validation and runner tests work on Linux and macOS.
No claim of complete PDF/browser task evaluation at this stage.

## E2: LLM User And LLM Judge

Add an adaptive user driver using a separately configured caller-mediated, tools-disabled provider. Give it the
persona, natural task, permitted transcript, and pending prompt shape. Require strict bounded typed actions;
runner-owned bindings and policy authorize actual wire input. Keep scripted drivers available.

Add a separate rubric judge that sees only permitted evidence and emits criterion findings with valid evidence
references. Hard checks remain authoritative. Pin and fingerprint model roles, prompts, rubrics, and generation
settings. Report unsupported tool-free providers rather than starting a CLI agent with host tools.

Keep private canary values behind runner fact handles. Do not expose golden field mappings to the simulator or
ask the target to score itself. Judge errors/incomplete evidence cannot become passes. Bound turns, calls, context,
and time; count usage separately for target, user driver, and judge with explicit unknown/coverage states.

Add `grade` to regrade retained eligible evidence without rerunning tasks. Validate safe redaction/retention before
transcript evidence reaches the judge. No default publication or provider-retention claim.

Calibration/negative cases include coherent versus mismatched controls, repeated accepted facts, fake success,
fabricated persona facts, malformed/injected judge output, references to nonexistent evidence, and attempted
approval-policy widening. Pin manually labelled calibration expectations alongside prompts.

**Exit:** an adaptive conversation completes within bounds; changing ordinary wording does not break it. A known
bad conversation or hard artifact failure cannot be judged pass. Judge-only regrading performs no gateway/tool
work. Reports identify all model roles and call/usage coverage. Ordinary CI uses stub providers only.

## E3: PDF Suite

Reuse pinned PDFI3 fixture generation and current PDF production contracts. Build domain checks outside the shared
runner: independent field readback, source digest, preserved/blank fields, visible/structural verification,
editability where promised, approval ordering, and document-operation/outbox-bound single logical delivery.
Include authenticated bounded media download; never rely only on a caption or `success` response.

First cases:

1. Small generic form: understand, ask coherent questions, supply synthetic data, review, separately confirm, deliver.
2. Clarify/Back plus natural correction: preserve the job, change the intended fact, invalidate old review/approval,
   verify the last corrected value in the output and unchanged unrelated fields.
3. Conditional applicability: clarify only material intent, do not invent missing routing or unrelated-person facts.
4. Source with serialized empty values: blank required fields are not treated as already complete.
5. Cancel or withhold confirmation: no write or delivered PDF, with truthful suspended/canceled state.
6. Bounded large-form section: coherent requested section and truthful scope, no raw-order or full-completion claim.

Add capability-gated PDFI4 free-text/multi-fact/partial-editable cases against its implemented contract; do not pass
them merely by scripting legacy `/answer` replies. Keep unsupported editable-hybrid output an explicit refusal case,
not a silently skipped promised capability. An official I-134 fixture is conditional on provenance and a clearly
admitted backend/output scope; no agency-specific production behavior.

Privacy tests use fresh private sentinels. Scan admitted raw runtime/eval surfaces before sanitization or deletion.
Reports contain predicates, counts, scope, and evidence IDs, never leaked plaintext. Pinned independent oracle
dependencies are optional for the runner but required for cases claiming their coverage.

**Exit:** the first six scenarios run through the real agent with independent results, including observed negative
outcomes; at least one adaptive case and one deterministic safety case exist. Linux/macOS coverage is explicit by
backend and oracle. Gateway transport results do not claim actual Telegram upload/reply/callback qualification.
Cross-channel ingress and full large-form filling remain separately admitted cases, not implicit suite promises.

## E4: Browser Suite

Reuse/adapt the safe browser smoke fixture. Add server-side event/state witnesses that the agent cannot replace
with a claimed result. Use an ephemeral eval profile/context, explicit origin permissions, and preflight checks for
the actual browser placement and driver. Keep broad privileged execute and cloud/billable profiles out of defaults.

First cases:

1. Open the fixture, read known page information, and answer accurately with real browser evidence.
2. Fill a synthetic form, choose an option, and submit to the fixture; check server-observed values/events.
3. Multi-turn change request: retain the same browser conversation/context without silently reopening a fresh task.
4. Deliberately ambiguous/stale target: ask or re-observe instead of acting on a stale reference or wrong control.
5. Download or screenshot: deliver and independently validate the actual artifact when the case requires it.
6. Denied/canceled protected action: no forbidden side effect, no private-value leak, and clean owned-resource closure.

Do not make exact tool counts or assistant phrasing normal quality criteria. Assert only identity, freshness,
approval, privacy, state, and cleanup invariants. Include screenshot-based judge input only where it improves a
specific rubric and the configured judge supports it; fixture state remains the hard oracle.

**Exit:** real browser cases produce verified state/artifacts, one adaptive user conversation, and safe negative
outcomes. Record the tested platform, driver, and gateway/companion placement. No public-site/account mutations.
A gateway-only pass does not claim companion, cloud, or all browser-provider parity.

## E5: Workspace Presets And Baselines

Add operator-reviewed preset provisioning from allowlisted instructions, skills, synthetic data, and capability
policy. Reuse existing MintClaw configuration/profile behavior. Credentials stay in local references. Explicitly
exclude production sessions, general memory, ledgers, and live browser state from copied targets.

Support private workspace-local case roots with the same strict schema as repository suites. Record effective
instruction/skill/config fingerprints and actual route identity. Real workspace runs require explicit side-effect
admission; no claim that a copied profile is the unchanged live workspace.

Add `--repeat` and `compare` for compatible case cohorts. Retain all trials; report pass/trial counts, errors,
inconclusive/skip coverage, and case-level changes. Separate quality drift from environment/model/config changes.
Keep raw token integers in JSON and compact `10.2k`-style display in summaries. Aggregate all available model calls,
not the final response alone; report unknown private/delegated usage rather than inventing totals or costs.

**Exit:** an operator can list/validate/run all cases, PDF only, browser only, tags, or one case, select a safe
workspace preset, repeat trials, regrade evidence, and compare to a chosen baseline. A case with a missing judge,
oracle, required artifact, unsupported capability, or failed cleanup cannot silently make the suite green.

## First Release Stop Criteria

An implementation goal covering E1-E5 is complete only when:

1. The agreed command/manifest/result contracts are implemented and documented with safe target setup examples.
2. Scripted and adaptive multi-turn drivers use the real same-session agent, with separate non-authoritative judging.
3. The initial PDF and browser cases have retained exact-revision qualification evidence and independent oracles.
4. Negative/calibration cases demonstrate that fabricated success, unsafe approval, privacy leaks, and missing
   evidence do not pass.
5. Suite/tag/case/preset selection, repeats, offline regrading, and compatible baseline comparison work.
6. Harness unit/contract tests, formatting/lint, platform checks, and exact-head PR review gates pass without live
   evals being added to normal CI. Code PRs receive actual review and authorized approval before merge.
7. Live evidence states which platforms/targets/channels were tested, dependencies, usage coverage, known limits,
   cleanup results, and how to reproduce safely. A separate deployed smoke occurs only if admitted and necessary.
8. Implementation/exit documentation is merged and no mandatory in-scope work remains. A published PR, design,
   first-question smoke, or aggregate judge number is not completion.

Do not create an unbounded goal to "keep improving evals." Subsequent incidents add focused cases or independent
milestones with new acceptance criteria, not endless expansion of the first release.

## Explicitly Deferred

- Live evals on every commit, required CI or automatic production schedules.
- Dashboard, result database, distributed workers, arbitrary hooks/plugins, and a generic workflow language.
- Autonomous skill/code/config modification, automatic deploy, or using the judge as runtime authority.
- Coding-host/Telegram/other-channel adapters until a concrete admitted case requires them.
- Windows runtime qualification, public-account mutations, cloud browser spending, privilege escalation, and
  automatic production-workspace approval.
- Generic secret-store/protected-input extraction, new PDF backends, browser-provider parity, and unrelated refactors.
- Large statistical experiment infrastructure; reconsider an existing evaluator framework if that becomes necessary.
