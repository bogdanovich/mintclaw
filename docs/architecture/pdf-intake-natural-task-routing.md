# Protected form task routing

This implements the routing portion of the
[bounded follow-up contract](pdf-intake-natural-task-routing-goal.md). It is
not full PDFI4, I-134 or XFA qualification.

## User behavior

While a form is collecting facts, normal text can either supply more facts or
ask for another task. The latter preserves the draft; `/cancel_form` is not
required to leave it. Cancellation still means explicit deletion of that job.

An attachment with unclear purpose asks whether to start a new task or continue
the old form without the new attachment. The existing protected question UI
offers two choices, free text and cancellation. Choosing a new task uses the
already attached file; it does not ask for a second upload. Continuing retains
accompanying text in protected field intake. Canceling this question discards
only its pending new task, not the original form.

A later clarification identifying the person for an earlier telephone number
and adding a city remains part of the original form. It is not a task switch.
Only native field decisions establish whether either fact was accepted; task
classification never assigns a field or approves an output.

## Ownership and persistence

There is one source of truth: the existing encrypted `FormJobStore` source.
Task messages and protected question replies live there until linked into
existing field intake or released once as a new task. No second value store,
service, task stack or approval path is introduced.

Trusted ingress binds the exact authenticated owner/route, job, mode, message
identity and owned media before ordinary model execution. A tool-free private
classifier sees only the current request, a bounded value-free role/question
and attachment count, never an old form's values or media authority. A clear
switch requires two separately reserved agreeing whole-request interpretations.
Uncertain/mixed scope or unavailable interpretation asks the operator instead.
Question choices use the existing registry's stage/commit claim boundary;
free-text scope selection requires the same independently checked evidence.

Ordinals and question context are captured at ingress, not when interpretation
finishes. Promotion links the original receipt atomically, preserving order
even when a later clarification was admitted first into field intake. Existing
field limits and extraction counters are not reset or expanded.

Routing allows four pending messages and 64 KiB of private text. Each request
uses at most four private reservations; each question reply at most two. Four
free replies leave one canonical-choice slot. Canonical protocol choices use
bounded answer slots, not the private-text allowance, so a full text buffer
does not disable the buttons. Expiry discards only unclaimed reply staging;
the original pending request remains. Refreshed questions reject stale tokens.

## Handoff and recovery

A native grant suspends the old draft without canceling it or replacing its
source. Only the newly authorized text/media enters existing live-only model
context. Canonical tool history, progress JSON and diagnostic previews receive
opaque routing metadata. Prior form values and unrelated question replies are
not copied into the new task. The whole handoff must fit the remaining live
context allowance; otherwise it stays retained before grant.

A grant is at most once. If execution crashes after grant, recovery reports
uncertainty; it never releases the payload again or repeats external effects.
Resuming the exact old job uses the existing `form_action=resume` and exclusive
route ownership check. Retained, known same-form replies are processed before
ordinary collection continues. Another active form must first yield the route;
this is not automatic multi-task stack management.

`/retry_form` reuses native retained input and existing budgets. It is not a
request to repeat facts or upload a file again. Unknown processing failures are
not described as temporary; closed provider/context/response categories from
the earlier diagnostics fix remain authoritative.

## Qualification boundary

Unit/integration coverage includes RU/EN task scope, claim-bound buttons and
free text, restart/replay, stale questions, capacity, original input order,
source preservation, owner rejection, live-context capacity and privacy.
Deployed gateway/model and operator Telegram results must be recorded separately;
neither this document nor a mock establishes a real Telegram pass. Historical
Telegram and I-134 jobs are not replayed or altered for qualification.
