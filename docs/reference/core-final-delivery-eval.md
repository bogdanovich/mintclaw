# Core Final Delivery Eval

`general.delivery.final-once` sends two scripted requests through one fresh
owned MintClaw gateway/session. The first requests a short streamed response
and supplies a synthetic code; the follow-up asks for that code without
repeating it. Limits: two inputs, two-minute turns, five-minute case, one
semantic judge call. PDF/browser scenarios are not involved.

## Native Evidence

The dedicated `final_delivery` capability requires `conversation` and
`bounded_calls`, native model metadata, a judge, Seahorse and no tools, workspace
inputs, model-user, MCP, hooks, fallback models or parallel turns. Model and
MintClaw channel streaming settings must match the case's explicit `streaming`
flag. Existing general profiles and historical case digests are unchanged.

The wire observer retains the actual `message.create`/`message.update` type.
Each request must have exactly one final, one physical message ID and one
complete native turn scope. Revisions must update that message, not create
another one. The second request must have distinct request/message/turn IDs
but the same native agent/session/workspace. Streaming requires a real
nonterminal revision before the first final; fluent ordinary finals do not
satisfy that requirement.

A received wire final is not yet a durable delivery receipt. Before sending
the follow-up, the observer polls the existing passive outbox reader for at
most one second, within the turn deadline. It retains the actual delivered
receipt. It does not create an outbox, mark delivery, recover state or sleep
as a substitute for settlement.

After clean owned shutdown, the shared core evidence owner freezes canonical
history and the complete bounded outbox census. Required evidence includes:

- Exactly two native delivered final intents for this trial, one transport attempt each, with
  unretired payloads and correct request/session/agent/workspace correlation.
- Each native platform message ID and turn scope matches the corresponding
  wire final. Both observed settlement receipts remain in the terminal census.
- Four clean canonical messages: user/assistant/user/assistant, no tool calls,
  no skipped or summarized history, and the supplied code in the second answer.
- Canonical assistant text equals pre-render outbox text; wire text may append
  the channel footer, which cannot replace canonical content or recall proof.
- Known sealed target budget, complete private-surface census, clean owned exit
  and untruncated logs, before any judging.

Repetitions have distinct sessions but share the owned gateway and its outbox.
Retain and validate the complete census; unrelated trial receipts are not
duplicates of this trial. A same-client receipt under another native session
cannot be ignored as an unrelated trial.

Missing evidence stays non-passing. The required judge sees eligible visible
conversation only and cannot override a failed native check. Offline `grade`
validates frozen receipts/history and run-scoped evidence before calling the
judge; it does not reopen live stores or replay target requests.

## Current Streaming Boundary

The native gateway enables the durable inbound spool and outbound coordinator.
Inbound turns therefore carry an outbound transaction. In
`pkg/agent/pipeline_streaming.go`, `configuredStreamingEligible` explicitly
refuses such a transaction with reason `durable_outbound_transaction`.
Channel streamers otherwise publish directly, outside durable final admission.

Consequently this repository scenario remains **pending/non-default**. There
is no paid streaming qualification or claimed streaming pass. This eval does
not remove the guard, disable the spool, substitute agent CLI/direct mode, or
count a normal delivered final as streaming. A future streaming change must
preserve native durable final ownership and stable message identity; it is a
separate runtime design decision, not an unbounded eval prerequisite repair.

The harness also supports an explicitly defined `streaming: false` case. The
model-free real-CLI control uses that variant to exercise native delivery,
settlement, history, cleanup and frozen offline grading across three distinct
sessions in one owned gateway. On Linux this combined native control passed in
6.94 seconds, including sixteen frozen negative and five global-unknown checks.
Its eight target calls and six judge calls are local synthetic HTTP responses,
not paid model qualification. A separate native
control with `streaming: true` verifies refusal before judging. Synthetic
revision-parser controls test create/update semantics but are not evidence
that the gateway streamed.

Coverage is one owned MintClaw client connection and its retained native
delivery census, not Telegram/Slack transport, reconnection replay, arbitrary
future deliveries or exactly-once remote-network effects. Complete census
bounds remain 128 files and 2 MiB; canonical history uses native passive bounds.
