# Local Coding Agent P3 Exit Record

Roadmap gate: [P3 — Terminal event and control plane](local-coding-agent-roadmap.md#p3--terminal-event-and-control-plane).

The merge containing this record completes the four ordered P3 packets. A
headless frontend can now drive the native coding runtime through a bounded,
transport-neutral event and control plane. The interactive terminal
application remains required P4 work; this record does not claim that the
current plain renderer is the final coding UI.

## Completion evidence

| Packet | Merged change | Contract evidence |
| --- | --- | --- |
| P3.1 | [#726](https://github.com/bogdanovich/mintclaw/pull/726), merge `1add7d3e` | The typed projector owns authoritative bounded snapshots and revisioned deltas for turns, tools, retries, fallback, compaction, interruption, failure, metadata, and verified write audits. Ordering, redaction, dropped-delta recovery, and slow-consumer tests keep runtime payload types and sensitive diagnostics out of frontend clients. |
| P3.2 | [#742](https://github.com/bogdanovich/mintclaw/pull/742), merge `6417d7de` | Accumulated answer and reasoning streams, non-streaming final content, context usage, and compaction lifecycle converge through one projection. Unicode bounds, duplicate-final suppression, fallback ownership, watcher overflow, and snapshot resynchronization are covered under race tests. |
| P3.3 | [#744](https://github.com/bogdanovich/mintclaw/pull/744), merge `3f69d080` | A single-writer controller owns one native runtime and thread lease while serializing submit, graceful interrupt, hard cancel, compact, and close. Headless native integration proves asynchronous turns, streamed snapshots, retained busy prompts, cancellation, foreground compaction, idempotent close, and lease reacquisition without importing TUI packages. |
| P3.4 | [#747](https://github.com/bogdanovich/mintclaw/pull/747), merge `d4311a6c` | Coding-only tool observations carry bounded stdout, stderr, combined background output, truncation, session, cancellation, timeout, status, and exit-code state without parsing model-facing prose or entering runtime logs. Successful file-kind write audits alone create bounded, deduplicated changed-file events. Tests cover non-zero background exits, revision-safe ownership, delta convergence, and timed-out hook isolation for observations and verified audits. |

Each packet passed the repository's nine-check CI matrix at its final reviewed
head. Confirmed reviewer findings were fixed with regression coverage and
resolved before merge.

## Exit decision

P3's exit statement is satisfied:

- `frontend.Projector` is the bounded read model; canonical transcript,
  journal, and tool-result persistence remain outside it;
- snapshots and correlated replacement deltas converge after dropped,
  coalesced, expired, or slow-consumer progress, rather than making the UI
  infer missing state;
- streaming and non-streaming provider paths produce the same final transcript
  semantics without duplicate answers or invalid UTF-8;
- the controller runs blocking agent and compaction work outside the UI loop,
  keeps one mutation owner, and makes interruption and second-prompt behavior
  explicit;
- tool cards can render bounded command lifecycle/output state, while file
  cards are sourced only from verified successful tool audits;
- the headless native integration exercises real turns, event consumption,
  overflow recovery, compaction, and interruption without a terminal toolkit.

## P4 boundary

P3 deliberately stops at the transport-neutral engine boundary. P4 must still
provide the user-facing terminal application: alternate-screen lifecycle and
restoration, transcript viewport, composer and scrollback, resize and tiny
terminal behavior, tool/diff/status cards, resume picker, commands, help, and
non-TTY fallback. P4 should consume the P3 snapshot, delta, and command
interfaces; it must not introduce a second transcript, session, tool-output,
or compaction source of truth.
