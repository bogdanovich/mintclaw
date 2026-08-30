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

Forking reads the source transcript and manifest under the same thread lease,
then publishes a child manifest containing only references reachable from the
copied transcript boundary. A durable reference present in the selected
history but absent from the source manifest fails closed before the child is
published. Child and source manifests retain independent authority while their
immutable blobs remain shared; deleting the source therefore does not break a
forked attachment. Garbage-collection lifecycle integration remains later
P6.3 work built on this storage layer.

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

Historical coding-attachment references remain in canonical JSONL, but the
provider-bound history adapter does not resolve or materialize them on later
turns. Generic channel media keeps its existing eager behavior. Current-turn
attachments are still verified and resolved normally, including an old image
explicitly selected by the attachment tool. Selected image media therefore
crosses the normal provider-media boundary and contributes the existing media
cost to prompt token accounting; selected UTF-8 text contributes its actual
bounded content.

The coding-only `coding_attachment` tool exposes a thread-authorized metadata
catalog without paths or blob reads. `list` returns bounded, newest-first
metadata and an optional filename filter. `open` accepts only an exact
reference returned by that catalog: images become current tool-result media,
while UTF-8 files return at most 64 KiB per page on valid character boundaries.
This lets a user ask to inspect a screenshot or log from an earlier turn,
including after compaction, without replaying every historical attachment in
every prompt. Missing, corrupt, foreign-thread, or non-UTF-8 content produces a
tool error and leaves canonical history and attachment state unchanged.

The plain and interactive command seams accept repeatable local file inputs:

```text
mintclaw code "inspect this failure" --attach build.log --attach screenshot.png
mintclaw code --attach build.log --json
mintclaw resume <thread-id> --prompt "compare this run" --attach latest.log
```

`--attach` order is preserved and an attachment-only turn is valid. On an
interactive command, the initial structured input crosses the same controller
boundary as a composer submission. On a plain or JSON command, it crosses the
same native runtime boundary directly. The composer checkpoint below builds on
that shared input contract.

## Interactive composer checkpoint

The interactive composer keeps rich payloads separate from their compact
display labels. A paste longer than 1,000 Unicode characters is written to a
process-private `0600` temporary text file and displayed as
`[Pasted Content N chars]`. Pasting a quoted, shell-escaped, or `file://` path
to a PNG, JPEG, GIF, or WebP image displays `[Image #N]`; `/attach <paths…>`
atomically adds one or more local regular files and displays file labels.
`Ctrl+V` or `Ctrl+Alt+V` explicitly reads a raw PNG representation advertised
by the local system clipboard and creates the same `[Image #N]` payload without
treating a normal terminal text paste as an image. It deliberately does not ask
the clipboard backend to decode or transcode native DIB/TIFF images before
MintClaw can enforce its bounds. Clipboard access is asynchronous; unavailable,
non-PNG, oversized, or invalid clipboards fail without changing the draft, and
Enter waits for an in-flight image read. Submitting a label sends the
corresponding structured `TurnAttachment`; the label itself is not mistaken for
canonical prompt text.

Removing a label also drops its pending payload. Failed runtime admission
keeps both the draft and payload for retry. Successful admission deletes only
composer-owned paste and clipboard-image files after the runtime has copied
them, and exiting the TUI cleans up remaining composer-owned files.
Caller-owned files are never removed. Rich turns are not placed in the
in-process text-only recall ring, and text-history navigation is disabled while
a rich payload is pending, so history cannot replay a detached label as if it
still carried an attachment.

Retention/GC command, restart and missing-state closeout, and the final roadmap
exit record remain later P6.3 work.
