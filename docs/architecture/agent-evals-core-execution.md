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
| C2 | C1q | Active-turn barrier, bounded in-flight driver, steering/mode/order and two-session isolation | In progress; native fixture #1890 merged; owned-profile/driver foundations next |
| C3a | C2 | Narrow read-only fixtures and tool failure/recovery | Queued |
| C3b | C2 | Native active stop/reuse, distinct from local driver stop | Queued |
| C3c | C3a | Exact-prompt denial and native terminal/no-effect proof | Queued |
| C3d | C2 | Final-once delivery with streaming/non-streaming controls | Queued |
| C3e | C1q | Explicit owned restart; no bypass of fresh-home admission | Queued |
| C3f | C1q | Controlled small context and actual compaction witness | Queued |
| C3g | C1q | Pinned native skill selection and no-skill control | Queued |
| Qualification | All prerequisites | Fixed attempts/batches, result matrix, explicit promotion | C1 complete; C2/C3 not started; goal remains active |

Split genuine prerequisites when needed, but do not silently redefine contracts
or add capabilities. Apply the autonomous PR skill's architecture checkpoints.

## Scenario Matrix

| Scenario | Definition/Harness | Required Native Evidence | Live/Default |
| --- | --- | --- | --- |
| `general.context.correction` | C1a scripted | Same session, corrected final, old marker absent from final; semantic judge | Qualification pass + fixed 3/3; active/default |
| `general.interaction.answer` | C1b scripted | Delivered prompt, terminal answer, one paired continuation/tool result | Qualification pass + fixed 3/3; active/default |
| `general.steering.boundary` | C2 native held-read driver/observer | Active barrier, enqueue/injection, paired result, final census | Pending; live qualification not run |
| `general.steering.order` | C2 explicit same-sender mode variants | FIFO/coalescence, no orphan/duplicate final; exact edge races in native Go tests | Pending; live qualification not run |
| `general.session.isolation` | Queued C2 | Two distinct scopes and correctly routed markers/injections | Not present |
| `general.tools.recovery` | Queued C3a | Failed read then successful pinned-fixture read | Not present |
| `general.control.stop` | Queued C3b | Terminal abort/queue settlement and successful next request | Not present |
| `general.approval.denied` | Queued C3c | Exact prompt denied; unchanged independent target/effect census | Not present |
| `general.delivery.final-once` | Queued C3d | Request/outbox/delivery identity, not streaming-revision counts | Not present |
| `general.history.restart` | Queued C3e | Distinct owned processes, preserved session/history | Not present |
| `general.context.compaction` | Queued C3f | Actual compaction receipt and corrected recall | Not present |
| `general.skills.selection` | Queued C3g | Selected pinned skill/revision and contrasting control | Not present |

## C1a Contract

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
campaign now qualifies these two contracts; ten C2/C3 contracts remain queued.

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
not falsely claimed live coverage. There is no live C2 qualification/promotion yet.

Next: distinct-session isolation, then fixed live qualification of C2. The native
scheduler remains the sole steering authority; C2 and the overall goal are incomplete.

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
