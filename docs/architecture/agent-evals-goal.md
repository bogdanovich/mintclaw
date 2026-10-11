# Manual Agent Evals First-Release Goal

## Admission

Admitted by the operator on 2026-10-10 after architecture review. Parent:
[roadmap](agent-evals-roadmap.md); ownership/alternatives:
[design](agent-evals-design.md). Baseline: `71ea07610f96a5ab0d1b78568d311f1b4a8044b3`.
Status: active; no implementation or live qualification is claimed by merging this document.

Objective: provide a small, manual real-agent quality harness with versioned scenarios, same-session interaction,
scripted and LLM-user drivers, independent hard checks and a separate LLM judge, private auditable reports,
feature/workspace selection, repeat trials, and comparable baselines. Commit a minimum PDF/browser/general corpus,
run it on an isolated target, and report every observed outcome honestly. Never add live evals to required CI.

## Frozen First Contracts

- Case version 1: stable dotted ID, suite, tags, description, natural task, declared capabilities, fixture references,
  scripted steps or user-driver policy/persona, limits, hard checks, and judge rubric. IDs and file references are
  bounded, unambiguous, root-contained, and unique. Reject unknown/duplicate JSON keys and unsupported versions.
- Target preset version 1: non-secret ID/config reference, expected agent/workspace, admitted isolation/resource
  surface and fixture capabilities, separately configured user/judge model aliases. Credentials resolve locally
  through existing MintClaw config/auth; never enter cases, process arguments, or reports.
- CLI: `eval list`, `validate`, `run`, `grade`, `compare`. Select suite, tag, or case; private case roots use the same
  parser. Offline commands never connect to a gateway or model. Preflight completes before billable work.
- Driver actions: ordinary message, protected answer/fact handle, typed navigation, explicit finish intent, or stop.
  The runner binds actual current request/prompt/interaction identity. No model-provided authority IDs or native tool
  calls. Scripted driver remains available when adaptive behavior is undesirable.
- Evidence/result version 1: run/trial/case identities and hashes, real target identity, normalized bounded events,
  independent check witnesses, separate role usage/timing, judge findings referencing evidence IDs, cleanup outcome,
  and `pass`, `fail`, `error`, `inconclusive`, or `skipped`. Required unknown/missing evidence cannot pass.
- Hard failure dominates later judge/cleanup errors; retain all findings. No retry-until-green, no dropping failed
  trials, and no judging the target's "done" claim as an independent observation.

### Default Bounds

Defaults are configurable within validated positive bounds, not provider runtime promises:

| Resource | Default | Admission maximum |
| --- | --- | --- |
| Parallel cases | 1 | 1 for first release |
| User turns | 18 | 64 |
| Turn wait | 3 minutes | 15 minutes |
| Case wall time | 10 minutes | 60 minutes |
| Suite wall time | 60 minutes | 4 hours |
| Driver calls | At most one per user turn | 64 |
| Final judge calls | 1 | 3 explicitly requested graders |
| Input message | 64 KiB | 64 KiB |
| Normalized case events | 256 / 2 MiB aggregate | 1024 / 8 MiB |
| One artifact | 64 MiB | 64 MiB |
| Artifact count | 4 | 16 |
| One driver/judge response | 16 KiB | 64 KiB |
| Driver/judge evidence context | 64 KiB | 256 KiB |

A limit ends the local work with an explicit code, never implies remote cancellation and never authorizes replay.
Use existing runtime status/stop behavior where available, settle owned resources, and preserve uncertainty otherwise.
Unknown target tokens remain unknown. Report all observed input/output usage separately by role; display `10252`
as `10.2k`, retain raw integers, and do not advertise a hard monetary cap from incomplete billing information.

## Isolation And Approval

The first target is a prepared isolated eval gateway/profile using the normal runtime. The runner does not embed
another AgentLoop. Fresh conversation IDs alone are insufficient: validate the admitted workspace, tool policy,
fixture/write roots, browser profile/context, and route identity before running a mutating case.

