# Core Skill Selection Eval

`general.skills.selection` runs three scripted inputs in one fresh owned native
gateway/session: an unselected control, explicit `/use eval-synthetic-format`
selection, then another unselected control. The public synthetic skill uppercases
`cedar` into `EVAL_SKILL[CEDAR]` for its selected turn only. Controls return
`cedar`. Limits are three inputs, two-minute turns, a five-minute case and one
semantic judge call. This is not automatic skill discovery or installation.

## Admission

The dedicated `skill_selection` profile has exactly `conversation`,
`bounded_calls` and `skill_selection`, native model metadata and a judge.
It pins one `skills/<name>/SKILL.md` workspace input to the corpus fixture's
SHA-256. No scripts, extra workspace inputs, tools, MCP, hooks, fallback models,
model user, request-user-input, parallel turns or streaming are admitted.
Seahorse preserves the native session/history; configured always-on skills are
refused because they would invalidate the controls.

Only metadata-only `agent.turn.end` logging is enabled at info level for this
profile. Public configuration policy inspects decoded channel settings without
opening credentials; actual runtime admission separately requires the native
resolved channel auth. No public-preview token can authenticate a runtime.

## Independent Evidence

After clean owned shutdown, the shared core evidence owner captures all facts
before allowing a judge:

- The pinned skill file has the same hash and single-file directory census
  before and after execution.
- Canonical history has exactly six clean user/assistant messages, the original
  native session identity, no tools, skipped history or summary. `/use` is
  normalized by the native command handler, not by a replacement command engine.
- The three native turn-end records bind the actual input request IDs to the
  observed native turn/session/agent. Each reports one completed LLM call.
- The complete [typed skill context digest](skill-context-observation.md) is
  empty for both controls and exactly the pinned name/path/workspace-scope/file
  revision for the selected turn. Expected JSON uses the native exported
  snapshot/identity types, not parsed Markdown or model assertions.
- Canonical answers match the selected rule and both controls. Three delivered
  final intents match those canonical answers and wire/native turn identities.
  The complete bounded outbox may include other repetitions' distinct sessions.
- Native call-budget usage is known/sealed, private-surface coverage is complete,
  and the owner exited cleanly without a truncated log.

Missing, limited or mismatched evidence cannot be replaced by a judge pass.
The metadata digest does not expose raw skill identities/instructions in logs;
the private report still retains synthetic canonical history and bounded native
observations. This is not a hostile-filesystem attestation or proof of tool use.

Offline `grade` validates the frozen file/history/log/delivery observations
before judging. It neither rereads live skill/session/outbox/log evidence nor
replays target requests. Credential/role authority remains independently checked.

## Validation And Status

The model-free real-CLI control runs three distinct sessions in one gateway.
Its synthetic provider transforms only when the **current** user request
actually carries the injected pinned revision; historical envelopes do not
select the current turn. It checks eighteen frozen negatives, five run-wide
unknowns and successful offline grading after deletion of live stores, logs
and SKILL.md. Missing frozen proof is refused before another judge call.

These controls use local synthetic HTTP, not a paid model or a live semantic
qualification. The repository case remains **pending/non-default** until its
separate bounded qualification and immutable three-trial batch succeed. Existing
six General defaults, historical case digests and deferred PDF/browser policies
are unchanged. A full repeat-three target budget needs at least nine calls;
ceilings also bound reported tokens, request bytes, output and concurrency.
