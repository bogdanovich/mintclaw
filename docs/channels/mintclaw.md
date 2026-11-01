# MintClaw Native Interaction Replies

An authenticated MintClaw WebSocket client can reply to a delivered interaction
prompt using the existing `message.send` envelope. Ordinary answers use the
wire-only `answer` choice:

```json
{
  "type": "message.send",
  "id": "fresh-answer-request-id",
  "session_id": "the-original-client-session",
  "payload": {
    "content": "Paper",
    "interaction_choice": "answer",
    "interaction_short_id": "the-observed-question-short-id",
    "interaction_prompt_message_id": "the-delivered-prompt-message-id"
  }
}
```

Content must be nonblank valid UTF-8, within the native interaction answer limit
of 16,384 Unicode code points. Outer whitespace is trimmed. Both identities are
required; the existing bounds are 64 bytes for short ID and 128 for prompt message
ID; ASCII spaces, tabs, CR and LF are rejected. Media cannot be combined with a projected reply.

The channel projects an ordinary response plus prompt identity, without inventing
an approval/control grant. Native interaction ownership still checks route,
sender, current record, delivered prompt and response validity before a claim or
continuation. Transport admission alone is not proof that the answer was accepted
or the interaction resumed. A caller must not retry an uncertain admitted answer.

Existing `allow_once`, `deny`, `cancel`, `clarify`, `back`, `skip` and
`not_applicable` choices retain their existing typed-control semantics. No
durable interaction registry, suspended call or session history is owned by the
channel adapter.
