# Core Regression Evals Roadmap

Date: 2026-10-24. This is a bounded follow-up to the completed
[first-release program](agent-evals-goal.md), not a reopening of E0-E5.

Implementation of C1-C3 is now authorized in a new goal. The
[execution ledger](agent-evals-core-execution.md) records dependencies, current
status, qualification and stop criteria without relabeling controls as live passes.

## Decision And Current Baseline

Focus occasional manual/paid evals on core agent behavior. Defer PDF and browser
qualification until the operator chooses to resume it. Deferral is not success:
the [fourteen-case baseline](../operations/manual-agent-evals-baseline-20261024.md)
remains **2 pass, 7 fail, 2 error, 3 inconclusive** on `1013693a9`.

The operational [catalog](../../evals/catalog.json) marks `general` active/default
and `pdf`/`browser` pending/non-default. The original fourteen case definitions, fixture
hashes and case digests remain unchanged. Offline list/validate include pending
cases; execution refuses any selected pending case without `--include-pending`,
including explicit `--suite all`. The flag alone does not widen the default set.
Historical grade/compare remain available without authorizing new target work.

The two original defaults below remain unchanged. The
[C1 fixed qualification](../operations/manual-agent-evals-core-c1-20261025.md)
adds correction and answer/resume. The
[C2 fixed campaign](../operations/manual-agent-evals-core-c2-20261025.md) adds
boundary and order, making six General defaults with an explicit owned run-wide
budget and active-tool profile. Isolation remains pending after quota/unknown-usage
blocked its batch. These records do not supersede the red full-suite record.

| Case | Actual contract | Important limit |
| --- | --- | --- |
| `general.context.followup` | Recall a synthetic marker in a second request; native session stays the same; required semantic judge | Not active-turn steering, restart or compaction |
| `general.interaction.navigation` | Ask a native ordinary question and cancel its delivered prompt; retained waiting/terminal records and required judge | Not successful answer/resume, active-turn stop or PDF controls |
| `general.context.correction` | Replace A with B, recall B, reject stale A in the last final; fixed 3/3 | Sequential conversation, not in-flight steering |
| `general.interaction.answer` | Answer exact delivered ordinary prompt; native terminal answer/history pairing and recall; fixed 3/3 | MintClaw wire coverage, not Telegram/PDF UI |

The original two passed in the recorded full run; the later cases have separate
C1/C2 records. The combined six-default suite was not paid-rerun in those campaigns.

## Investigation And Risk Map

Reviewed feature documentation, core ownership boundaries, existing deterministic
regression tests and the current eval driver. This is a risk-based coverage plan,
not evidence of a newly discovered runtime defect or a complete security audit.

| Feature surface and source | Regression users would notice | Existing protection and live gap |
| --- | --- | --- |
| [Turn execution](../../pkg/agent/pipeline_execute.go), [steering](steering.md) | A new message is ignored, duplicated, applied too late or sent to another scope; tool history becomes unpaired | Agent pipeline/steering tests exercise boundaries; evals currently wait for each terminal response |
| [Interaction ownership](../../pkg/agent/interaction_coordinator.go), [continuation](../../pkg/agent/interaction_resume_service.go) | A button answers the wrong question, answer is lost, or continuation runs twice | Extensive human-interaction tests; current live case proves cancellation only |
| [Session identity](../../pkg/agent/runtime_session_claim.go), [session lifecycle](../../pkg/agent/session_lifecycle.go) | Different chats/workspaces share context, or restart loses a conversation | Identity/steering/recovery tests; each current eval trial owns only one session |
| [Stop](../../pkg/agent/agent_stop.go) and [delivery](durable-outbound-delivery.md) | Work continues after stop, a later request cannot start, final is missing or duplicated | Stop/terminal/delivery tests; client close is explicitly not remote cancellation |
| [Context/compaction](local-coding-agent-p5-compaction-lifecycle.md) | Long conversations forget corrections, compact incorrectly or cannot continue | Long-session deterministic tests; no current eval proves compaction actually happened |
| [Skills](skill-bundling.md), [skill selection tests](../../pkg/agent/skill_selection_test.go) | Workspace instructions or an explicitly selected skill stop affecting behavior | Unit/integration coverage exists; copying a skill file is not proof it was loaded |
| Tools, approvals, providers, background work, hooks and remote nodes | False success after a tool error, unauthorized effects, lost completion or changed routing | Prefer existing deterministic integration/fault tests first; broad live permissions would make a cheap smoke suite unsafe |

