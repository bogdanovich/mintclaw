# Local Coding Agent P2.6: Native Turn Across Restart

The `mintclaw code <prompt>` command now runs the request through MintClaw's
own coding-profile `AgentLoop`. A prompt-bearing `mintclaw resume <thread-id>`
reopens the same external thread state and runs a follow-up through a newly
constructed loop. Neither path launches Codex or another coding-agent
subprocess.

## Command and runtime ownership

The coding command remains the owner of thread admission:

1. resolve the canonical project and external coding store;
2. validate and provision metadata, then acquire the thread lease;
3. publish metadata so a startup or provider failure leaves a discoverable
   thread;
4. hand one prompt to the native runtime adapter while holding the lease;
5. persist the effective model/provider selection and render the response;
6. close the loop and release the lease.

The runtime adapter owns only one turn. It loads configuration, resolves the
thread's persisted model selection, constructs exactly one `main` coding agent,
binds that agent to the thread's coding runtime layout, and calls
`ProcessDirect` with the canonical `coding:<thread-id>` session key. This keeps
the CLI outside provider, tool, prompt, journal, workspace-refresh, and crash
repair internals.

Coding continuation always uses the derived Seahorse context index owned by
the thread layout, even when the personal-agent configuration selects `none`.
It does not inherit a custom personal database path. This is what makes prior
decisions available after reopen while canonical JSONL remains authoritative,
and keeps one disposable SQLite file per coding thread instead of one global
database.

`--model` is a user-facing model alias from `model_list`. Its selected provider
is stored in thread metadata after admission. A later `--model` replaces both
parts of the selection so the new model's configured provider is resolved
rather than inheriting the old provider accidentally. The per-turn config is a
copy and does not mutate the loaded process configuration.

## Durability and failure semantics

The native pipeline appends the accepted user message before its first
provider call. The command checks the canonical session after the turn instead
of assuming that every returned error happened before admission.

- If configuration or runtime construction fails before prompt admission, the
  small thread descriptor still exists and can be inspected or resumed.
- If the prompt was admitted and a provider/tool/output step then fails, the
  error carries the existing committed-prompt warning and the exact
  `mintclaw resume <thread-id>` recovery command.
- Tool intent, side-effect start markers, terminal results, and startup repair
  continue to use the P2.5 canonical journal contract.
- A successful response is printed by the plain renderer and included in JSON
  output without terminal control sequences.

The command lease prevents another process from mutating the thread while the
native loop is active. Closing the command closes the loop; a later resume
constructs a new loop over the same per-thread JSONL state.
The short-lived plain command suppresses post-turn background compaction so it
does not close Seahorse underneath newly scheduled work. Dedicated compaction
lifecycle and foreground waiting remain later roadmap work; normal history
assembly and JSONL-to-Seahorse reconciliation remain enabled.

## Deterministic evidence

The command-level restart fixture uses the scripted provider harness but the
real prompt builder, coding tools, session backend, workspace observer, and
agent pipeline. Across two separately constructed loops it verifies:

- the configured provider/model selection and coding-only prompt identity;
- `AGENTS.md`, project/cwd/thread/trust context, and a clean Git snapshot;
- real `read_file`, `apply_patch`, `exec`, and `write_file` definitions and
  execution;
- repository refresh from clean to dirty after the patch;
- a passing `go test ./...` command;
- prior assistant decisions and correlated tool history after reopen;
- a second repository edit, durable user journal, and plain final output;
- an injected provider failure leaving metadata and the accepted request
  available for an explicit resume.

This packet deliberately does not add the terminal UI or long-session
compaction. Those remain P3/P4 concerns, while P2.7 measures the quality and
edge behavior of the underlying tool contracts.
