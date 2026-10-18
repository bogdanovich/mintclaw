# PDFI4 Qualification Progress

## Decision

PDFI4 is **not complete**. This is a dated evidence checkpoint, not an exit report. The
[implementation goal](pdf-agentic-intake-pdfi4-goal.md) remains authoritative. As of 2026-10-18 02:56 UTC,
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

The final protocol packet #1677 merged as `223b9af0dd0a7a4243e56100df0470212703c452` after all 17 checks
were green on reviewed head `b3a25ae0fc7ce09d99349caa458e2fc0d5790814`, substantive clean review
`6244296064`, owner PR-level rocket `576720481`, clean mergeability and no actionable unresolved thread.
Its final local evidence includes the document/tool/agent intake selections, focused races, formatting, scoped
production lint and documentation lint. This is not a claim that the entire agent package suite ran on that head.

The actual main gateway used by the latest qualification runs executes merged `223b9af0d`; its process and
runtime-binary SHA-256 are `6de69d361198439c1145c103ff92f6055fc1cf340b07455a1b6d964d6931e320`.
A parallel workflow subsequently advanced the checkout and CLI to `aa6b3767a`; those are not proof that the
running gateway changed. Fresh qualification traces identify their actual source revision. Do not downgrade or
overwrite another workflow's checkout to make version labels agree.

The coherent stopped-main backup is `/home/server/mintclaw-pdfi4-backup.IYZquw`, with a 4,342,702,080-byte
profile archive plus binary/config/unit backups. Archive and all six backed-up binary checksums passed.
The old main process exceeded the normal shutdown deadline during deployment and systemd forcibly stopped it;
this warning is recorded, not described as a clean shutdown. The new main and all ten expected services were active,
with no failed units, legacy processes or error-level journal entries at the post-deployment check.

Stateless smoke trace `trace-turn-df46bfb593ba806342f0974b` identifies exact source `223b9af0d`, is complete
and untruncated, and contains one request, one response and no tools. Its response hash matches the exact smoke
marker. The original retained source/schema and accepted field revisions survived the actual restart.

New protected settlement payloads require compatible readers. Prefer forward repair after new writes. Do not
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

Earlier evidence-stage failures left accepted data and input intact. Subsequent real requests succeeded, but the
specific transient cause was not established. Private source/evidence probes do not substitute for actual runtime
or channel qualification. Do not report a guessed worker, timeout or ownership cause as proven.

## Remaining Mandatory Gates

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
