# Reliability And Code Health Roadmap — October 2026

Status: admitted repository work complete. The owner authorized this roadmap on
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

## Work And Disposition

| ID | Priority | Packet | Admission/status |
| --- | --- | --- | --- |
| R1 | High | Isolate damaged outbox records during recovery | Merged in #1685 |
| R2 | High | Propagate MCP tool-discovery failures | Merged in #1681 |
| R3 | High | Prevent stale Web UI session switches | Merged in #1682 |
| R4 | High | Bound outbox payload storage and recovery cost | Merged: writer/source prerequisites, #1703 receipts and #1708 offline controls; permanent metadata growth remains |
| R5 | Medium | Carry typed MCP failure classifications | Merged in #1687 |
| R6 | High | Finish behavioral evaluation qualification | Linked disposition to existing active eval goal; no duplicate harness or completion claim |
| R7 | Medium | Simplify tool execution around existing stages | Intentionally deferred; trigger not observed |
| R8 | Medium | Expand frontend lifecycle regression coverage | Merged in #1691; rendered-browser qualification remains separate |
| R9 | Conditional | Decompose browser and coding integration files | Intentionally deferred; trigger not observed |
| R10 | Conditional | Measure storage contention before redesign | Measurements and focused pruning optimization merged in #1690; remaining budget misses recorded |
| R11 | Low | Improve current architecture navigation | Current-owner map added to code-health.md |
| R12 | High | Triage and remediate frontend dependency advisories | Merged in #1688; three bounded tooling exceptions expire 2026-11-17 |

R1, R2, R3, and R5 are independent first-batch fixes and may be delivered in the
order that keeps each PR smallest and easiest to validate. R4 follows a written
retention decision. R8 builds on R3. R6 retains its existing acceptance gates.
Conditional packets are not instructions for a repository-wide cleanup.

## R1: Damaged Outbox Recovery

Baseline evidence: `pkg/outbox.Store.Recover` calls `list`, which aborts on the first
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

The first implementation isolates JSON syntax failures and per-record permission
denials only under canonical delivery filenames. The original file remains the
blocked identity and repair evidence. Typed recovery reports separate usable
independent work from record issues; errors invalidate the whole result and
transfer no dispatch claims.
Unsupported schemas, identity conflicts, unknown filenames, missing records,
directory failures, and recovery-write failures remain fatal. Gateway startup
reports bounded identity/code diagnostics and accepts only that explicit partial
recovery contract. No record migration or automatic deletion is introduced.
Operational repair restores an authoritative original record and lets normal
recovery classify its state again. Deleting an isolated file would remove its
duplicate-prevention identity and is not a repair. If its original state cannot
be established, retain the blocked record and its uncertain outcome.

## R2: MCP Discovery Failures

Baseline evidence: `pkg/mcp.listServerTools` logs iterator errors and returns a successful
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

Baseline evidence: `switchChatSession` awaits history without a selection generation.
Delayed promises reproduce selecting A then B, completing B first, and letting
A's older response replace the active B conversation.

Give history selection an explicit current-request identity. Invalidate stale
work across a newer selection, new conversation, and teardown. Stale failures
must not publish misleading errors or alter the current conversation.

Completion gates: reverse response order, selection followed by new conversation,
teardown, and stale rejection preserve the latest intended session. Keep socket
generation checks and history hydration semantics consistent with that owner.

## R4: Outbox Retention

Baseline evidence: completed outbox payloads remain on disk, startup reads the entire
directory, and the assessed repository has no outbox pruning path.

Write a retention decision before deleting or migrating records. Define the
replay horizon, settled-domain prerequisites, deduplication/tombstone lifetime,
payload privacy, byte/count budgets, and crash-safe cleanup behavior. Terminal
transport status alone is insufficient when transcript or document settlement
is still outstanding. Preserve uncertain outcomes and all active ownership.

Completion gates: bounded payload growth and measured recovery cost; cleanup
cannot recreate a previously delivered identity, discard pending obligations,
or turn an ambiguous outcome into a retry; interrupted cleanup recovers safely.

