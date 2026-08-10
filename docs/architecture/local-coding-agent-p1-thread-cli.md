# Local Coding Agent P1.4 Thread CLI

Status: implemented

Roadmap packet: P1.4 in
[`local-coding-agent-roadmap.md`](local-coding-agent-roadmap.md)

## Decision

MintClaw admits two top-level, frontend-neutral command boundaries:

```text
mintclaw code <prompt> [--model <model>] [--json]
mintclaw resume [thread-id] [--last] [--all]
                [--prompt <prompt>] [--model <model>]
                [--offset <n>] [--limit <n>] [--json]
```

P1.4 deliberately does not run an LLM turn. It proves durable creation,
selection, lease acquisition, canonical append, restart, and plain/machine
rendering before P2 installs the native coding runtime profile. The command
help says this explicitly rather than presenting a stored prompt as a completed
coding task.

The first accepted `code` prompt provisions a private thread directory, obtains
the P1.3 writer lease, durably appends one user message to the thread's
canonical JSONL, and only then publishes `thread.meta.json` to the catalog.
`resume --prompt` uses the same append seam. P2 consumes this exact history; it
does not convert a personal chat session or replay a display projection.

## Selection contract

With no selector, `mintclaw resume` lists a bounded first page for the current
canonical project. The admitted mappings are:

| CLI input | P1.2 catalog query | Result |
| --- | --- | --- |
| `resume` | current `project_key` | plain or JSON page |
| `resume --all` | all projects | plain or JSON page |
| `resume --last` | current project plus `Last` | selected thread |
| `resume --all --last` | all projects plus `Last` | selected thread, still subject to location validation |
| `resume <uuid>` | exact ID | selected thread, no scan |

`--offset` and `--limit` are list-only. An exact ID cannot be combined with
`--last` or `--all`. `--model` and `--prompt` require an exact ID or `--last`;
they never mutate an arbitrary row merely because it happened to be first in a
page.

Selection never silently crosses project authority. Before a selected thread
is leased, MintClaw inspects its persisted project root and invocation cwd:

- a different live project reports both roots and asks the operator to change
  directory;
- a missing or moved root reports that explicit relocation is required; and
- an unknown UUID suggests current-project and all-project list commands.

`--all` broadens discovery, not tool cwd authority. Even `--all --last` cannot
resume a thread from an unrelated checkout. A later relocation command must
make any project rebind explicit.

## Persistence and mutation order

All MintClaw-owned files remain below:

```text
${MINTCLAW_HOME:-~/.mintclaw}/coding/threads/<thread-id>/
  thread.meta.json
  thread.lock
  sessions/
    coding_<thread-id>.jsonl
    coding_<thread-id>.meta.json
```

Nothing is created in the project. Each thread has a distinct `sessions/`
root, so neither canonical JSONL nor future Seahorse state accumulates in one
global coding database.

Before the first write and before every resume mutation, the command binds the
thread root into the admitted coding `RuntimeLayout`. Its physical and
symlink-aware containment validation rejects an overridden MintClaw home that
equals or descends from the canonical project root. Rejection is side-effect
free; it cannot create the in-project state path while discovering the error.

Canonical append requires a live, matching `thread.Lease`. The lease guards
append against concurrent `Release`, rejects a token after release, and cannot
write another thread or the same thread ID in another store. Prompts are
required, valid UTF-8, and bounded to 1 MiB. Supplying `--prompt` with an empty
value is therefore an error rather than a metadata-only resume. The existing
thread store durably creates `sessions/` below the already-provisioned thread
root before opening the JSONL store. The JSONL turn journal then fsyncs the
accepted user message and, for the first session record, its new file entry
before append returns.

