# Reliability And Code Health Roadmap — October 2026

Status: active. The owner authorized recording and executing this roadmap on
2026-10-17 after reviewing the repository assessment.

Assessment baseline: `origin/main` at `e6d19e2787891417595edc307150b38b29457d6a`.
Each implementation packet must recheck its finding against the latest main.
Previously completed code-health programs remain complete; this is a fresh,
bounded admission based on current evidence.

## Assessment

Keep the modular Go runtime and its existing lifecycle owners. Generation-owned
turn wiring, canonical session history, derived Seahorse state, durable task and
interaction registries, explicit delivery outcomes, and conservative treatment
of uncertain external effects are useful architectural foundations.

The strongest maintenance concerns are specific recovery and discovery errors,
frontend asynchronous state, unbounded delivery history, and the concentration
of several responsibilities in execution and adapter files. Approximately
432,000 lines of non-test Go and 471,000 lines of test Go describe the assessed
repository; these counts are context, not coverage or quality scores.

## Ordered Work

| ID | Priority | Packet | Admission/status |
| --- | --- | --- | --- |
| R1 | High | Isolate damaged outbox records during recovery | Admitted correctness work |
| R2 | High | Propagate MCP tool-discovery failures | Merged in #1681 |
| R3 | High | Prevent stale Web UI session switches | Merged in #1682 |
| R4 | High | Bound outbox payload storage and recovery cost | Admitted; retention design precedes implementation |
| R5 | Medium | Carry typed MCP failure classifications | Admitted correctness work |
| R6 | High | Finish behavioral evaluation qualification | Existing agent-evals program; reuse its owners and evidence |
| R7 | Medium | Simplify tool execution around existing stages | Intentionally deferred; trigger not observed |
| R8 | Medium | Expand frontend lifecycle regression coverage | Implemented; CI/review pending |
| R9 | Conditional | Decompose browser and coding integration files | Intentionally deferred; trigger not observed |
| R10 | Conditional | Measure storage contention before redesign | Measurement admitted; replacement requires measured need |
| R11 | Low | Improve current architecture navigation | Current-owner map added to code-health.md |
| R12 | High | Triage and remediate frontend dependency advisories | Admitted follow-up from publication-time GitHub evidence |

R1, R2, R3, and R5 are independent first-batch fixes and may be delivered in the
order that keeps each PR smallest and easiest to validate. R4 follows a written
retention decision. R8 builds on R3. R6 retains its existing acceptance gates.
Conditional packets are not instructions for a repository-wide cleanup.

## R1: Damaged Outbox Recovery

Evidence: `pkg/outbox.Store.Recover` calls `list`, which aborts on the first
unreadable or invalid record. Gateway startup propagates that error. A synthetic
probe created one completed and one pending delivery, damaged the completed
record, and observed zero recoverable deliveries plus a JSON decode error.

Implement explicit, operator-visible record isolation or repair. Recovery may
continue only when the damaged delivery identity remains blocked and valid work
is independent of it. Preserve the damaged evidence rather than treating the
record as absent or deleting it. Directory-level failures and cases that cannot
be isolated safely must still fail closed.

Completion gates:

- a damaged unrelated record does not prevent safely independent recovery;
- the affected identity cannot be admitted as a fresh send;
- damaged, missing, ambiguous, and unsettled records remain distinguishable;
- recovery exposes bounded, redacted diagnostic issues;
- tests cover mixed valid/damaged records, startup behavior, repair/restart,
  and preservation of no-blind-replay and domain settlement guarantees.

## R2: MCP Discovery Failures

Evidence: `pkg/mcp.listServerTools` logs iterator errors and returns a successful
partial or empty catalogue. A synthetic HTTP MCP server returned an error for
`tools/list`; `Manager.ConnectServer` returned nil and registered zero tools.

Propagate discovery failure or represent it explicitly as degraded state. Never
publish an incomplete catalogue as a healthy authoritative replacement. Preserve
the distinction between a server with no tool capability, a valid empty list,
and failed enumeration. Clean up unpublished connections through the existing
owner.

Completion gates: first-page and later-page failures, cancellation, valid empty
catalogues, successful pagination, and connection cleanup have deterministic
tests; existing reconnect and uncertain-call behavior remains intact.

## R3: Web UI Session Selection

Evidence: `switchChatSession` awaits history without a selection generation.
Delayed promises reproduce selecting A then B, completing B first, and letting
A's older response replace the active B conversation.

Give history selection an explicit current-request identity. Invalidate stale
work across a newer selection, new conversation, and teardown. Stale failures
must not publish misleading errors or alter the current conversation.

Completion gates: reverse response order, selection followed by new conversation,
teardown, and stale rejection preserve the latest intended session. Keep socket
generation checks and history hydration semantics consistent with that owner.

## R4: Outbox Retention

Evidence: completed outbox payloads remain on disk, startup reads the entire
directory, and the assessed repository has no outbox pruning path.