R4's source audit found that inbound spool writes, renames and acknowledgements
needed filesystem sync confirmation before an acknowledgement could support a
retirement proof. That focused durability prerequisite preserves spool versions,
message IDs and replay rules. Merged #1697 confirms directory creation, writes,
claims, failures and acknowledgements; #1698 carries exact source/instance native
acknowledgement proof. Source absence or process exit cannot replace that proof.

Merged #1693 excludes cooperating writers. Merged #1703 qualifies only ordinary
successful Telegram-configured native final replies, sealed after exact source
acknowledgement; delivery and transcript obligations must also settle. Legacy,
unknown, media, interaction, domain, guarded-intake and native-evaluation payloads
remain protected. Later domain work durably revokes eligibility. Delivery keys
stay version 3 while strict schema-4 full records and permanent delivered receipts
share one canonical filename. Old readers fail closed; retired duplicates cannot
dispatch a candidate body. Interrupted replacement preserves full or compact
authority rather than deleting a key.

Merged #1708 adds explicit offline inspect/retire commands, positive operator age,
dry-run by default, bounded exact-ID selection and whole-store capacity reports.
Preflight, apply and after-scan share one exclusive owner; unknown authorities
block mutation. Byte/count budgets request action without discarding protected
work or keys. The [operator guide](../operations/outbox-retention-20261018.md)
records activation prerequisites, partial-batch behavior and paired measurements.
Payload savings do not bound permanent identity growth or linear recovery cost.
No production policy, inventory, deployment, migration or cleanup was performed.

## R5: Typed MCP Failure Classification

Baseline evidence: `pipeline_execute.go` decides fatal transport behavior from a
200-character summary and substring matching. A probe of the current classifier
recognized `typeof` as EOF and lost a genuine transport suffix after truncation.

Classify errors once at the MCP boundary and carry a bounded typed outcome to
the execution owner. Centralize unavoidable SDK text compatibility there rather
than interpreting user-facing summaries downstream. Retain original causes for
internal classification without exposing sensitive diagnostics to the model.

Completion gates: ordinary tool failures, cancellation, timeouts, fatal framing,
closed transports, long wrapped errors, and uncertain execution have distinct
tests; reconnect never silently repeats an uncertain side effect.

The implementation carries malformed-response classification through a typed
MCP error with a fixed public message that preserves its underlying cause internally.
Current-turn control and repeated-failure accounting consume that classification,
not error summaries. SDK compatibility matching stays at the MCP boundary and
excludes structured remote application errors and canceled/timed-out causes.
Session-loss recovery keeps its existing uncertain outcome and never replays the
call automatically.

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

The [October storage measurements](../operations/storage-costs-20261017.md)
record realistic snapshot/history sizes, concurrent callers, actual durability,
profiles, outbox recovery cost, and platform limits. Byte-budget event trimming
missed the pre-recorded latency budgets and allocated about 130 MiB per update.
That evidence admits exact event-byte accounting in the existing pruner. The
same workloads improve substantially, while remaining budget misses stay
explicit. A storage backend replacement is not admitted at the measured scale.

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

The first compatible dependency refresh reduces a fresh pnpm audit from 69 to
three findings. The [frontend audit record](../operations/frontend-dependency-audit-20261017.md)
records the remaining tooling paths, built-output witness, expiring dispositions,
and the repeatable CI check. Merged #1688 reduces the fresh pnpm audit from 69
findings to three and the publication-time GitHub alert set from 56 to three.
The esbuild, braces and postcss-selector-parser tooling exceptions expire at
2026-11-17T00:00:00Z; the bounded CI policy rejects changed or expired exceptions.
This is not a deployed security or live-browser qualification claim.

## Validation And Delivery

