# Manual Agent Evals Roadmap

## Status

Approved on 2026-10-10; implementation is active under the [first-release goal](agent-evals-goal.md).
The [design](agent-evals-design.md) records source findings, alternatives, and ownership. Agreement preceded
implementation; an admission document alone is not a functioning eval engine.

The roadmap builds a small manual quality harness, not a replacement runtime or an automatic self-improvement system.

The [private browser v1 pilot](../operations/manual-agent-evals-browser-private-v1-pilot-20261019.md) after merged
#1847 attempted submit and followup once each on separate fresh Linux profiles. Both failed with expected submissions
missing: the target used ordinary `request_user_input`, and followup also had an unavailable LLM-user provider.
No protected value, grant, correction, submission or judge qualified. Scoped raw privacy and physical exit were
observed, but unanswered logical questions remain waiting. Both failures and their offline self-comparisons are
preserved; no task was retried. Protected-form live qualification and private regrade/E5 remain open.

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

The [empty-fields v1 experiment](../operations/manual-agent-evals-pdf-empty-v1-pilot-20261019.md) on merged #1830
reported pass for cancellation/no-write, judge, offline regrade and comparison, but passive native inspection found
three errored file-tool calls and no document invocation. Its frozen pass is preserved; actual empty-PDF recognition
is **not qualified**. Repair the missing native discovery admission and source-bound inspection/field-use check,
with no-document negative controls, before claiming the empty case or rerunning a changed contract. No task/prompt
tuning or historical upgrade is authorized by that coverage gap.

The focused discovery repair admits/requires only the native BM25 tool for PDF profiles. Native composition and
actual search-promotion controls replace the previous unusable tool allowlist; broad tools remain refused before
probes or preparation effects. This prerequisite does not qualify source inspection, change the empty case, or
authorize replay of its retained used profile. Its source-use evidence repair remains mandatory.

The native field-read producer adds only content-free, source-SHA-bound `fields`/`form.discover` observations to
opt-in existing diagnostic traces. Complete schema counts remain distinct from bounded returned windows; hook
responses and unsuccessful invocations cannot invent a read. This is a producer prerequisite, not a new eval
verdict: a follow-up consumer must reuse native trace privacy/integrity/routed-scope checks, add a fixture-bound
hard check and no-document negatives, then run a new versioned empty-fields experiment. Do not upgrade the old
pass, replay its used profile, or substitute successful `capabilities`/inspect-only calls for field use.

The consumer adds an explicit `pdf_source_fields` no-output/question-turn hard check and uses the same bounded,
raw-first native-trace reader as model observations. It freezes exact fixture/route/build/call bindings and counts
before judge work; offline regrade recomputes the check without runtime access. The empty-required case's task,
driver and rubric stay unchanged, but its manifest hash now includes independently inspected fixture counts and
requires opted-in metadata capture. Known no-document evidence fails regardless of a positive judge; unknown
legacy/incomplete evidence cannot pass. Consumer merge/tests alone are not a v2 experiment or E3 qualification.

The [empty-fields v2 pilot](../operations/manual-agent-evals-pdf-empty-v2-pilot-20261019.md) on merged #1838 preserved
one **inconclusive** attempt. Native BM25 and one document call succeeded, but Gemini then returned missing
`thought_signature` HTTP 400; the unqualified error-final had no route identity. There was no field-read/question/
judge qualification or offline regrade. All credential copies were removed and production was unchanged; the
used fixture remains retained under unknown logical settlement. A protected live-call signature projection is a
concrete separate runtime triage candidate, not a reason to loosen eval gates or rerun the task. E3 and remaining
PDF/browser/private/adaptive/repeat/exit work remain open; no historical result is upgraded.

