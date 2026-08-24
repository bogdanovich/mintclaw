# Local Coding Agent P4 Exit Record

Roadmap gate: [P4 — Required terminal UI](local-coding-agent-roadmap.md#p4--required-terminal-ui).

The merge containing this record completes the five ordered P4 packets. The
native coding runtime now has a mandatory interactive terminal application for
capable TTYs, while redirected and machine-oriented invocations retain the
bounded plain path. This is the planned alpha-quality P4 endpoint, not a claim
that long-session continuity or every thread-lifecycle control is complete.

## Completion evidence

| Packet | Merged change | Contract evidence |
| --- | --- | --- |
| P4.1 | [#761](https://github.com/bogdanovich/mintclaw/pull/761), merge `f123fdc7` | A Bubble Tea application owns alternate-screen, focus, resize, signal, panic, and bounded shutdown behavior around a transport-neutral controller. Capable TTYs enter the application; non-TTY, dumb-terminal, JSON, and replaced-stream calls retain the plain renderer. Pseudo-terminal tests prove restoration on normal exit, `Ctrl+C`, SIGTERM, and induced panic. |
| P4.2 | [#764](https://github.com/bogdanovich/mintclaw/pull/764), merge `3a3967b6` | The bounded viewport renders live current views plus an optional read-only historical window without reading JSONL from the TUI. Multiline Unicode input, bracketed paste, bounded composer history, admission failure, manual-scroll anchors, cell-width behavior, lazy transcript paging, changed-history rejection, and slow/coalesced view replacement have semantic tests. |
| P4.3 | [#767](https://github.com/bogdanovich/mintclaw/pull/767), merge `3a9d0603` | Collapsed and optionally expanded tool cards expose bounded lifecycle and command outcomes without arguments. Verified writes remain distinct from deterministic repository observations. Project, branch, model, context, activity, changed paths, diff stat, explicit refresh, truncation, redaction, and non-color outcomes are projected through the same current view. |
| P4.4 | [#836](https://github.com/bogdanovich/mintclaw/pull/836), merge `729ec844` | Interactive resume uses a searchable, paged, project-scoped catalogue with explicit all-project, refresh, loading, empty, corrupt, missing, moved, locked, stale, and truncated states. Selection remains a hint: the command layer acquires the writer lease, reloads metadata, re-inspects project identity, and validates admission before constructing the controller. |
| P4.5 | [#838](https://github.com/bogdanovich/mintclaw/pull/838), merge `85f98b39` | Frontend-owned `/help`, `/status`, `/model`, `/diff`, `/compact`, `/rename`, `/new`, and `/exit` parsing is typed at the controller boundary. Read-only panels render the live snapshot; real compaction is serialized; escaped prompts retain safe history semantics; structured fields cannot forge rows; and unsupported lifecycle operations report the admitted workflow instead of mutating frontend-only state. |

Every implementation packet passed the repository's final nine-check matrix:
linter, security, tests, race, Darwin and Windows compilation, macOS
portability, integration tests, and browser tests. Automated reviewer findings
were fixed with focused regression coverage, re-reviewed on the final packet
head, and resolved before each merge.

## Current presentation architecture

P4 exits on the simplified C1 architecture implemented before the final two
packets, not the earlier P3 revision/delta design:

- `frontend.Projector` publishes one immutable, bounded `ThreadSnapshot` and a
  coalescing one-slot in-process subscription. A slow consumer converges to the
  newest complete view instead of replaying revisions or reconstructing state.
- The active-thread TUI owns only presentation state: viewport and composer
  position, tool selection, the open command panel, pending UI admission, and a
  bounded optional historical display window. It does not own a transcript,
  tool, repository, compaction, or thread-persistence source of truth.
- Historical hydration is a read-only fixed-prefix window requested through an
  optional controller interface. Canonical replacement or reordering disables
  that window rather than merging incompatible history with the live view.
- The resume picker is a separate pre-controller bounded catalogue page.
  Search, scope, refresh, and pagination replace the page directly; the picker
  owns no writer lease and is not an active-thread snapshot reducer.
- Mutations cross the typed controller and remain serialized with the native
  runtime and thread lease. Repository refresh and compaction return through
  the ordinary current-view projection rather than a TUI-specific mirror.

The [P3 exit record](local-coding-agent-p3-exit.md) is retained as historical
evidence for the engine boundary that enabled P4. Its references to revisioned
deltas describe that earlier milestone, not the current presentation contract.

## Exit-gate decision

The P4 exit statement is satisfied:

- `mintclaw code` creates a durable project coding thread and enters the TUI on
  a capable terminal; `mintclaw resume`, explicit ID, and `--last` all enter the
  same resumed application after authoritative lease and project checks;
- turns stream through the current presentation subscription while a slow or
  manually scrolled view remains bounded and converges without duplicating
  canonical state;
- user, assistant, reasoning, warning, error, tool, verified-write, and current
  repository state have cell-bounded, control-safe terminal presentation;
- graceful interrupt, repeated hard cancel, idle exit, signal exit, panic, and
  controller cleanup preserve the documented terminal and lease lifecycle;
- the resume picker owns one current catalogue page, and the active TUI owns one
  current thread view; neither reintroduces the removed revision/delta/replay
  protocol; and
- help and slash commands are deterministic, preserve literal slash prompts,
  serialize mutation admission, invoke the real compaction lifecycle, and
  expose honest unsupported-operation guidance.

## Deliberate P5 and later boundary

P4 remains alpha-quality by design. These items are not evidence gaps in the
P4 gate:

- P5 owns coding-specific compaction policy, automatic pressure handling,
  restart continuity, repeated-compaction behavior, context accounting, and
  long-session stress evidence. Manual `/compact` is real, but it does not by
  itself make multi-hour or multi-day sessions release-ready.
- New threads are created with `mintclaw code`; in-place controller switching
  for `/new` is not admitted. Durable `/rename` remains P6.1 work. Both commands
  currently return explicit safe guidance rather than false success.
- Non-TTY automation continues to use the bounded plain path until P7 defines
  the stable `code exec` machine protocol.

P5 may now proceed from the merged P4 terminal surface without changing the
current-view ownership model.