Write a retention decision before deleting or migrating records. Define the
replay horizon, settled-domain prerequisites, deduplication/tombstone lifetime,
payload privacy, byte/count budgets, and crash-safe cleanup behavior. Terminal
transport status alone is insufficient when transcript or document settlement
is still outstanding. Preserve uncertain outcomes and all active ownership.

Completion gates: bounded payload growth and measured recovery cost; cleanup
cannot recreate a previously delivered identity, discard pending obligations,
or turn an ambiguous outcome into a retry; interrupted cleanup recovers safely.

## R5: Typed MCP Failure Classification

Evidence: `pipeline_execute.go` decides fatal transport behavior from a
200-character summary and substring matching. A probe of the current classifier
recognized `typeof` as EOF and lost a genuine transport suffix after truncation.

Classify errors once at the MCP boundary and carry a bounded typed outcome to
the execution owner. Centralize unavoidable SDK text compatibility there rather
than interpreting user-facing summaries downstream. Retain original causes for
internal classification without exposing sensitive diagnostics to the model.

Completion gates: ordinary tool failures, cancellation, timeouts, fatal framing,
closed transports, long wrapped errors, and uncertain execution have distinct
tests; reconnect never silently repeats an uncertain side effect.

## R6: Existing Behavioral Qualification

Continue the existing [Manual Agent Evals Roadmap](agent-evals-roadmap.md) rather
than creating another harness. At the assessment baseline, per-trial isolation,
effective-input evidence, native PDF/browser execution, privacy, cleanup, and
domain qualification remain incomplete.

Link qualification evidence and unresolved dependencies here as that program
progresses. Keep its independent artifact/state witnesses, retained failures,
and platform limits. A passing unit suite or admission document does not
substitute for its domain acceptance gates.

## R7: Tool Execution Simplification

`pipeline_execute.go` combines admission, approval, invocation, protected
continuations, journal updates, delivery settlement, and steering in roughly
3,500 lines. It already has meaningful stages; preserve them.

Admit a focused extraction only when a concrete change crosses these
responsibilities and the extraction makes ownership or the behavioral diff
clearer. Begin with existing same-package methods and owners. Preserve one
settlement path for invoked and hook-supplied results. Do not add a workflow
engine, service locator, or new interface family to satisfy a file-size target.

Completion is either an evidenced extraction with transition tests or a recorded
decision that the trigger did not occur.

## R8: Frontend Lifecycle Coverage

The assessed frontend has four test files concentrated around model forms and
secret placeholders. Build from R3's deterministic history-selection tests to
cover reconnect scheduling, stale socket events, initial hydration, failed
sends, and the current credential interaction/secret-preservation contracts.

Use injected promises, sockets, and clocks where useful. Test observable
transitions rather than implementation-shaped snapshots. Keep the scope bounded
to real lifecycle behavior; do not introduce a general frontend state framework.

## R9: Browser And Coding Adapter Layout

`pkg/tools/browser.go`, `pkg/browser/playwright.go`, and the native coding
`runtime.go` mix several existing tool, translation, lifecycle, persistence, and
presentation responsibilities. File size is supporting context, not admission.

Split along existing boundaries only during related work when a specific review
or ownership benefit is demonstrated. Reuse `browseraction` and current model
session ownership. Independent validation at gateway, node, and driver trust
boundaries must remain independent. Record an intentional defer if no trigger
appears.

## R10: Storage Measurements

Task mutations clone registry state, serialize snapshots, and perform durable
writes while holding their lock. Measure realistic retention sizes, concurrent
sessions, mutation latency, lock contention, and recovery time before choosing a
new store or transaction design.

Record the workload, hardware, warm/cold distinction, distributions, and budgets.
Preserve real durability tests. A measured budget failure may admit a separate,
focused redesign; attractive throughput numbers alone do not justify replacing
bounded, reliable storage.

## R11: Documentation Navigation

Provide a compact current-system map and active-work index using the existing
architecture entry points. Separate active work from completed program evidence
without discarding provenance. Avoid another parallel catalogue or rewriting
historical completion records.

## R12: Frontend Dependency Advisories

Publication-time follow-up: GitHub reported 56 open Dependabot alerts, including
three critical and 23 high alerts. Authenticated alert inspection located all
56 in `web/frontend/pnpm-lock.yaml`. Advisory severity and dependency scope are
triage inputs, not proof that a deployed browser or launcher exposes the issue.

Identify the affected resolved versions, dependency paths, build-time versus
shipped code, and reachable behavior. Prefer bounded compatible updates or
removing unnecessary dependencies. Record evidence for any advisory requiring a
different disposition. Do not apply an unreviewed force-upgrade across majors.

Completion gates: addressed or explicitly evidenced dispositions for current
alerts, reproducible frozen installation, frontend formatting/lint/tests/build,
and a bounded repeatable advisory check that distinguishes actionable findings
from accepted, documented exceptions. Keep this separate from runtime fixes.

