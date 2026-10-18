# PDF Intake Role-Clarification Follow-Up

## Scope And First Divergence

This is the bounded repair defined by the
[follow-up goal](pdf-intake-telegram-followup-goal.md), not full PDFI4 or I-134
qualification. The motivating actual Telegram run remains negative evidence:

- `trace-turn-a2ada6dc42077ff463370329`, sequence 9, accepted only a city;
  the final answer incorrectly said the borrower's telephone was accepted.
- `trace-turn-506b2bdfcdd9db3ff49e2df2` delivered an introduction through
  `message` and repeated it in the later collection response. These were two
  authored descriptions, not proven transport duplication.

The original Telegram job and both historical I-134 jobs are read-only evidence.
No retry, cancellation, limit reset, retention extension or replacement of
those jobs is authorized by this repair.

## Mechanism And Bounds

The protected interpretation view now carries immutable admission order
derived from the existing native input sequence. Job revisions can be equal;
retry selection does not make an earlier utterance newer. The private wire
distinguishes the utterance being interpreted from later related clarification,
so an explicit role-only clarification can identify the owner of an earlier
literal without repeating that literal. Chronology itself grants no authority
to infer a role, overwrite a value or dismiss a real conflict.

No persisted shape or new state store was introduced. Admission order stays
out of ordinary JSON and values still require source-literal evidence and the
existing native owner/route/source/schema/revision validation.

`accepted_fields` is derived from actual new native accepted IDs, checked
against current strong native state and bounded to eight value-free
descriptors. A longer result is explicitly truncated. Aggregate acceptance or
accounting alone cannot support a field-specific acknowledgement. Replay does
not describe historical acceptance as new. The existing response-only renderer
uses these descriptors and does not repeat an introduction already delivered
earlier in the same turn; the protected question must still be presented.

There is no form-specific parser, Russian/English phrase rule, new planner,
delivery deduplication store, approval bypass or limit increase.

## Reviewed Merge And Executable Evidence

