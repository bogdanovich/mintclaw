# Durable Human Interaction

MintClaw can pause an agent turn or durable background task, ask the authorized
user for input, release runtime resources while waiting, and resume the exact
tool call after the answer arrives. Pending interactions survive process
restarts.

## Default Behavior

`request_user_input` is enabled by default:

```json
{
  "tools": {
    "request_user_input": {
      "enabled": true,
      "default_timeout_seconds": 3600,
      "max_timeout_seconds": 86400,
      "retention_hours": 168
    }
  }
}
```

The default wait is one hour. A model or trusted approval hook may request a
timeout from 60 seconds up to the configured maximum of 24 hours. Terminal
interaction records are retained for seven days by default and then become
eligible for pruning.

Set `enabled` to `false` to prevent new model-requested questions. Existing
records remain available for recovery, timeout, cancellation, and retention
cleanup.

## Asking and Answering

The model asks one bounded question per tool call. It can ask another question
after the previous answer resumes the task. Each question may accept free text
or offer two to three choices. MintClaw sends the prompt to the same routed
conversation and accepts an answer only from the recorded channel, account,
chat, topic, session, and sender.

The model owns every user-facing question, header, option label, and option
description. It writes them in the language and general style of the
conversation and includes enough context for the prompt to be answerable
without runtime-added prose. MintClaw renders that content directly. It does
not detect a locale, translate text, or make another model call after
suspension.

For one question, the prompt contains the model-authored content followed by a
compact command fallback:

```text
Which environment should be used?

• development — Local development.
• staging — Pre-production validation.
• production — The live environment.

`/answer 16131195 …`
`/stop`
```

The short interaction ID, option layout, and `/answer` syntax are runtime-owned
machine structure. A normal message reply remains sufficient.

On Telegram, each predefined option appears as a one-time inline-keyboard
button, followed by `⛔ Cancel turn`. The message composer remains available, so
you can always type any free-text answer, such as `generate it yourself`, even
when the model supplied options. Replying to the prompt strips Telegram's
quoted-message decoration before parsing the answer. The keyboard is removed
after answering or canceling.

For example, a completed single-question command is:

```text
/answer 16131195 production
```

An ordinary answer, including a negative answer such as `no` or
`/answer <short-id> no`, supplies that answer and resumes the agent. It does not
cancel the operation.

`⛔ Cancel turn` on Telegram or `/stop` on any channel terminates the pending
foreground turn or background task. MintClaw
durably records the cancellation, completes the suspended tool call with a
cancellation result, and does not resume the model. `/new`, `/reset`, and
`/clear` perform the same durable cancellation first, then continue with their
normal session change without emitting an extra stop acknowledgement.

## Background Tasks

Spawned and delegated durable tasks move to `waiting_for_input` while a question
or approval is pending. `task_status` exposes the safe short
interaction ID and bounded summary. Waiting does not publish task completion or
consume a completion ID.

After an authorized answer, the task returns to `running`, resumes once, and
uses the normal completion and delivery path. Restart reconciliation preserves
waiting tasks instead of marking them lost.

## Human Approval

Human approval is opt-in. A trusted tool approval hook can return:

```json
{
  "require_human": true,
  "action_summary": "Delete the production cache namespace",
  "timeout_seconds": 3600
}
```

`action_summary` is trusted presentation data and must be action-specific,
bounded, and free of secrets. MintClaw renders the exact runtime-owned tool
name and trusted summary without model-authored approval presentation or
generic tool arguments:

```text
filesystem_delete
Delete the production cache namespace

`/answer a1b2c3d4 allow_once`
`/answer a1b2c3d4 deny`
```

Direct `allow_once` and `deny` replies also work. The runtime binds approval to
the tool call and canonical argument hash, checks expiry, revalidates current
policy, and consumes the grant before execution. The model cannot create its
own approval authority or select the approving user.

For channel buttons, admission also matches the callback to the confirmed
platform message ID of the durable prompt. Short IDs and the channel's
in-memory control projection are never sufficient to grant authority.

An approval that expires before its one-time grant is consumed is definitively
not executed. Telegram removes the original inline controls when the
interaction becomes terminal. A button press is acknowledged immediately with
a neutral callback receipt; the agent then reports the exact status from the
durable interaction record. A late or repeated press cannot resume the agent or
execute the protected tool. If the action is still wanted, request it again so
the runtime can prepare fresh authority and issue a new approval. Do not
blindly retry an action whose approval was already consumed and whose execution
result is unknown.

## Restart and Delivery Semantics

Interaction state is stored at:

```text
<workspace>/state/interaction_registry.json
```

Workspace discovery records used during restart recovery are stored under:

```text
<mintclaw-home>/state/interaction_workspaces/
```

On startup MintClaw loads pending records, expires overdue requests, restores
task state, and resumes already-claimed answers. Duplicate or concurrent answers
produce one accepted claim and one continuation.

Remote chat APIs generally do not provide exactly-once publication. MintClaw
therefore does not resend a prompt or final response after an ambiguous send
window, which avoids duplicate user-visible delivery at the cost of reporting a
delivery-unknown failure.

## Debugging

Startup logs include `Loaded human interaction registry` with workspace,
record, nonterminal, retention, and load-error fields. Runtime lifecycle events
cover creation, delivery, waiting, answer claim, resumption, terminal outcome,
and cancellation without exposing raw answers or tool arguments.

Optional trace capture is debugging evidence only. It is not required for
interaction correctness, may be incomplete, and must never block interaction
progress, task reuse, pruning, delivery, or shutdown.

## Limits

- Only one unresolved interaction is allowed per canonical session.
- The runtime does not keep an LLM request or goroutine open while waiting.
- Timeout never chooses an answer automatically.
- Persistent approval allowlists and approve-for-session grants are unsupported.
- A channel must provide trusted sender and route metadata plus outbound
  delivery for durable interaction support.
