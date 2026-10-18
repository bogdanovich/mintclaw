# PDFI4 Qualification Progress

## Decision

PDFI4 is **not complete**. This is a dated evidence checkpoint, not an exit report. The
[implementation goal](pdf-agentic-intake-pdfi4-goal.md) remains authoritative. As of 2026-10-18 06:49 UTC,
implementation is merged and deployed, five artifact lanes have passed independent checks, and the complete
official I-134 transaction and actual Telegram qualification remain outstanding. Do not infer a complete milestone
from a merged implementation, ready review, or successful headless response.

Use the [operator test guide](../operations/pdfi4-free-text-form-test.md) for the repeatable user workflow.
All qualification values are synthetic; private inputs, oracles, artifacts and rich traces are not published here.

## Implementation And Deployment

The implementation reuses the document job, encrypted ledger, protected ingress, bounded private interpretation,
native validation, review, approval, existing writer/verification and outbox. It does not add a generic secret store,
daemon, alternate planner or form-specific production rules. Major merged packets are:

| Boundary | Merged PRs |
| --- | --- |
| Composite ledger and retained protected utterances | [#1484](https://github.com/bogdanovich/mintclaw/pull/1484), [#1488](https://github.com/bogdanovich/mintclaw/pull/1488) |
| Sensitive provider calls, native proposals and private interpretation | [#1492](https://github.com/bogdanovich/mintclaw/pull/1492), [#1495](https://github.com/bogdanovich/mintclaw/pull/1495), [#1499](https://github.com/bogdanovich/mintclaw/pull/1499) |
| Guarded spool, grouped dialogue and review revision binding | [#1497](https://github.com/bogdanovich/mintclaw/pull/1497), [#1502](https://github.com/bogdanovich/mintclaw/pull/1502), [#1505](https://github.com/bogdanovich/mintclaw/pull/1505) |
| Native draft scope, explicit blank/applicability and output interpretation | [#1510](https://github.com/bogdanovich/mintclaw/pull/1510), [#1516](https://github.com/bogdanovich/mintclaw/pull/1516), [#1521](https://github.com/bogdanovich/mintclaw/pull/1521) |
| Production composition and renderer prerequisite | [#1529](https://github.com/bogdanovich/mintclaw/pull/1529), [#1528](https://github.com/bogdanovich/mintclaw/pull/1528) |
| Retained retry, draft review and qualification recovery | [#1611](https://github.com/bogdanovich/mintclaw/pull/1611), [#1612](https://github.com/bogdanovich/mintclaw/pull/1612), [#1633](https://github.com/bogdanovich/mintclaw/pull/1633), [#1640](https://github.com/bogdanovich/mintclaw/pull/1640) |
| Provider heartbeat and connective-prose accounting | [#1642](https://github.com/bogdanovich/mintclaw/pull/1642), [#1645](https://github.com/bogdanovich/mintclaw/pull/1645) |
| Closed failure reasons and bounded clarification-window priority | [#1649](https://github.com/bogdanovich/mintclaw/pull/1649), [#1653](https://github.com/bogdanovich/mintclaw/pull/1653) |
| Value-independent PDF schema and output-only guidance | [#1662](https://github.com/bogdanovich/mintclaw/pull/1662), [#1663](https://github.com/bogdanovich/mintclaw/pull/1663) |
| Current-proof correction/guidance settlement and verified quoted booleans | [#1667](https://github.com/bogdanovich/mintclaw/pull/1667), [#1674](https://github.com/bogdanovich/mintclaw/pull/1674), [#1677](https://github.com/bogdanovich/mintclaw/pull/1677) |
| Delayed advisory reconciliation against current native accepted events | [#1709](https://github.com/bogdanovich/mintclaw/pull/1709) |
| Cross-window collection validated by current native unresolved state | [#1720](https://github.com/bogdanovich/mintclaw/pull/1720) |

The quoted-boolean packet #1677 merged as `223b9af0dd0a7a4243e56100df0470212703c452` after all 17 checks
were green on reviewed head `b3a25ae0fc7ce09d99349caa458e2fc0d5790814`, substantive clean review
`6244296064`, owner PR-level rocket `576720481`, clean mergeability and no actionable unresolved thread.
Its final local evidence includes the document/tool/agent intake selections, focused races, formatting, scoped
production lint and documentation lint. This is not a claim that the entire agent package suite ran on that head.

The delayed-advice fix #1709 merged as `694d716db119e14044faffbe4cd68fb1567cba25`, after exact head
`36419e63cb61fadea00b84f1c8c6324cba9ef04a` passed 13 CI checks; Frontend was skipped by the existing path
filter. Clean substantive review `6245417076`, owner PR-level rocket `576855612`, clean mergeability and no
actionable thread preceded normal merge. Full local document tests, tools/agent intake selections, complete
three-package intake races, repository formatting, scoped test-inclusive production lint and docs lint passed.

The #1709 deployment executed merged `694d716db`; process, CLI and runtime-binary SHA-256 agreed:
`caa371ecbf119d402101b2fcb6f9a9e17a0564f211e05e0d4bd9798e9abb4cee`.
Before this deployment the checkout/CLI had advanced to `aa6b3767a`, but the actual main still executed `223b9af0d`.
Both distinct old binaries were preserved. Fresh traces identify actual runtime source; a checkout or CLI version
alone is not proof of the running version. The workspace and harness revisions were not changed.

That coherent stopped-main backup is `/home/server/mintclaw-pdfi4-backup.78F76h`, with a
4,346,920,960-byte profile archive, six binary backups, five configurations and systemd units. Archive and binary
checksums passed; live configurations remained byte-identical. Independent coding workspaces were excluded from
the quiescent-main snapshot and left running. The old main exceeded its shutdown deadline and systemd forcibly
stopped it, as in the previous deployment; this is not described as a clean shutdown. All ten expected services
were subsequently active, with no failed units, legacy processes or error-level journal entries.

Native Linux intake tests passed on the staged merged source before switching: document 28.522s, tools 14.238s,
agent 2.557s. All five configurations loaded with zero error findings; existing policy findings were retained.
Stateless smoke trace `trace-turn-0b6c72559ff1dae174887ac7` identifies exact source `694d716db`, is complete
and untruncated, and contains one request, one response and no tools. Its 23-byte response hash matches the exact
smoke marker. The retained generic role test kept revision 5, ledger 4 and its three text values plus explicit blank
across this actual restart. Earlier artifact evidence below retains its actual older runtime attribution.

The #1709 reconciliation itself keeps the persisted shape; the #1677 reader reopened the updated private copy.
Earlier protected settlement payload changes still require compatible readers. Prefer forward repair after new writes. Do not
perform binary-only rollback to an old strict reader or restore stale profile data over later operator answers.
A deliberate data restore requires quiescence, a separately preserved current snapshot and an explicit decision.

## Completed Artifact Lanes

These used the configured deployed agent over the authenticated loopback MintClaw WebSocket transport. They are
real agent/native writer/outbox transactions, **not actual Telegram ingress**. Each successful lane reached review,
waited for a separate finish request and operator test approval, and delivered one logical PDF. Source hashes stayed
unchanged. Independent pypdf checks and human inspection of every Poppler-rendered page supplement native checks.

| Case | Pages / provided facts | Result | Artifact SHA-256 |
| --- | --- | --- | --- |
| Conditional full | 2 / 1 fact plus applicability decisions | 4,179-byte editable PDF | `91a9faf6520c542d05b20c42f26764ccb30a75ed5ec4be21908d862587b66b70` |
| Small partial | 2 / 2 | 7,387-byte editable draft; required deferred contact retains its flag | `96397a08cabb5a20cb20f8ff0fffa033c920b34300ffb6f2cb7789e221cba38d` |
| Medium partial, post-schema fix | 3 / 8 | 14,060-byte editable draft; unassigned fields unchanged | `5ef7be77fa8ff1275dc2f6f570de00491db5be0c69859900bb949ac3749ceac7` |
| Large partial | 10 / 20 | 45,425-byte editable draft; 80 deferred fields unchanged | `c32ea3cd533dcfcb50aac2fd5ebabb3526c74dee30973c756ac04ce938fddd6e` |
| Medium full | 3 / 18 plus one preserved field | 14,431-byte editable PDF; 19 writable fields resolved | `19b9f7910d383e842b327c8a741507829e23c3f0c6203711c704e669e13a21b0` |

The [small/conditional/large fixture generator](../operations/pdfi3-fixtures.py) and
[medium fixture generator](../operations/pdfi4-fixtures.py) define the unrelated synthetic documents. Exact source
hashes are the ones in the operator guide and the [PDFI3 fixture record](pdf-agentic-intake-pdfi3-exit.md#qualification-fixtures).
Field checks include all fields, not only assigned values: unchanged unassigned values, types, flags and field
inventory. Small, medium-partial and large-partial page background streams additionally matched their source bytes.
The first four lanes predate the final protocol deployment; the medium-full lane ran on exact source `223b9af0d`.

Native terminal records for all five successful jobs contain only public metadata and the integrity digest:
wrapped keys, protected source, field events and pending input have been erased. The native job/delivery identities are:

| Case | Job | PDF delivery |
| --- | --- | --- |
| Conditional full | `form_job_ab3b7858d802e8e2d6645f7385173710` | `out_36bc0af285dc291ee2552529f37763d2` |
| Small partial | `form_job_8b5a0d298d0d1b0e2406794c07b88ba4` | `out_40c56395685ef1e77a6d080dafb6430e` |
| Medium partial | `form_job_0f3d52e4824da467c84684840c14c6ef` | `out_a99bfb434b3ea690e30c3784cf7b9887` |
| Large partial | `form_job_e12c555c549115cc71b2ebe043876b11` | `out_0abf71c0c3f048e0b286a25d40108108` |
| Medium full | `form_job_e8b9af96e08bec9d4aa8fe48ef96b6a7` | `out_920ace76a1e2c1d0884a266b0f315a8f` |

### Medium Full And Quoted Boolean Proof

The original job retained its accepted facts through restart and retries. A direct natural-language correction of
the existing return method supplied no native boolean token. Private quote/meaning verification and native apply
accepted a boolean `true` at revision 21. A native owner-authorized read on an isolated snapshot confirmed event
`form_value_4dd408badba601e20ef7129b6fb6b76f`, type `boolean`, confirmed/user/valid, without displaying its quote.
Trace `trace-turn-69b76f8ea1a64f482388a561` binds that acceptance to the deployed source.

The next explicit retained-input retry cleared remaining old markers without resending the original person facts.
Trace `trace-turn-92d60ff4cd29a23eda22950a` reached review revision 22, with 18 provided, one preserved,
zero missing/deferred/blocking fields and no pending input. Separate finish and approval produced the artifact above.
Trace `trace-turn-2f62a1b4f979b64b4f48a021` records its queued/sent delivery; the authoritative outbox record is
delivered, one attempt, one PDF part. A separate final text response is not a second PDF delivery.

The initial private oracle incorrectly included a telephone correction that had been prepared but never sent.
The action's request is absent from canonical session records and its output directory does not exist. The PDF
matches the actually submitted original telephone. Preserve both oracle versions and this discrepancy; do not
claim this run exercised telephone correction. That remains part of the actual Telegram check.

## Privacy, Recovery And Negative Results

### Same-Name Role Clarification And Delayed Advice

A separate medium-form test deliberately used two different people with the same full name, an initially
unassigned telephone and mixed explanatory guidance. Initially neither role received that telephone. A later
clarification assigned it to the emergency contact without repeating the number and explicitly left the borrower
telephone unanswered. Native readback verified both names, the contact telephone and a valid explicit borrower
blank. However, two fully accounted inputs still retained ambiguous/confirmation advice on source `223b9af0d`.
This was a real stale-advice defect, not successful clarification or the separate I-134 capacity failure.

#1709 reconciles only fully accounted advisory findings against a still-current accepted native event whose
admission snapshot includes the target input. It does not change values, clear native validation failures, discard
unaccounted text, reset counters or introduce a persisted/model protocol. Its checkpoint records the
[native gates and private-copy compatibility proof](pdf-agentic-intake-pdfi4-delayed-guidance-checkpoint.md).

After merged deployment, an explicit `/retry_form` used the same job
`form_job_5de92f1697c0396503e537e7df4b8863` and retained inputs, without resending facts. Exact-source trace
`trace-turn-96a16eb4d83fefc0176eb52b`, sequence 8, reports zero accepted values, two acknowledgements,
zero pending inputs and zero retained bytes at revision 5. A later collect action at sequence 18 updated the plan
to revision 6. An independent owner-authorized native read verified the three original text values and explicit
blank, with ledger revision still 4. The ordinary reply asked for remaining details, not those names or telephone.
This proves the deployed fix, not merely the earlier private-copy transition.

A separate `/cancel_form` on that job reached canceled revision 7, ledger 4. The protected record retained only
public metadata and its integrity digest: wrapped keys, source, values and pending input were erased. No PDF was
delivered or found in this test's output directories. Before and after cancellation, two distinctive private
sentinels had zero hits across canonical metadata/history, 34 derived messages, two summaries, 24 tool parts,
six matching untruncated traces, public/encrypted records and the bounded main journal. The old I-134 job remained
collecting at revision 47, ledger 45. This same-name negative/recovery case is headless real-agent evidence,
not a complete-form output or an actual Telegram pass.

### Earlier Artifact Privacy And Remaining Failures

Per-case sentinel scans covered the exact canonical session/history/summary, derived context and tool parts,
retained matching untruncated traces, public job, encrypted record and bounded main journal. They found zero hits:
conditional 1 distinctive sentinel; small 2; medium partial 5; large partial 20; medium full 9. The medium-full scan
included 80 derived messages, five actual summaries, 50 tool parts and 28 matching traces. These are bounded
case-specific observations, not a universal absence-of-leaks claim or a provider-side retention guarantee.

The small job survived actual compaction, recorded by
`trace-compaction-3108ea832cb53bd35fadda5b`, without losing its two accepted field events. Original jobs survived
actual deployments; retained-input retries did not reset their pass counts or require re-sending accepted facts.

Two failures must not be hidden:

1. The earlier medium-partial job `form_job_342f34507ec48af634c2001d87c18346` failed closed at structural
   verification, delivered no artifact and erased its protected key. #1662 corrected a value-dependent field-schema
   classification in the existing PDF library integration. The later medium-partial pass is an explicitly new
   post-fix transaction, not recovery of the erased job. Its first eight-fact apply was atomically rejected;
   an explicit retained-input retry then accepted all eight in that same new job without re-sending them.
2. The old official-I-134 test `form_job_97fe234d0675f876db226813b3ffaa4e` reached the persisted 32-pass
   ceiling for one retained utterance after historical retries. On `223b9af0d`, a new direct clarification accepted
   five checkbox corrections and preserved other data, but the exhausted older input still blocks output.
   Trace `trace-turn-9f33a54d49f14a08f417aa18` reports reservation/capacity failure at revision 47, ledger 45.
   No counter reset, retention extension, live-store rewrite or replacement is claimed. The ordinary model called
   the limit temporary and asked a misleading status/address clarification; that wording is not authoritative
   native state and is not a successful large-form UX result.

On 2026-10-18 the operator chose a separately identified fresh current-code I-134 qualification. The original job
has not been reset, canceled, replaced or misrepresented as recovered; the delayed-advice fix does not bypass its
capacity or unaccounted-input gates.

Earlier evidence-stage failures left accepted data and input intact. Subsequent real requests succeeded, but the
specific transient cause was not established. Private source/evidence probes do not substitute for actual runtime
or channel qualification. Do not report a guessed worker, timeout or ownership cause as proven.

## Remaining Mandatory Gates

### Fresh I-134 And Separately Deferred Backend Boundary

The fresh job `form_job_2e2aa653dd135ecc6e793e2d42f19f0a` used the same pinned official source, new synthetic
facts and actual runtime `694d716db`. Native progress reached revision 32, ledger 31, with 16 confirmed fields,
15 intentional blanks and one retained clarification. Independent readback matched 13 predeclared text values and
two controls, with no mismatched expected values or unexpected positive assignments. This is progress, not a
full-form pass. The accepted facts and pending counters survived a subsequent actual external gateway restart.

Trace `trace-turn-94c67d8bd8a11339202c3b0b` additionally exposed a coherent-selection defect: after browsing
several bounded views, a source-valid eight-field selection was rejected by latest-view-only proposal validation.
The focused fix is tracked in [PR #1720](https://github.com/bogdanovich/mintclaw/pull/1720); this checkpoint does
not treat that fix as full I-134 qualification. It merged and was deployed as recorded below.

Before sending the remaining financial facts, source-only inspection found that three original full-output targets
are read-only: employment type, employer name and the asset total. The source XFA scripts enable the first two
after an employment choice and compute the third from six amounts. Independent source flag inspection confirmed
this is not merely an inventory error. The current hybrid writer deliberately does not execute those scripts.
The original full-output oracle stays unchanged and no full artifact has been produced in this fresh test.

The operator explicitly chose to finish current intake fixes and defer this generic backend capability to the
[conditional/calculated follow-up](pdf-conditional-calculated-fields-roadmap.md). This does not qualify I-134,
waive its original full-output oracle, admit script execution or complete PDFI4. The unrelated ordinary-form and
actual Telegram acceptance remain distinct.

### Cross-Window Deployment And Same-Job Negative Continuation

#1720 merged as `727600bcc6619f87d3528df2cf1ad72c33c202fe` after exact head
`dcdb043a0c339a1be5a585c18d4b3e9da2c00aad` passed ten applicable CI checks; two jobs were skipped by existing
path policy. Clean substantive review `6246456622`, owner PR-level rocket `576983072`, clean mergeability and
no actionable thread preceded normal merge. An earlier inconclusive advisory was resolved by correcting its
wrapper invocation to use a literal worktree path; no shell guard or review policy was disabled.

The deployment deliberately pins that merged fix instead of adding unrelated PRs arriving during the build.
Actual main PID 1839561, CLI and runtime-binary SHA-256 matched
`766058265eedc2687a880d58df7686e7bd4ea1b9fdc20ffeec6cf2b688dce3f0`; Go build metadata identifies the full
merge commit. A changed-checkout guard stopped the first attempt before service mutation. The verified stage was
then reused only after clean-tree, exact-build, source ancestry, three-package test and configuration checks.
Staged intake tests passed: document 34.168s, tools 33.124s, agent 1.711s. All five configurations had zero error
findings and remained byte-identical; pre-existing policy findings were retained.

Backup `/home/server/mintclaw-pdfi4-backup.7qmIID` contains a 4,358,287,360-byte stopped-main archive, seven
binary copies including the actual running inode, five configurations and systemd units. Archive and all binary
checksums passed. Independent coding workspaces were excluded and left running. The old main again exceeded
its shutdown deadline and systemd forcibly stopped it; this was not a clean shutdown. All ten expected services
were subsequently active with zero failed units, legacy processes or error-level ten-minute journal entries.
Stateless smoke trace `trace-turn-9b0f6fbb4159b6e45d58fb57` identifies the exact merged source, is complete
and untruncated, and confirms the exact marker with six records and no tool calls.

The fresh I-134 retained revision 32, ledger 31 and its accepted values across deployment. A single targeted
four-checkbox clarification then used the same job; no accepted name, address or birth fact was resent. Trace
`trace-turn-a9505093edbe00f8d902634c`, sequence 8, reports four accepted assignments and four acknowledgements,
revision 36, ledger 35 and one old retained input. Independent native readback confirmed the original 13 text
matches, unchanged original control matches and all four newly supplied negative controls. A separate clarification
oracle augments, rather than replaces, the original 40-text/15-control oracle. No mismatched expected value or
unexpected positive assignment was found. No PDF was requested or delivered.

This is **not successful pending-input recovery**. The earlier 859-byte input went from 21 to its persisted
32-pass ceiling and still retains four ambiguity findings, even though the new clarification was accepted and
consumed. The public result reports `reservation/capacity_exceeded`. The ordinary reply incorrectly says the
clarification was not applied and again offers alternative statuses despite the accepted citizenship and negative
controls. At that source these were unresolved retained-input/progress UX findings, separate from cross-window
selection and XFA support. The continuation stopped at pending-input failure, so it does not qualify a live post-fix cross-window
collect action. Do not reset its counters, repeat accepted facts or label another fresh job as recovery.

Three distinct already-accepted private sentinels had zero hits across canonical metadata/history, 35 derived
messages, one summary, 26 tool parts, six matching untruncated traces, public/encrypted records and the main journal
bounded from this job's creation. This is a case-specific scan, not a provider-retention guarantee. The historical
I-134 job remained unchanged at revision 47, ledger 45. Both incomplete jobs remain protected, not terminally erased.

### Bounded Generic Clarification Repair

The [repair checkpoint](pdf-intake-clarification-checkpoint.md) records #1730 and #1736 merged after exact-head
CI, substantive review and authorized owner rocket approval, then deployed as
`95c58f41b18e258e404933283491574c14c6029f` with verified backups and actual running-inode/VCS proof.
Generic 13-field and 193-field regressions cover completed-window skipping, retained failure alongside new
acceptance, advisory chronology, actual conflicts, explicit retry, restart, fixed bounds and owner/privacy checks.

A fresh unrelated medium-form job preserved two accepted names across the actual deployment/restart. A later
message resolved the borrower's retained telephone without repeating its number and added a city: independent
native readback verified all four values, no wrong-person assignment and no repeated question for those values.
The remaining emergency-contact telephone ambiguity and its original text stayed retained; review/output were not
claimed complete. The final-source continuation reports two new acceptances and `received_input_state=accounted`.
The ordinary reply asks only the still-missing contact telephone. It does not report new acceptance as failed,
although it does not explicitly state the accepted count.

The new synthetic job was canceled normally, with terminal key/source/event/intake erasure, no PDF delivery and
unchanged source. Three actually accepted private sentinels had zero hits in the bounded ordinary-history,
derived-context, retained-trace, public-record and journal scans before and after cancellation. Both historical
I-134 encrypted records remained unchanged at revisions/ledgers 47/45 and 36/35. This headless gateway proof
qualifies only the narrow repair, not exhausted historical recovery, actual Telegram, full I-134 or PDFI4.

### Open Qualification And Exit

- The generic clarification/progress repair has the bounded automatic evidence above. Confirm its actual Telegram
  behavior using the [short operator check](../operations/pdfi4-free-text-form-test.md#bounded-clarification-repair-check).
  Keep fixed limits and original jobs; exhausted-input recovery requiring new authority still needs explicit admission.
- Complete applicable filling of the pinned official I-134 with synthetic data, followed by separate approval,
  one verified print-ready artifact, all-ten-page independent text/control/layout QA and terminal cleanup.
  Its Linux hybrid backend is admitted; editable hybrid output remains explicitly deferred.
- Actual Telegram attachment, Reply and standalone input, three successive messages, person-specific telephone
  correction, pause/resume, explicit partial finish and a separate cancellation test. The operator has received
  the blank medium fixture and short scenario; no actual channel pass is asserted yet.
- Reconcile every goal acceptance row with its exact live or executable evidence, including portable tests,
  wrong-owner/route/revision, stale/duplicate receipts and uncertain delivery without replay.
- Merge the final exit and updated operator guide only after these gates pass. Keep the goal and roadmap open
  until then; this checkpoint does not waive a gate or turn a negative test into a completed transaction.
