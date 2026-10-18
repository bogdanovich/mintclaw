# Local Coding Agent

MintClaw can work in a repository as an interactive, durable coding agent or
as a noninteractive command for scripts. Coding threads belong to the current
project, survive process restarts, and keep their state outside the checkout.

## Configure a model first

Use the same active home and config for authentication and coding. Set
`MINTCLAW_HOME` and `MINTCLAW_CONFIG` explicitly when operating multiple
profiles; an optional `MINTCLAW_AUTH_FILE` selects a shared authentication
store. Do not copy a production config into a public checkout.

For OpenAI device authentication:

```console
mintclaw auth login --provider openai --device-code
mintclaw auth status
mintclaw auth models
mintclaw model
```

Follow the URL and code printed by login. The model chosen by onboarding is a
starting default, not a promise that every account can use it. `auth models`
shows authentication-backed choices; `model` shows the enabled routes in the
loaded config. Select an available configured alias with
`mintclaw model <configured-alias>`, or override a new coding thread with
`mintclaw code --model <configured-alias>`.

For another provider, configure an enabled `model_list` route with its
provider, native model ID, endpoint where required, and protected credential
binding. An OpenAI-compatible endpoint can be onboarded with
`mintclaw model add --help`; `--model` skips network discovery, and `--name`
defines the local alias and changes the default. Its `--api-key` argument can
appear in shell history or process listings: use a private credential binding
in config for routine operation, not a real key pasted into a shared command
or issue. See [configuration](configuration.md).

Check configuration without starting an agent:

```console
mintclaw doctor --config /path/to/config.json
```

Exit 1 means a command/config load error; exit 2 means reported policy
findings, not necessarily a parser failure. Resolve the relevant findings and
try a harmless read-only prompt before admitting repository mutations.

## Start and resume

Run these commands from the repository or subdirectory you want MintClaw to
use:

```console
mintclaw code
mintclaw code "inspect this repository and summarize its structure"
mintclaw code "investigate this failure" --attach build.log --attach screenshot.png

mintclaw resume
mintclaw resume --last
mintclaw resume <thread-id>
mintclaw resume --all
```

`mintclaw code` without a prompt opens an empty composer on an interactive
terminal. A compact, non-transcript startup card identifies the MintClaw
version, model, working directory, and effective permission mode; it disappears
when work starts or when dismissed with `Esc`. `mintclaw resume` opens a
searchable picker scoped to the current project; `--all` includes other projects
and shows their paths. `--archived` selects archived threads, and `--search
<query>` searches bounded metadata and retained history.

To submit a turn while selecting a known thread, use `--prompt`:

```console
mintclaw resume <thread-id> --prompt "continue from the failed test"
mintclaw resume --last --prompt "run the focused checks"
```

A thread keeps its selected model. `--model <name>` sets it for a new thread
or replaces it when resuming an explicit thread. While the TUI is idle,
`/model` opens a route-aware picker:

1. choose a recent route or a provider;
2. choose one of that provider's configured model aliases; and
3. choose `Provider default` or a verified reasoning level for that exact
   provider/model route.

Press `/` inside the picker to search configured aliases, providers, and
provider-native model IDs. The default picker never invents catalog entries:
it contains only enabled `model_list` routes from the loaded configuration.
Both the provider view and search results separate `Configured` routes from
`Setup required` routes. A hosted provider's standard API endpoint does not
count as configured without credentials; keyless routes are accepted only for
providers that explicitly support them or for an explicitly configured custom
endpoint. The ten most recently selected provider/model identities are stored
outside configuration and transcripts under the active MintClaw home at
`coding/model-recents.v1.json`; stale routes are hidden when they are no longer
configured.

`/model provider/name [reasoning-effort]` is the scriptable equivalent. A
unique alias may be used without the provider; an ambiguous alias is resolved
only by the current provider and otherwise requires `provider/name`.
Unsupported combinations are rejected before the thread changes. A successful
model-and-reasoning selection is saved atomically before the next turn starts
and is restored by `mintclaw resume`. MintClaw coding is autonomous by default;
there is no approval dialog and no required `--yolo` flag. The effective
permission and autonomy modes remain visible in `/status`.

## Delegate from chat

The always-running gateway agent can delegate a coding task to a configured
repository on a paired Linux or macOS development machine. The gateway owns
the Telegram conversation and durable task; the thin `mintclaw-node`
companion starts the same native coding core through a private
`mintclaw _worker` child. Investigations are read-only. Repository `mutate`
and `project-yolo` profiles use isolated worktrees; `machine-yolo` operates
directly in its configured machine scope, which need not be a Git repository.
Released threads appear in the normal local resume catalogue.

