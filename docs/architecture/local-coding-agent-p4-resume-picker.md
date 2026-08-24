# Local Coding Agent P4.4 Resume Picker

Status: implemented

Roadmap packet: P4.4

## Boundary

Interactive `mintclaw resume` opens a standalone Bubble Tea catalogue screen
before MintClaw acquires a writer lease or constructs an active-thread
controller. The picker consumes one bounded `picker.Page`; search, scope,
refresh, and pagination replace that page directly.

The picker domain is intentionally separate from `frontend.ThreadSnapshot`.
It has no reducer, revision, delta, replay, or reconnect contract. This keeps
the C1 current-view simplification intact while allowing catalogue discovery
to evolve independently from active-thread presentation.

Non-interactive, redirected, `TERM=dumb`, and JSON calls retain the bounded
plain or machine-readable catalogue. An explicit thread ID and `--last`
bypass selection but enter the same resumed TUI on an interactive terminal.

## Discovery and admission

The catalogue searches metadata only. A trimmed valid-UTF-8 query of at most
256 bytes matches ID, title, preview, project root, invocation cwd, or branch
before deterministic pagination. The current canonical project is the default
scope; `A` explicitly toggles all-project discovery.

Rows combine persisted metadata with bounded, concurrent observations:

- current project location and identity;
- Git branch and dirty state;
- staleness when persisted branch or HEAD differs from the current complete
  observation; and
- a non-owning OS lease probe with bounded owner diagnostics.

These are presentation hints, never admission authority. After selection the
command layer acquires the OS-backed writer lease, reloads canonical metadata,
re-inspects the current project, resolves the model, validates runtime layout,
and only then creates the controller. A lock or project change between display
and Enter therefore fails safely. Cancellation creates neither a controller
nor a lease.

## Interaction and bounded failure states

Rows expose title, preview, age, branch, invocation path, short ID, and textual
`[dirty]`, `[clean]`, `[stale]`, `[missing]`, `[moved]`, `[locked]`, or
`[state unknown]` markers. Meaning does not depend on color. Arrow or `j`/`k`
keys select, Page Up/Page Down page, `/` searches, `A` changes scope, `R`
refreshes, Enter resumes, and Escape, `Q`, or Ctrl+C cancels.

Missing, moved, locked, unknown-state, and cross-project rows remain visible
but cannot be selected silently. Empty search results, corrupt-entry counts,
scan truncation, load errors, and tiny terminal widths have explicit bounded
rendering and recovery actions.

## Evidence

Automated tests cover:

- metadata search validation, ordering, scope, pagination, corruption, and
  truncation contracts;
- real Git dirty/stale observation, non-Git projects, missing projects, and
  live lease hints;
- searchable/paged/scope-toggle picker transitions, accessible textual state,
  width bounds, empty/error views, strict unavailable-row selection, and
  program selection/cancellation;
- interactive picker, direct-ID, and `--last` entry into the resumed TUI while
  preserving the non-TTY list path; and
- final under-lease metadata reload, live contention and project mismatch
  rejection, prompt forwarding, controller cleanup, and lease release.
