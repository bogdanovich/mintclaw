# Prompt-Cache Diagnostics And Rollback

MintClaw uses the same provider-neutral request planner and cache-lineage
contract for gateway turns, interactive coding, `code exec`, resumed coding
threads, and task-scoped coding workers. Prompt-cache reuse affects cost and
latency only. A hit or miss must never change the messages, tools, recovery
semantics, or answer available to the model.

## Safe evidence

Enable metadata-only passive traces for a bounded canary:

```json
{
  "diagnostics": {
    "trace_capture": {
      "enabled": true,
      "content_mode": "metadata_only",
      "retention_hours": 24
    }
  }
}
```

With an empty `state_dir`, completed traces are owner-readable JSON below
`WORKSPACE/state/diagnostics/traces`. A `model.request` record contains only
the effective cache mode, hashes, counts, and boundary facts:

- `prompt_cache_mode`;
- `stable_prefix_version` and `stable_prefix_hash`;
- stable-system, tool-schema, historical-transcript, and dynamic-tail hashes;
- message and system-part counts; and
- whether a trusted dynamic-tail boundary was found.

A `model.response` record contains provider-reported usage truth:

- `cache_read_known` and `cache_read_input_tokens`;
- `cache_write_known` and `cache_write_input_tokens`; and
- `cache_outcome`, one of `hit`, `miss`, or `unknown`.

`unknown` is not a zero-token miss. It means that the selected provider or
adapter did not report cache usage. The fingerprints contain no prompt text,
session identifier, cache key, tool arguments, or credentials.

Inspect the ordered request and response evidence without printing other trace
content:

```bash
jq '
  .records[] |
  select(.kind == "model.request" or .kind == "model.response") |
  {sequence, kind, data: (
    if .kind == "model.request" then {
      prompt_cache_mode: .data.prompt_cache_mode,
      stable_prefix_version: .data.stable_prefix_version,
      stable_prefix_hash: .data.stable_prefix_hash,
      history_hash: .data.history_hash,
      dynamic_tail_hash: .data.dynamic_tail_hash,
      history_messages: .data.history_messages,
      dynamic_tail_messages: .data.dynamic_tail_messages,
      tail_boundary_found: .data.tail_boundary_found
    } else {
      cache_read_known: .data.cache_read_known,
      cache_read_input_tokens: .data.cache_read_input_tokens,
      cache_write_known: .data.cache_write_known,
      cache_write_input_tokens: .data.cache_write_input_tokens,
      cache_outcome: .data.cache_outcome
    } end
  )}
' TRACE.json
```

The structured runtime log for `agent.llm.request` exposes the same
`cache_mode` and `cache_*_hash` fields. `agent.llm.response` exposes the known
flags, counts, and outcome. Prefer passive traces for a complete ordered turn;
do not enable full prompt logging to investigate cache reuse.

## Canary expectations

Use a harmless session with a provider known to report cached input:

1. Send an ordinary first turn and wait for its final response.
2. Send a related second turn without changing model, provider, tools,
   instructions, or configuration.
3. Confirm both requests report `prompt_cache_mode: enabled` and the same
   stable-prefix hash.
4. Confirm the second response reports `cache_read_known: true` and a positive
   `cache_read_input_tokens` value.
5. Resume or restart the same session and confirm its historical fingerprint
   is unchanged.

Provider minimum-prefix and retention rules can still produce a legitimate
miss for a very small or old prompt. Repeat with a sufficiently large benign
conversation before treating a known zero as a MintClaw defect. Never treat a
cache hit as proof of answer correctness.

After compaction, expect one deliberate lineage discontinuity. The first
request using the new checkpoint may miss; the following ordinary request
should reuse the new stable prefix. Repeated lineage changes without another
compaction, model/tool/instruction change, or schema upgrade require
investigation.

## Provider-planner rollback

Set the shared rollback switch and restart the gateway:

```json
{
  "agents": {
    "defaults": {
      "prompt_cache_mode": "disabled"
    }
  }
}
```

The environment equivalent is:

```bash
MINTCLAW_AGENTS_DEFAULTS_PROMPT_CACHE_MODE=disabled
```

In disabled mode MintClaw removes its opaque `prompt_cache_key` and replaces
the internal provider cache plan with a non-serializing disable marker before
every ordinary, streaming-fallback, side-question, final-render,
vision-fallback, and Seahorse summarization call. This marker also prevents
native adapters from falling back to legacy message-level cache controls.
The same request messages, durable JSONL, Seahorse database, checkpoints, and
compaction generations remain in use. Providers with implicit caching or CLI
subprocesses may still report cache usage; disabled mode means MintClaw no
longer supplies explicit cache controls.

Run one metadata-only canary and confirm `prompt_cache_mode: disabled`. If the
incident persists, the cause is not MintClaw's explicit provider cache plan.
Restore `enabled`, restart, and run the two-turn canary to re-admit the planner.

Do not use `context_manager: none` as this rollback: it intentionally removes
stored conversational context from provider requests. If message ordering,
canonical history, or compaction correctness is in doubt, stop the affected
runtime and use the release's binary/data rollback procedure instead of
discarding context.
