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
| C1b transport | C0 | [Prompt-bound ordinary answers](../channels/mintclaw.md) through existing native projection | Implemented; channel/bus/interaction tests pass; PR review pending |
| C1b | C1a | Ordinary answer/resume native observer and stale/wrong-prompt controls | Queued |
| C1q | C1b | Explicit target-call/token qualification bounds and retained metadata checks | Queued; user-turn limits alone are not call coverage |
| C2 | C1q | Active-turn barrier, bounded in-flight driver, steering/mode/order and two-session isolation | Queued |
| C3a | C2 | Narrow read-only fixtures and tool failure/recovery | Queued |
| C3b | C2 | Native active stop/reuse, distinct from local driver stop | Queued |
| C3c | C3a | Exact-prompt denial and native terminal/no-effect proof | Queued |
| C3d | C2 | Final-once delivery with streaming/non-streaming controls | Queued |
| C3e | C1q | Explicit owned restart; no bypass of fresh-home admission | Queued |
| C3f | C1q | Controlled small context and actual compaction witness | Queued |
| C3g | C1q | Pinned native skill selection and no-skill control | Queued |
| Qualification | All prerequisites | Fixed attempts/batches, result matrix, explicit promotion | Not started |

Split genuine prerequisites when needed, but do not silently redefine contracts
or add capabilities. Apply the autonomous PR skill's architecture checkpoints.

## Scenario Matrix

| Scenario | Definition/Harness | Required Native Evidence | Live/Default |
| --- | --- | --- | --- |
| `general.context.correction` | C1a scripted candidate | Same session, corrected final, old marker absent from final; semantic judge | Not run; pending, non-default |
| `general.interaction.answer` | Queued C1b | Delivered prompt, terminal answer, one paired continuation/tool result | Not present |
| `general.steering.boundary` | Queued C2 | Active barrier, enqueue/injection, ordered emitted-batch results | Not present |
| `general.steering.order` | Queued C2 | FIFO/mode injection, late-input ownership, no orphan/duplicate final | Not present |
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

There are now 15 cases: two active/default general, one pending correction, six
pending PDF and six pending browser. Bare `run` keeps its two-case default.
Explicit `--suite general` selects the pending candidate too and requires
`--include-pending`; existing active case IDs do not. C1a claims no live pass or
promotion: qualification bounds and the fixed batch are still pending.

The [fourteen-case baseline](../operations/manual-agent-evals-baseline-20261024.md)
remains red and unchanged. No old scenario, fixture or result is rewritten.