The present [conversation engine](../../internal/eval/conversation.go) sends a
request and waits for final/question/approval before the next action. Its `stop`
action ends the local driver as inconclusive; it does not send `/stop` to the
runtime. The [wire protocol](../../pkg/channels/mintclaw/protocol.go) has request,
session, delivery and trace identities plus tool-call progress, but that progress
alone does not prove a tool is still active. A follow-up after final does not
exercise steering. Ordinary question cancellation has a native observer; answer
resolution needs its own retained terminal evidence, not an assumed reuse of
cancellation evidence.

Ordinary steering is model-boundary input: finish the emitted tool batch, retain
one ordered result per emitted call, then include new input. It is not hard abort
or graceful interrupt. Tests must pin the selected native execution profile and
its current contract instead of treating all control modes as interchangeable.

## Proposed Scenarios

The sections below specify the twelve target contracts. Correction and
answer/resume are qualified C1 defaults. C2 implements steering and isolation;
C3a recovery, C3b native stop/reuse and C3c exact approval denial have implementation
packets with separate pending qualification status. Four later C3 definitions remain queued in the ledger.
P0 is the first useful expansion; P1 follows after its required harness support. Use synthetic random
markers and local fixtures, never production conversations or accounts. Fixed
user actions are preferable to an LLM user for these mechanical regressions.

### P0: Conversation Correction

`general.context.correction`: remember marker A, replace it with B in the same
session, then ask for the current marker. A regression recalls stale A or loses
the session. Require same-session identity and B in the last final; use a required
judge to reject A as the current value. This can use today's JSON scripted driver.
Do not apply the existing `response_absent(A)` across the whole transcript: an
earlier legitimate acknowledgement may contain A. A final-only negative check
would be a separately tested small enhancement, not a hidden schema assumption.
Budget: 3 user turns, no LLM-user calls, at most 1 judge call, 5 minutes.

### P0: Answer And Resume

`general.interaction.answer`: request one native email/paper question, choose
paper through the delivered prompt, then ask which choice was saved. A regression
shows mismatched options, resumes the wrong suspended call or answers twice.
Require matching prompt/interaction/route, terminal answered record, exactly one
paired tool result/continuation and the correct final. The current question
observer supports waiting/cancel evidence; extend it narrowly for answer/resume
and prove wrong/stale prompt rejection with deterministic tests first. No generic
protected-answer authority is needed. Budget: 3 inputs, 1 judge call, 5 minutes.

### P0: Steering At A Real Boundary

`general.steering.boundary`: while a synthetic read-only tool is demonstrably
active, send a correction B, then release that tool. The final must incorporate B.
Require native active-turn/owner evidence, correlated enqueue/injection, complete
ordered results for the already emitted batch, and one legitimate final. This
catches both dropped input and accidental cancellation of emitted tools.
Prerequisite: bounded in-flight send support plus a deterministic local barrier
and read-only witness; preserve existing scheduler/AgentLoop authority. Reuse a
native observable boundary if sufficient; otherwise introduce the smallest test
fixture, not a second steering queue. Missing witness is inconclusive, never pass
based on a sleep or the model saying it accepted a correction.
Budget: 2 inputs, at most 1 judge call, 5 minutes including barrier timeout.

### P0: Steering Ordering And Late Arrival

