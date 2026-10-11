# Manual Agent Evals Roadmap

## Status

Approved on 2026-10-10; implementation is active under the [first-release goal](agent-evals-goal.md).
The [design](agent-evals-design.md) records source findings, alternatives, and ownership. Agreement preceded
implementation; an admission document alone is not a functioning eval engine.

The roadmap builds a small manual quality harness, not a replacement runtime or an automatic self-improvement system.
Live model/tool scenarios never become a mandatory CI workload. Fast deterministic tests for the harness itself
may run in normal CI without credentials, a live gateway, paid models, or installed browser/PDF executables.

## Milestones

| Stage | Outcome | Dependency | Status |
| --- | --- | --- | --- |
| E0 | Agree architecture and freeze first-goal acceptance | Operator review | Approved; admission recorded |
| E1 | Run bounded scripted multi-turn cases and produce honest reports | E0 | Offline contracts and shared client merged; admission/reporting in progress |
| E2 | Evaluate natural conversations with LLM user and independent judge | E1 | Not started |
| E3 | Reusable PDF regression suite with independent artifact/privacy checks | E1, E2 for adaptive cases | Not started |
| E4 | Reusable browser regression suite backed by fixture state | E1, E2 for adaptive cases | Not started |
| E5 | Workspace presets, profile provisioning, and comparable repeated runs | E3 and E4 | Not started |

E3 and E4 share the runner, not one another's domain logic. Keep implementation PRs focused; split a milestone into
prerequisites when necessary. Do not reimplement protected intake, browser sessions, approval, or delivery in the harness.

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