[PR #1763](https://github.com/bogdanovich/mintclaw/pull/1763) merged normally
on 2026-10-18 at 22:20:04 UTC as
`82178cbd7156ff1ea77cd6c37002ec71fa6938e8`. Its exact reviewed head was
`6671196745cc87576fd7affad89ac4b101cf999d`: thirteen checks succeeded;
Frontend was skipped by existing path policy. Substantive clean review
`6255137642`, owner PR-level rocket `578010053`, clean mergeability and no
actionable thread preceded merge. No substantive review-fix cycle was needed.

The change was 13 files, +607/-28: production five files +102/-28, tests five
files +440, documentation three files +65. Production stayed within document,
tools and agent packages. Tests cover native order across equal revisions,
replay, consumption and reopen; bounded private wire; role-only clarification
on independent 13/37-field schemas in RU/EN; actual native values and untouched
other-person fields; partial acceptance; bounded nine-field projection and
replay; exhausted older input alongside new acceptance; and the real
response-only rendering context with an earlier delivered introduction.

Local full `Intake` tests passed for document/tools/agent in
142.774/96.357/4.125 seconds; focused races passed in 2.326/11.196/3.281
seconds. Formatting, documentation lint, diff checks and test-inclusive scoped
lint passed. The actual pre-push formatter and lint passed with zero issues.
One additional native lifecycle test initially stalled before Go runtime
startup on macOS; only its own processes were stopped. The identical retry
passed in 1.731 seconds. Exact-head macOS and Linux CI also passed. That stall
was not represented as a successful test or fixed by weakening validation.

## Deployment And Rollback Evidence

The verified deployment pins the merged commit above. Fresh preflight found
an intervening deployment at `c497347b8227b8489725735aaca5fea8f837d0b1`;
the update advanced from that clean source, not an obsolete baseline.
Its actual running bytes were recorded; that old binary omitted Go VCS
metadata, so only its checksum and embedded commit identify the old build.

All three new binaries were built before any service change, with full exact
Go VCS revision and `vcs.modified=false`. Server intake tests passed for
document/tools/agent in 34.278/31.325/1.979 seconds. Five configurations loaded
with zero error findings; existing policy findings were not suppressed.

Backup
`/home/server/mintclaw-telegram-followup-backup-20261018T222847Z.iJFaTs`
contains seven checksum-verified binary copies, including the actual old main
inode, five configurations, user units/service states and a 315,307-byte
stopped-main document-state/key archive with verified checksum. This is a
scoped no-migration backup, **not** a full profile/history backup. No old backup
or cache was deleted. Old main exceeded its shutdown deadline and systemd
stopped it forcibly; this was not a clean shutdown.

The installation script stopped at its post-install config comparison:
main's credentials/computer settings had changed during the operation.
Four other configs were byte-identical; PDF/model settings and all other main
keys were unchanged. No config was written or restored by this deployment.
The current main config was preserved and independently revalidated with zero
errors. The exact installation, active services and backup checksums were
then independently verified; the failed guard was not labeled a successful
uninterrupted script run or bypassed by replaying installation.

Actual main PID 3284317 and all five gateway processes had SHA-256
`32608891edb2b52eb68ac0f54462061f085315e6a835e1f73880f224e615b310`,
matching the verified build, CLI and installed runtime. Actual main Go VCS
metadata names the full target commit and clean source. Actual launcher PID
3284318 also matched its verified staged/installed binary. All ten expected
services were active, with zero failed units, legacy processes or ten-minute
error-level journal entries. Stateless exact-marker smoke
`trace-turn-9c11f11423ecad1cfc975e14` completed on that source with six
untruncated records and no tool calls.

If rollback is needed, stop only affected units and restore the verified old
binary copies from this backup to their corresponding CLI/runtime paths,
then start those units and repeat health/source checks. Do not overwrite the
concurrent main config change or automatically restore mutable document data
after new writes. Config/data restoration requires comparison and a separate
explicit decision. Keep failed artifacts and traces.

## Real Russian Gateway/Model Qualification

A separate authenticated gateway session ran four real requests with the
unchanged `gpt-5.6-terra` model. This is not a mock, stateless substitute or
Telegram emulator. It used the unrelated three-page/20-field equipment-loan
AcroForm, SHA-256
`ca3ea05ef03e6356ab5ea3ab8170d6712fcf6c60b9198de16d1b5db17efeaeff`.
The new job was `form_job_38268ead26fb618e50ea0c8db788f0f6`.
The canonical session route came from its unique native metadata; protected
owner, source and schema were verified before sending any private facts.
Native readers opened isolated copies only, never the canonical store.

1. The Russian start request produced one introduction and activated intake
   at revision/ledger 1/0. Trace
   `trace-turn-6a9e03b3c8b550c8a543e33d` completed with 50 records.
2. One Russian paragraph supplied two role-specific names and one telephone
   whose owner was explicitly unspecified. Native revision/ledger became 3/2:
   both names matched the private oracle; neither telephone was assigned.
   The 204-byte input remained at three passes with two role ambiguities.
   Trace `trace-turn-dcadee2701f074088ba01e07`, sequence 8, identifies the
   two actually accepted name fields. It completed with 15 records.
3. One later Russian message assigned the previous number only to the
   borrower and added a city, without repeating either name or the number.
   Native revision/ledger became 5/4: all four private values matched, with no
   mismatched value or unexpected assignment. The contact telephone had no
   user assignment, including no invented intentional blank. The old input
   remained at 204 bytes/five passes with only the contact-phone finding;
   the new receipt was accounted. Trace
   `trace-turn-8b295fc08e1f3c94ba60823c`, sequence 8, reports the borrower
   telephone and city as the two new accepted fields. It completed with 15
   records. The ordinary reply correctly acknowledged those two fields and
   asked only for the missing contact telephone, not either accepted name or
   the resolved borrower number.
4. Normal `/stop` on this new session canceled only this synthetic job at
   revision/ledger 6/4. Trace
   `trace-turn-aea820089d180779aebfd0d8` completed with 22 records. Native
   intake was absent; wrapped key, source, events and intake were erased.
   The retained record contained only public metadata and integrity digest.
   No output operation/artifact existed, no PDF was delivered, and the original
   source hash stayed unchanged.

All four exact retained traces name the merged source, have the expected
diagnostic schema/redaction policy and are complete/untruncated. A broad
`diagnostics list` reported scan truncation, so it was not used as proof of
absence: exact reads and a separately bounded retained-session scan established
these four traces.

All four actually accepted private literals had zero hits in the bounded
ordinary-surface scan before and after cancellation. Before cancellation:
22 derived messages, 16 tool parts and three matching traces; afterward:
28 messages, 20 tool parts and four traces. Both scans also covered canonical
metadata/history, public/encrypted job records and the main journal since the
native job creation; there were no derived summaries. Explicitly private
inputs, oracle, artifacts and isolated snapshots were excluded. This is a
case-specific privacy check, not a provider-retention guarantee.
The four temporary snapshot state/key copies created by this run were removed
after verification, using exact guarded paths. Canonical data, historical
snapshots and the verified deployment backup were not deleted.

Canonical-JSON fingerprints of the original Telegram job and both historical
I-134 jobs stayed identical, with revisions/ledgers 4/3, 47/45 and 36/35.
Their inputs, counters and retention were not repaired, reset or extended.

One wording limitation remains visible: before role clarification the model
called the two unresolved telephone fields “two numbers” even though only one
unassigned number was supplied. It did not falsely accept either number;
native state remained unresolved. This narrow field-acceptance repair is not
a claim that every generated explanation or the full large-form UX is perfect.

## Remaining Boundaries

Actual Telegram requalification must use fresh operator messages and their
traces; a headless gateway test is not Telegram ingress. The
[short operator check](../operations/pdfi4-free-text-form-test.md#bounded-clarification-repair-check)
describes the role-only clarification without repeating the number and the
required native-state verification. Stop that separate synthetic job rather
than requesting a PDF. Do not replay the historical negative test.

Exhausted historical recovery, full I-134 filling, conditional/calculated XFA,
editable hybrid derivatives and the broader PDFI4 exit remain open under their
own goals. This checkpoint does not waive any of those gates.
