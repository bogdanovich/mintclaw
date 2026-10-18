# Bounded PDF intake clarification repair

## Status and scope

Both code changes merged and the final code is deployed. This checkpoint
qualifies the bounded generic repair, not the PDFI4 exit or I-134.

The [bounded goal](pdf-intake-clarification-goal.md) follows the negative
continuation in #1726. Four new decisions were accepted while older input
exhausted its budget; ordinary prose incorrectly reported failure of the new
answer and repeated resolved questions. Historical inputs were not reset.

## Merged implementation

[PR #1730](https://github.com/bogdanovich/mintclaw/pull/1730) merged as
`67863aed9485290364de14c98f6a56c6845e41ff`, after exact reviewed head
`b1e8a57e445163a443b7c581b10768f95789b9e4` passed all 13 applicable CI checks.
Frontend was skipped by existing path policy. A clean substantive review,
authorized owner PR-level rocket and clean mergeability preceded normal merge.

[PR #1736](https://github.com/bogdanovich/mintclaw/pull/1736) then removed a
redundant receipt-selection condition so explicit retry of the old input uses
the same native chronology proof. It merged as
`95c58f41b18e258e404933283491574c14c6029f`, after exact head
`0148c959faaf046d11008d95e81ab0a817f2187e` passed ten applicable CI checks;
Frontend/PDFium were policy-skipped. Clean review, owner rocket and normal
match-head merge again preceded deployment. No new state/authority was added.

- Native protected reads derive completed candidate groups from existing
  committed pass outcomes. Automatic continuation of older input skips an
  exact completed group without findings; unvisited/failed groups and known
  blockers remain eligible. Explicit selected-receipt retry retains its trusted
  identity and the original persisted 32-pass limit.
- Progress distinguishes this message's accounting state, newly accepted or
  acknowledged decisions, retained native findings and processing failure.
  A retained failure does not undo successful field events.
- Confirmation alone cannot hide a question. Only older advisory uncertainty
  superseded by a strictly later, still-current native user decision for the
  same field can be omitted from the public question projection. Existing
  native event identity and revision checks supply that evidence; no new
  persisted proof registry is introduced.
- Current ambiguity, pre-existing confirmation, unrelated later updates and
  actual invalid/conflicting facts remain visible. Presentation filtering does
  not consume text, settle native findings or grant review/output authority.

There is no form-specific production rule, new planner/store/service, limit
increase, historical input rewrite, new PDF backend or XFA script execution.

## Executable qualification

The unchanged-window regression failed before the fix on independent generic
13-field and 193-field schemas. Exact reviewed #1730-head Linux intake tests passed:
document 92.575s, tools 57.351s, agent 4.204s. Focused races passed: document
4.190s and tools 134.851s. Timings are shared-host observations, not benchmarks.

Tests cover partial acceptance alongside exhausted older input, truthful
accounting status, canonical value preservation, advisory-versus-conflict
projection, genuinely new ambiguity, unrelated later updates, current-event
provenance, retry/replay/restart, fixed budgets, owner denial and protected JSON
serialization. Native pending input still fences review/output.

Formatting, documentation validation and test-inclusive repository pre-push
lint passed without weakening policy. The initial head had an existing
session-concurrency timeout in CI Race, not a data-race report; that exact test
passed five independent race repetitions, and the final head's complete CI was
green. Some local macOS test launches stalled before Go runtime entry and were
not counted as passes; the final-head macOS CI gates passed.

The explicit-retry regression failed against the deployed #1730 source for
both advisory codes while keeping a genuine conflict visible. A controlled
Go compiler overlay left all underlying source and canonical jobs unchanged.
With the one-line correction, the full tool-intake selection passed in 34.421s
and focused races in 21.484s. The follow-up changed one production predicate
and added 15 test lines; current-branch exact-head CI qualified the whole branch.

## Deployment and live evidence

The final deployment identifies full source
`95c58f41b18e258e404933283491574c14c6029f` in the actual main process's Go
build metadata, with `vcs.modified=false`. CLI and running inode SHA-256 agree:
`d7d4330c49a794cc30e0771dc5c4b6e92bbf373a5ba6bfb843563bcce943933a`.
Tests from the final merged staging source passed: document intake 40.416s,
tools intake 30.131s and agent intake 2.212s.

Backup `/home/server/mintclaw-clarification-followup-backup-20261018T091819Z.vEq29o`
contains a 3,163,072,324-byte compressed stopped-main archive, seven verified
binary copies including the actual running inode, five configs and systemd
units. Archive and binary checksums passed. Independent coding workspaces were
excluded and left running. The old main exceeded its shutdown deadline and
systemd forced it to stop; this was **not a clean shutdown**. No mutable-state
rollback or historical-job repair occurred. Rollback must target the affected
binary/config layer; restoring mutable data after new writes requires a
separate comparison and explicit recovery decision.

All ten expected services were active with zero failed units, legacy processes
or ten-minute error-level journal entries. All five configurations loaded with
zero error findings and remained byte-identical. Existing doctor policy
findings (main/family/nutrition/reviewer/spouse: 14/7/7/8/9) were not weakened.
The disk had 9.8 GB free after deployment and 9.7 GB at final health check
(96% used); no backups or unrelated caches were deleted.

The exact-marker stateless smoke completed on the final source with no tools:
`trace-turn-8a5e19356c9ae0814c943ca5`, six records, complete/untruncated,
redacted-content capture with `mintclaw.config_filter.v1`.

### Same-job generic clarification

An authenticated headless gateway session used the unrelated three-page
equipment-loan AcroForm (20 fields, 19 writable), source SHA-256
`ca3ea05ef03e6356ab5ea3ab8170d6712fcf6c60b9198de16d1b5db17efeaeff`.
This is **not Telegram ingress**. The protected mode, exact native owner,
source and schema were verified before sending fresh private synthetic facts.
Native readers opened isolated copies only, never the canonical store.

The initial ordinary paragraph accepted two role-specific names while retaining
an unassigned telephone. Native revision/ledger were 3/2 with two exact private
oracle matches and one 171-byte input at three passes. That same job retained
both values and the pending input across the actual final deployment/restart.

A single later message assigned the previously supplied number to the borrower
without repeating it, explicitly left the emergency-contact number unspecified
and supplied a new city. Native revision/ledger became 5/4: four exact text
matches, no mismatched expected value and no unexpected positive assignment.
The emergency-contact number remained unfilled. The old input was preserved
at 171 bytes, five passes and one remaining native ambiguity. The ordinary
reply asked only for the genuinely missing emergency-contact telephone, not
either accepted name or the resolved borrower number. This proves preservation
and clarification, not a completed form or consumed pending input.

The final-source continuation trace
`trace-turn-ce19c88bf9683ffbd0f8e7a5`, sequence 8, reports two new accepted
assignments, `received_input_state=accounted`, one retained finding and one
pending input. It completed with 15 untruncated records. The ordinary answer
did not falsely report failure of the new message; it omitted an explicit
acceptance count, so counts above are native evidence, not a prose claim.

Cancellation trace `trace-turn-1ad2db8eb0a864f65cdd39d0` completed on the same
final source with 22 untruncated records. Native state became canceled at
revision/ledger 6/4, with no active intake. The retained record has only public
metadata and its integrity digest: wrapped key, source, events and intake were
erased. Zero matches after terminal erasure are expected cleanup, not loss of
accepted values during collection. No PDF was created or delivered in any of
the four test messages; the original source hash remained unchanged.

Before cancellation, three actually accepted private synthetic sentinels had
zero hits across canonical metadata/history, 22 derived messages, 16 tool
parts, three complete matching traces, public/encrypted records and the main
journal since job creation. After cancellation the same scan passed across 28
derived messages, 20 tool parts and four complete matching traces. There were
no derived summaries in this session. This is a bounded case-specific check,
not a claim about provider retention or unrelated historical content.

Both historical I-134 encrypted-record fingerprints remained byte-equivalent
under canonical JSON hashing: revisions/ledgers stayed 47/45 and 36/35.
Their counters, retained inputs, state and retention were not repaired or reset.
The synthetic job is separately identified as
`form_job_40d523e99358621fbad098c960bb9d32`; it is not recovery evidence for
either historical job. Actual Telegram acceptance remains pending.

## Remaining boundaries

- Exhausted historical input remains protected and bounded. Recovery requiring
  new authority needs separate admission; a new job is not recovery evidence.
- Conditional/calculated XFA and editable hybrid derivatives remain separate
  work in the [backend follow-up](pdf-conditional-calculated-fields-roadmap.md).
- Full I-134, actual Telegram ingress and the broader PDFI4 acceptance/exit stay
  open. The [operator scenario](../operations/pdfi4-free-text-form-test.md#bounded-clarification-repair-check)
  is the manual follow-up, not an assertion that the channel test passed.