`general.steering.order`: enqueue B then C during the same admitted turn and
exercise the finalization edge. Require correlated FIFO injection in the selected
`one-at-a-time`/`all` policy, C as the final current value and no orphaned input or
duplicate delivery. Modes are separate explicit variants, not mixed expectations.
Reuse the preceding barrier/observer; final text alone cannot distinguish proper
queueing from a later independent turn. Budget: 3 inputs per variant, 1 judge call,
5 minutes. Keep exact race interleavings in deterministic Go tests as well.

### P0: Scope Isolation

`general.session.isolation`: session A holds marker A while session B uses marker
B, including one concurrent steering message; query both. Require different
resolved session/owner identities, markers on the correct routes and no cross-
scope enqueue/injection. A regression leaks context or hijacks another chat.
Prerequisite: bounded two-session coordinator and per-scope evidence, not today's
`same_session` check. It proves the configured session scopes, not Telegram topic
or cross-workspace coverage unless those are explicitly provisioned variants.
Budget: 6 inputs total, 1 judge call, 5 minutes; maximum 2 concurrent sessions.

### P1: Tool Failure And Recovery

`general.tools.recovery`: request a missing synthetic local file, then provide
the correct read-only fixture. The agent must not invent its content, should use
the actual tool failure, and must return the real marker after correction.
Require a native failed call followed by successful read, paired results and
independent fixture digest/marker. Prerequisite: narrow read-only eval preset and
safe fixture preparation; current conversation admission is intentionally too
restrictive for arbitrary tools. Do not enable shell/exec for convenience.
Budget: 3 inputs, 1 judge call, 5 minutes.

### P1: Active Stop And Reuse

`general.control.stop`: enter the active-turn barrier, enqueue real guidance,
send native `/stop`, prove that owner's terminal abort/queue settlement, then submit a new request in the
same session. Require no post-stop effect and a new successful final. A stop reply
or closed WebSocket alone is insufficient. Reuse the steering observer but retain
stop's distinct semantics; current `Action{kind: stop}` is not this scenario.
Budget: 4 inputs, 1 judge call, 5 minutes; only owned synthetic work. The queued
input makes clearing non-vacuous rather than testing an empty queue. See the
[native stop contract](../reference/core-stop-eval.md).

### P1: Approval Denial

`general.approval.denied`: request one narrow synthetic write that genuinely
reaches native approval; deny its exact delivered prompt. Require terminal denied
record and unchanged independently hashed target with no admitted operation.
Distinguish actual user denial from a default policy refusing a task before any
prompt. Prerequisite: reviewed single-target fixture, native approval observer
and effect census; never widen blanket authority or borrow browser/PDF bindings.
Budget: 2 inputs, 1 judge call, 5 minutes.

### P1: Final Delivery Once

`general.delivery.final-once`: stream one response, then make a same-session
follow-up. Require native outbox/delivery/request correlation, one final per
logical response, and correct follow-up. Create/update revisions of one message
are not duplicates. Missing terminal delivery must not be inferred from prose.
Prerequisite: retained final-delivery observer and bounded settlement observation
that also works when streaming is disabled. This is MintClaw-channel coverage,
not proof of Telegram/Slack delivery. Budget: 2 inputs, 1 judge call, 5 minutes.

### P1: Restart Continuity

`general.history.restart`: save a marker, stop the owned gateway cleanly, restart
the same isolated profile and ask for it. Require distinct process identities,
persisted same-session identity and the correct final without duplicate replay.
Prerequisite: explicit bounded owned restart lifecycle. Today's owned mode refuses
used homes and must not be bypassed with PID deletion or by adopting production.
Budget: 2 inputs, 1 judge call, 8 minutes including startup/cleanup.

### P1: Compaction Continuity

