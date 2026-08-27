# Local coding agent P5.5: compaction experience

Status: implemented

This document records the user-visible contract for coding-session compaction.
It complements the compaction lifecycle and policy documents; it does not add
a second TUI-owned compaction state machine.

## Ownership and data flow

Seahorse remains the source of compaction results. The agent runtime publishes
one correlated typed lifecycle, the frontend projector retains its latest
bounded state, and terminal surfaces render that state:

```text
Seahorse -> agent lifecycle event -> frontend projector -> TUI footer and /status
```

The lifecycle reports:

- trigger and background/blocking ownership;
- context tokens observed before and after the attempt;
- tokens saved and leaf/condensed summaries created;
- attempt duration; and
- running, progress, completed, no-progress, interrupted, or failed status.

Token counts are explicitly marked unavailable when either observation fails.
An interrupted attempt uses a short, cancellation-independent final
observation so reporting cannot indefinitely delay the terminal lifecycle.

## Interaction contract

Background compaction never claims foreground activity. The status footer and
`/status` identify it as background work, while the composer remains usable.

Blocking compaction identifies that the current turn is waiting. A failed or
interrupted blocking attempt says the current turn may stop. The equivalent
background result says work can continue. Completed and no-progress attempts
always converge on one terminal `LastCompaction` result, including manual
`/compact` requests.

The compact footer is deliberately concise. `/status` provides the complete
metrics, continuation guidance, and recommends `/new` when repeated
compactions or a changed objective make a focused thread preferable.

## Verification

Regression coverage establishes that:

- Seahorse reports consistent before/after token observations;
- the adapter preserves all metrics and correlation fields;
- foreground and background results have distinct language;
- failure guidance states whether work can continue;
- manual compaction produces a single terminal result; and
- a prompt can still be submitted while background compaction is running.