This surface is deny-by-default and accepts only operator-defined target and
project aliases plus exact requester grants. It never accepts a model-authored
path, repository URL, executable, provider credential, or cleanup policy. See
[Remote coding tasks](../operations/remote-coding-tasks.md) for setup and
operations.

An ordinary gateway chat session owns channel history and delivery, not a
repository checkout or a local coding transcript. A delegated worker owns its
own coding thread, model context, process lifecycle and worktree/machine
effects. The gateway retains a durable task link and can report status, relay
a question, steer or cancel that same task; it does not merge the worker's
history into the chat. Ask for the existing task ID after uncertainty instead
of creating a replacement mutation.

A local coding session can also use already-paired companions through an
optional same-user gateway broker on Linux/macOS. This requires a running
gateway, owner-only IPC configuration and an exact local-coding grant. It does
not pair nodes itself or inherit every chat permission. Direct capabilities
keep the local thread as the reasoning owner; a remote coding task gives the
remote worker its own thread. See
[companion access and setup](../architecture/local-coding-agent-p7-5-companion-access.md).

## Project instructions and trust

For each admitted directory, MintClaw selects one instruction file in this
order: `AGENTS.override.md`, `AGENTS.md`, then `CLAUDE.md`. It does not merge
same-directory `AGENTS.md` and `CLAUDE.md`. Configured global instructions and
the project-root-to-working-directory chain are combined in scope order;
deeper instructions apply to their subtree. The scoped loader also checks
new file targets rather than treating every sibling's instructions as global.
Instruction reads are bounded (16 KiB per file, 64 KiB total by default), and
`/status` reports admitted sources and warnings without showing their content.

Repository instructions and skills guide behavior; they cannot grant a new
filesystem root, companion, credential or privilege. Local coding is
autonomous within its admitted authority, not an OS security sandbox. Treat
untrusted repositories accordingly. Remote project/machine/root profiles are
separate operator grants; neither a prompt nor `--yolo` creates one.

## Interactive and noninteractive modes

The interactive TUI is selected only when standard input and output are TTYs
and `TERM` is not `dumb`. It uses raw input in an adaptive inline surface, so
the invoking command and earlier shell output remain in terminal scrollback.
A fresh empty session occupies only the compact startup card, composer,
separation rows, and footer; constrained terminals omit the card before they
sacrifice the input path. The transcript grows with content until it reaches
the available terminal height, then becomes a bounded scrolling viewport. On
exit the last inline frame remains visible without printing a duplicate final
answer. Separation rows are omitted only when they do not fit.

The resume picker remains a temporary full-screen surface. Inside an active
thread, `/transcript` or `Ctrl+T` temporarily enters the alternate screen for
search and copy, then restores the inline session when closed.

Pipes, redirected streams, `TERM=dumb`, and JSON calls use plain output and do
not emit terminal control sequences. For automation, use the stable execution
surface directly:

```console
mintclaw code exec "run the focused tests and summarize the result"
mintclaw code exec --json "inspect the current diff"
mintclaw code exec resume <thread-id> "continue the investigation"
```

Plain `code exec` prints the final response. `--json` emits schema-versioned
JSONL lifecycle events suitable for a programmatic caller.

## Reading the TUI

The main transcript is a compact, causal view:

- plan updates appear as a live pending/current/completed checklist;
- progress commentary stays between the work that caused it;
- compatible exploration and successful foreground commands group into short
  summaries, while failures remain individual and prominent;
- command, MCP, and file-change cells use typed execution evidence rather than
  parsing assistant claims;
- assistant commentary and final answers render source-backed Markdown,
  including headings, lists, emphasis, code, links, and responsive tables;
  narrow tables become labeled records instead of overflowing horizontally;
- verified diffs use green addition and red deletion rows where terminal color
  capabilities permit, while signs and line numbers preserve meaning without
  color;
- compaction has an explicit running/completed/failed lifecycle; and
- concrete work ends at a subtle full-width elapsed separator before the final
  response, whose leading bullet and continuation inset distinguish it from
  the terminal edge.

