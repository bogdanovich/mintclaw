# PDF Background Draft Routing

## Scope and architecture checkpoint

A form draft must not turn chat into a mandatory questionnaire. Text-only
conversation can proceed independently, while later form details still use the
same authenticated, protected draft. This is distinct from selecting another
document or replacing the active form workflow.

This routing change is independent of PR #1867's candidate-exclusion fix. Together
they implement the bounded background-draft goal recorded in PR #1867 at
`docs/architecture/pdf-intake-background-draft-goal.md`.
Neither admits full PDFI4, I-134/XFA completion or a general task stack.

The checkpoint compared a prompt-only switch change, a new ingress classifier
with its own journal, a generic focus/task service, and extension of the existing
native route model. The first leaves two lifecycle bugs; the next two duplicate
authority or change durable privacy boundaries. The chosen model extends native
routes and replies, reusing encrypted source, reservations and live handoff.

## One owner for each boundary

- Durable ingress binds authenticated input identity, owner, route and media.
- The encrypted form source owns admitted text, draft/source identity, revision,
  immutable admission order, private decisions and one-shot handoff.
- The interaction registry owns the visible question and answer/resumption.
- Existing live tool context owns a freshly released request for its current
  turn. Canonical user input, ordinary tool history, journals and traces retain
  receipts, not the released raw input. This is not a second chat transcript.

There is no new service, queue, value store, registry, mutable focus flag or
language/form-specific classifier. No model response can assign a field, approve
an output or authorize an external action through routing.

## Routing decisions

`continue_form` retains form facts, corrections, role clarification, questions
and return requests in protected intake. A later role clarification need not
repeat an earlier value.

`side_task` covers a whole text-only independent message: ordinary conversation,
check-ins and unrelated requests do not require explicit task-switch words. Two
separately reserved private decisions must agree on the exact whole text at the
same current revision. A native once-only grant releases only that message.
The form stays capturing; ordinary form return does not require a mode command.
Media cannot be released by a root `side_task` decision.

`switch_task` preserves the explicit replacement-task behavior. A distinct
document/attachment request requires its exact existing authority. The old draft
is suspended, not canceled, and can be resumed by its exact identity. This also
covers replacement documents selected by a text path rather than attached media.

`ask` remains appropriate for genuine uncertainty, mixed form facts and another
task, or unclear attachment purpose. Private failures remain protected. A
text-only question does not claim a nonexistent attachment.

Prior conversational focus is derived from earlier settled native admission
order and supplied privately as a boolean hint. It contains no prior values or
request text and cannot become assignment or release authority. Conversational
follow-ups still need current-message interpretation and the normal evidence
and revision fences.

## An open question is not consent for everything

The exact visible scope-question identity is durably frozen before admitting a
bare reply. Existing canonical choices still select the original request. Free
text may instead be form facts, unclear purpose, or an independent message.

For an independent reply, two private proofs authorize a separate reply-only
handoff. The original route stays pending with its original text/media, without
setting its consent-answer identity. The registry can finish the answered
interaction; that does not grant the original native request. Replay returns no
reply text or media. A successful protected form continuation does not
automatically reopen a different retained scope question. Retained uncertainty
remains inspectable/retryable rather than being deleted or implicitly accepted.

## Safety, limits and rollout

Owner/topic/source/schema/revision/replay fences, extraction budgets, retained
byte/receipt/pending capacities and generic approval behavior remain unchanged.
Stale, disagreeing or invalid interpretations cannot release either payload.
Concurrent grant callers have one fresh winner. A crash after a grant is an
uncertain handoff, not permission to replay external effects.

Older strict readers reject the new private action rather than treating it as
old switch consent. Deploy with a verified backup; do not automatically downgrade
a binary against mutable source written by the new version. Forward repair or an
explicitly compared data restore is required if rollback would cross this bound.

Implementation tests and live qualification evidence must be recorded separately.
A gateway/model test is not proof of a Telegram callback/upload pass. Existing
operator drafts and historical I-134 jobs are read-only; qualify on fresh owned
synthetic sessions and clean up only those sessions and temporary key copies.