The [empty-fields v3 pilot](../operations/manual-agent-evals-pdf-empty-v3-pilot-20261019.md) on merged #1840 passed
six native Gemini request/response exchanges and reached source-bound `form.discover` with all five expected fields.
It still returned **inconclusive**: the model did not call the trusted `document` continuation tool, the runtime
guard stopped the turn and the error-final had no routed identity. No question/cancel/judge/regrade qualified;
passive native field-use and 53.2k input / 581 output token observations do not backfill frozen unavailable evidence.
All credential copies were removed; logical settlement/source cleanup remained unknown. Preserve this checkpoint,
do not weaken the protected guard or retry/tune until green, and keep target-continuation triage separate from the
remaining mandatory harness/domain work. E3 and the full goal are not complete.

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

The [browser denial v1 pilot](../operations/manual-agent-evals-browser-denial-v1-pilot-20261019.md) on merged
#1786 retained one failed `unexpected_turn_outcome` attempt: an ordinary North-removal permission question arrived
where the script required native approval. No cancel or grant was dispatched, no removal was prepared, and the
visited fixture had zero effects. The unanswered native question remains waiting after physical shutdown. Scoped
privacy and credential-copy cleanup passed, but no judge/regrade or native denial assessment qualified. This is a
scenario interaction-shape mismatch, not evidence of an unsafe product action; do not rewrite or retry the old trial.
The native path remains unqualified. The forward ordinary-question contract keeps a distinct native cancellation
scope, delivered prompt digest/outbox binding and required semantic judging; its changed driver/rubric digest
requires a separately identified fresh experiment, without upgrading or retrying the historical v1 result.

The [denial v2 experiment](../operations/manual-agent-evals-browser-denial-v2-pilot-20261019.md) on merged #1798
actually exercised that revised case once. It failed before any permission question/approval: the target returned
a final readiness claim and closed the browser; native state showed only navigation. There were zero fixture
effects, no cancel/grant and no judge/regrade. One returned final exposed 52,522 input / 586 output tokens, with
all-call coverage still unknown. Both cancellation paths remain without positive live qualification; preserve
this negative checkpoint result instead of accepting arbitrary finals or retrying/tuning target behavior away.

## Milestones

