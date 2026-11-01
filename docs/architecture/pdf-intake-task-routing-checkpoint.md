# Protected PDF Intake: Natural Task Routing Checkpoint

## Scope And Historical Evidence

This is the bounded repair admitted by the
[natural task routing goal](pdf-intake-natural-task-routing-goal.md), not a
PDFI4, I-134/XFA or general task-stack completion claim. Actual uploaded-file
Telegram requalification remains an operator gate.

The motivating Telegram trace, `trace-turn-2770bfdd04277421f2ae55f2`,
sequence 9, retained a 194-byte protected message with no accepted values at
revision 1. Its interpretation failure was collapsed to
`interpretation_unavailable`. The precise historical cause is unknown: the
word “temporary” and concurrent compaction are not causal evidence. The
historical Telegram job and I-134 jobs were not retried or canceled.

## Implementation And Reviewed Merges

- [#1782](https://github.com/bogdanovich/mintclaw/pull/1782), merge
  `6e69f26dfe2f3aba33bd3810f9e9a090a56053cd`: preserve closed private
  interpretation failure categories without publishing protected values.
- [#1784](https://github.com/bogdanovich/mintclaw/pull/1784), merge
  `f2e2636e56798fbf8b874512ce2f82243a55b0d0`: document-owned native routing
  foundation inside the existing protected form store.
- [#1796](https://github.com/bogdanovich/mintclaw/pull/1796), merge
  `4439ac067b2edc9132c2b49e8a7f2f3efbb70729`: runtime consumer, protected
  task-scope questions, authenticated media references and resumable drafts.
- [#1817](https://github.com/bogdanovich/mintclaw/pull/1817), merge
  `973d36b0abcb10a67b04f33e3879d20d371a0ec1`: a bare reply to the current
  task-scope question takes precedence over new document-root admission.
  The exact question/response identity is durable before native acceptance.
- [#1822](https://github.com/bogdanovich/mintclaw/pull/1822), merge
  `cc8f2ae1d8e765d97d30dfc23620562c60293364`: retain authorized task input
  through discovery and subsequent tool/model steps in the same prepared turn.

Each code PR used normal exact-head CI, substantive review, authorized
PR-level rocket and confirmed merge. #1822's reviewed head was
`ebd6e7dfeb9f7464b76a3d358c03ff109170934c`; all configured checks passed,
with Frontend and PDFium WASM skipped by existing path policy. Review comment
`6261221572`, owner rocket `578722304`, clean mergeability and no actionable
thread preceded its normal merge at 2026-10-19 06:35:45 UTC.

## Ownership And Lifetime

The existing encrypted document job owns the old draft and pending task
selection; there is no second value store, new service, phrase parser or
form-specific production rule. A switch suspends the old draft rather than
erasing it. Role-only clarification remains protected input to the same form.
An ambiguous attachment asks for meaningful scope with predefined choices,
free text and cancellation; it cannot silently replace the old source.

Only a newly authorized task's input/media may enter its continuation. Old
form values remain protected. Owner, route, source, schema and revision fences,
native at-most-once grants and approval/output authority remain unchanged.
Stale or uncertain handoffs do not release the payload again. Unknown or mixed
private intent stays protected; a task switch is not permission to submit a
form or approve an external effect.

The runtime distinguishes two existing live-context lifetimes: extracted
document data remains one model call, while typed authorized `ContextInput`
lasts for the current prepared tool/model loop. Both reuse the same unique
projection identities and monotonic aggregate text budget. All remaining
projections are released on turn exit, including error, abort and suspension.
Neither lifetime mutates the canonical root or stores raw input in history.

## Negative Qualification Findings And Regressions

The first real gateway sequence preserved four native values and handled an
explicit arithmetic task switch, but its bare answer to a second-file question
was admitted as a new root. The original file was not released; asking for
another upload was a failure, not a pass. #1817 repairs that ingress precedence.

After deployment of #1817, native authorization succeeded but reader discovery
consumed the one-shot purpose. In
`trace-turn-bcf9dc4f19a3a8ea6298b9b9`, sequences 7–8 grant the task, 14–15
discover a reader, and 21–29 return to the old form. A fresh synthetic session
also failed. Automatic recovery correctly returned `handoff_uncertain`
without releasing the consumed task again. Those attempts were not replayed
or represented as successful second-file reads. #1822 repairs that lifetime.

An architecture checkpoint on #1817 chose the existing turn-owned projection
over retaining all extracted data or adding another task runner/store.
#1822 adds 27 production lines across four files and 109 test lines across
three files; no review-fix cycle was needed. Its real coordinator/native-sink
regression fails before the repair and passes afterward, requiring the original
purpose again after harmless discovery. Focused tests passed in 12.242 seconds,
focused races in 14.711 seconds, and broader context/compaction/cache/
interaction/document/protected-input/turn regressions in 182.173 seconds.
`make fmt`, diff checks and scoped production lint passed with zero issues.

The wider ad-hoc Linux backend run was not a pass: it encountered missing
Unicode appearance resources, an extraction deadline and a whole-package
timeout. The appearance failure reproduced on an identical preexisting backend
tree. No backend/font installation or weakened test is part of this repair.
These observations do not invalidate focused routing results or establish
that every PDF backend is qualified.

## Deployment And Rollback

The final deployment installed merged source
`cc8f2ae1d8e765d97d30dfc23620562c60293364` from a clean normal clone, not a
dirty worktree build. Preflight found an intervening deployment at
`b00976e76f03e8562f1c96a4757601fb47de6b8e`. Its actual main checksum and
embedded revision were recorded; that old binary omitted Go VCS metadata, so
no full old-source VCS proof is claimed. No parallel changes were reverted.

Native Linux intake tests passed for document/tools/agent in
56.568/33.503/8.286 seconds, including the new lifetime regression. All five
configs loaded with zero errors and were byte-identical after installation.
Existing policy findings remained visible: main 10, family 5, nutrition 6,
reviewer 5, spouse 4. No model, approval bypass or backend resources changed.

Backup
`/home/server/mintclaw-task-routing-backup-20261019T065240Z.n8uUtN`
contains seven checksum-verified binary copies including the old main inode,
five configs, user units/service states and a checksum-verified stopped-main
document-state/key archive. It is a scoped backup, not full profile/history
recovery. Old main exceeded its stop deadline and systemd terminated it;
this was not a clean shutdown. No last-known-good backup was deleted.

The CLI, runtime files, all five actual gateway executables and the actual
launcher matched their new builds byte-for-byte, with exact full Go VCS
revision and `vcs.modified=false`. Main PID 4168917 had SHA-256
`58caba7aca3faba544dae92afbca9c7b81cb8f9e4deff109d9f23aa4781fe4a7`;
launcher PID 4168925 also matched its verified build. All ten expected services
were active, with zero failed units, legacy processes and ten-minute
application error-level journal entries. Launcher returned 302 and webhook
GET 404 as expected. Workspace and harness revisions were unchanged.

The first smoke command was rejected before a model call because `agent`
does not accept `--config`; that negative log was preserved. The corrected
environment-based invocation ran once and returned the exact 36-byte marker
`MINTCLAW_TASK_ROUTING_1822_DEPLOY_OK`. Its hash was
`bc5b0617b158ceab9767cdf06a5e207b2c8b456ed04c1b7c2e10c70e8908763c`.
New trace `trace-turn-37b0b9b3f7488832fef12a7a` names the full installed
source, completed with six untruncated redacted records and no tools.

For rollback, stop only affected services, restore the verified old binaries
to their corresponding CLI/runtime paths, restart and repeat identity/health
checks. Do not automatically restore mutable document data or overwrite live
configs after new writes; compare them and obtain a separate restore decision.

## Real Gateway/Model Qualification

Two isolated synthetic gateway sessions used the unchanged `gpt-5.6-terra`
model and unrelated three-page/20-field equipment-loan AcroForm, source SHA-256
`ca3ea05ef03e6356ab5ea3ab8170d6712fcf6c60b9198de16d1b5db17efeaeff`.
Both accepted two role-specific names, kept a telephone initially unassigned,
then accepted the borrower-only telephone and city from a role-only clarification
without repeated literals. Native readback matched all four private oracle
values, with no unexpected assignment or intentional blank for the contact
telephone. Arithmetic task switching returned `MINTCLAW_TASK_SWITCH_OK:42`
without canceling the draft. These initial steps ran before the final lifetime
repair and are not presented as clean new-source runs.

After the real deployment/restart, the second session's same job
`form_job_884ed1caf28118de57f17d8f49a631e3` still had four exact matches at
revision/ledger 9/4. New-source qualification used a **new** second-file
selection, not replay of its previously consumed handoff:

1. A bare selector for the independent `text.pdf` produced question `b287c69e`,
   with two predefined choices, free text and cancellation. The sealed binding
   referenced the correct job, revision 9 and new native route event. The old
   source, four values and ledger were unchanged; the pending file selector
   occupied 58 bytes. Trace `trace-turn-1c2697cba7d85461b1b45e5a`
   suspended with 15 untruncated records on the final source.
2. An ordinary standalone free-text answer selected a new task, explicitly
   requested BM25 reader discovery followed by extraction, and contained no
   filename, reply-to ID or repeated upload. Trace
   `trace-turn-73063dfcfc52d97207f67095` records native routing at 8–9,
   BM25 discovery at 15–16, document inspection at 22–23 and extraction at
   29–30. It completed with 37 untruncated redacted records on the final
   source, with no `exec` or old-form collection fallback. The final response
   returned `MintClaw text fixture`, page 1, with its request identity.
3. The actual extract call was `call_32j9g3Ypt1cdnqIq7HGDnJPd`, generation
   `tool-call-generation-219d237d-e777-4825-9a86-2ccd8a598a77`.
   Its report succeeded for the selected 669-byte PDF, source SHA-256
   `bb1c32a5cbf83bc27a6828ef35ad96e598e26882c07d1cb16bc2eeb805b98797`,
   page 1 of 1. The 42-byte extracted NDJSON artifact had SHA-256
   `97a2b8d1044f1c8f2641456edc45858581ef20fd0f45e22e3c5f591602b5d8d3`:
   independently hashing the known page-one marker record produced the same
   digest. This is extraction evidence, not only a plausible model answer.
   The old draft was suspended at revision/ledger 10/4, with four exact values,
   no pending task and no reset of its older 206-byte/five-pass retained input.
4. An ordinary request to return activated the same job/mode at revision/ledger
   11/4. All four values, role isolation, source and retained-input counters
   were unchanged. Trace `trace-turn-6e90913663965097254043a6` completed
   with 15 untruncated records on the final source. No accepted fact was asked
   for again and no new form or filled PDF was created.

**Remaining dialogue limitation:** that successful native return produced a
generic pending-input/`/retry_form` suggestion rather than a useful next
missing-field question. The earlier unresolved contact finding remained;
no retry was executed or represented as necessary recovery. Native resume
and preserved values are verified, but polished continuing intake is not.
This remains a PDFI4 dialogue qualification item, not an excuse to increase
budgets or claim full completion.

## Cleanup, Privacy And Evidence Limits

The successful session's explicit cancel was acknowledged and left its job
canceled at revision/ledger 12/4, retaining only public metadata and integrity
digest. Protected source, wrapped key, events and intake were erased.
All gateway test steps delivered zero filled-PDF attachments; no signature,
approval bypass or submission was requested.

The older negative session did **not** recover through ordinary resume:
`trace-turn-45db9a99e4c7e3f3fe86f5c5` ended with a protected-continuation
error, and `/cancel_form` was not recognized while its intake stayed suspended.
This is negative legacy-recovery evidence, not another successful channel
test. Its four values remained intact and its uncertain task was never
replayed. Cleanup used the existing native `Cancel` API in an ephemeral
exact-source operator helper, with fixed synthetic owner/job/mode/source and
compare-and-swap revision 10. It canceled only that owned test job at
revision/ledger 11/4; no raw JSON patch, counter reset or historical-job
repair was used. Both test records now retain public metadata only.

The bounded privacy audit covered both synthetic oracles across two canonical
histories and metadata files, 22 outbound event files, owned interaction
records, the two public/encrypted job records, 43 selected retained traces
including exact-tool-correlated continuation/recovery surfaces, and 3,442
main-journal entries since 2026-10-19 03:55 UTC. All literal/sentinel counts
were zero. The journal scan was below its 4,000-entry/8-MiB bounds. Negative
and incomplete historical traces were not called completed runs; exact new
traces were inspected separately for schema, policy, source and truncation.
Explicitly private input/oracle/snapshot/artifact/backup surfaces were excluded.
This is case-specific evidence, not a global/provider-retention guarantee.

All 19 temporary snapshot state/key copies created during this work were
removed using exact guarded paths after value-free metadata was retained.
No active profile directory, config or verified rollback backup was deleted. Two old
generated build/dependency directories were separately archived and verified
byte-for-byte before removal to recover build headroom; those archives remain
recoverable in the operator's private working directory.

Encrypted-record fingerprints show the original Telegram incident unchanged
and exactly the two new synthetic job IDs. They do **not** show all seven
preexisting records unchanged: one other preexisting record was absent by the
final comparison. Its cause was not established by this audit; no values were
decrypted and it was not an explicit cancellation target. No claim of a global
unchanged-store snapshot or completed historical I-134 recovery is made.

## Remaining Boundaries

Use the [short Telegram scenario](../operations/pdfi4-free-text-form-test.md#natural-task-changes-without-canceling-the-form)
with fresh synthetic uploads. A WebSocket/local-path check is not a Telegram
upload, callback or standalone-reply pass. Historical uncertain tasks must not
be blindly retried, and extraction budgets must not be reset.

Full PDFI4 remains in progress. Complete applicable I-134 filling, conditional
and calculated XFA semantics, editable hybrid derivatives and the complete
large-form qualification matrix stay in their separately admitted lanes.
