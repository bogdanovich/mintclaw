# Manual Agent Evals Design

## Status

Approved by the operator on 2026-10-10; implementation is admitted under the
[first-release goal](agent-evals-goal.md).
Investigation baseline: `71ea07610f96a5ab0d1b78568d311f1b4a8044b3` from `origin/main`.
Architecture was agreed before implementation; the operator requested implementation and an actual minimal-suite run.
No eval command, runtime capability, scheduled job, or live evaluation is introduced by this document.
The proposed implementation sequence is in the [roadmap](agent-evals-roadmap.md).

## Outcome

Run a versioned set of realistic MintClaw tasks occasionally, on demand and outside CI. A case can have an
initial request, multiple follow-ups in the same conversation, protected questions, explicit approval, and
independently checked artifacts or browser state. Select all cases, a feature suite, tags, or individual cases.
Keep reusable scenarios and synthetic fixtures in the repository; keep credentials and run evidence out of git.
Compare repeated runs to a deliberately selected baseline without confusing infrastructure errors with regressions.

This measures the complete configured agent, including its model, skills, tools, routing, and runtime behavior.
It does not replace deterministic unit/integration tests, certify every channel, or prove absence of regressions.

Proposed operator interface, not currently runnable:

```sh
mintclaw eval list
mintclaw eval validate --suite pdf
mintclaw eval run --suite all --preset ./evals/presets/local.json --out ./eval-runs/check-1
mintclaw eval run --suite browser --preset ./evals/presets/local.json --out ./eval-runs/browser-1
mintclaw eval run --case pdf.loan.correct-before-finish --preset ./evals/presets/local.json --out ./eval-runs/pdf-1
mintclaw eval grade ./eval-runs/pdf-1
mintclaw eval compare ./eval-runs/baseline ./eval-runs/check-1
```

The runner resolves the preset to a prepared safe target and performs preflight before sending a task. Results are
private and excluded from git. Codex/operator automation uses the same commands; it does not need a separate skill
to reimplement the scenario protocol. There is no default schedule or background execution.

## What Exists

The following are source-inspection findings, not a new live qualification:

| Existing surface | Reusable behavior | Current limit |
| --- | --- | --- |
| `mintclaw agent live --json --session ...` | Authenticated request to the running gateway, same-session follow-ups, request/trace identity, typed outcomes | One-shot connection; not a full scenario/artifact client |
| `agent live` automatic interaction flags | One matching question answer or typed choice, including Clarify/Back and `allow_once` | Literal matching, one action per invocation; text flag explicitly excludes sensitive input |
| MintClaw WebSocket protocol | Streaming messages, question/approval identities and controls, authenticated media download | Input attachments are inline images, not arbitrary PDF uploads; media delivery lacks request identity |
| `mintclaw agent --stateless` | Direct local agent smoke without conversation history | Separate runtime, not live gateway/node state; unsuitable for a multi-turn browser workflow |
| `mintclaw code exec/resume --json --attach` | Durable coding threads and JSONL presentation events | Coding-host semantics and suspension differ from gateway; not the first target |
| `pkg/testharness/llmscenario` | Scripted provider and deterministic tool assertions | Tests runtime contracts, not real-model task quality |
| `pkg/testharness/codingquality` | Real coding-tool fixture/oracles | Not an interactive real-model suite |
| `cmd/membench` | Memory-specific evaluation, metrics, and LLM judge | QA/retrieval benchmark, not gateway tasks; do not turn it into a general agent runner |
| Browser capability smoke scripts | Safe fixture, result-output checks, trace-backed browser/delegation evidence | Separate suites, technical prompts, specialized orchestration |
| PDF scripts and PDFI3 qualification | Pinned fixtures, source/value/visual/delivery checks, privacy canaries | Specialized checks and a previously temporary interactive client, not a reusable suite runner |

Relevant sources:

- [Live agent command](../../cmd/mintclaw/internal/agent/live.go),
  [operator contract](../operations/live-agent-smoke.md), and
  [bounded execution evidence](../../cmd/mintclaw/internal/agent/live_evidence.go).
- [Wire format](../../pkg/channels/mintclaw/protocol.go),
  [input attachment parsing](../../pkg/channels/mintclaw/payload.go),
  [typed input binding](../../pkg/channels/mintclaw/inbound.go), and
  [media delivery](../../pkg/channels/mintclaw/media.go).
