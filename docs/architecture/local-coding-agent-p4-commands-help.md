# Local Coding Agent P4.5 Commands and Help

Status: implemented

Roadmap packet: P4.5

## Command boundary

Slash-command parsing belongs to the terminal frontend. A draft is considered
a command only when its trimmed text begins with one `/`; `//` escapes a
literal leading slash and submits an ordinary prompt. Unknown or malformed
commands remain in the composer with an actionable `/help` error, so input is
never silently converted into a model prompt or discarded.

Commands that mutate runtime state cross the existing typed
`frontend.CommandSink` boundary. The terminal never calls the agent loop,
session store, thread store, compactor, or Git directly. `/compact` invokes the
real controller compaction command; success is not synthesized in local UI
state. Compaction activity and its terminal result arrive through the same
coalescing current-view subscription as turns, tools, workspace observations,
and context usage.

`/rename` and `/new` also exercise their admitted typed controller methods.
The current native controller deliberately returns a shared unsupported error:
P6.1 owns durable thread rename, and active-controller switching has not been
admitted. The TUI converts that result into honest guidance: the title remains
unchanged, or the user exits and runs `mintclaw code <prompt>`. It does not
create frontend-only metadata or pretend a new durable thread exists.

## Read-only panels

`/status`, `/model`, and `/diff` select a frontend-local presentation panel.
The panel stores only which view is open. Every render reads the subscribed
current `ThreadSnapshot`; it does not copy status, model, repository, diff, or
compaction fields into a command-specific mirror. A slow subscriber that
receives a coalesced newer view therefore renders the newer branch, repository
state, context use, and compaction result immediately.

The bounded panels expose:

- thread ID, title, activity, project, cwd, model/provider, context use,
  branch, repository state, and last compaction;
- current workspace root, branch, dirty/clean state, bounded diff stat and
  changed paths, truncation, and capture warning; and
- the current model/provider plus the existing CLI model-switch workflow.

Terminal controls are sanitized, content wraps by cell width, and excess
panel lines are replaced by an explicit bounded remainder count.

## Discovery and exit

`/help` lists every command plus Enter, Ctrl+J, Ctrl+C, Page Up, Alt+End,
Ctrl+R, Alt+J/Alt+K, Ctrl+O, and Escape behavior. Escape closes a panel without
changing the composer. `/exit`, `/quit`, and `/q` leave the Bubble Tea program;
the existing application owner then closes the controller with its bounded
fresh shutdown context and releases the thread lease after terminal
restoration.

## Evidence

Automated tests cover:

- help visibility, width bounds, panel close, unknown commands, malformed
  arguments, and literal-slash prompt escape;
- live `/status`, `/model`, and `/diff` rendering from replacement current
  snapshots, including branch and clean/dirty changes;
- typed compact, rename, and new-thread dispatch plus explicit exit;
- real projected compaction completion becoming visible through a later
  current snapshot rather than a local success flag; and
- operation-specific safe guidance for unsupported native lifecycle commands.
