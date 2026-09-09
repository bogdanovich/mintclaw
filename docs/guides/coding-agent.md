# Local Coding Agent

MintClaw can work in a repository as an interactive, durable coding agent or
as a noninteractive command for scripts. Coding threads belong to the current
project, survive process restarts, and keep their state outside the checkout.

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
terminal. `mintclaw resume` opens a searchable picker scoped to the current
project; `--all` includes other projects and shows their paths. `--archived`
selects archived threads, and `--search <query>` searches bounded metadata and
retained history.

To submit a turn while selecting a known thread, use `--prompt`:

```console
mintclaw resume <thread-id> --prompt "continue from the failed test"
mintclaw resume --last --prompt "run the focused checks"
```

A thread keeps its selected model. `--model <name>` sets it for a new thread
or replaces it when resuming an explicit thread. MintClaw coding is autonomous
by default; there is no approval dialog and no required `--yolo` flag. The
effective permission and autonomy modes remain visible in `/status`.

## Interactive and noninteractive modes

The interactive TUI is selected only when standard input and output are TTYs
and `TERM` is not `dumb`. It uses raw input and the alternate screen while it
is open. On exit it restores the terminal and leaves a bounded thread/status
summary plus the latest final answer in ordinary scrollback.

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
- command, MCP, and repository cells use typed execution evidence rather than
  parsing assistant claims;
- verified diffs use green addition and red deletion rows where terminal color
  capabilities permit, while signs and line numbers preserve meaning without
  color;
- compaction has an explicit running/completed/failed lifecycle; and
- concrete work ends at a subtle elapsed separator before the unprefixed final
  response.

The working line names the current phase, elapsed time, and interrupt key.
The footer is deliberately small: use `/status` for the complete operational
view.

## Keyboard bindings

| Key | Action |
| --- | --- |
| `Enter` | Submit a prompt, or queue guidance into the active turn |
| `Ctrl+J` or `Shift+Enter` | Insert a newline |
| `Ctrl+C` | Interrupt active work; repeat for hard cancel; exit while idle |
| `Page Up` / `Page Down` | Scroll the transcript or open panel |
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
| `/model` | Show the current model/provider and switching guidance |
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

By default, coding state lives under:

```text
${MINTCLAW_HOME:-~/.mintclaw}/coding/threads/<thread-id>/
```

Each thread has its own metadata, lock, canonical JSONL session file, Seahorse
SQLite context database, attachment references, and derived presentation
state. MintClaw does not put these files in the source repository. Only one
process may write a thread at a time.

Context compaction summarizes bounded older context while canonical history
remains the authority. Its progress is visible, and a completed checkpoint is
restored on resume. A process crash never authorizes blind replay of an
ambiguous command or file mutation: recovered work is marked interrupted or
uncertain, and `mintclaw resume <thread-id>` continues from the durable edge.

Provider retry and fallback are visible as sanitized warning rows. A fallback
before visible output can complete the same turn once; a failure after visible
output stays a failure and is not silently replayed through another provider.

## Terminal, SSH, tmux, and accessibility

- `--no-color` or `NO_COLOR=1` keeps the interactive layout but removes color.
- `MINTCLAW_TUI_THEME=auto|light|dark` controls the semantic palette. `auto`
  uses conventional terminal metadata and never sends a blocking color query.
- `MINTCLAW_TUI_MOTION=animated|reduced|disabled` controls motion. Reduced and
  disabled modes retain explicit activity text.
- SSH sessions skip the remote machine's native clipboard. tmux first uses
  verified clipboard forwarding where available; bounded OSC 52 is the final
  terminal fallback.
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
  `Ctrl+T` shows the complete retained copy-safe evidence and hydrates older
  history on demand.

## Deliberate differences from Codex

MintClaw follows Codex's useful terminal hierarchy, but it is not a port. It
uses Go with Bubble Tea/Lip Gloss, is autonomous by default, supports multiple
providers and MintClaw authentication, keeps one bounded in-process frontend,
and integrates MintClaw attachments, Seahorse state, always-on agents, and
paired companions. It does not import Codex's Rust TUI, app-server transport,
approval UI, sandbox selector, storage model, branding, or hidden reasoning.
