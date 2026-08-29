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
atomically publishing its thread manifest entry. Copied manifests record no
source path. External mode additionally stores a canonical absolute
caller-owned path plus its admitted size and digest; the source must still be
available and unchanged whenever selected, and MintClaw never claims or deletes
that file. Consumers receive the immutable snapshot path only after the
external availability check, so they never reopen the verified caller path.

A `media://coding-attachment/<uuid>` reference is meaningful only through the
manifest of its owning thread. Knowing another thread's reference does not
grant access to its blob. Blob filenames are content identities, not authority
tokens.

The admission layer is bounded to 32 MiB per file, 1,024 manifest entries per
thread, and a 1 MiB manifest. Inputs must be stable singly linked regular files.
Resolution rechecks type, path identity, size, and digest. Missing, replaced,
changed, or corrupt state fails closed and is reported as unavailable; readers
do not repair or rewrite it.

## Deduplication, deletion, and garbage collection

Identical copied bytes share one blob across threads. Trashing a thread moves
its manifest as recoverable MintClaw-owned state but never removes shared blob
bytes. A later garbage collector must mark references from both active and
recoverable-trash manifests before sweeping. It must fail closed on corrupt,
unreadable, or concurrently changing manifests and must never traverse external
paths. Until that collector lands, unreferenced blobs are retained.

Forking must create a new thread manifest containing only references reachable
from the copied transcript boundary. Copied blobs remain shared; external paths
retain their explicit availability semantics. Fork and garbage-collection
lifecycle integration belongs to a later P6.3 PR built on this storage layer.

## Prompt and runtime boundary

Admission alone does not add historical bytes to model input. A later P6.3 PR
will convert selected file, pasted-log, and supported-image inputs into the
canonical user message, inject a thread-bound resolver into the coding runtime,
and account for every selected attachment in prompt limits. Historical media is
resolved only when selected or contextually required. Missing media becomes a
bounded diagnostic message and does not corrupt or truncate canonical history.

The TUI, CLI flags, canonical-message integration, provider representation,
fork reachability, retention/GC command, and final roadmap exit record are
therefore intentionally outside this foundation PR.