The working line names the current phase, elapsed time, and interrupt key. On a
focused truecolor terminal, animated mode sweeps a soft brightness wave across
the phase label without changing its text; reduced motion, disabled motion,
limited color, hidden, and unfocused states use static or one-second fallbacks.
The footer is deliberately small: use `/status` for the complete operational
view and `/diff` for bounded repository details. Repository state is not kept
as a persistent transcript block. A known dirty branch gains a compact `*` in
the footer; `Ctrl+R` refreshes it. An unset reasoning effort appears as
`reasoning default` in the footer and `provider default` in `/status`; only an
explicitly configured `off` appears as `off`.

## Keyboard bindings

| Key | Action |
| --- | --- |
| `Enter` | Submit a prompt, or queue guidance into the active turn |
| `Ctrl+J` or `Shift+Enter` | Insert a newline |
| `Ctrl+C` | Interrupt active work; repeat for hard cancel; exit while idle |
| Mouse wheel/trackpad | Scroll the active transcript, open panel, or transcript overlay |
| `Page Up` / `Page Down` | Scroll the transcript or open panel without a mouse |
| `Alt+End` | Return to the latest transcript row |
| `Ctrl+R` | Refresh repository state |
| `Ctrl+T` | Open or close the complete transcript overlay |
| `Esc` | Close the current panel or overlay |
| `Ctrl+V` or `Ctrl+Alt+V` | Read a supported image from the system clipboard |

Inside the transcript overlay, arrows or `j`/`k` select a row, `g`/`G` or
Home/End jump to its ends, `/` starts search, `n`/`N` moves between matches,
`Ctrl+L` clears search, `c` copies the selected row, `C` copies all retained
rows, and `?` opens overlay help. Closing the overlay restores the prior
composer focus, panel, selection, and transcript position.

## Slash commands

| Command | Purpose |
| --- | --- |
| `/help` | Show current commands and bindings |
| `/status` | Show session, model, project, context, trust, and plan state |
| `/model [provider/name [reasoning-effort]]` | Browse recent routes or providers, search configured models, and select provider-default or verified reasoning between turns |
| `/skills [name]` | List effective coding skills or insert an exact `$skill` mention |
| `/transcript` | Open the same complete surface as `Ctrl+T` |
| `/diff [current\|base <ref>\|commit <ref>]` | Show bounded typed repository evidence |
| `/review [current\|base <ref>\|commit <ref>] [-- instructions]` | Run native read-only review |
| `/attach <paths...>` | Add local files to the current draft |
| `/compact` | Start real context compaction while idle |
| `/rename <title>` | Rename the current thread |
| `/archive` / `/unarchive` | Change catalog visibility |
| `/new` | Request a new coding thread |
| `/exit` | Restore the terminal, close the controller, and exit |

Start prompt text with `//` when the intended text itself begins with a slash.

## Skills

The coding agent discovers repository skills from the current directory upward
to the admitted project root, then user and MintClaw-managed roots. A repository
skill lives at `<directory>/.agents/skills/<name>/SKILL.md`; a portable personal
skill lives at `$HOME/.agents/skills/<name>/SKILL.md`. Repository skills are not
added to ordinary gateway chat prompts.

Run `/skills` to inspect the effective, shadow-resolved catalog. Run
`/skills <name>` to insert `$name` into the composer, or type `$name` directly
in a prompt:

```text
$mintclaw-deployed-ops inspect the deployment and report its current version
```

The `$name` text remains part of the visible user message. At turn admission,
MintClaw resolves it to an exact catalog path, reads the complete bounded
`SKILL.md`, and freezes the path, scope, content hash, and instructions for the
current turn. Provider retries and current-turn compaction reuse that snapshot
even if the file changes concurrently. A later turn reads the then-current
catalog and must select the skill again when its instructions are still needed.

Unknown dollar tokens such as `$HOME` are left alone and do not become skill
errors. Explicit gateway `/use` selections instead report deterministic
unknown, profile-disabled, or runtime-incompatible errors. Selecting a skill
never adds tools, filesystem access, MCP servers, network access, or any other
authority; the admitted coding policy remains unchanged.

## Optional capability policy

Document work and brokered browser automation have coding-specific
switches. They are independent from the always-on gateway tool switches:

```json
{
  "coding": {
    "capabilities": {
      "document": true,
      "browser": true
    }
  }
}
```

Both switches default to `true`, preserving the normal coding surface. A switch
only permits composition; it does not create a document backend, configure a
remote browser grant, start a gateway, or grant delivery, node, or MCP
authority. Setting it to `false` removes the corresponding coding tools before
the model catalog is built.