`general.context.compaction`: introduce a marker and a later correction, trigger
the selected native context policy at a controlled small threshold, then ask for
the current value. Require an actual compaction witness plus preserved identity
and grounded answer. Without a witness this only repeats ordinary context recall.
Prerequisite: admitted controlled context configuration, metadata-only compaction
observer and a bounded synthetic history; no giant paid prompt as a proxy.
Budget: at most 6 inputs, 1 judge call, 8 minutes, explicit target-token ceiling.

### P1: Skill Selection

`general.skills.selection`: explicitly select a pinned synthetic workspace skill
whose small transformation rule differs from the no-skill control; require the
expected transformed output. Require native selected skill/revision evidence,
not only a magic phrase in a reply or the existence of `SKILL.md`. Prerequisite:
admit the specific native profile/tool policy and a scoped read-only observer.
Existing `workspace_files` can pin/copy the file but cannot prove activation.
Budget: 2 inputs plus control, 1 judge call, 5 minutes.

## Boundaries And Run Policy

- Keep parser/driver/oracle regression tests model-free in CI. These manual live
  evals complement them and do not replace deterministic concurrency tests.
- Prefer scripted users. The judge assesses grounded/coherent behavior only;
  it cannot override hard failures, missing native evidence or unsafe admission.
- Future presets use isolated synthetic workspaces and least privilege. No
  credentials, real URLs/accounts, private transcripts or artifacts in Git.
- The scenario budgets above are proposed ceilings, not latency predictions.
  The two qualified core cases require the explicit owned, run-wide
  [target call budget](../reference/owned-eval-call-budget.md), shared across
  cases/repetitions rather than attributed to individual trials. New C2/C3
  packs must retain native call/token bounds before promotion; user-turn limits
  alone do not bound a tool loop. Provider-internal/billed work remains unknown.
- After each new scenario: stub controls must distinguish pass/fail/unknown,
  one admitted live qualification, then three independent trials on one immutable
  candidate with 3/3 pass before promotion to the default. Preserve every attempt;
  no rerun-until-green or prompt relaxation to hide a failure. This small sample
  is a smoke baseline, not a statistical reliability estimate.
- C0 lifecycle policy is suite-scoped, not a per-case override system. Qualify
  new general definitions on their candidate branch/private corpus before adding
  them to the active/default suite on main; merely merging a new general JSON
  file would otherwise include it in defaults.
  C1a adds a compatible per-case narrowing list so new candidates can remain
  pending on main without widening their parent suite's gate.
- Record immutable binary/corpus/case identities and scope with each baseline.
  A two-case default pass never supersedes the fourteen-case red record.
- Defer provider failover/retry to deterministic injected failures first: never
  exhaust real credits or disable a production provider to create a test. Cron,
  MCP, remote nodes, async delegation and channel-specific media belong in later
  capability-specific packs when safe fixtures and independent witnesses exist.

## Bounded Delivery Plan

| Packet | Status | Acceptance And Stop |
| --- | --- | --- |
| C0: Catalog + proposal | Complete, #1868 | Default = 2 general; 12 domain cases pending; deliberate opt-in; unchanged identities/historical eligibility |
| C1: Correction + answer/resume | Complete | Two new cases, answer evidence, stale-answer controls, native budget/frozen admission and fixed live 3/3 batches |
| C2: In-flight evidence + steering | Authorized, queued | One bounded native-boundary fixture/observer, steering/order and two-session isolation; no copied scheduler or timing-only oracle |
| C3: Focused P1 packs | All seven authorized, queued | Admit prerequisites; separate lifecycle/tools/delivery/skill scope and stop criteria |

The initial C0 goal ended after reviewed merge and delivery of the proposal.
The new C1-C3 goal ends at twelve implemented/merged contracts, deterministic
validation and the bounded honest qualification matrix in the ledger. It does
not run/fix pending PDF/browser cases, deploy production or promise a growing
all-green suite indefinitely. Steering needs a real in-flight oracle before
it can be claimed as coverage.