Production `main`, reviewer, and other active services/configuration are not modified. Shared provider credentials
may be referenced narrowly; production sessions, ledgers, general memory, and signed-in browser state are not cloned.
Later provisioning copies only reviewed instructions/skills/synthetic data under an explicit allowlist.

Approval is denied by default. Output cases require the review and separate finish/confirmation checkpoint, plus an
evidenced approval for exactly the admitted fixture-bound operation. Only then may the runner send native
`allow_once`. If current metadata cannot establish scope, request operator involvement or admit a small prerequisite
projection; do not rely on a prompt substring or broad automatic approval.

Synthetic private canaries stay behind runner handles and native protected ingress. Scan admitted raw runtime and
eval surfaces before redaction/deletion. Logs/reports/judge input never publish the sentinel. Scan bounds/exclusions
are part of evidence. Results live outside git with restrictive permissions or qualified platform equivalents.
Cleanup removes only runner-owned resources; failure or unresolved mutation cannot make a suite green.

## PR Packets And Dependencies

| Packet | Scope | Dependency |
| --- | --- | --- |
| Admission | Design, roadmap, this goal, architecture index | None |
| E1A | Strict corpus/preset contracts, selection, offline CLI and unit tests | Admission |
| E1B1 | Shared live client, scripted turn engine, result/check contracts and bounded conversation evidence | E1A |
| E1B2 | Target preflight, public scripted run command, private reports and general-case qualification | E1B1 |
| E2 | Tools-disabled model roles, action/rubric validation, calibration, offline grading | E1B2 |
| E3 | Pinned PDF fixtures and independent document/delivery/privacy graders, six PDF cases | E1B2, E2 for adaptive cases |
| E4 | Safe browser fixture/state/artifact graders, six browser cases | E1B2, E2 for adaptive cases |
| E5 | Allowlisted presets/private corpus, repeats/comparison, role usage and operator guide | E3, E4 |
| Qualification/exit | Actual merged-revision runs, retained redacted results, limits, roadmap status | E1-E5 |

Split a packet further when it is not a coherent review unit; keep dependencies explicit. Do not silently expand into
runtime replacement, generic orchestration, or domain-state duplication. A shared client is justified by the actual
existing `agent live` and new eval consumers, not a speculative export surface.

E1B1 was started as a stack on E1A while its full CI queued/reran, to avoid blocking implementation progress. It must
be retargeted onto current `main` after E1A merges, and does not expose a live eval command without E1B2 admission.

Code PRs require exact-head green required CI, actual review, no actionable unresolved feedback, authorized PR-level
rocket approval, and confirmed merge. Query the issue-reactions endpoint and monitor on the autonomous PR skill's
schedule. Docs-only PRs follow its exception. Architecture checkpoints apply to review-driven expansion.

## Minimum Corpus

These are natural tasks and outcomes, not exact tool-call counts or prose transcripts. At least one PDF and one
browser case use the adaptive driver; deterministic negative cases remain scripted. Every selected required case
is reported even when prerequisites fail. A product failure can be a valid eval result, not evidence the harness
should retry until it becomes green.

| ID | Required behavior / independent evidence |
| --- | --- |
| `general.context.followup` | Two turns retain supplied synthetic context in one session; no transcript replay |
| `general.interaction.navigation` | Prompt-bound question/navigation; stale/foreign replies rejected |
| `pdf.loan.fill` | Generic small form, review/confirmation, correct independent field readback, one delivery |
| `pdf.loan.correct` | Clarify/Back and natural correction preserve job; last value and unrelated fields verified |
| `pdf.conditional.intent` | Material own/representative intent only; no invented routing/person facts |
| `pdf.empty.required` | Empty serialized source value remains missing; no false completed claim |
| `pdf.cancel.no-write` | Cancel/withheld confirmation produces no document mutation or delivery |
| `pdf.large.section` | Requested bounded section, truthful progress/scope, no whole-form completion claim |
| `browser.fixture.read` | Read known fixture information with actual admitted browser evidence |
| `browser.fixture.submit` | Fill/select/submit; server-side state observes expected values and events |
| `browser.fixture.followup` | Multi-turn change retains context; verified resulting fixture state |
| `browser.fixture.ambiguous` | Ambiguous/stale control re-observed or clarified; no wrong target action |
| `browser.fixture.artifact` | Actual bounded downloaded/screenshot artifact independently validated |
| `browser.fixture.denied` | Denied/canceled action has no forbidden effect/leak and closes owned resources |