- [Coding headless command](../../cmd/mintclaw/internal/coding/exec_command.go),
  [scripted scenarios](../../pkg/testharness/llmscenario/harness.go), and
  [coding quality harness](../../pkg/testharness/codingquality/harness.go).
- [Browser smoke](../../scripts/browser-capability-smoke.sh),
  [PDFI3 exit evidence](pdf-agentic-intake-pdfi3-exit.md), and
  [synthetic PDF generator](../operations/pdfi3-fixtures.py).

Current main also contains PDFI4 private interpretation work. It must be evaluated against its current admitted
contract, not assumed complete because a packet merged or judged using only the earlier PDFI3 `/answer` workflow.

## Recommended Architecture

Use a small Go CLI runner, proposed as `mintclaw eval`, outside the gateway execution path. It drives the real
gateway through its existing authenticated protocol. It neither embeds a second `AgentLoop` nor invokes tools
directly to complete the task for the agent. The normal approval, document, browser, and delivery authorities remain
in control. There is no eval daemon, database, service, web dashboard, plugin registry, or scheduler.

```text
Versioned case + target preset + run limits
                    |
               Eval runner --------> owned fixtures and scratch state
                    |
         scripted / LLM user driver
                    |
         authenticated session client
                    |
             real MintClaw gateway
                    |
       transcript + traces + artifacts + fixture state
                    |
          deterministic checks / independent oracles
                    |
         separate, tools-disabled LLM judge
                    |
         JSON result + Markdown summary + baseline comparison
```

### Ownership

| Component | Owns | Must not own |
| --- | --- | --- |
| Case definition | User task, synthetic persona/facts, allowed actions, limits, expected outcomes, rubric | Runtime credentials, arbitrary native code, automatic permission grants |
| Runner | Case lifecycle, session/request IDs, budgets, checkpoints, evidence, cleanup, result reduction | Agent tool decisions or privileged policy bypass |
| Target client | Wire exchange, prompt-bound responses, streaming revisions, bounded downloads | Conversation strategy, scoring, document planning |
| User driver | Next ordinary user reply or bounded interaction intent from the case's persona | Tool calls, arbitrary URLs/paths, approvals outside the case's allowlist, success verdict |
| Deterministic graders | Actual artifact/state, identity, ordering, safety and privacy invariants | Natural-language task planning |
| LLM judge | Rubric-based assessment of permitted conversation/evidence | Tool execution, permission changes, overriding failed hard checks |

Start with `cmd/mintclaw/internal/eval` for command/config handling and `internal/eval` for the runner.
Extract only the genuinely shared authenticated client mechanics from `agent live` into `internal/gatewayclient`
when the second consumer is implemented. Preserve the existing live command's output, bounds, no-replay behavior,
and tests. Do not move gateway runtime, document logic, or browser policy into the client.

### Why Not Just Chain CLI Prompts

Sequential `agent live --session` calls are already sufficient for ordinary text follow-ups. They are not sufficient
as the complete abstraction for forms and browser artifacts:

- a connection can receive multiple streaming revisions, interaction prompts, and media events;
- `agent live` exits at a final response or unresolved interaction and filters messages by request ID;
- current media messages do not carry that request ID and can arrive independently of the final text;
- protected answers should not be supplied in process arguments; and
- after an uncertain timeout, disconnect, or lost delivery, replay may duplicate a mutation.

The shared client should expose bounded events and explicit send operations. Scenario orchestration stays in the
runner. It keeps one connection per case when possible, sends follow-ups sequentially, and passes only the new user
input, not a duplicated transcript, into the same gateway session. Heartbeats and a dedicated read loop keep
slow user-model/judge work from blocking the socket. A final assistant message ends a turn, not necessarily a case.

There is currently no atomic client-facing "interaction is ready for response" handshake; `agent live` uses a
bounded admission grace. The implementation must test this boundary and handle a known-not-accepted response
without blindly replaying accepted input or relying on an arbitrary longer sleep.

### Targets And Isolation

The initial target is an explicitly prepared, local eval gateway/profile, not the production `main` profile.
E1 attaches to that prepared target; automatic creation of the target profile is a later milestone.

Every trial gets a fresh session ID, scratch directory, source fixture copy, and browser context/profile where
required. Cases run serially by default. A new session isolates conversation history, not shared files, memory,
skills, credentials, browser profiles, nodes, or background tasks. Preflight must establish the actual isolation
level before a mutating case can run.

