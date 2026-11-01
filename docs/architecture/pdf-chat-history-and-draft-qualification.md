# Ordinary PDF Conversation And Verified Draft Qualification

## Status — 2026-11-01 UTC

The bounded [conversation/history goal](pdf-chat-history-and-draft-goal.md) has
reviewed implementation, deployment and fresh authenticated gateway/real-model
qualification. Ordinary conversation remains in normal owner-scoped history;
verified values independently remain in the existing encrypted native draft.
This intentionally permits authored form details in the owner's ordinary
history under its normal retention policy. History is not execution authority.

Actual Telegram upload/callback acceptance remains operator-driven and is not
claimed here. Full PDFI4, official-large-form/I-134 completion, editable hybrid
output and conditional/calculated XFA remain outside this bounded result.

## Reviewed implementation

- [#1909](https://github.com/bogdanovich/mintclaw/pull/1909) records the approved contract.
- [#1910](https://github.com/bogdanovich/mintclaw/pull/1910) adds canonical conversation identity,
  idempotent retention and diagnostic provenance; merge `2080afdb9e7909d28dff14f0f4662953fe17bde0`.
- [#1916](https://github.com/bogdanovich/mintclaw/pull/1916) connects ordinary ingress,
  steering and task-question replies, source-bound provider projection and
  privacy boundaries; merge `ce4d10f163bac4152763802758fb82c4755c2d06`.
- [#1922](https://github.com/bogdanovich/mintclaw/pull/1922) keeps a complete-draft
  return conversational while another task remains pending; merge
  `ce26a6091c65f159b553a73fb42ddfe1b887b1a4`.

Code PRs used exact-head CI, substantive review, authorized owner PR-level rocket
and normal merge commits, with no unresolved actionable threads. Final #1922
head was `66dc4f0c2e6cf5fb70e49929476b599faf972093`: 10 successful checks and two
policy skips, review comment `6553558281`, rocket reaction `615309023`.
Its production change is 13 lines in one existing adapter, with 144 test lines;
no new store, planner, queue or form-specific rule. No reviewer-driven fix cycle
was needed for this follow-up.

Regressions cover duplicate/repeated input, durable identity, steering order,
suspension/recovery/replay, current-input projection, complete provider tool
pairing, native person/source/schema/revision isolation, explicit secret/auth
channels and diagnostic privacy. Existing native task/output guards remain.
For #1922, both new regressions failed before the fix. Final local results:

- Full repository formatter and final changed-file formatting diff: clean.
- Actual affected-package lint: zero issues.
- Normal form suites: document 285.534s; tools 151.215s.
- Race form suites: document 289.835s; tools 196.712s.
- Final new regressions: normal 3.617s; race 5.536s.
- Protected-answer agent continuation suite: 8.551s.

The additional fix was required by a real negative qualification, not just a
unit test: `trace-turn-19acebb2bdb3490f9e34ac7c`, sequences 21–22, admitted a
review from field completeness while `pending_tasks=1`. Native authority
correctly rejected it and the user received a continuation error. That run was
not counted as completion; its owners were canceled. The fix preserves the
native rejection while admitting a response-only status reply. Missing-field
planning still works when fields are incomplete.

## Verified deployment

Deployed merged source: `ce26a6091c65f159b553a73fb42ddfe1b887b1a4`.
Actual executed core SHA-256:
`095de8b37facd508b9f53369b587c60d3041824c2727df5c7a27c8bacb36401a`.
The CLI and four shared gateway executables match; full Go VCS metadata reports
that source with `vcs.modified=false`. All three Linux binaries were built and
validated from a clean real-`.git` build source; only core was installed.

Capacity was checked before mutation, including a 2GB reserve and independent
old binary copies. Rollback checkpoint:
`/home/server/mintclaw-history-core-backup-20261101T044911Z.mrIuni`.
It contains verified binaries, units, all five configs and quiescent affected
native state/keys. The earlier checkpoint remains preserved. Do not downgrade
mutable data automatically after new writes; compare a restore or forward-fix.

All five configs remained byte-identical and loaded without schema errors.
Existing policy findings were not described as zero. Nutrition's intentional
pin, launcher and companion were preserved. Final health: all ten expected
services active, zero failed/legacy processes, expected HTTP 302/404, no
error-level journal entries in the ten-minute observation window, 5.9GB free.
Selected active configs, gateway scripts and units had no former product names.

Stateless smoke returned exact marker `MINTCLAW_PDF_HISTORY_SMOKE_OK`.
Fresh trace `trace-turn-34d974d18df47e751f6bada7`, created
`2026-11-01T04:50:17.768467566Z`, has schema v1, six records, completed outcome,
no truncation/drops and matching response/final marker hashes.

## Fresh two-form runtime qualification

Authenticated SDK gateway input used NEW isolated synthetic sessions with nonce
`c52e5e286d44`, after deployment. This was the running production agent, not a
mock provider or Telegram emulator. Observed public reply model:
`gpt-5.6-terra`; production model/config overrides were preserved.

| Fixture | Source SHA-256 | Shape |
| --- | --- | --- |
| Community Equipment Loan | `ca3ea05ef03e6356ab5ea3ab8170d6712fcf6c60b9198de16d1b5db17efeaeff` | 3 pages, 20 fields |
| `pkg/document/testdata/acroform-fields.pdf` | `33c467eee9a6c23eaa154dc1810f6b733ba51aa65d43146fa8da19aff33868dc` | 2 pages, 8 fields |

All five source pages were rendered/visually inspected. Both source hashes and
native schema bindings remained unchanged. No output PDF was approved or made.

Primary job `form_job_2991dfb6eb91c0645ce898a2c917e683` retained the same mode:

1. Russian free text supplied two names and a deliberately unowned phone.
   Exactly two native facts were confirmed; neither phone field was assigned.
2. A normal clarification assigned the prior phone ONLY to the borrower and
   supplied a city. Exactly four facts were confirmed; the contact phone stayed
   genuinely missing, not guessed, N/A or deferred. No repeated ownership question.
3. A fresh arbitrary two-label/color mapping received only `ACK_HISTORY`, with
   no label echo. `test` received `Test received.` without a task/form choice.
   A later reply correctly recalled BOTH labels and their colors from history.
   Native facts remained four throughout the detours.
4. One mixed message supplied borrower email and asked `17 + 26`. Native email
   became the fifth confirmed fact and the ordinary answer included 43.
5. Ordinary draft return listed accepted and missing information without
   repeating accepted questions or requiring re-entry. Same job/mode, five
   exact native values, no pending input, no output approval/operation/artifact.

Final primary canonical inspection: 56 records; all seven post-start authored
paragraphs exactly once, in order, with durable identities; no duplicate or
missing IDs, hidden form-value carrier or other-owner value match.
Recall/mixed/return traces respectively:
`trace-turn-32692d016a685586fe275fe0`,
`trace-turn-847bef98a461f6a871698263`,
`trace-turn-03a81453014ac60a95d9ab9d`.

Independent English job `form_job_952cfb40092cd5045efdae578d69e54f` accepted two
exact new values and preserved six existing entries. A bare reference to another
document created a genuine native question. Its original authored text was
visible exactly once in normal history; safety did not depend on hiding it.
The operator's unrelated reply received only `ACK_PENDING`, and a later reply
recalled its fresh arbitrary label from that user-only paragraph.

Original route `form_route_32c7de2c4265d0fe414e26c253db8696` stayed `question`:
one interpretation pass, unchanged decision revision 4, identical 58-byte payload
digest and media digest. Revision 4 here is the last decision, NOT an execution
grant. Its interaction was `interaction_8ae1a38b4dd41dac453f0254032d5ecb`.
The unrelated reply resolved that interaction but did not grant the original
native request. A stale form review was invalidated, not reused as authority.

The formerly failing final return now completed normally in
`trace-turn-4beb25e660d89ef29b880958`. Its only tool call was the bound native
`form/route`; no review, inspection of the other document or output call followed.
Same job/mode, two exact accepted values, unchanged pending request, zero output
authority. Final independent history: 35 records; five post-start inputs each
exactly once with IDs and order, zero other-owner/hidden-value matches.

Canonical receipt chronology legitimately places the ordinary question reply
before the parked tool result. Actual provider projections normalize that
pairing; canonical chronology was not rewritten to pretend it was provider order.

## Privacy, support and cleanup

The bounded scan selected 22 exact-owner/child traces from 23 candidates before
cleanup. There were 45 model requests: 31 fully decoded provider previews with
zero pairing errors, plus 14 intentionally bounded initial previews confined
to the two form-start turns. This is NOT full prompt-content coverage. The
captured trace data, native public state, owned interaction and 699 journal
entries had zero literal synthetic form-value matches. No ordinary turn failed;
four one-record interaction fragments legitimately have no turn outcome.

The default metadata-only support bundle selected three exact traces. All five
manifest-listed files were size/SHA-256 verified; six files including manifest
were privacy-scanned, with zero test-value matches and no incomplete marker.
Manifest SHA-256:
`72ee9f6894e3efef9cf600ac111e0efba03b0303ca7b466ed4afb419231a2ee2`.
No upload was performed. Dedicated secret/password/auth inputs remain protected;
internal native proposals were not promoted into ordinary conversation.

Only the two fresh owners were canceled. Both native tombstones have no wrapped
key, private source/events, public fields, operation or artifact. All three
preexisting job record contents match the pre-deploy backup without changes.
Eleven fresh temporary snapshots/key copies were removed immediately
after inspection; all 24 recorded temporary snapshot directories, including
the 13 retired negative-run copies, are absent. Operational backups and the
deliberately retained synthetic owner history/evidence were not erased.

## Remaining limits

- Telegram-specific upload/reply/button behavior is not covered by SDK input;
  use the manual check below. It is not an unobserved pass.
- One final model reply called the separate unresolved request a “pending review
  task.” Native evidence shows it is task-scope state, NOT a review request or
  permission to execute. The successful conversation/fence result is bounded;
  it is not a claim that every model explanation is perfectly worded.
- Initial diagnostic previews are bounded; arbitrary unlimited verbatim recall,
  general compaction redesign and complete large-form/output qualification are
  not claimed. Successful background compactions did not erase canonical input
  or accepted native facts in this run.

## Manual Telegram check

Use a fresh synthetic Community Equipment Loan draft (hash above), not an old
human job or an I-134/XFA test. No signature, output or submission is required.

1. Attach the PDF: “Я заёмщик. Объясни документ и сгруппируй необходимые данные.
   Помоги заполнить обычными сообщениями. Итоговый PDF пока не создавай.”
2. Send: “Я заёмщик Casey Mint. Контакт на случай чрезвычайной ситуации — Robin
   Mint. Телефон 202-555-0147; пока не указываю, чей он.” Expect both names and
   one genuine ownership clarification, not a guessed phone assignment.
3. Reply: “Предыдущий телефон принадлежит только заёмщику. Телефон контактного
   лица пока не указан. Город заёмщика Mintford.” Expect four accepted facts;
   no repeat question asking whether that same number belongs to both people.
4. Change topic: “Код PINE_483 означает бирюзовый, MAPLE_927 — янтарный. Ответь
   только ACK, не повторяй коды.” Send `test`, then ask which code meant each
   color. Expect normal conversation and correct recall, no task/form menu.
5. Send: “Мой email как заёмщика — casey@example.invalid. И короткий вопрос:
   сколько будет 17 + 26? PDF пока не создавай.” Expect email acceptance AND 43.
6. Say: “Вернёмся к форме. Какие сведения приняты и что ещё неизвестно? PDF пока
   не создавай.” Expect the same saved draft without repeating accepted fields.

No `/answer`, task-switch or cancel command is necessary for these ordinary
messages. `/cancel_form` is optional cleanup of YOUR new test draft. If the
result differs, preserve the message time and inspect the exact passive trace
and native owner; never replay an uncertain external effect.
