# Local Coding Agent P0 Terminal Frontend Admission

Status: implemented

Roadmap packet: P0.5

## Decision

MintClaw's interactive coding frontend will use:

- [Bubble Tea](https://github.com/charmbracelet/bubbletea) `v1.3.10` for the
  terminal event loop;
- [Bubbles](https://github.com/charmbracelet/bubbles) `v1.0.0` for textarea and
  viewport primitives; and
- the existing Lip Gloss `v1.1.0` dependency for presentation.

P0.5 retains a small, testable Bubble Tea model under `pkg/coding/tui`. It is
not a user-facing coding command or the polished P4 application shell. The
transport-neutral protocol and reducer live under `pkg/coding/frontend`, so a
headless renderer, a future local daemon client, and the terminal frontend do
not import agent internals.

The framework was selected because its model/update/view boundary can be
driven deterministically in tests, Bubble Tea owns terminal setup and
restoration, and its current input decoder exposes bracketed paste and resize
events. Bubbles supplies Unicode-aware textarea and viewport primitives while
remaining isolated from the agent and persistence packages.

## Screen and scrollback contract

The eventual interactive `mintclaw code` TUI uses the alternate screen by
default. A stable transcript viewport, multiline composer, tool cards, resize,
and streaming updates cannot be implemented reliably by continually appending
to native scrollback.

The supported fallback is a distinct inline/plain renderer:

- non-TTY and `mintclaw code exec` never enter the alternate screen;
- terminals where interactive capabilities are unavailable use the plain
  renderer or fail with an actionable suggestion rather than emitting control
  sequences blindly;
- an explicit no-alternate-screen mode may use the same bounded frontend
  protocol with reduced interaction; and
- after a normal alternate-screen exit, P4 prints a bounded final summary to
  native scrollback. It does not replay the entire transcript automatically.

This keeps shell history useful without coupling canonical conversation state
to what happened to remain visible in a terminal buffer.

## Frontend protocol

`mintclaw.coding.frontend.v1` has two forms:

1. `ThreadSnapshot` is an authoritative, revisioned, bounded projection.
2. `Delta` is an ordered replacement for one bounded projected entity and
   names its exact predecessor revision.

The snapshot includes the thread ID, revision, activity, bounded transcript
entries, bounded tool states, context usage, status, and whether older entries
exist. It is not canonical history. JSONL remains authoritative and large or
old content is hydrated later by the owning runtime.

Every delta carries:

- protocol and thread identity;
- `previous_revision` and the next monotonic `revision`;
- a typed event kind;
- a complete bounded replacement for the affected entry, tool, usage, or
  activity state; and
- `requires_snapshot` when an entity eviction makes an incremental view
  insufficient.

The admitted delta kinds cover thread open/resume, turn start/end/failure,
interruption request/interruption, answer and reasoning updates, tool start/output/end, context
usage, and compaction start/completion/failure. Types do not imply that every
engine observation already exists; current mapping gaps are listed below.

`Projector.Watch` atomically bridges retained and live deltas. Its queue and
retained log are bounded and never block a running agent turn. A slow consumer
may lose a delta; the next delivered revision exposes the gap. `Reducer`
rejects unknown predecessors and obtains a fresh `ThreadSnapshot` through the
controller boundary. It never reads runtime state, JSONL, or Seahorse directly.

## Controller boundary

The read side exposes only:

- `Snapshot`;
- `ChangesSince`; and
- `Watch`.

The typed command side admits `Submit`, `Interrupt`, `HardCancel`, `Compact`,
`Rename`, `NewThread`, and `Close`. P0.5 defines this boundary but does not
invent persistence or turn semantics for commands owned by later packets.

The Bubble Tea model consumes snapshots and deltas. On a gap it requests a
snapshot from the controller. The composer remains frontend-local, so a
transcript resynchronization does not overwrite text being composed.

## Existing-runtime projection

The retained adapters reuse two existing seams without changing turn behavior:

- `agentadapter` consumes the runtime event channel for turn, tool, interrupt,
  error, and completed compaction observations; and
- `frontend.StreamDelegate` implements the existing message-bus streaming
  interface for accumulated assistant/reasoning content and final context
  usage.

The event adapter scopes subscriptions to the coding session. Tool argument
values are not copied into the frontend projection; only sorted field names
are exposed at this stage. Large content is bounded independently of canonical
history.

## Feasibility evidence

Automated tests prove:

| Concern | Evidence |
| --- | --- |
| Answer and reasoning streaming | Existing accumulated stream callbacks update stable transcript entities and finalize answer content |
| Tool lifecycle | Runtime tool start/end events produce one bounded tool state without exposing argument values |
| Dropped delta | Applying a later revision fails with a gap and resynchronizes to the authoritative snapshot |
| Slow consumer / expired window | Catch-up falls back to a snapshot after the bounded delta log advances |
| Resize | Bubble Tea `WindowSizeMsg` updates viewport and composer dimensions, including a narrow terminal |
| Multiline paste | A bracketed-paste key event preserves newlines, CJK text, emoji, and grapheme input in the Bubbles textarea |
| Long history | The projector evicts old entries, marks older history, and renders the bounded tail in a narrow viewport |
| Cancellation | First `Ctrl+C` during work requests graceful interruption; a repeated press requests hard cancellation |
| Framework isolation | Core protocol/projector tests do not import Bubble Tea; TUI dependencies remain under `pkg/coding/tui` |
| Supported builds | The retained TUI test package cross-compiles with CGO disabled for Linux amd64 and Windows amd64, and builds natively on macOS |

Bubble Tea supports alternate-screen entry, bracketed-paste setup/reset,
terminal resize delivery, and renderer restoration in its public program API.
The retained model uses only public model and component APIs.
The three platform test binaries were approximately 6.3-6.4 MB each including
the Go test harness; P4 will measure the incremental release-binary cost once a
real command is linked.

### Terminal compatibility assessment

| Environment | P0.5 conclusion | P4 acceptance work |
| --- | --- | --- |
| Local xterm-compatible terminal | Alternate screen is the default | Verify normal exit, signal, and induced-panic restoration |
| tmux and SSH | No transport-specific state is assumed; resize and input arrive through Bubble Tea | Run the supported-terminal matrix and verify final native-scrollback summary |
| Native scrollback | Dynamic full-screen rendering is intentionally not attempted | Provide the bounded plain renderer and explicit no-alternate-screen behavior |
| IME | The model accepts composed Unicode runes and uses Unicode-aware Bubbles primitives | Manual composition/cursor tests across supported terminals remain required |
| Tiny terminal | Width and height are clamped and the model remains renderable | Define the polished degraded layout and help surface |
| Non-TTY / automation | Alternate screen is forbidden | P1.4 supplies a frontend-neutral plain renderer; stable JSONL belongs to later non-interactive work |

The P0 spike establishes feasibility, not final terminal certification. Live
tmux, SSH, IME, signal, panic, and screen-reader acceptance remain explicitly
owned by P4.1 and P4.2.

## Missing engine observations

P0.5 found bounded gaps rather than adding speculative events:

1. `agent.llm.delta` contains lengths, not displayable content. Coding answer
   and reasoning text therefore use the existing stream delegate. A future
   in-process coding runtime must install it explicitly.
2. Context compression currently exposes a completed compression event but no
   reliable started/failed lifecycle pair. P3.2 owns those observations.
3. Runtime tool completion exposes bounded lengths and a diagnostic result, but
   no stable redacted live-output protocol. P3.4 owns tool-output projection and
   secret policy.
4. Repository file-change and diff updates do not yet exist as runtime events.
   P2.3 owns the deterministic workspace snapshot and its frontend update.
5. Context usage is available on finalized streaming output, not as a complete
   provider-neutral live budget stream. P3.2 owns the durable compaction/usage
   lifecycle.
6. Thread metadata, title, project identity, and old transcript hydration do
   not exist yet. P1 and P4.2 own them.

These gaps do not weaken snapshot recovery. Unsupported surfaces stay absent
until their owning roadmap packet can define authoritative semantics.

## P0 exit

Together with P0.1-P0.4, this packet completes the P0 exit gate:

- execution and state roots are distinct;
- session and Seahorse stores are injected before construction;
- coding tools are isolated from personal tools;
- personal-agent behavior remains on its existing path; and
- the TUI framework, screen model, typed frontend protocol, controller
  boundary, and recovery rule are decided.

P1 can now implement durable project thread identity and catalogues without
depending on the Bubble Tea model or inventing another event protocol.