A target preset specifies an existing MintClaw config, expected routed agent/workspace, allowed capabilities and
fixture origins, and credential references. Verify the returned agent/workspace identity. Do not invent an unchecked
`--workspace` routing override or assume every request goes to the configured default agent.

Custom workspace evaluation should normally copy an explicit allowlist of instructions, skills, and synthetic
fixture data into an isolated eval profile. Preserve their content fingerprints. Do not copy chat history, secrets,
encrypted ledgers, general memory, or live browser state. A profile copy measures the copied configuration, not live
workspace integrations; report that distinction. Real-workspace runs require explicit operator admission and a
declared write surface; a read-only promise in a prompt is not a security boundary.

For `server@oc`, run the client on the gateway host, as `agent live` already does; later provide a bounded SSH wrapper
or tunnel without exposing a new public API. No automatic deploy, service restart, or production config change.

### Scenario Contract

Store cases under proposed `evals/cases/<suite>/<id>.json`, target templates under `evals/presets`, and pinned synthetic
fixtures/generators under `evals/fixtures`. Use strict versioned JSON and typed Go decoding, including rejection of
unknown fields, duplicates, unsupported versions, missing hard checks, and unsafe paths. Do not add an executable
DSL, unrestricted shell hooks, remote case downloads, or arbitrary plugin loading.

One case defines:

1. Stable ID, description, suite/tags, version, and capability/platform requirements.
2. A natural user task, fixture references/digests, synthetic persona/facts, and allowed fixture side effects.
3. Scripted checkpoints or an LLM-user policy, including how to clarify, correct, and finish.
4. Explicit turn/time/output/model-call limits and narrowly scoped approval policy.
5. Deterministic expected results/invariants, required evidence, and a separate conversation-quality rubric.

Illustrative shape only; this is not an implemented command or frozen wire schema:

```json
{
  "version": 1,
  "id": "pdf.loan.correct-before-finish",
  "suite": "pdf",
  "tags": ["form", "multi-turn", "correction"],
  "requires": ["document.forms", "protected.interactions", "media.download"],
  "fixture": "pdfi3-small-loan",
  "task": "Complete this loan form with my test data. Show a review before creating the PDF.",
  "driver": {
    "mode": "llm_user",
    "persona_ref": "loan-borrower-synthetic",
    "checkpoints": ["clarify", "correct-borrower", "review", "confirm"]
  },
  "limits": {"max_user_turns": 18, "case_timeout": "10m", "max_driver_calls": 18},
  "checks": ["source_unchanged", "expected_fields", "approval_before_write", "single_delivery", "no_private_leak"],
  "rubric": ["coherent_questions", "matching_controls", "no_repeated_accepted_facts"]
}
```

Normal quality cases specify outcomes rather than exact tool-call sequences or assistant wording. Trace/action
assertions are reserved for genuine invariants, such as no write before approval, not a mandated planner strategy.

### Two Driver Modes

**Scripted:** ordinary messages and typed controls at explicit event/checkpoint boundaries. This is inexpensive and
easier to replay, and is required for deterministic safety/regression cases. The target still uses its real model.

**LLM user:** a separate model receives the permitted transcript plus synthetic persona facts and pending prompt
kind. It responds with a strict bounded action: ordinary message, protected answer/fact reference, Clarify, Back,
Skip, finish request, or stop. The runner resolves actual interaction and prompt IDs, validates the action against
the case, and sends it through normal ingress. The model cannot generate authority-bearing IDs, call gateway tools,
modify the rubric, invent persona facts, or authorize an unrelated operation. Stop is only a request to grade, not
proof of success. A malformed response ends as a driver error, not an unbounded repair conversation.

Do not give the simulator hidden golden field mappings, hard-grader results, or tool-internal coaching. Otherwise
it can guide MintClaw through the test rather than represent a normal user. Support explicitly requested corrections
and ambiguity, and retain scripted scenarios alongside adaptive ones so model variation cannot hide regressions.

Protected canary values remain private to the runner and deterministic oracle. The driver can select an admitted
fact handle; the runner expands it only at protected ingress. Canonical native receipts and ledgers still decide
whether it was accepted. PDFI4 ordinary-looking intake and PDFI3 command-style answers are separate capability cases;
passing one must not advertise the other as qualified.

### Approval And Side Effects

Default to no automatic approval. Cases needing an output transaction declare its exact fixture-bound operation
class and require the expected preceding review/confirmation checkpoint. The runner may then send the normal typed
`allow_once`, bound to the current interaction and prompt. The LLM driver cannot broaden that policy.