## Validation And Delivery

The assessment's focused outbox, task, interaction, and provider-error suites
passed. The broader local run completed 30 packages successfully but was
incomplete: a sampled test process stalled in macOS `_dyld_start`, and the eval
suite reached a four-minute cap. Its named test passed alone. Existing frontend
tests were not run during the assessment. These observations do not certify the
full CI graph or establish a production incident.

For implementation, use one isolated session worktree, focused PRs from latest
`origin/main`, regression tests for changed behavior, repository formatting and
lint, and required CI/review gates. Do not weaken checks to accommodate the host.
Keep operational rollout or data migration evidence separate from a code merge.

Update this ledger with PRs, final heads, validation, dependencies, and any
conditional admission decisions. Completion requires the admitted correctness
and retention work, bounded frontend coverage, the linked disposition of R6,
measured storage results, and explicit implemented/deferred decisions for the
conditional packets. Never mark unfinished work complete to close the goal.


## Architecture Decisions And Linked Dispositions

R4's [retention decision](outbox-retention-decision-20261017.md) records the actual
canonical replay readers, callback hazard, domain exclusions, identity version,
crash ordering, cross-process exclusion and activation gates. Keep the file
store at measured scale. Retirement implementation remains admitted and
unfinished; this design merge does not authorize or claim production cleanup.

R6 is assigned to the existing [agent-evals first-release goal](agent-evals-goal.md)
and its [Next Mandatory Work](agent-evals-roadmap.md#next-mandatory-work).
That program remains active: per-trial/effective-input proof, six PDF and six
browser cases, native cleanup/privacy and platform-specific qualification are
unfinished. General pilots and judge calibration provide narrower evidence;
retained PDF failures and judge errors remain part of the record. Merged
[#1680](https://github.com/bogdanovich/mintclaw/pull/1680) improves owned post-stop
privacy observation but does not finish whole-runtime privacy or domain
qualification. This is the reliability goal's linked disposition, not a claim
that the separately owned evaluation goal is complete. Do not create another
harness or pay for duplicate live runs as a code-health task.

R7: intentional defer. R5 replaces downstream summary-text classification with a
typed MCP boundary and the existing tool execution journal. It does not require
new orchestration stages, approval owners or settlement paths. No demonstrated
cross-stage ownership benefit admits a broad pipeline extraction in this batch.
Reconsider when a concrete change crosses the existing boundaries or a repeated
lifecycle defect exposes unclear ownership.

R9: intentional defer. No admitted fix requires changing browser/coding adapter
layout. Moving large files during transport, frontend or storage work adds review
surface without an evidenced ownership benefit. Preserve independent authority
checks at gateway, node and driver boundaries. Reconsider during related work
when an extraction makes a specific behavioral change or owner easier to review.

R11: the current ownership map and existing architecture/operations entry points
now lead directly to runtime, persistence, delivery, frontend and evaluation
contracts. Historical exit records remain intact; there is no new parallel work
catalogue.

Merged correctness evidence: R2 [#1681](https://github.com/bogdanovich/mintclaw/pull/1681)
(`b4aa5d1df41c6b31c3ec3fa08f171d11d902dbe0`) and R3
[#1682](https://github.com/bogdanovich/mintclaw/pull/1682)
(`5fef84bbebb2047d1ab3eb766ef06dcf5e1a3bc4`) passed exact-head CI, substantive
review and owner merge approval. R1 #1685, R5 #1687, R8 #1691, R10 #1690 and
R12 #1688 are published separately and remain subject to their own gates.

## Frontend Delivery Evidence

R3 merged in [PR #1682](https://github.com/bogdanovich/mintclaw/pull/1682)
at `5fef84bbebb2047d1ab3eb766ef06dcf5e1a3bc4` after green CI, substantive
review, and owner merge approval. Seven controlled-promise regressions first
failed against the original implementation and now preserve the latest session
selection, including teardown and stale failures.

R8 adds controlled sockets and clocks to the existing Vitest suite: duplicate
connection suppression, reconnect backoff/reset, gateway stop/error and teardown,
stale socket events, failed sends without automatic replay, and initial history
hydration. A new regression reproduced old hydration overwriting a reinitialized
session; request identity now rejects stale success and failure without clearing
a newer request. Credential request tests cover token placement, encoded flow
identifiers, cookie credentials, expired-session redirects without mutation
replay, and omitted versus explicit replacement model secrets. These are request
and store contract tests, not browser/OAuth popup qualification.

Failed sends also restore the preceding typing state, preserving an in-flight
assistant response when a newer send fails synchronously. The added active-typing
regression first failed against the original rollback.

Local validation: 38 tests across six files, ESLint, Prettier, TypeScript and
production build passed with supported Node 22.23.1 and the frozen lockfile.
No new frontend dependency or state framework was introduced. CI and review
remain required before this packet is complete.