| Stage | Outcome | Dependency | Status |
| --- | --- | --- | --- |
| E0 | Agree architecture and freeze first-goal acceptance | Operator review | Approved; admission recorded |
| E1 | Run bounded scripted multi-turn cases and produce honest reports | E0 | Contracts/client/runtime admission merged; both required general scripted cases passed on Linux after #1598, with recorded platform/cleanup limits |
| E2 | Evaluate natural conversations with LLM user and independent judge | E1 | Role foundation/adaptive runner/grade merged (#1531, #1541); judge v2 live calibration passed 6/6; adaptive live qualification pending |
| E3 | Reusable PDF regression suite with independent artifact/privacy checks | E1, E2 for adaptive cases | Fixtures/oracle, preparation, approval contracts, dialogue, write intent, native write observation, metadata settlement and artifact collection merged (#1544, #1546, #1547, #1550, #1552, #1553, #1557, #1558); uniqueness/privacy/cleanup and live qualification pending |
| E4 | Reusable browser regression suite backed by fixture state | E1, E2 for adaptive cases | Read trial/regrade passed after #1754; artifact first attempt inconclusive; denial assessment merged in #1786, first attempt failed an ordinary-question/native-approval mismatch; adaptive private input, remaining scenarios and domain qualification pending |
| E5 | Workspace presets, profile provisioning, and comparable repeated runs | E3 and E4 | Repeats/all-trial regrade/comparison (#1588), owned gateway (#1639), file observations (#1648) and explicit allowlisted seed provisioning (#1665) merged; Linux general repeats/regrade/error comparison exercised; per-trial/effective-input witnesses and domain qualification pending |

E5's explicit owned-profile metadata capture prerequisite merged in #1801, with
[validation and preserved failures](../operations/manual-agent-evals-model-metadata-validation-20261019.md).
It is described in the
[operator guide](../operations/manual-agent-evals.md#opt-in-model-metadata-capture): fixed native metadata-only
limits, no attached targets/content previews/alternate roots, fresh preparation without old traces, and post-stop
raw privacy inspection even without private facts. Capture alone does not provide an effective-input witness or
all-call census. The [routed native observer](../operations/manual-agent-evals-native-models.md) adds explicit
source/turn/agent/filtered-session joins, bounded raw-before-projection reads, native request fingerprints and
available response usage across tool loops. It preserves missing/truncated/ambiguous observations, legacy usage
presence and unknown internal attempts; per-trial isolation, loaded skill-version proof and domain cases remain
mandatory. No historical run is backfilled or newly qualified by this prerequisite.

The [native-model v1 experiment](../operations/manual-agent-evals-native-models-v1-20261019.md) on merged #1814
passed one unchanged context task, independent judge, regrade and comparison, but retained a partial native
observation with two invalid scopes. Actual traces omitted provider names and contained explicit failed-attempt
retries before successful replacement requests, exposing overly strict consumer assumptions absent from its
fixtures. Preserve the original partial report; a focused native-shape fix and regression controls were required, not
invented provider identities, retroactive usage backfill or a promoted native-observer qualification.

That focused fix merged in #1820, with [retained-source validation](../operations/manual-agent-evals-native-retry-validation-20261019.md).
Both the reviewed head and exact merge source accepted the actual v1 metadata in a read-only control: four
requests, two responses and two bound retries, available 4,707 input / 28 output tokens, provider/failed-attempt/
all-call usage still unknown. No additional model task ran and original reports stayed partial and byte-identical.
This validates the consumer contract, not a new canonical live run, loaded-skill proof or completion of E0-E5.

The [native system-source producer](../operations/native-system-source-fingerprints.md) merged in #1826. Its
eval consumer retains validated per-request workspace-definition/catalog/active-skill content witnesses and
compares them with explicit legacy/unavailable coverage, using frozen reports rather than current files.
These are filtered runtime-labeled content identities, not complete skill/helper-version provenance or provider
wire attestation. Consumer #1830 then merged, and one
[fresh merged-source experiment](../operations/manual-agent-evals-system-sources-v1-20261019.md) passed the
unchanged context task, independent judge, offline regrade and comparison. Both actual routed requests retained
observed workspace/catalog groups and an explicitly empty active-skill group; frozen projections remained equal.
Per-trial resources and remaining domain cases/repeats/exit remain mandatory; broader version/provider/all-call
unknowns and every historical negative result are preserved.

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
   Admission now requires the native normalized session policy to retain `chat`: fresh client IDs alone do not
   prevent sender-only/empty scopes from sharing history. Native allocator controls reproduce that collision;
   shared scopes are refused before probes/tasks and before preparation opens the native repository or allocates
   a profile. This closes a configuration admission gap, not the remaining filesystem/browser resource evidence.
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
   Full private-surface/temporary-resource and positive delivery qualification still require completion.
   A bounded cancellation/no-write contract can now admit post-stop judging
   and offline regrade only with valid raw-profile/typed-role/joined-frame evidence, unchanged source, zero native
   effects and verified source removal. Protected questions additionally bind both canceled native records.
   Complete output assessments can now retain offline eligibility, but regrade separately requires exclusively v1
   generated facts (or no private facts), all frozen native/oracle witnesses and unchanged bounded private PDF bytes.
   The shared raw/typed format guard protects JSON and judge context without retaining values; the authorized binary
   remains separate from model content. Literal and historical ineligible reports stay refused. Deterministic controls
   are not live PDF qualification. Native cleanup completes for the cohort before suite-budgeted judging begins.
   One ordinary cancellation/no-write live path and offline regrade now passed on merged #1731; the other five PDF
   cases and protected/private-value/worker/output behavior still require actual qualification.
   Preserve earlier failures and unknowns.
3. E4: finish native browser admission, fixture execution, approval/artifact/context correlation and owned
   cleanup; qualify the other five cases, including adaptive and denied-action evidence. Read/artifact/denied tasks can run on
   owned profiles. The read post-stop assessment now binds raw native owner/action/policy records and final outbox
   replies, requires native shutdown, complete scoped privacy and empty owned temporary roots, and admits separate
   judging/offline regrade only after those witnesses. One Linux read trial and judge-only regrade now passed on #1754.
   Artifact now has a bounded native prompt/HMAC-bound allow-once, authenticated actual CSV collection, exact
   post-stop approval/transfer/wire join and separate judge/offline-regrade path; its live qualification is still
   pending. Its first live attempt refused a click that did not match the scoped download grant; investigate actual
   tool exposure/action choice before any new experiment. Denial now has exact native approval/HMAC-bound cancel,
   post-stop unconsumed cancellation/unaccepted invocation/outbox evidence and independent no-effect/privacy/cleanup
   checks before separate judging or offline regrade. It never grants the removal or claims which identically named
   account button would have been dispatched; live denial qualification remains pending after its preserved ordinary-
   question/native-approval mismatch. A natural permission-question path now has its own bounded native cancellation
   scope, delivered-prompt binding and required semantic criterion, without weakening the existing approval guard;
   the changed case revision was actually run in v2 and failed before a permission checkpoint, so neither
   cancellation branch gained positive live qualification. This is a recorded negative result, not permission
   for an open-ended target-repair loop. The other three scenarios
   previously remained gated. Submit/followup now have the scoped workflow described below; ambiguity now has
   the owned clarification/approval/post-stop workflow, with a fresh identified live attempt still required.
   A [native reference-region producer](browser-reference-regions.md) now binds
   direct-library main-document button refs to bounded unique ARIA-region context,
   including approval hashes and fresh revalidation. It grants no permission by itself.
   [Ambiguity admission A](agent-evals-browser-ambiguity.md) adds the native
   answered-question, unchanged owned-page and exact South-only approval join,
   reusing existing HMAC/single-use authority. B now connects typed post-stop
   receipts, complete native action/prompt/delivery/cleanup inspection and the
   independent South-only fixture oracle before judging/offline regrade. It
   activates the scripted question/approval case only on owned metadata-captured
   profiles; it does not qualify a live attempt or upgrade older skipped reports.
   The [ambiguity v1 pilot](../operations/manual-agent-evals-browser-ambiguity-v1-pilot-20261020.md) on merged #1853
   then reached clarification and exact South approval but failed before consumption: the harness's descriptive
   typed allow-once content violated the native channel's canonical-label contract. The sealed fixture retained
   zero effects and a hard failure; no judge or used-profile retry ran. The bounded canonical-label producer fix
   and real-channel regression merged in #1854 without changing channel validation or approval authority.
   Its [fresh v2 experiment](../operations/manual-agent-evals-browser-ambiguity-v2-pilot-20261020.md) completed the
   script, consumed native approval and independently removed exactly South, but stayed inconclusive: the
   collector mistook the native control-removing final for another prompt. A bounded native-predicate/final-identity
   correction is required; neither old report is promoted and no source records may be filtered away.
   No local fixture pass qualifies native browser behavior.
   The [private-input prerequisite](agent-evals-browser-private-input.md) separates the missing native ingress from
   already protected model-produced fill. B1 adds only generation-bound, value-free broker target bindings and
   transient staging through existing preparation; it adds no question, sink, dispatch bypass, eval admission or
   live qualification. B2 connects native protected questions/claims/resource retention/resume; B3 must add the
   eval private-fact consumer and independent mutation/context evidence before enabling either required case.
   B3 now adds pre-answer native question/trace/current-session joins and explicit per-field initial/corrected
   fact-handle mapping. The complete workflow now connects fresh per-trial cohort generation, actual accepted fills
   and correction, native question confirmation followed by exact HMAC-bound submit allow-once, unchanged reviewed
   page/context, independent submitted values and post-stop native/delivery/privacy/cleanup assessment before judging.
   Trials require owned startup and explicit metadata capture before any probe/model work. Offline controls are not
   live qualification: fresh identified private-form experiments remain mandatory. Private offline regrade now admits
   only otherwise eligible browser forms with exclusively v1 generated facts, using a conservative public-format
   guard on raw reports and typed judge boundaries rather than retaining values. Literal facts remain gated;
   generated PDF output has its own native/artifact contract. These features do not make either failed private-form
   pilot eligible or qualify a live regrade.
   Ordinary chat substitution and a new value/vault/approval engine remain forbidden.
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
