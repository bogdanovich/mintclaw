# Automatic voice transcription

MintClaw transcribes incoming audio during native message intake, before the
conversation model runs. Select an enabled, audio-capable model alias with
`voice.model_name`. For example, an existing authenticated Gemini alias can be
used directly:

```json
"voice": { "model_name": "gemini-flash-lite" }
```

The alias must have a supported audio model (including
`gemini-3.5-flash-lite`) and its own valid provider credential. A working chat
OAuth token does not establish permission for OpenAI's separate transcription
endpoint. Keep credentials in the profile's existing security store.

If a configured transcriber fails, returns no text, or transcribes only some
attachments, MintClaw sends a retry/send-text notice and stops before the
conversation model or its tools. The same rule applies during active-turn
steering and deferred interaction intake. The inbound request is acknowledged
only after its notice is accepted for delivery. Protected audio answers retain
their existing behavior: the question stays open after failed transcription.
Profiles with voice transcription disabled keep their existing media behavior.

Before switching profiles, test synthetic English and Russian Ogg/Opus audio
against the intended provider, validate a copy of each config with `mintclaw
doctor`, and keep a rollback copy. Restart only the affected gateways, then
confirm their native transcriber is enabled and the runtime smoke test produces
a fresh diagnostic trace. Never replay private voice messages as a test or
disable shell tools globally to hide a transcription failure.
