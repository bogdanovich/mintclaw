# PDFI4 Value-Independent AcroForm Schema

PDFI4 is not complete. One conditional-form transaction passed; the unrelated medium editable draft failed before
artifact publication. No medium-draft or I-134 completion is claimed.

## Proven Divergence

On deployed `bf58e20ef05529422e3066e5ee98ef78df619105`, medium job `form_job_342f34507ec48af634c2001d87c18346`
reached a current partial review and separate commit approval, then failed with `verification_structural_failed`.
Trace `trace-turn-d7f19f87fc6e00d50cc1ec1d`, sequence 4, records the closed failure. No PDF was delivered. The source
remained unchanged; the existing terminal policy erased the failed job's protected material. Do not replay its
approval, restore old state over live user data, or describe this attempt as a qualification pass.

An isolated native backend probe reproduces the failure with the exact immutable source and eight private oracle
values, without opening any canonical job. The candidate's field rediscovery returns `field_unsupported`, specifically
the `date_without_AA` refusal. pdfcpu v0.15.0 `form.extractDateFormat` first examines format actions, then guesses a
date type from `/DV` or `/V`. A plain text field becomes an exported date field solely because its supplied value
looks like a date. MintClaw correctly refuses a scripted date field without its admitted formatting action, exposing
the upstream value-based classification as a schema-instability bug, not evidence of a newly added script.

## Invariant And Bounded Fix

PDF field kind is structural, not inferred from personal values or defaults. Keep a plain `/Tx` field without actions
as text, including when its value resembles a date. Retain explicit, admitted date-format actions as date semantics.
Do not relax the action whitelist, allow new JavaScript, modify the source, change backend capabilities, or add
agency/field-name rules. Reuse a shared structural normalization at discovery, writing and candidate verification.
Ensure editing an already filled date-looking text field also follows text semantics rather than pdfcpu's heuristic.

Regress writing date-looking text, rediscovery, editing it back to ordinary text, date-looking defaults, genuine
formatted dates and refused actions. Run native/portable field/write suites and independent visible verification.
Then require normal exact-head CI/review/approval and merged deployment. Requalification must preserve the failed
attempt as evidence; a fresh post-fix transaction is not a recovery claim for its already erased terminal job.
