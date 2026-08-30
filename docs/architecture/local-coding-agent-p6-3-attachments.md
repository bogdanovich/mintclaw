# P6.3 rich input and attachment contract

Status: implementation contract; P6.3 remains open until the roadmap exit
record is merged.

This contract keeps rich coding input durable without replaying file or image
bytes in every later model request. It also keeps attachment authority inside
the selected coding thread and outside the checked-out project.

## Ownership and layout

The external coding state root owns two distinct layers:

```text
<coding-root>/
  blobs/sha256/ab/<full-sha256>       immutable, shared copied bytes
  threads/<thread-id>/
    attachments/manifest.json        thread-owned references and presentation data
```

Every admission publishes a verified immutable snapshot by SHA-256 before
atomically publishing its thread manifest entry. Manifests never record the
caller-owned source path. After admission, changing, moving, or deleting the
source does not affect the attachment; consumers resolve only the immutable
MintClaw-owned snapshot.

A `media://coding-attachment/<uuid>` reference is meaningful only through the
manifest of its owning thread. Knowing another thread's reference does not
grant access to its blob. Blob filenames are content identities, not authority
tokens.

The admission layer is bounded to 32 MiB per file, 32 files and 64 MiB per
atomic turn batch, 1,024 manifest entries per thread, and a 1 MiB manifest.
Inputs must be stable singly linked regular files. A batch verifies every
source before publishing one manifest update, so a failed later input cannot
strand a partial set of thread-owned references. Resolution reads through
pinned direct store directories and returns verified bytes rather than a
pathname that a consumer must reopen. It rechecks type, directory and file
identity, size, and digest. Missing, replaced, changed, or corrupt state fails
closed and is reported as unavailable; readers do not repair or rewrite it.

## Content deduplication, deletion, and garbage collection

Identical bytes share one blob across imports and threads, while every explicit
admission receives a new thread-owned reference. Trashing a thread moves its
manifest as recoverable MintClaw-owned state but never removes shared blob
bytes. A later garbage collector must mark references from both active and
recoverable-trash manifests before sweeping. It must fail closed on corrupt,
unreadable, or concurrently changing manifests. Until that collector lands,
unreferenced blobs are retained.

Forking must create a new thread manifest containing only references reachable
from the copied transcript boundary. Blobs remain shared. Fork and
garbage-collection lifecycle integration belongs to a later P6.3 PR built on
this storage layer.

## Prompt and runtime boundary

The coding runtime admits one structured turn batch before dispatch and stores
only its canonical placeholders and thread-owned references in JSONL. Image
inputs use `[image: filename]`; other files and pasted logs use
`[file: filename]`. Caller paths never enter canonical history. Prompt text and
the generated placeholder content remain inside the canonical 1 MiB UTF-8
bound.

The agent media boundary resolves a current attachment only through the
selected thread manifest, verifies its immutable bytes, and materializes it in
a random process-private directory for provider adaptation. Closing the coding
runtime removes that temporary view. Another thread's reference, a missing
blob, or a replaced temporary hierarchy fails closed. If history proves that a
turn was not stored after admission, the runtime atomically removes exactly the
new references without deleting shared blobs. If post-turn history cannot be
read, it conservatively retains them because the prompt may have committed.

This checkpoint does not yet define historical selection or complete media
token accounting. Until those land, the existing generic media adapter may
resolve historical references to path tags while building a later request.

The plain and interactive command seams accept repeatable local file inputs:

```text
mintclaw code "inspect this failure" --attach build.log --attach screenshot.png
mintclaw code --attach build.log --json
mintclaw resume <thread-id> --prompt "compare this run" --attach latest.log
```

`--attach` order is preserved and an attachment-only turn is valid. On an
interactive command, the initial structured input crosses the same controller
boundary as a composer submission. On a plain or JSON command, it crosses the
same native runtime boundary directly. The Codex-like TUI presentation,
historical selection, fork reachability, retention/GC command, and final
roadmap exit record remain later P6.3 work.