When document support is effective, coding admits `inspect`, `extract`,
`render`, `fields`, `fill`, and `verify`. A fill is written only to the current
thread's durable artifact journal and returns an opaque artifact ref; it does
not publish to a chat channel or modify the repository. The protected
multi-turn `form` workflow and retained-render delivery remain gateway-only
until their additional audit and interaction owners are admitted for coding.
Assignments are protected tool input and are replaced by value-free bindings
in durable model history.

`mintclaw status` shows these configured switches. Inside `mintclaw code`, run
`/status` to inspect the final effective capability report. Effective entries
distinguish policy disablement, missing configuration, unavailable services,
missing dependencies, and runtime-incompatible operations.

## Pasted text and attachments

Long pasted text is represented by a compact `[Pasted Content N chars]` label
and held in a private temporary file until submission. Supported image paths
and clipboard PNG data appear as `[Image #N]`; other explicit attachments use
file labels. Removing a label removes that pending payload. A failed admission
keeps the draft available for retry.

Submitted files are copied into immutable MintClaw-owned attachment storage.
Historical attachments are not replayed into every later model request. The
agent can select an earlier attachment through the thread-authorized attachment
tool when needed, including after compaction or restart.

## Threads, compaction, and recovery

Coding state lives under the active MintClaw home:

```text
coding/threads/<thread-id>/
```

That home is `MINTCLAW_HOME` when set, otherwise the discovered `main` profile
when it exists, and otherwise `~/.mintclaw`.

Each thread has its own metadata, lock, canonical JSONL session file, Seahorse
SQLite context database, attachment references, durable document write journal
and artifacts, and derived presentation state. MintClaw does not put these
files in the source repository. Only one process may write a thread at a time.

Context compaction summarizes bounded older context while canonical history
remains the authority. Its progress is visible, and a completed checkpoint is
restored on resume. A process crash never authorizes blind replay of an
ambiguous command or file mutation: recovered work is marked interrupted or
uncertain, and `mintclaw resume <thread-id>` continues from the durable edge.

Provider retry and fallback are visible as sanitized warning rows. A fallback
before visible output can complete the same turn once; a failure after visible
output stays a failure and is not silently replayed through another provider.

## Delete and retain coding state

Archiving hides a thread; it does not delete its history. To inspect a deletion
plan and then move one thread into recoverable MintClaw trash, run these from
the thread's project:

```console
mintclaw threads delete <thread-id> --json
mintclaw threads delete <thread-id> --confirm <thread-id> --json
```

The complete known thread state moves together, including canonical history,
Seahorse state, previews, runtime state, diagnostics and artifacts. The receipt
names its recovery location. Project files and Git state have their own
lifecycle and are outside this operation.

Recoverable trash becomes eligible for explicit purge after 30 days by
default. Set `coding.state.trash_retention_hours` to change that window
(24–8760 hours; omitted or zero means 720). Nothing expires automatically
during startup or resume. Preview the store-wide pass before confirming it:

```console
mintclaw threads trash purge --json
mintclaw threads trash purge --older-than 720h --confirm purge-expired-thread-trash --json
```

These commands can run outside a repository. Purge is permanent. Its JSON
receipt reports attempted and completed opaque trash IDs; an
`incomplete_trash_id` means a recursive removal failed after it began. Inspect
that root before retrying. Unknown or linked top-level entries abort the pass
so they can be inspected separately.

Shared attachment bytes remain until no active, trashed or fork-quarantined
thread refers to them. Reclaim old, unreferenced blobs with a separate plan and
confirmation:

```console
mintclaw threads gc --older-than 24h --json
mintclaw threads gc --older-than 24h --confirm delete-unreferenced-blobs --json
```

Thread deletion cannot revoke prior exports or remove service logs, remote
task ledgers, credentials, worktree checkouts or a configured shared absolute
diagnostic directory. A custom relative trace directory with an unrecognized
top-level name requires ownership inspection before thread deletion. The
[state ownership and retention audit](../architecture/local-coding-agent-p8-state-lifecycle.md)
lists those separate owners. For an incident, keep the relevant evidence
until the investigation is complete and use a
[diagnostic support bundle](../operations/diagnostic-support-bundles.md)
instead of uploading the private state directory.

## Terminal, SSH, tmux, and accessibility

- `--no-color` or `NO_COLOR=1` keeps the interactive layout but removes color.
- `MINTCLAW_TUI_THEME=auto|light|dark` controls the semantic palette. `auto`
  uses conventional terminal metadata and never sends a blocking color query.
- `MINTCLAW_TUI_MOTION=animated|reduced|disabled` controls motion. Reduced and
  disabled modes retain explicit activity text.