The assessment's focused outbox, task, interaction, and provider-error suites
passed. The broader local run completed 30 packages successfully but was
incomplete: a sampled test process stalled in macOS `_dyld_start`, and the eval
suite reached a four-minute cap. Its named test passed alone. Existing frontend
tests were not run during the assessment. These observations do not certify the
full CI graph or establish a production incident.
Current implementation validation and exact-head CI are recorded in the
confirmed ledger below.

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
store at measured scale. The qualified receipt implementation and offline
controls are merged as described above. Operational activation remains separate;
code merge does not authorize or claim production cleanup.

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

Merged [#1706](https://github.com/bogdanovich/mintclaw/pull/1706) adds owned PDF
finalization after shutdown and bounded raw evidence inspection. Its current
roadmap still requires E5 effective-input/isolation, E3 positive PDF qualification,
E4 browser integration and the exit evidence. It is narrower progress in the
linked program, not completion of that goal. The retained
[post-stop PDF pilot](../operations/manual-agent-evals-pdf-post-stop-v1-pilot-20261018.md)
reports a scoped no-write pass with an overall inconclusive trial; it does not
qualify the PDF suite.

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
review and owner merge approval. The final ledger below records the remaining
confirmed merges and final reviewed heads.

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
Combined validation with the current R12 dependency graph passed 51 tests across
seven files, formatting/lint, advisory policy and production build. R8 merged in
#1691 after current-head CI, review and owner approval. No frontend state framework
was introduced. Rendered-browser and OAuth-popup qualification remain separate.

## Confirmed Implementation Ledger

Every code PR below passed current-head CI, substantive review and authorized
PR-level merge approval. The test-fixture prerequisites preserve existing
assertions and timeouts rather than suppressing unrelated CI failures.

| Packet | PR | Final reviewed head | Merge commit |
| --- | --- | --- | --- |
| R2 | [#1681](https://github.com/bogdanovich/mintclaw/pull/1681) | `36a2636fdde256173d43f4dfe9c8696972a783d3` | `b4aa5d1df41c6b31c3ec3fa08f171d11d902dbe0` |
| R3 | [#1682](https://github.com/bogdanovich/mintclaw/pull/1682) | `468b5dc65de6561b018319ee65769f19cf76f9bb` | `5fef84bbebb2047d1ab3eb766ef06dcf5e1a3bc4` |
| R1 | [#1685](https://github.com/bogdanovich/mintclaw/pull/1685) | `6190225a9da5743bdf980f80b351551235ee6e9a` | `7f00354aaab1458bc7b4f51f317942a86438bc1c` |
| R5 | [#1687](https://github.com/bogdanovich/mintclaw/pull/1687) | `c6b17df8693a6ba9c20f189c1b6b96d578085e47` | `0dae73c41448fe03a5b7331f358c6e03af015065` |
| R12 | [#1688](https://github.com/bogdanovich/mintclaw/pull/1688) | `3b9cb0c9475f3cd6c4532de5f9da571bfc40aa9f` | `1ccf307a416bbf003e1afd469cbfc8c8ad5c4b08` |
| R10 | [#1690](https://github.com/bogdanovich/mintclaw/pull/1690) | `a852ab7edcc3b7f3026875b35302213a4255c909` | `7df7bc5b135f324fded07c659985bab8879a193b` |
| R8 | [#1691](https://github.com/bogdanovich/mintclaw/pull/1691) | `00a185686cbfe28136432e24aed2683db05ac70b` | `664dda54d2c3f31560e3205de8d74ab14706b687` |
| R4A | [#1693](https://github.com/bogdanovich/mintclaw/pull/1693) | `4ed78b4fea5149d846b6d913a4517b36435dd4d7` | `8b2c52720ab80fd1c2561f0e599e991ed16176cb` |
| CI prerequisite for R1 | [#1695](https://github.com/bogdanovich/mintclaw/pull/1695) | `22d6ae9821618c19f947411e93f9c51d9e65b202` | `fe2edfa52cf1df66d684e760f64bdfc895c5d848` |
| R4 source durability prerequisite | [#1697](https://github.com/bogdanovich/mintclaw/pull/1697) | `d2a0f6307cbeed4bb471ecec3f6c82d9e1a407fe` | `80b80781324b5d100af37e92dc5b934a5a1a117b` |
| R4 native source acknowledgement receipt prerequisite | [#1698](https://github.com/bogdanovich/mintclaw/pull/1698) | `70b13c1c96c0af272050366df7cf06700e0bc4eb` | `b39b2210da293e8e4606e5d88143988e8e0cd744` |
| R10 CI-discovered queued authority correction | [#1701](https://github.com/bogdanovich/mintclaw/pull/1701) | `cc338af2d9c6218e0215e1bf29f49c381772b2ad` | `f55cf72a5856a382d3b224e1bc1a317e9b79ba11` |
| R4B native source-completion and compact receipt foundation | [#1703](https://github.com/bogdanovich/mintclaw/pull/1703) | `157e492c89dae972d6326a050e84f369fb2c6c14` | `53261ad1512f9c495824e3766fd754386179bcd5` |
| R4B CI prerequisite: atomic document worker PID fixtures | [#1707](https://github.com/bogdanovich/mintclaw/pull/1707) | `918e4d1ff9801679eb24d33896f59d7e8ceb5ed8` | `89d991379e6ba3b37d0322bb505c8c5056fcbfd0` |
| R4C offline operator/capacity and compact-history measurements | [#1708](https://github.com/bogdanovich/mintclaw/pull/1708) | `37318b3d9fa021bd6b8c9fdaef097bc8de71753f` | `f41bc0d196f167af04da3a5ae5e9f2e190b39f8c` |
| CI prerequisite: shared terminal readiness observation | [#1712](https://github.com/bogdanovich/mintclaw/pull/1712) | `58eabf18259cc79726f334390ae4b76bf069185c` | `04269cb041d627dc43728f9d29b86dcdcfef2cba` |
| CI prerequisite: drain transcript fixture before restart | [#1714](https://github.com/bogdanovich/mintclaw/pull/1714) | `8a89b974bd87d87d60179bf6e6b34c4f1615d051` | `c8cc5ffcbbe5b2dc7cd65919a8cc5776aae92e0a` |

Roadmap #1678 and architecture/navigation decisions #1692 were documentation-only
merges. The native queued-authority correction (#1701), including suspended work
return, uses existing steering/turn owners and does not admit a pipeline rewrite.

## Exit Disposition And Remaining Limits

The bounded admitted repository work is implemented and measured. R6 stays with
the existing active evaluation goal; R7 and R9 remain intentionally deferred.
This closes the code-health program without claiming broader product
qualification is finished.

- R1 preserves damaged evidence and blocks affected replay. Unknown schemas,
  identity conflicts, missing authorities and unsafe recovery failures stay fatal;
  automatic data repair is not implemented.
- R4 retires only qualified payloads. Permanent receipts, excluded payload classes
  and recovery scans still grow. At 10,000 synthetic identities, compact warm
  recovery averaged 432–445 ms and allocated about 31 MiB/scan on the Intel macOS
  host. This establishes no cold-start or deployed-Linux guarantee. Activation
  requires an operator-selected policy, stopped older writers, a compatible
  rollback binary, backup and target-filesystem qualification.
- R10's exact event-byte accounting reduced near-budget pruning cost by about
  three times and allocations by about 70%. Concurrent mutation p95 remains
  approximately 338–347 ms against the recorded 100 ms budget. No storage backend
  replacement is admitted; the measured miss stays visible for future scope.
- R12's three evidenced tooling exceptions expire at 2026-11-17T00:00:00Z. Future
  expiry requires fresh triage, not silent renewal.
- Frontend request/store tests do not establish rendered-browser or OAuth-popup
  behavior. Native PDF/browser trial isolation, effective-input, cleanup/privacy
  and domain acceptance remain in the existing linked evaluation program.

No deployment, runtime restart, production data cleanup or duplicate paid live
run is part of this goal. Preserve the existing owners, conservative uncertain
outcomes and independent evidence requirements when admitting subsequent work.
