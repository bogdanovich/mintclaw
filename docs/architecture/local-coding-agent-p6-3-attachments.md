# P6.3 rich input and attachment contract

Status: complete. See the [P6.3 exit record](local-coding-agent-p6-3-exit.md).

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
bytes. `mintclaw threads gc` performs a store-wide dry-run by default with a
24-hour retention window. Deletion requires the exact
`--confirm delete-unreferenced-blobs` phrase and repeats the complete scan under
one cross-process attachment-maintenance lock. The retention cutoff cannot be
in the future.

The collector marks active manifests, recoverable thread trash, and quarantined
fork preparations before considering a blob. Admission, fork publication, and
active-to-trash moves share the narrow maintenance lock, closing the
blob-before-manifest and directory-move races without blocking ordinary coding
turns or attachment reads. The lease retains pinned store, lock-directory, and
lock-file identities for its lifetime; producers validate that authority around
blob, manifest, fork, and trash commits. Before publishing a blob, admission
durably publishes a bounded per-digest in-flight marker tied to its thread
writer. It removes that marker only after the canonical manifest commits under
the same maintenance authority. A collector marks live markers, reconciles
crash-stale markers under non-blocking thread authority, and rechecks the exact
digest after identity-bound detach. Manifest, marker, and blob scans are bounded
and pinned. Once blob storage exists, the active `threads/` authority is
required; trash authorities remain optional. Corrupt, unreadable, unknown,
over-limit, missing, replaced, or concurrently changing state fails closed
before deletion. A partial deletion or directory-sync failure is reported as a
committed GC outcome with exact deleted counts.

Deletion is identity-bound rather than a final name-based unlink. The collector
creates a unique same-shard hard-link quarantine for the verified candidate,
holds an exclusive OS lock on that blob inode from before quarantine
publication through cleanup or rollback, durably detaches its digest name,
then revalidates lifecycle authority and removes only that pinned identity. If
authority changed, it restores or reconciles the canonical digest before
returning an uncommitted error. A later dry-run or delete pass first acquires
the same inode-bound lock before recovering an interrupted `.gc-…` shard entry.
A live owner therefore makes replacement-authority recovery fail closed, while
a crash releases the lock so recovery cannot strand referenced bytes.

Forking reads the source transcript and manifest under the same thread lease,
then publishes a child manifest containing only references reachable from the
copied transcript boundary. A durable reference present in the selected
history but absent from the source manifest fails closed before the child is
published. Child and source manifests retain independent authority while their
immutable blobs remain shared; deleting the source therefore does not break a
forked attachment. The store-wide collector treats both manifests as
independent authorities and retains their shared digest until neither active
nor recoverable state references it.

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
attachments are still verified and resolved normally. The coding media store
explicitly opts a current supported image into provider vision even when its
user turn also contains text; generic channel media retains its conservative
image-only default. An old image explicitly selected by the attachment tool
also crosses the normal provider-media boundary. Current and selected image
media therefore contribute the existing media cost to prompt token accounting;
selected UTF-8 text contributes its actual bounded content.

The coding-only `coding_attachment` tool exposes a thread-authorized metadata
catalog without paths or blob reads. `list` returns bounded, newest-first
metadata and an optional filename filter. `open` accepts only an exact
reference returned by that catalog: images become current tool-result media,
while UTF-8 files return at most 64 KiB per page on valid character boundaries.
This lets a user ask to inspect a screenshot or log from an earlier turn,
including after compaction, without replaying every historical attachment in
every prompt. Missing, corrupt, foreign-thread, or non-UTF-8 content produces a
tool error and leaves canonical history and attachment state unchanged.

After process restart, the manifest remains the only authority for resolving a
canonical attachment reference. Historical references stay lazy until
selected. If a selected blob is missing or invalid, the tool returns an
explicit unavailable diagnostic to the model; the canonical turn remains
readable and the thread can continue with later turns rather than failing
resume.

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

Restart and missing-state behavior is covered by the native multi-process
coding scenario and recorded in the P6.3 exit evidence.