- SSH sessions skip the remote machine's native clipboard. tmux first uses
  verified clipboard forwarding where available; bounded OSC 52 is the final
  terminal fallback.
- When launched inside Herdr, `mintclaw code` uses Herdr's injected pane
  environment to report `working`, `blocked`, and `idle` lifecycle states. It
  releases the pane on exit and stays completely inactive outside Herdr; no
  Herdr configuration or patched Herdr build is required.
- The layout reflows at narrow widths. Plain and no-color representations keep
  status, lifecycle, diff signs, and navigation discoverable without color or
  a mouse.
- Unicode search, combining characters, CJK, emoji, bidi-adjacent text, and
  multiline terminal/IME input use cell-width-aware wrapping and sanitized
  control data.

## Troubleshooting

- **`model is required`:** configure a coding-capable model or pass
  `--model <configured-name>`. OpenAI device authentication is available with
  `mintclaw auth login --provider openai --device-code`.
- **Authentication failed during a turn:** an OAuth access token normally
  refreshes automatically. If the saved login is no longer usable, follow the
  sign-in command in the error on the same host and under the same account,
  then retry in the existing session. API-key routes instead require checking
  the configured key and provider permissions. MintClaw does not automatically
  start a login flow or replay a failed turn.
- **The TUI did not open:** both streams must be terminal files and `TERM` must
  not be `dumb`. Use `mintclaw code exec` intentionally for pipes and scripts.
- **Colors are hard to read:** set `MINTCLAW_TUI_THEME=light` or `dark`, or use
  `--no-color` to rely on explicit text and symbols.
- **A thread is busy:** another process owns its lease. Finish that process or
  recover only after confirming it is no longer alive.
- **A prior command is uncertain after a crash:** inspect `/status`, `/diff`,
  and `Ctrl+T` before asking MintClaw to continue. It will not claim or replay
  an unverified mutation automatically.
- **The main view omitted output:** compact cells are intentionally bounded.
  Scroll with the wheel/trackpad or Page Up to revisit earlier compact cells.
  `Ctrl+T` shows the complete retained copy-safe evidence and hydrates older
  history on demand.

For reliability incidents, start with the active profile, approximate time and
exact thread/task/trace ID. Use metadata before private history:

```console
mintclaw diagnostics list --since 2h
mintclaw diagnostics correlate --thread <thread-id>
mintclaw diagnostics correlate --task <task-id>
mintclaw diagnostics show <trace-id>
mintclaw diagnostics export --trace <trace-id> --output ./support-new-directory
```

These commands never resume work or replay tools. Default export excludes
prompts, responses, command/source/diff content, credentials, absolute paths
and unrelated traces. Keep the bundle private until its contents and
destination policy have been verified.

| Symptom | First evidence boundary |
| --- | --- |
| No response, duplicate response, or completed task with no channel result | Compare task terminal state, completion and delivery IDs with the durable outbox; model completion alone is not delivery proof. |
| Wrong tool or repeated tool calls | Compare model attempt, tool-call ID/generation, result and loop decision; a successful tool can still be irrelevant. |
| Provider/authentication failure | Check sanitized attempt/retry/fallback status and credential availability in the same profile; do not replay a turn that already produced effects. |
| Remote task is uncertain | Inspect the same task/generation, invocation and worker ledger before deciding whether to continue; uncertainty is not safe-to-retry. |
| Thread busy or output differs from history | Compare the writer owner and canonical history with the content-free presentation checkpoint; do not delete a live lock. |
| Compaction/resume failed | Preserve canonical JSONL, inspect the checkpoint/reconciliation boundary and recover through the normal runtime; do not rewrite history to repair a derivative. |
| No matching trace | Check capture enablement, store selection, retention, scan bounds, truncation and drops; missing evidence is not proof that execution did not happen. |

See [diagnostic support bundles](../operations/diagnostic-support-bundles.md)
for exact selectors, authoritative `owners.json`, privacy and the
incident-to-regression loop. For upgrades and rollback, use the
[coding-state operations guide](../operations/coding-state-upgrades.md).

## Deliberate differences from Codex

MintClaw follows Codex's useful terminal hierarchy, but it is not a port. It
uses Go with Bubble Tea/Lip Gloss, is autonomous by default, supports multiple
providers and MintClaw authentication, keeps one bounded in-process frontend,
and integrates MintClaw attachments, Seahorse state, always-on agents, and
paired companions. It does not import Codex's Rust TUI, app-server transport,
approval UI, sandbox selector, storage model, branding, or hidden reasoning.