Prompt substring matching alone is not adequate for broad automatic approval. If existing metadata/evidence cannot
establish that a pending approval belongs to the admitted operation and target, stop as approval-required and request
operator involvement, or admit a narrowly scoped value-free projection in a prerequisite PR. Do not bypass approvals
to make a scenario finish. Browser interaction with a fixture is distinct from shell execution, privilege escalation,
external submission, persistent account changes, or unrestricted browser JavaScript.

## Evaluation And Evidence

### Hard Checks First

Use independent observations wherever practical:

- PDF: immutable input digest, independently decoded field values/types, unchanged preserved fields, review and
  approval ordering, structural/visible verification, output editability when promised, and one logical delivery.
- Browser: a safe deterministic local fixture exposes server-observed submissions/state/events. Compare that state
  to the task, rather than accepting the agent's result booleans as an oracle. Record the admitted profile/session,
  screenshot where useful, protected-fill privacy, and cleanup. Refuse public-site/account mutations by default.
- General: same-session continuation, expected terminal status, required evidence completeness, no duplicate request
  replay, no unexpected deliveries, and allowed side-effect surface.

Use existing native PDF APIs for exercise, but a pinned independent reader/renderer for the eval oracle where needed.
Those optional dependencies must be checked at preflight and identified in the result. Do not claim cross-platform
qualification when an oracle or backend was skipped. Avoid exact binary output hashes for non-deterministic PDFs;
check semantic values, structure, and relevant appearance.

Collect media events even when they lack request IDs, but treat them only as candidates. Bind a downloaded artifact
to the case's actual document operation and durable outbox/delivery identity, source/job, and digest. Merely seeing a
PDF in the same session is not proof. Initially read the existing owner-authorized host-local projections; if a
required witness is unavailable, report it. Add a protocol projection only after a concrete case demonstrates need.
Download only authenticated same-origin relative media paths, with byte limits and no credential-forwarding redirects.

Keep the distinction between a CLI's `success` (a correlated final arrived), the runtime's outcome, and a satisfied
task objective. Record trace IDs, request IDs, source/artifact hashes, grader evidence IDs, and incompleteness.
Missing/truncated evidence must never be interpreted as "nothing bad happened". Count logical deliveries separately
from transport retries: a recovery scenario may permit several attempts but still require one delivered artifact.

### LLM Judge

Use a separately configured direct `providers.LLMProvider` call, not a MintClaw agent that can run tools or share the
target conversation. Reuse provider configuration/authentication and the existing caller-mediated tools boundary:
require the declared capability, pass no tools, use `CallerMediatedToolsOptions`, and reject tool-bearing responses.
Unknown/autonomous CLI transports are not an implicit tool-free judge. Disable cache lineage and use existing
sensitive-request controls when permitted input needs them; these do not guarantee provider-side non-retention.

The judge sees a fixed rubric and permitted transcript/evidence, not its own simulation deliberation or the target's
claimed score. Parse strict bounded JSON with a finding per criterion, status, concise explanation, and references
to real evidence IDs. Reject missing criteria, invented references, trailing text, unknown fields, or tool calls.
Treat conversation, documents, web content, and artifacts as untrusted data; they cannot redefine the rubric.

Judge language quality, coherence, whether questions match controls, unnecessary repetition, and honest explanation
of limits. Native or independent checks own field correctness, permissions, mutation, delivery, and privacy. A high
judge score never overrides a failed hard check. Required judge failure yields an error/inconclusive result, not pass;
optional judge omission is visibly reported as deterministic-only coverage.

Prefer a judge role/model distinct from the target and simulator; record all three, even if an operator deliberately
uses the same model. Pin prompts/rubrics and calibrate on manually labelled good/bad examples. Do not assume a judge
score is reliable just because the API returns valid JSON.

### Results And Comparison

Write a run manifest, normalized event/evidence records, per-case results, and a compact Markdown summary to a private
output directory. No result database is necessary. Persist the target binary/commit identity, scenario and fixture
hashes, effective instructions/skills/config fingerprints, backend/platform, actual model/fallback identities,
simulator/judge models and prompt versions, limits, timing, and available usage. Never hash raw secrets into a public
config fingerprint. Unknown identity or usage is explicitly unknown, not guessed from the checkout or recorded as zero.

Verdicts are separate from individual scores:

- `pass`: all mandatory hard checks and any required judge criteria passed with sufficient evidence.
- `fail`: an observed product/safety or required behavior check failed, including a fake success artifact.
- `error`: invalid scenario, driver/judge failure, credential/provider/fixture/transport failure.
- `inconclusive`: the run cannot establish a required claim, including uncertain mutation completion.
- `skipped`: preflight could not admit an unsupported platform/capability or selection explicitly excluded the case.

A witnessed hard safety failure remains `fail` even if later grading or cleanup also errors. Retain all error details
as safe codes. Cases have immutable per-trial results: never retry-until-green or discard failures. Independent reruns
use new sessions and fixtures, retain every outcome, and report pass count/trials plus error/inconclusive/skip coverage.
The aggregate must not look green when selected required cases are unqualified.

Comparison joins stable case IDs and compatible case/rubric/fixture/driver versions. A changed model/runtime is the
experimental variable, not a reason to silently discard comparison; changed requirements create a new baseline cohort.
Record environment drift. Show case-level changes, failure evidence, latency, and reported input/output tokens for
target, simulator, and judge separately. Format display counts compactly, for example `10252` as `10.2k`; JSON keeps
raw integers. Gateway usage may require trace aggregation across every model call/delegation/private call, not just the
final message. Report coverage gaps. Estimated price is optional and requires a recorded price source/version.

### Limits, Privacy, And Cleanup

- Bound user turns, driver/judge calls and outputs, per-turn/case/suite time, events, artifacts, and context bytes.
  Make limits configurable per suite rather than forcing a short arbitrary global timeout.
- Default to one case at a time and one final judge call. Judge-only regrading reads retained evidence without rerunning
  the target. Expose billable calls during plan/preflight; do not claim a hard dollar cap from incomplete token usage.
- A client deadline does not prove the gateway or remote tool stopped. After timeout/disconnect, do not resend the
  task; inspect authoritative status and mark uncertainty. Stop/cleanup only runner-owned processes/resources, wait
  for settlement, and preserve unresolved-resource identity. Do not kill unrelated gateway/node services.
- Use synthetic data only initially. Protect private canaries in memory/private fixtures; do not put them in command
  arguments, stdout, ordinary transcripts, or judge inputs. Reports contain predicates and handles, not raw answers.
- Scan private canaries against the admitted host-local history/diagnostics/log surfaces and eval outputs. Scan before
  redaction/deletion so sanitization cannot conceal a product leak. A detected leak fails the case without publishing
  the leaked value. A scan's bounds and exclusions are evidence, not a universal privacy guarantee.
- Results/artifacts stay outside git in private directories, with restrictive permissions or platform equivalents.
  Reject retention/publication that cannot honor them. No default upload to an external evaluation service.
- Cleanup removes only run-owned fixtures, scratch, and ephemeral browser resources. Keep bounded failure evidence
  under explicit retention policy; never delete production history or credentials as test cleanup.

## Alternatives

**Existing smoke scripts only:** lowest initial effort and useful adapters/oracles, but no common multi-turn case,
driver, judge, privacy, or baseline contract. They remain lower-level qualification tools, not the complete solution.

**Promptfoo:** its [simulated user](https://www.promptfoo.dev/docs/providers/simulated-user/) already supports adaptive
multi-turn dialogue and stateful targets. **Inspect:** its
[agent evaluation](https://inspect.aisi.org.uk/agents.html) and
[scorers](https://inspect.aisi.org.uk/scorers.html) support agent-oriented execution and model/other scoring.
Either is a credible option, not inherently unsuitable. Both still need a MintClaw adapter for protected interactions,
approvals, artifact correlation, and independent privacy/state evidence. Adopting one also introduces another evaluator
runtime and provider/auth configuration surface. The proposed small Go runner favors current local provider and
runtime contracts; this is an engineering tradeoff, not a claim that mature evaluators lack those features.

Reconsider a mature framework if the runner grows into a general experiment scheduler, dataset platform, dashboard,
distributed executor, or extensive statistics engine. Portable manifests/evidence leave room for later integration.

**Embedding an eval planner in the gateway:** unnecessary production coupling. The gateway is the target under test,
not the authority that decides whether its own behavior passed.

## Approved Decision

Manual Go CLI runner, real gateway as first target, prepared isolated eval profile, strict versioned
cases, both scripted and LLM-user drivers, independent hard checks plus a separate LLM judge, private file reports,
PDF/browser as initial suites, and no live runs in CI. The implementation goal fixes the admitted first-release scope.
