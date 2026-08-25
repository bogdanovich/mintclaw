# Local Coding Agent P2.2 Project Instructions

Status: implemented

Roadmap packet: P2.2 in
[`local-coding-agent-roadmap.md`](local-coding-agent-roadmap.md)

## Decision

Coding runtimes load declarative project instructions independently from the
personal-agent bootstrap. A coding thread never loads personal `SOUL.md`,
`USER.md`, skills, hooks, or executable repository configuration as project
instructions. A root `AGENTS.md` is admitted only through the coding project's
hierarchical instruction rules, not through the personal bootstrap loader.

MintClaw recognizes these filenames, in descending same-directory priority:

1. `AGENTS.override.md`
2. `AGENTS.md`
3. `CLAUDE.md`

Exactly one file is selected in a directory. If `AGENTS.override.md` exists,
neither `AGENTS.md` nor `CLAUDE.md` in that directory is read. If `AGENTS.md`
exists, `CLAUDE.md` is ignored. An unreadable or unsafe higher-priority file is
reported and still shadows the lower-priority names; MintClaw does not silently
bypass it.

`CLAUDE.md` is an intentional MintClaw compatibility fallback. Stock Codex
does not load it by default; Codex users can configure additional fallback
filenames through `project_doc_fallback_filenames`. See the official
[Codex `AGENTS.md` documentation](https://learn.chatgpt.com/docs/agent-configuration/agents-md).

## Root And Scope Precedence

The coding frontend admits these ordered instruction locations:

1. the global coding configuration directory,
   `~/.mintclaw/coding/config/` under the selected MintClaw home;
2. the project root; and
3. the invocation working directory when it is below the project root.

At initial prompt construction MintClaw selects one global instruction file,
then walks every directory from the project root to the invocation cwd. Blocks
are rendered from lower to higher precedence:

```text
global coding instructions
project-root instructions
...
invocation-cwd instructions
```

A repository or nested block applies only to its directory and descendants.
Later applicable blocks override earlier blocks. Scope and source path are
included next to every block, so instructions discovered together for a
recursive search or command are not promoted to global rules.

## Late Discovery Barrier

Initial context does not eagerly scan unrelated repository subtrees. Before a
path-aware coding tool first accesses a path with previously unseen, more
specific instructions, MintClaw returns those instructions as a tool result and
does not execute that tool call. The model must review the scoped rules and
retry. This makes late discovery safe for writes and patches, not merely reads.

The current path projection is:

| Tool | Instruction scope inspected before execution |
| --- | --- |
| `read_file`, `write_file`, `append_file`, `load_image`, `send_file` | Exact target ancestry |
| `list_dir` | Listed directory ancestry |
| `search_files` | Target ancestry plus bounded nested instruction scan |
| `apply_patch` | Every parsed patch target ancestry |
| `exec run` | Explicit/default cwd ancestry plus bounded nested instruction scan |

Relative filesystem paths, patch targets, and command working directories are
resolved explicitly from the invocation cwd before both instruction projection
and tool execution. `search_files` targets that resolve to a file use that
file's containing-directory scope without a recursive directory scan.

When one call in a provider tool-call batch opens an instruction barrier,
remaining calls in that batch are deferred. They can be retried after the next
model step. This prevents later writes in the same batch from racing ahead of
new rules.

Each delivered document and warning carries a content-identity marker. The
active turn and retained raw history use those markers to avoid repeating the
same instruction. A changed file receives a new identity and is delivered
again.

## Bounds, Cache, And Failure Reporting

Defaults are deliberately finite:

- 16 KiB per selected instruction file;
- 64 KiB total instruction content per assembly; and
- 4,096 directories per recursive instruction scan.

When the total byte budget is exhausted, more-specific documents are retained
before less-specific documents. Truncated and omitted documents, unreadable
files, unsafe symlinks, outside-root paths, and bounded recursive scans are
reported in the model-visible instruction section. Invalid UTF-8 cut points are
removed rather than emitting broken text.

File reads are cached by logical path plus resolved filesystem identity, size,
mode, and modification time. Creation, deletion, replacement, or ordinary
content modification invalidates the selected file or its cached contents. The
coding static prompt is assembled on each provider request so a changed
instruction cannot remain hidden behind the personal prompt cache.

## Symlink And Authority Rules

Instruction discovery uses the already admitted, absolute runtime roots. A
selected instruction symlink must resolve inside its global or project
instruction authority. A symlink that escapes that root, a broken symlink, a
directory masquerading as an instruction file, or an ambiguous read is
reported and not loaded. Recursive scans do not follow directory symlinks.

Tool targets are canonicalized for scope discovery, including resolution
through the nearest existing ancestor for a path that will be created. This
prevents a file or directory symlink from bypassing nested instructions and
reports canonical paths that escape the project root. Delivery identity also
includes the logical source and scope, so two allowed scoped symlinks to the
same instruction content remain independent rules.

Instruction discovery does not broaden tool filesystem authority and does not
load executable extensions. Coding-root enforcement for all filesystem and
command tools remains the separate P2.4 packet.

## Done Evidence

P2.2 is complete when tests prove:

- one-file same-directory selection, including the `CLAUDE.md` fallback;
- global, repository, and root-to-cwd ordering;
- sibling scopes stay independent and recursive scopes remain labeled;
- unseen nested rules stop a write before mutation and allow an informed retry;
- active-history deduplication and changed-file redelivery;
- per-file, total-byte, and recursive-scan bounds with visible diagnostics;
- unsafe and broken symlink rejection; and
- prompt refresh plus invocation-cwd propagation through the production coding
  runtime.