An uncommitted lease or append failure leaves only an unpublished private
directory, which list and exact-ID catalog queries cannot select. A file or
partial write or file/directory fsync failure after `Write` is reported as
indeterminate and unsafe to retry, but does not publish new thread metadata.
Once the JSONL line and every new directory entry are durable, a later close,
journal-metadata, thread-metadata, or lease-finalization failure is reported
as a typed committed-prompt error with the thread ID and explicit do-not-retry
guidance.
Before any later append under the session lock, a dirty journal is reconciled
with completed JSONL records and an incomplete trailing fragment is durably
truncated to the last newline. A different later prompt therefore cannot be
concatenated onto or hidden behind a failed partial record.

Catalog metadata is only a selection hint. Once a thread ID is selected,
`resume` acquires its writer lease and reloads the authoritative metadata under
that lease before location checks or mutation. A writer that updated the thread
between catalogue selection and lease acquisition therefore cannot have its
model, preview, project observations, or timestamps overwritten by a stale
snapshot.

Prompt bounds and the complete next metadata snapshot—including preview,
model, current project observations, timestamp, and runtime layout—are
validated before lease acquisition and before canonical append. Invalid input
therefore cannot leave an orphan metadata-only thread or a committed prompt
whose requested metadata was never admissible.

On resume, canonical prompt append precedes the metadata preview/timestamp
update. If the subsequent cross-file metadata write fails, the command reports
that the prompt was committed and explicitly warns against blind retry. This
preserves JSONL authority and avoids pretending that two files form an atomic
transaction.

Model selection is persisted in metadata. There is no approval or `--yolo`
plumbing in this packet: no tools execute yet, and the admitted coding runtime
profile is permissive by default rather than requiring a complex approval
state machine.

## Rendering and startup output

The default P1 renderer is stable plain text. `--json` emits one JSON document
and no decorative prefix. List output exposes bounded catalog metadata and
pagination/truncation fields; it never loads transcripts.

Once a prompt is committed, result construction and plain/JSON output failures
retain the typed committed-prompt state and do-not-retry guidance. A broken
stdout consumer therefore cannot turn a completed `code` or `resume --prompt`
operation into an apparently safe retry.

The process-level ASCII banner, timezone diagnostics, and console logger are
suppressed when the root command is `code` or `resume`. This keeps today's
plain/JSON output clean and reserves the same entrypoints for P4's alternate
screen without a second command migration. Other root commands retain their
existing banner and help behavior. A configured `TZ` still updates
`time.Local`; only its human diagnostics are suppressed.

## Evidence

Automated tests prove:

- a new command instance creates a thread and a later command instance lists,
  resumes, and appends to it;
- the restarted canonical history contains exactly the accepted prompts in
  order;
- thread state resolves below the injected MintClaw home while the project
  remains empty;
- a MintClaw home overridden inside the project is rejected before any state
  directory is created;
- separate threads use separate canonical session roots;
- `--all`, current-project `--last`, exact ID, model override, prompt append,
  offset, and limit semantics are deterministic;
- cross-project, moved-project, unknown-ID, selector-conflict, and live-owner
  errors are actionable;
- catalog listing succeeds without taking the writer lease;
- selected metadata is reloaded under the writer lease before resume mutation;
- released, wrong-thread, and same-ID/different-store lease tokens cannot
  append;
- lease and ordinary append failures during creation do not publish selectable
  metadata, while post-fsync failures retain committed-prompt classification;
- dirty metadata and incomplete trailing JSONL fragments are reconciled before
  a later distinct append;
- output writer failures after `code` and `resume --prompt` preserve typed
  committed-prompt errors;
- canceled, empty, invalid, and oversized prompt appends fail before success;
- root command registration and startup-output detection retain existing help
  expectations; and
- focused packages pass ordinary and race tests and cross-compile for Darwin,
  Linux, and Windows with the repository's `goolm,stdjson` build tags.

P1 therefore exits with durable project identity, bounded discovery,
single-writer ownership, canonical append, and restartable command plumbing.
P2 can attach the native coding agent loop without changing the CLI selector,
state layout, session key, or frontend protocol.
