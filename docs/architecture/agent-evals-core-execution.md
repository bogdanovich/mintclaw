# Core Evals Execution Ledger

Started: 2026-10-24. The operator authorized all twelve contracts in the
[core roadmap](agent-evals-core-roadmap.md). C0 is complete in
[#1868](https://github.com/bogdanovich/mintclaw/pull/1868); this new C1-C3 goal
does not reopen C0 or E0-E5.

## Acceptance And Stop

- Implement all twelve loadable scenario definitions and their genuine native
  prerequisites, through focused reviewed/merged PRs with green exact-head CI,
  SHA-bound review, authorized PR-root approval and no actionable feedback.
- Add model-free success/failure/unknown, admission and bypass controls; preserve
  Linux/macOS portability and run relevant formatting/lint/broader/race tests.
- Never replace native witnesses with fluent prose, a stub pass, sleeps or a
  sequential follow-up after final. Reuse native scheduler/interaction authority;
  do not copy a steering queue or build another approval engine.
- Use fresh synthetic isolated profiles and native resolution of previously
  authorized credentials. Bound inputs/sessions, target calls/tokens, time and
  cleanup. Script users; semantic judges never override mandatory hard checks.
- Per scenario, retain one qualification attempt and one fixed three-trial batch
  on an immutable candidate. Preserve all outcomes, no retry-until-green. Promote
  only independently evidenced 3/3 passes. Failed/unavailable qualification may
  remain pending with reproducible evidence and concrete blockers.
- Stop after all twelve implementations/prerequisites are merged and a fixed,
  honest result matrix is delivered. No extra packs, indefinite runtime repair,
  production deploy/service/config mutation, production PID adoption, broad
  tool/approval authority, or PDF/browser qualification.

## Packet Order

| Packet | Dependency | Deliverable | Status |
| --- | --- | --- | --- |
| C0 | None | Two active/default general cases; PDF/browser pending; proposal | Complete, #1868 |
| C1a | C0 | Per-case narrowing and three-turn correction with final-only absence oracle | Complete, [#1872](https://github.com/bogdanovich/mintclaw/pull/1872); 14/14 CI and reviewed merge |
| C1b transport | C0 | [Prompt-bound ordinary answers](../channels/mintclaw.md) through existing native projection | Complete, [#1874](https://github.com/bogdanovich/mintclaw/pull/1874); reviewed green merge including wire UTF-8 regression |
| C1b | C1a | Ordinary answer/resume native observer and stale/wrong-prompt controls | Complete, [#1881](https://github.com/bogdanovich/mintclaw/pull/1881); qualified in C1q |
| C1q | C1b | Explicit target-call/token qualification bounds and retained metadata checks | Complete: #1884, #1882, #1885, #1887 and #1888 merged; both cases qualified 1 + fixed 3/3 on `c69659c5` |
| C2 | C1q | Active-turn barrier, bounded in-flight driver, steering/mode/order and two-session isolation | Implemented, #1890-#1893; boundary/order fixed batches qualified; isolation pending quota |
| C3a | C2 | Narrow read-only fixtures and tool failure/recovery | Implemented/merged #1895; qualification pass, fixed batch 1 pass/2 judge errors; pending |
| C3b | C2 | Native active stop/reuse, distinct from local driver stop | Implemented/merged #1897-#1898; native model-free validation and green reviewed CI; live qualification pending |
| C3c | C3a | Exact-prompt denial and native terminal/no-effect proof | Implemented/merged #1899-#1900; native model-free validation and green reviewed CI; live qualification pending |
| C3d | C2 | Final-once delivery with streaming/non-streaming controls | Implemented; real-CLI repeat-three control, negative/unknown controls and frozen grading validated; durable gateway streaming guard is an explicit qualification blocker |
| C3e | C1q | Explicit owned restart; no bypass of fresh-home admission | Foundation #1903 merged; full typed case/run-scoped rollup and real-CLI repeat-three/frozen grading pass; review/merge and live qualification pending |
| C3f | C1q | Controlled small context and actual compaction witness | Queued |
| C3g | C1q | Pinned native skill selection and no-skill control | Complete implementation #1906 merged after all-green CI and SHA-bound review; native model-free repeat-three/frozen controls pass; live qualification not started |
| Qualification | All prerequisites | Fixed attempts/batches, result matrix, explicit promotion | C1/C2/C3a fixed campaigns complete; C3b and later live campaigns not started; goal active |

Split genuine prerequisites when needed, but do not silently redefine contracts
or add capabilities. Apply the autonomous PR skill's architecture checkpoints.

## Scenario Matrix

The [skill context observation](../reference/skill-context-observation.md)
commits to existing typed native snapshots without raw identity or full-payload
logging. The [C3g contract](../reference/core-skill-selection-eval.md) joins that
native witness to a pinned file, actual `/use`, canonical history, deliveries
and contrasting controls. It remains pending/non-default, not live-qualified.

The C3g model-free native repeat-three control passes locally. Broader and
rebased race attempts on this macOS host stalled in `_dyld_start` before Go
started (12 KiB RSS, zero CPU); those attempts were sampled and stopped, not
counted as passes. Exact-head CI for #1906 subsequently supplied all-green
broader/race and platform merge gates before its reviewed merge.

The [owned restart foundation](../reference/eval-owned-restart.md) is an
internal prerequisite. The [full restart contract](../reference/core-owned-restart-eval.md)
adds actual same-session continuation and independently retained generation
evidence; it remains pending/non-default, not live-qualified. Neither path
introduces a public used-home/attach option.

| Scenario | Definition/Harness | Required Native Evidence | Live/Default |
| --- | --- | --- | --- |
| `general.context.correction` | C1a scripted | Same session, corrected final, old marker absent from final; semantic judge | Qualification pass + fixed 3/3; active/default |
| `general.interaction.answer` | C1b scripted | Delivered prompt, terminal answer, one paired continuation/tool result | Qualification pass + fixed 3/3; active/default |
| `general.steering.boundary` | C2 native held-read driver/observer | Active barrier, enqueue/injection, paired result, final census | Qualification + fixed 3/3 pass; active/default |
| `general.steering.order` | C2 explicit same-sender mode variants | FIFO/coalescence, no orphan/duplicate final; exact edge races in native Go tests | Both mode batches 3/3; one failed qualification retained; active/default |
| `general.session.isolation` | C2 scope-pair, #1893 | Distinct owners, enclosed held reads, scoped markers/history/delivery | Qualification pass; batch 3 inconclusive from HTTP 429/unknown usage; pending |
| `general.tools.recovery` | C3a, #1895 | Failed read then successful pinned-fixture read | Qualification pass; batch 1 pass/2 judge provider errors; pending/non-default |
| `general.control.stop` | C3b typed in-flight native control | Terminal abort, canceled held read, absent queued B, canonical pairing, outbox census and new final | Pending/non-default; no live qualification |
| `general.approval.denied` | C3c, #1900 | HMAC-bound exact denial, terminal resolution, canonical pairing, no execution, unchanged target and complete delivery | Pending/non-default; no live qualification |
| `general.delivery.final-once` | C3d typed native settlement/history/delivery observer | Stable create/update identity; delivered receipt per request; complete terminal census | Pending/non-default: durable gateway intentionally disables configured streaming; no paid qualification |
| `general.history.restart` | C3e retained owner/successor and generation-scoped observer | Distinct native child receipts; decreasing budget ceiling; unchanged history prefix and grounded recall | Pending/non-default; model-free native repeat-three/offline grade and broader controls pass; no live qualification |
| `general.context.compaction` | Queued C3f | Actual compaction receipt and corrected recall | Not present |
| `general.skills.selection` | C3g typed native selection observer and pinned read-only profile | Complete native revision digest; selected turn and two empty controls; unchanged file/history/delivery census | Pending/non-default; model-free real-CLI repeat-three and frozen grading pass; no live qualification |

## C1a Contract

C3d's [delivery contract](../reference/core-final-delivery-eval.md) retains a
real stream requirement instead of accepting ordinary finals. Its explicit
nonstreaming native control is not a streaming qualification. No runtime
delivery guard or durable spool is bypassed to manufacture a green result.
The bounded wire census observes both sent request IDs before follow-up
filtering and through reader shutdown. A late duplicate first final or update
is rejected even when unchanged canonical history/outbox and the filtered
transcript would pass; frozen grading requires that complete capture.

Optional catalog `cases` narrows individual suite members without changing case
JSON/digests. Overrides name unique existing cases, validate like suite policies,
and cannot activate a pending parent or add default membership the parent forbids.
Other members inherit their parent unchanged. Catalog-free private corpora retain
their old behavior; historical grade/compare do not authorize new target work.

Correction sends three sequential requests: remember marker A, replace with B,
then return only the current marker. Randomly generated synthetic markers are
pinned in the case; there is no per-trial interpolation or protected-fact grant.
Hard checks require B, absence of A in the final, same native session and final
outcome; a required judge evaluates grounded correction. `final_response_absent`
is final-only; existing `response_absent` stays transcript-wide. An earlier
acknowledgement of A is legitimate.

At the C1a/C1b implementation merges, there were 16 cases and two defaults;
correction/answer were pending. C1a alone claimed no live pass. The later
[C1 qualification](../operations/manual-agent-evals-core-c1-20261025.md) records
one attempt and fixed 3/3 batches for both, so defaults now contain four General
cases. Their native budget/owned-profile requirement is not bypassed by promotion.
Six PDF and six browser cases stay pending/non-default.

The [fourteen-case baseline](../operations/manual-agent-evals-baseline-20261024.md)
remains red and unchanged. No old scenario, fixture or result is rewritten.

## Passive History Prerequisite

The [native session inspection API](../reference/session-inspection.md) supplies
canonical filenames and a bounded pure decoder without opening a store or
repairing dirty state. C1b will use this boundary for ordered pairing evidence;
scope admission, stable file capture, raw privacy inspection and frozen receipt
validation still belong to the eval observer. The API alone is not an implemented
answer/resume eval or a live qualification result.

The passive memory prerequisite was reviewed and merged in
[#1876](https://github.com/bogdanovich/mintclaw/pull/1876). The unrelated existing
worker terminal/write race exposed by C1b transport CI was reproduced and fixed
separately in [#1877](https://github.com/bogdanovich/mintclaw/pull/1877), with a
regression that fails on the old client, repeated tests and the full worker race
suite. Both merges required substantive exact-head review, green CI and owner
PR-root approval. No test was skipped or relaxed to hide the shutdown race.

## C1b Contract

The `general.interaction.answer` definition sends three scripted inputs:
ask one native email/paper question, answer `paper` through the delivered prompt,
then ask for the saved choice. This first pack deliberately supports that one
synthetic ordinary choice, not arbitrary questions, protected facts, approval
grants or adaptive answers. A required judge checks visible option coherence;
it cannot replace the `interaction_answered` hard check.
This covers the native MintClaw protocol, not physical Telegram/client UI button
rendering. The positive control uses the real `RequestUserInputTool.Execute`;
ordinary options expose labels/descriptions, not a public `value` field.

The existing native question observer joins waiting and resolved/answered
records, unchanged origin/questions/route, exact answer request/prompt identity,
one resume and the same scoped session. It passively captures canonical history
between equal metadata reads, without opening/recovering a session store. The
history must retain one emitted question call, one succeeding matching tool
result and exactly one assistant continuation before the next user turn. The follow-up
must recall `paper` in the same session. Missing, dirty, malformed, stale or
contradictory observations remain non-passing.

Native history may be 0644 under the admitted 0700 home: anchored directory/file
checks reject symlinks and group/other writes instead of chmodding runtime
files. Bounds are 2 MiB combined native metadata/history, 256 records, 64 KiB
metadata/individual lines and 3 MiB retained answer evidence. Raw JSON/JSONL
privacy inspection precedes projection. Retained native JSON strings/lines
preserve native schema boundaries, nulls and byte identities; offline `grade`
uses native passive parsers and the frozen observations, not current files.
Operator-owned reports are observations, not hostile-filesystem attestations;
`compare` summarizes recorded outcomes rather than performing a new grading.

Model-free controls are not live qualification. C1q's separately retained live
campaign qualifies these two contracts. C3e restart, C3f compaction and C3g skill
selection remain queued; C3b-C3d implementations do not claim live qualification.

## C2 Active Tool Prerequisite

The [active tool fixture](../reference/eval-active-tool-fixture.md) uses the real
MCP SDK/native client to hold a synthetic read until driver release. It has no
model-facing release authority, file/network target arguments or paid CI work.
At most two calls are active; entry/release/settlement are independently ordered.
Missing, canceled and closed calls cannot masquerade as successful release.
The existing native enqueue summary retains its filtered message hash without
raw text. Native event logging remains asynchronous and may drop evidence.

The primitive was reviewed/merged in
[#1890](https://github.com/bogdanovich/mintclaw/pull/1890), with all applicable CI
green, exact-head substantive review and owner PR-root approval. It did not widen
eval admission or claim C2 qualification.

The following foundation, reviewed/merged in
[#1891](https://github.com/bogdanovich/mintclaw/pull/1891), admits only this exact owned fixture with the existing
native budget, metadata capture, pinned steering mode and a narrow info-level
event filter. Its synchronous in-flight sender owns actual wire IDs, privacy and
sticky input limits. A bounded raw-inspecting log observer waits for one native
enqueue hash while the registered fixture remains active and unreleased. The
passive native-history reader is reused with unchanged guards.

Model-free controls include a real CLI run for both ordinary and active-tool
profiles, each with three native sessions, six logical provider calls and one
shared ceiling. The active profile exposes only the selected MCP tool; no paid
model or production profile is used. These controls are not live steering proof.

The [steering contracts](../reference/core-steering-evals.md)
([#1892](https://github.com/bogdanovich/mintclaw/pull/1892)) add pending boundary
and order definitions with native frozen evidence and post-stop history/outbox
rechecks. Real CLI/local-model controls pass for boundary and both recorded order
modes, including frozen regrade after deleting live native files. Same-sender
guidance coalesces in both modes by the native authority API; anonymous queue
presentation and exact finalization-edge races are separate deterministic tests,
not falsely claimed live coverage. The later fixed campaign is recorded below.

The isolation packet adds two source-derived, three-input scopes under one
six-input contract and a native two-turn concurrency pin. A model-free run
reaches both corrected finals/follow-ups (six calls). Native logs accept normal
nullable fields without weakening duplicate-key/privacy guards. Large storage
JSON containers use bounded token scanning rather than the model-input string
ceiling; nested escaped strings keep their existing bounds. Both scopes must
retain the same complete post-stop outbox census. Missing peers, foreign markers,
swapped traces, non-overlapping calls and inconsistent census copies are
non-passing controls. Full Linux/macOS CI, exact-head review and owner root rocket
preceded the [#1893](https://github.com/bogdanovich/mintclaw/pull/1893) merge.

The [fixed C2 campaign](../operations/manual-agent-evals-core-c2-20261025.md)
retains 12 pass, 1 fail and 3 inconclusive trials on immutable `49f7fc7e6`.
Boundary and both explicit order modes passed their fixed 3/3 batches and join
defaults (six total). One order qualification failed after Gemini HTTP 429/unknown
usage; it is retained, not relabeled. Isolation qualification passed, but its
batch's shared budget became unknown after another HTTP 429, refusing all three
judges; isolation stays pending. No retry or candidate change followed failure.
The native scheduler remains the sole steering authority. The overall goal is
active and incomplete. PDF/browser remain deferred.

## C3b Native Stop

The [native stop/reuse contract](../reference/core-stop-eval.md) uses four actual
inputs to make queue clearing non-vacuous. The SDK cancellation prerequisite was
reviewed/merged in [#1897](https://github.com/bogdanovich/mintclaw/pull/1897), with
green applicable CI, substantive exact-head review and owner PR-root approval.

Only the sent native `/stop` request may select its control ACK. Native held-read
cancellation, original owner/turn abort, committed error-result pairing, absent
queued guidance, a distinct successful reuse turn and complete delivery census
are required before judging. Footer rendering is independent of native content.
The shared post-stop/frozen-grading owner remains the only evidence coordinator;
native AgentLoop is the sole queue/cancellation authority.

Model-free CLI controls include real queued input/abort/reuse, failure/unknown
gates, and frozen regrade after deleting live observations. They are not live
qualification. Stop stays pending/non-default until its separately bounded fixed
campaign; restart, compaction and skill selection remain queued. The
[C3a report](../operations/manual-agent-evals-core-c3a-20261025.md)
retains recovery's immutable fixed results; no further paid recovery run is planned.

## C3a Read Recovery

The [read-only recovery contract](../reference/core-read-recovery-eval.md) was
reviewed/merged in [#1895](https://github.com/bogdanovich/mintclaw/pull/1895): all
14 CI checks green, exact-head substantive review, owner PR-root rocket and no
actionable feedback. Native CLI/frozen grading, admission/privacy/budget controls
and race tests cover the real failed read, corrected read and same-session recall.
The review's premature tool-call-content marker gap has two negative regressions.

The [fixed C3a campaign](../operations/manual-agent-evals-core-c3a-20261025.md)
retains a passing qualification and a batch of one pass/two judge errors on
immutable `2853ede5a`. All four target conversations and required hard checks
passed; two batch judge calls returned `provider_unavailable` without usage.
Their deeper provider error class was not retained; do not infer quota/HTTP 429.
Recovery stays pending/non-default, with no retry or candidate change. Three C3
contracts remain queued; the overall goal is active and incomplete.

## C3c Native Denial

The [approval denial contract](../reference/core-approval-denied-eval.md) reuses
the existing native approver, registry and continuation coordinator. The fixed
fixture prerequisite was reviewed/merged in
[#1899](https://github.com/bogdanovich/mintclaw/pull/1899), with green CI,
SHA-bound review and owner PR-root approval. The full case remains pending.

Its observer passively verifies exact native argument HMAC/prompt identity, sends
a bound denial, waits for actual terminal resolution and refreshes all native
proofs after owned shutdown. A real writable target's independent before/after
census complements denied/unconsumed authority, paired history and native
no-execution witnesses. Suspension `exec_end` is not confused with execution.
The existing private signer-key classifier admits only the exact HMAC-verified
key; neither scanner strictness nor arbitrary binary-file admission is relaxed.

Native model-free controls and frozen regrade pass; this is not live
qualification. Four later C3 contracts remain, and the overall goal stays active.

## C1q Provider Primitive

The opt-in [owned provider call budget](../reference/provider-call-budget.md)
supplies shared logical-call, serialized-input, requested-output, concurrency
and reported-usage limits. Admission/unknown-usage failures stop further calls;
snapshots retain counters rather than payloads. Provider-internal retries and
billed work are explicitly outside this API's coverage.

Wrapping also requires the explicit output-limit capability supplied by
[#1884](https://github.com/bogdanovich/mintclaw/pull/1884). OAuth Codex deliberately
omits that request field and is therefore refused in bounded mode, including
through schema wrappers; ordinary unwrapped behavior is unchanged.

The primitive alone does not wire owned gateway provider creation or qualify a
case. Existing native-model evidence keeps its conservative `AllCalls: unknown`
meaning. The generation wiring was reviewed on its final immutable head and
merged in [#1885](https://github.com/bogdanovich/mintclaw/pull/1885), with green CI,
owner PR-root approval and a regression that fails on the old dynamic-fallback
factory path.

The agent-generation integration wraps existing registry primary/light/candidate
providers and future model-selection providers before the context manager and
shared tools capture them. Config reload is explicitly refused in this opt-in
mode; existing provider cleanup owners stay unchanged. Native turn/final-render,
captured summary-boundary and local HTTP factory controls cover these paths.
This does not cover independent provider-owning tools, grant authority, wire
owned gateway launch, or establish a C3f compaction/live-qualification witness.

The [owned child protocol](../reference/owned-eval-call-budget.md) was reviewed
and merged in [#1887](https://github.com/bogdanovich/mintclaw/pull/1887), including
reported-token/counter consistency controls, exact-head CI and owner approval.
Its real CLI/local-model integration is deterministic evidence, not a live model
qualification. The dependent runner adds one run-wide ceiling and retains the
terminal proof; offline grading checks mandatory manifest requirements before
judge work and refuses omission of the requested raw privacy observation.
Both General cases passed the fixed C1 qualification. Their earlier C1q
budget requirement changed only the then-unqualified candidate digests; no original
default or PDF/browser case is rewritten.

Qualified C1q case identities (the added `bounded_calls` requirement
is part of the definition, not a relabeling of an earlier run):

| Case | SHA-256 |
| --- | --- |
| `general.context.correction` | `2fd7760a9d5f54a530a48b3197f0440a02759c3ca42fc837d99af1823c82240b` |
| `general.interaction.answer` | `a8eaee4e5515b36c0f320a30a96c7bfc2ea853d11f5670a54ec79ec50c1b20f4` |

## C1 Qualification

The [2026-10-25 record](../operations/manual-agent-evals-core-c1-20261025.md)
preserves all four planned runs on merged `c69659c5`: 1/1 qualification and 3/3
fixed batch per case. All eight trials passed hard checks and required judges;
all owned children exited cleanly, privacy scans observed no known leak, and
native budgets retained 24 completed calls total with no unknown usage.
Answer-batch routed-model telemetry remains explicitly partial (7/9 records);
required answer/history witnesses and the independent nine-call budget passed.
No production deploy, paid CI, domain execution or rerun-until-green occurred.
Only these two cases are promoted. The overall goal is not complete; next is C2.
