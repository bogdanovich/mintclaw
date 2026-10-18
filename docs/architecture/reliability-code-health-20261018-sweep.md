# Reliability And Code Health — Second Sweep

Status: S1 and S2 are implemented and merged. S3 remains conditional; S4 and S5
retain their existing qualification owners and measured follow-up triggers.

The modular runtime remains a sound foundation. This sweep found two reproducible
correctness defects and a concrete opportunity to reduce duplicated private login
logic. Keep the existing lifecycle owners and independent trust-boundary checks.
Broader extraction of the execution pipeline or replacement of the file stores
still needs a demonstrated behavioral or performance benefit.

Review snapshot: `60beb5641b499ee0b2c3c87c1f61d8392b4ad11c`, October 18, 2026.
The change set since the [completed first roadmap](reliability-code-health-20261017-roadmap.md)
at `ada7cc581c2eccaac07583a2fce593e412120db5` contains 52 merged PRs and 281 changed
files, including 90 Go/JavaScript test files and 46 documentation paths. Review
covered credential/browser boundaries, native computer ownership and transfer,
external waits, final-answer repair, and browser/PDF evaluation evidence, with
targeted tests of their surrounding call paths. This is a bounded assessment of
that snapshot, not a certification of every repository path or deployed service.

Publication update: main advanced to `74fc5f041` during the assessment, including
credential waiting (#1764) and returned eval usage (#1766/#1770). S1/S2's native
host, computer cleanup consumers and private JavaScript implementation files are
byte-identical to the reviewed snapshot. S4 below reflects those new merges;
the test results remain attributed to the frozen review snapshot.

## Prioritized Roadmap

| ID | Priority | Improvement | Evidence and disposition |
| --- | --- | --- | --- |
| S1 | High | Preserve unresolved native computer cleanup through status and finalization | Merged in [#1778](https://github.com/bogdanovich/mintclaw/pull/1778); cleanup failure remains unresolved until confirmed |
| S2 | Medium | Correct fixed-alias login event ordering and share private fill primitives | Merged in [#1781](https://github.com/bogdanovich/mintclaw/pull/1781); shared sequential private fill |
| S3 | Medium, conditional | Share repeated eval report-finalization mechanics | Source-backed maintenance candidate; preserve domain evidence checks and extract during related work |
| S4 | High, existing owner | Finish approval-wait and behavioral qualification | Link existing credential/eval programs; avoid a second implementation queue |
| S5 | Medium, carried forward | Revisit storage budgets and dependency exceptions on their stated triggers | Preserve first-sweep measurements and November 17 expiry; no new backend project |
| S6 | Low | Correct architecture navigation status | Assessment and navigation published in [#1773](https://github.com/bogdanovich/mintclaw/pull/1773); implementation disposition now recorded |

The owner selected S1 and S2 for implementation as separate review units. Both
are merged; no deployment or real-account rollout was performed. S3 remains a
candidate for the next related change, and S4/S5 keep their existing owners.

## Implementation Delivery

The defect analysis and original probes below describe the frozen review
snapshot. The following commits contain the corrections and permanent regression
tests; the original expected-failure probes remain historical evidence.

| Fix | Reviewed head | Merge commit |
| --- | --- | --- |
| S1 / [#1778](https://github.com/bogdanovich/mintclaw/pull/1778) | `be42e21d1ce8a4400124e23592b125e4913a5688` | `5a4f03746b07e64743c516df7d62f0843caaff91` |
| S2 / [#1781](https://github.com/bogdanovich/mintclaw/pull/1781) | `624ba71504bab0cd2402e519da3c792d064adf98` | `468c30c938109de9b5ee0cf9f98da7980efb8819` |

S1 joins the existing native cleanup error into terminal status. It retains the
current control state machine. Six worker/observer release/loss/expiry cases
first failed against the old host and then passed with the fix. A composed
public-tool/gateway regression verifies that release, cached status, finalization,
receipts and shutdown cannot acknowledge unresolved cleanup. Successful status
later reconciles the original cleanup without another release, replacement or
capture. The surrounding computer/native/companion suites and gateway/tool
selections passed; native, gateway and tool race selections also passed. The
broader gateway race selection took 243.471 seconds after an initial four-minute
local cap; no assertion or CI timeout was changed. Root formatting and full lint
passed.

S2 removes the duplicate alias fill sequence and reuses the generic path's
private per-field input/change/recheck/submit operation. Each path retains its
own admission and authentication-evidence rules. Both new alias event regressions
first failed and then passed, alongside the generic control. The direct Node
suite completed 43 tests: 41 passed and two Linux-only startup cases skipped on
macOS. Full Go browser/secret-value suites and synthetic gateway/browser flows
through both provider adapters passed, including the integrated race selection.
Both PRs completed CI and current-head review before merge.

This closes the two selected correctness fixes. It does not complete the live
credential/evaluation programs, authorize a broad eval framework extraction, or
change the retained storage and dependency follow-ups.

## S1: Terminal Computer Status Hides Failed Worker Cleanup

In [host.go:318](https://github.com/bogdanovich/mintclaw/blob/60beb5641b499ee0b2c3c87c1f61d8392b4ad11c/pkg/nodes/computerhost/host.go#L318),
`refresh` discards the error from `closeEntry` and returns terminal session state
successfully. `Control` correctly reports an uncertain release when its initial
cleanup fails, but a later `Status` retries cleanup and hides another failure.

A synthetic worker whose `Close` keeps failing reproduces
`control=released worker_closed=false retired=false` with a nil status error.
The existing test covers one close failure followed by a successful retry;
it does not cover persistent failure. Both probes and commands are retained in
the [reproduction record](../operations/code-health-20261018-reproductions.md).

This matters beyond a status display. The public
[release fallback](https://github.com/bogdanovich/mintclaw/blob/60beb5641b499ee0b2c3c87c1f61d8392b4ad11c/pkg/tools/computer.go#L362)
accepts successful terminal status as reconciliation, and gateway shutdown also
accepts terminal status after a failed release. Cached terminal sessions are
subsequently skipped by turn cleanup/finalization. Native worker or observer
resources can therefore remain unresolved while completion is acknowledged.
Logical observation authority has already ended; the defect concerns cleanup
confirmation and premature disposal of its remaining obligation.

Keep the existing control state machine. Propagate unresolved cleanup through
the native status contract and all completion gates. Status may retry only owned
cleanup; it must not reexecute release, open a replacement, capture pixels, or
restore observation authority. Review cached terminal-state handling so an earlier
unconfirmed cleanup cannot be mistaken for a confirmed resource disposition.

Acceptance gates:

- repeated worker-close and observer-close failures remain visible through status;
- public release, turn finalization, shutdown and reload retain the unresolved obligation;
- a later successful cleanup confirms completion without replaying a control action;
- logical revocation stays immediate, transfer bytes stay invalidated, and native
  application lifetime remains independent of automation cleanup.

## S2: Private Login Paths Have Diverged

The generic website path completes each field's input/change events before
filling the next field. The supported configured fixed-alias path still fills
both username and password before dispatching username input events in
[credential-login.cjs:91](https://github.com/bogdanovich/mintclaw/blob/60beb5641b499ee0b2c3c87c1f61d8392b4ad11c/runtime/browser/playwright-library/credential-login.cjs#L91).

On a synthetic same-origin POST form whose username handler clears the dependent
password, the fixed-alias helper returns `denied` and makes no submission. The
generic helper succeeds with `submitted` and one POST on the same fixture. The
gateway conservatively retires an attempted invalid submission; this reproduction
does not show a secret leak or blind replay. The legacy sequence predates its new
generic dispatch branch, so this is a discovered compatibility defect rather
than an established newly introduced regression.

Fix the field ordering for the supported alias path and share the narrow private
fill/event/recheck primitive used by both helpers. Their distinct configured
success/handoff markers, generic verification, origin/domain rules and supported
form kinds should remain explicit. Avoid a general website workflow framework.
The duplication has an observed cost: the dependent-password fix reached one
helper while the other retained the old sequence.

Acceptance gates:

- the paired reproduction succeeds through both supported paths, with one POST each;
- username input and change handlers finish before password injection;
- replaced/hidden/disabled controls and changed destinations remain subject to
  each path's explicit readiness contract before further value exposure;
- private values stay outside public tool arguments, results, history, logs and
  diagnostic exports; uncertain attempts retain current retirement behavior.

## S3: Eval Finalization Has A Narrow Extraction Opportunity

`internal/eval/pdf_post_stop.go` and `browser_run.go` each collect pending trials,
read and validate retained reports, enforce retained-byte limits, build a local
`finalizedTrial` list, finish native observation before judging, scan final JSON
for private values, and publish canonical trial summaries. The sequencing is
valuable, but future changes to these mechanics currently need parallel edits.

During the next related change, consider same-package helpers for bounded pending
report loading and canonical final trial publication. First write a small matrix
of the shared ordering and failure rules; retain tests for report-write failure,
privacy failure, cancellation and a slow judge. Compare pending-file retirement
explicitly: the browser finalizer removes its pending file after canonical
publication, while PDF finalization retains it. Decide whether that difference
is intentional before sharing a publication helper.

Keep PDF settlement, browser approval, artifact bytes, native stores and privacy
witnesses separate. Their independent checks prevent a fixture, model claim or
metadata receipt from granting unrelated evidence. The shared `MediaWireEvidence`
is already a useful bounded reuse. A generic evaluator framework or extraction
of all 66 production Go files in `internal/eval` is not justified by this sweep.
S3 remains conditional until related work demonstrates a smaller, clearer diff.

## S4: Continue Existing Qualification Programs

The [credential approval-wait design](credential-approval-wait.md) already owns
the two-hour phone request and durable automatic continuation. The external-wait
foundation and browser credential wiring in
[PR #1764](https://github.com/bogdanovich/mintclaw/pull/1764) are now merged.
Its qualification should retain delayed approval, expiry/revocation, stop, restart,
duplicate polling, delegated identity and fresh browser admission through actual
provider adapters. Production rollout and the owner pilot stay separate from
that code merge. Keep the original execution/route, cleanup receipts and
no-replay rules. The physical website-login pilot remains with that program.

The [agent-evals roadmap](agent-evals-roadmap.md#next-mandatory-work) owns remaining
per-trial/effective-input, PDF/browser and privacy/cleanup qualification. New
native read/artifact evidence is meaningful progress; preserve its retained
failures and limited claims. Reuse that program's exact gates and evidence rather
than adding another harness or paying for duplicate trials during code review.

## S5: Preserve Measured Follow-Ups

The first sweep's [storage measurements](../operations/storage-costs-20261017.md)
still record concurrent task-mutation p95 around 338–347 ms against a 100 ms
diagnostic budget. Outbox compaction preserves permanent identities and leaves
linear recovery cost. Reconsider a focused storage change when a representative
workload, growth forecast or operational latency makes that miss actionable;
retain durability, replay and domain-settlement guarantees.

The three [frontend tooling exceptions](../operations/frontend-dependency-audit-20261017.md)
expire at 2026-11-17T00:00:00Z. Perform fresh advisory triage before expiry and
retain the bounded CI policy. These are carried-forward obligations, not a fresh
security audit or an instruction to force major dependency upgrades.

## What To Keep

- The explicit principal, session, worker, policy and generation bindings across
  gateway and companion; transfer checks repeat at actual authority boundaries.
- Conservative unknown outcomes, independent cleanup evidence, exact source
  acknowledgement and durable replay identities.
- Native fixture and artifact witnesses that complement model judgments;
  missing evidence stays ineligible rather than becoming a pass.
- Existing interaction recovery and turn cleanup reused for external waits,
  with ordinary chat unable to approve a trusted external condition.
- Focused tests for lifecycle transitions and negative controls. Test volume is
  substantial in this change set, and the two findings identify specific missing
  cases rather than a reason to replace the test strategy.

## Assessment Validation And Scope Limits

On Intel macOS with Go 1.26.9, the following local runs passed:

```sh
go test -tags=goolm,stdjson ./internal/credentials/vault \
  ./internal/credentials/approval ./pkg/credentialbroker ./pkg/nodes/computerhost \
  -count=1 -timeout=3m
go test -tags=goolm,stdjson ./pkg/browser ./pkg/interactions \
  -run 'Credential|Loopback|Ephemeral|ExternalWait' -count=1 -timeout=3m
go test -tags=goolm,stdjson ./pkg/gateway \
  -run 'Computer|Credential' -count=1 -timeout=3m
go test -tags=goolm,stdjson ./pkg/agent \
  -run 'ExternalWait|ObjectivePresentationRepair|VerifiedPresentation|BrowserArtifactResult' \
  -count=1 -timeout=3m
go test -tags=goolm,stdjson ./internal/eval \
  -run 'Browser|PrivateSQLite|RoleBoundary' -count=1 -timeout=3m
```

The private-sidecar frozen install and Node 26.0.0/Playwright 1.63.0 run of
`credential-login.test.cjs`, `credential-steps.test.cjs` and
`ephemeral-startup.test.cjs` completed 37 tests: 35 passed and two Linux-only
cases skipped on macOS. The two separate negative probes reproduced S1 and S2;
their expected failures are not failures of those existing suites.

These checks cover nine Go packages with the selections above, not the full Go
suite, race matrix, frontend suite or deployed Linux qualification. No production
service, credential, real account, GUI application or paid model trial was used.
The initial assessment publication (#1773) contained no runtime fix. The
implementation delivery above records the subsequently authorized S1/S2 work;
these original assessment results remain attributed to their frozen snapshot.
The completed first goal stays complete.
