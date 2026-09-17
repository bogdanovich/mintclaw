# Local Coding Agent P7.1: Non-interactive Execution

Roadmap packet: [P7.1 — Non-interactive `code exec`](local-coding-agent-roadmap.md#p71--non-interactive-code-exec).

## Command contract

P7.1 adds one non-interactive frontend over the existing native coding
controller and durable thread store:

```text
mintclaw code exec [prompt] [--model <model>] [--attach <path>] [--json]
mintclaw code exec resume <thread-id> [prompt] [--model <model>] [--attach <path>] [--json]
```

Both forms run exactly one turn. The first creates a durable project-bound
thread; the second acquires the existing thread's single-writer lease and uses
the same strict project-location and resume-recovery checks as the interactive
frontend. A prompt or at least one attachment is required. Coding mode remains
trusted-local by default; P7.1 does not add an approval subsystem.

Plain mode writes only the final assistant response to stdout. It does not
print the MintClaw banner, TUI frames, terminal control sequences, or progress
cards. This makes command substitution and simple scripts predictable.

`--json` writes UTF-8 JSON Lines to stdout. Every line is one complete object
with `schema_version: 1`; startup logs and human formatting never enter stdout.
Consumers must ignore unknown object fields and may reject an unsupported
schema version.

## JSONL lifecycle

The first successful setup event is `thread.started`. It identifies the durable
thread, canonical session key, project root, invocation directory, model,
provider, and whether the thread was resumed. `turn.started` follows after the
controller admits the prompt.

Bounded semantic frontend items are emitted as `item.started`, `item.updated`,
or `item.completed`. Each event contains the current revision of one item:

- assistant commentary, final answers, and reasoning carry bounded `text`;
- tool calls carry their typed status, arguments shape, bounded output,
  command observation, duration, truncation, and verified write audit;
- plans carry typed steps and statuses;
- compaction markers carry their correlated lifecycle and bounded metrics;
- terminal work boundaries carry their truthful outcome and duration; and
- warnings and errors carry bounded text.

Coalescing may cause an item first observed in a terminal state to appear only
as `item.completed`; consumers must not require a preceding start event. Item
IDs and revisions make replacement state idempotent.

`turn.completed` is the successful terminal event and includes the bounded
context-usage observation. `turn.failed` carries a stable failure category.
Failures before a thread or turn can be admitted use a top-level `error` event.
No event stream is synthesized from logs or parsed model prose.

The headless frontend emits either terminal event only after the controller's
turn-settlement barrier completes. This includes runtime post-turn checks and
durable metadata persistence, so a caller cannot observe success and then lose
a late persistence failure during process shutdown.

## Exit status

| Code | Category | Meaning |
| ---: | --- | --- |
| `0` | success | The turn completed normally |
| `1` | `model_failure` | Provider, model, runtime, or unclassified turn failure |
| `2` | `invalid_project` | The current or retained project location cannot be admitted |
| `3` | `tool_failure` | The terminal failed snapshot contains a failed tool |
| `4` | `context_failure` | Context overflow or terminal compaction failure |
| `6` | `suspended` | Durable work is waiting outside this one-shot process |
| `130` | `interrupted` | SIGINT, SIGTERM on Unix, or caller context cancellation |

A failed tool that the agent handles before completing the turn does not make
the process fail. The tool event remains visible, while the terminal turn and
process status stay successful. Classification uses typed provider errors and
the authoritative terminal frontend snapshot rather than matching arbitrary
error strings.

## Ownership and shutdown

The headless frontend subscribes to the same bounded `ThreadSnapshot` stream as
the TUI. It does not open another agent loop, event bus, transcript, database,
or writer. On interruption it requests controller interruption, then closes the
controller with a fresh bounded context. Controller shutdown owns active-turn
cleanup, runtime closure, durable metadata completion, and lease release.

P7.1 does not add a daemon, background queue, gateway dispatch, paired-node
routing, remote filesystem authority, automatic Git publication, or a new
session format. Those require later roadmap admission and reuse this command's
thread, event, cancellation, and exit contract rather than bypassing it.
