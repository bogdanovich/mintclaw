# Skill Context Observation

The existing metadata-only `agent.turn.end` event log includes a bounded
commitment to the native `TurnEndPayload.SkillContextSnapshots` sequence:

| Field | Meaning |
| --- | --- |
| `skill_context_version` | Observation schema, currently `1` |
| `skill_context_count` | Original snapshot count, not a truncated subset |
| `skill_context_code` | `observed` for a complete bounded projection; `limit` otherwise |
| `skill_context_sha256` | SHA-256 of native typed snapshot JSON, present only when observed |

The digest preserves snapshot/selection order, sequence, trigger, skill names,
paths, scopes and revisions without logging those raw identities or skill
instructions. Nil and empty snapshot sequences normalize to the JSON array
`[]`. The native exported `SkillContextSnapshot` and `SkillRevisionIdentity`
types define the JSON representation; a changed identity produces a different
digest. This does not require enabling full event payloads or parsing rendered
Markdown in a canonical turn envelope.

Bounds are 16 snapshots, 128 combined skill-name/selection entries, 64 KiB of
source string bytes and 64 KiB of encoded JSON. Exceeding any bound emits no
partial digest. This projection does not change selection, prompt injection,
tool authority, event filtering or existing full-payload opt-in behavior.

`observed` means complete observation, not a successful skill invocation. A
consumer must independently bind the native turn/session/request and compare
the digest against the complete expected pinned selection. A zero-count
observation can witness an empty control; an absent event/hash cannot. Skill
execution still requires separate behavioral and no-skill controls.

This is a C3g prerequisite only. It adds neither a runnable skill-selection
eval nor live qualification or default promotion.