PDF oracles use pinned independent field/structure/render checks as needed, not the production parser alone.
Browser oracles observe safe fixture state/events, not target-supplied booleans. Correlate media to the actual
document operation/outbox/source/digest; session presence alone is insufficient.

PDFI4 natural free-text/multi-fact/partial output cases are capability-gated against its actual current contract;
legacy `/answer` cannot qualify them. Full large-form filling, official I-134 provenance/backend qualification,
actual Telegram uploads/replies/callbacks, companion/cloud/browser-provider parity are not implicit first-release
claims. Unsupported promised capability is visibly unqualified, never folded into passing coverage.

## Acceptance Matrix

| Boundary | Required checks |
| --- | --- |
| Parser/selection | Strict keys/versions/types/bounds, unsafe roots, unique IDs, suite/tag/case filters, offline only |
| Transport | Streaming revisions, foreign/duplicate events, request/route identity, heartbeat, prompt admission race |
| Lifecycle | Turn versus case terminal, bounded context/events/artifacts, timeout/disconnect/cancel, no replay |
| Approval | Stale/foreign refusal, expected preceding checkpoints, exact operation binding, deny-default |
| Driver | Bounded valid actions, persona constraints, private handles, no hidden oracle coaching or tools |
| Judge | All criteria/evidence IDs present, strict JSON, no tools/injected rubric, calibrated good/bad examples |
| Evidence | Independent artifact/state, no fake success, required missing/truncated evidence never passes |
| Privacy | Raw-surface sentinel scans, non-echoing errors/argv/reports/model input, private retention |
| Comparison | Every trial retained, compatible cohort/identities, drift/unknown usage reported, no retry-until-green |
| Cleanup | Owned resource identity, settlement, safe removal, leaked/unresolved/failed cleanup visible |
| Platforms | Offline runner/client Linux/macOS; live domain/backend/oracle qualification explicit, skips not passes |

CI tests use scripted/fake providers and local fake protocol/fixture servers only; real-model cases stay manual.
Run `make fmt`, affected non-test lint, targeted suites, and broader package checks appropriate to shared-client
changes. Do not weaken formatters, tests, policy, redaction, or required CI to make a head mergeable.

## Completion And Stop Gates

Complete after the agreed E1-E5 functionality and 14 minimum cases are implemented, reviewed, and merged; real
merged-revision isolated runs have been performed and every verdict is retained; independent evidence and
negative/calibration tests show the harness cannot pass fabricated success or unsafe behavior; and an exit report,
reproducible operator guide, roadmap status, platform/transport/oracle/usage limits, cleanup/rollback, and the actual
results have been delivered. No mandatory in-scope work may remain.

An observed target product failure does not require endless remediation for this goal. First establish that the
grader/fixture is correct, record/triage the target issue, and preserve the failed trial. Fix an actual harness defect
in scope; do not redefine the expected result, discard failures, or convert them to passes. If the harness cannot
run or grade the required workflow, that is incomplete work, not successful qualification.

Excluded: eval daemon, database/dashboard/distributed scheduler, executable DSL/arbitrary hooks/plugins, live CI
or automatic schedules, production deploy/mutations or broad approval, new PDF/browser/secret engines, autonomous
self-modification, coding/Telegram adapters, cloud/public-account changes, Windows qualification, unrelated refactors,
and an open-ended goal to keep improving all agent behavior. Future expansion needs separate acceptance criteria.
