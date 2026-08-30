# Local Coding Agent P6.3 Exit Record

Roadmap packet: [P6.3 — Rich input and attachments](local-coding-agent-roadmap.md#p63--rich-input-and-attachments).

The merge containing this record closes P6.3. Coding turns can carry files,
large pasted text, and supported images through one structured input boundary,
while durable bytes remain outside project files and historical provider
prompts.

## Implementation sequence

The merged implementation is split into independently reviewed checkpoints:

- [#983](https://github.com/bogdanovich/mintclaw/pull/983), merge
  `4717ddb0`, added the content-addressed attachment store, thread manifests,
  immutable copied bytes, project-external ownership, and bounded admission;
- [#990](https://github.com/bogdanovich/mintclaw/pull/990), merge
  `9bf9ec67`, introduced structured turn input without changing canonical text
  history;
- [#991](https://github.com/bogdanovich/mintclaw/pull/991), merge
  `997b982f`, made multi-file admission and manifest publication atomic;
- [#992](https://github.com/bogdanovich/mintclaw/pull/992), merge
  `f9926761`, connected admitted references to native runtime turns and defined
  rollback versus indeterminate post-turn outcomes;
- [#993](https://github.com/bogdanovich/mintclaw/pull/993), merge
  `0f71e585`, added repeatable `--attach` inputs to `code` and `resume`;
- [#994](https://github.com/bogdanovich/mintclaw/pull/994), merge
  `94201b3b`, added the rich TUI composer, large-paste spooling, compact labels,
  `/attach`, and temporary-file ownership cleanup;
- [#995](https://github.com/bogdanovich/mintclaw/pull/995), merge
  `ae12df80`, kept historical attachments lazy and added bounded, thread-scoped
  `coding_attachment` list/open selection;
- [#996](https://github.com/bogdanovich/mintclaw/pull/996), merge
  `ee9ebbb9`, added bounded asynchronous PNG clipboard input without native
  clipboard transcoding;
- [#997](https://github.com/bogdanovich/mintclaw/pull/997), merge
  `99b196c9`, preserved only transcript-reachable attachment references across
  thread forks while sharing immutable blobs;
- [#998](https://github.com/bogdanovich/mintclaw/pull/998), merge
  `b41334bf`, added retention-aware, reference-safe garbage collection,
  in-flight publication markers, identity-bound quarantine, and crash
  recovery; and
- [#999](https://github.com/bogdanovich/mintclaw/pull/999), merge
  `89e80b6d`, completed image-with-text provider input, supported-byte
  verification, restart selection, media accounting, and missing-blob
  diagnostics.

The resulting behavior is specified by the
[P6.3 attachment contract](local-coding-agent-p6-3-attachments.md).

## Requirement evidence

### Rich input and bounded canonical state

- Plain, JSON, and interactive commands use the same structured turn boundary.
  Repeatable file input preserves order, and an attachment-only turn is valid.
- The composer displays compact labels such as `[Pasted Content N chars]` and
  `[Image #N]` while retaining the payload separately. Removing a label removes
  its pending payload; failed admission keeps it for retry; successful
  admission deletes only composer-owned temporary files.
- Canonical JSONL stores bounded `[file: name]` or `[image: name]` placeholders
  plus opaque thread-owned references. It stores neither caller paths nor
  historical base64.
- Per-file, batch-count, aggregate-byte, manifest-entry, manifest-byte,
  canonical-prompt, text-page, and media-prompt limits are enforced before an
  unbounded expansion can commit.

Representative coverage includes `TestNativeRuntimeAdmitsAttachmentAndStoresExactReference`,
`TestAttachmentBatchRejectsUnboundedCountBeforeReading`,
`TestAttachmentBatchBoundsAggregateBytesWithoutOverflow`,
`TestComposerSpoolsLargePasteAndKeepsItWhenSubmissionFails`, and
`TestComposerHistoryNavigationCannotDetachPendingAttachment`.

### Durability, privacy, and authority

- Admission copies a stable regular file into MintClaw's external SHA-256 blob
  store before publishing a manifest reference. Later caller-path mutation or
  deletion cannot alter the attachment, and caller paths are not retained.
- Direct, pinned store and thread authorities reject symlinks, FIFOs, detached
  or replaced directories, changed identities, digest mismatches, corrupt or
  oversized manifests, cross-thread references, and released writer leases.
- Admission and resolution do not write into the checked-out project. An
  attachment reference grants access only through the selected thread's
  manifest, preserving project-private discovery and coding-thread writer
  authority.
- Identical bytes deduplicate by digest, but each admission receives an
  independent thread reference. Removing a reference does not directly unlink
  shared bytes.

Representative coverage includes
`TestAttachmentSurvivesSourceChangesAndRestartWithoutPathDisclosure`,
`TestAttachmentAdmissionRejectsUnsafeOrOversizedInput`,
`TestAttachmentAdmissionRejectsSymlinkedBlobHierarchyDuringOpen`,
`TestResolveAttachmentRejectsReplacedBlobHierarchy`,
`TestAttachmentReferenceIsThreadScoped`, and
`TestCodingAttachmentMediaRejectsAnotherThreadReference`.

### Lazy history, selection, recovery, and accounting

- Current verified PNG, JPEG, GIF, or WebP inputs can accompany ordinary text
  in provider vision. Unsupported SVG or malformed image-labelled bytes remain
  path-only files.
- Historical attachment references remain in canonical history but are not
  resolved, materialized, or base64-encoded into later provider requests.
  `coding_attachment` exposes a bounded metadata catalog and loads an exact
  thread-authorized image or UTF-8 text page only when the model selects it.
- Current and selected image media use the existing media token cost; selected
  text contributes its actual bounded content.
- After restart, the manifest remains authoritative. Missing, corrupt,
  foreign-thread, binary, or invalid selected content becomes an explicit tool
  diagnostic; canonical history remains readable and later turns continue.

The native multi-process
`TestNativeCodingAttachmentsRemainLazySelectableAndDiagnosableAcrossRestart`
proves current image-with-text delivery, absence of eager historical image
bytes, explicit selection after restart, media accounting, a deleted blob on a
second restart, and continued thread usability. It is complemented by
`TestNativeCodingCaptionedUnsupportedImagesStayOutOfProviderVision`,
`TestResolveMediaRefsKeepsHistoryLazyAndAccountsForSelectedToolImage`, and the
bounded `coding_attachment` list/open tests.

### Forking, deletion, and garbage collection

- Fork publication copies only references reachable from the selected
  transcript boundary. A reference missing from the source manifest fails
  closed; source and child retain independent authority over shared blobs.
- Trashing or deleting one thread does not delete blob bytes. The explicit
  `threads gc` operation marks active manifests, recoverable trash,
  fork-preparation quarantine, and live in-flight publication before sweeping
  only old unreferenced blobs.
- GC is dry-run by default, requires an exact confirmation phrase for deletion,
  is bounded, serialized across processes, and fails closed on malformed,
  missing, replaced, changing, over-limit, or unknown authority state.
- Deletion binds to the verified blob inode through same-shard quarantine and
  an OS lock. A live owner prevents recovery; a crashed owner releases the lock
  so a later pass can recover without deleting newly referenced bytes.

Representative coverage includes
`TestForkThreadCopiesOnlyReachableAttachmentsAndSharesBlobs`,
`TestAttachmentGarbageCollectionKeepsDigestReferencedByAnotherThread`,
`TestAttachmentAdmissionKeepsInflightAuthorityThroughManifestCommit`,
`TestAttachmentGarbageCollectionRechecksInflightAfterDetach`,
`TestAttachmentGarbageCollectionDoesNotRecoverLiveQuarantine`, and
`TestAttachmentGarbageCollectionRecoversInterruptedQuarantineAfterRestart`.

## Validation and review gates

Every implementation PR above is merged. Each final head passed the repository
nine-check matrix: linter, security, tests, race, Darwin and Windows
compilation, macOS portability, integration tests, and browser tests. Each
merged exact head also has an automated reviewer result tied to that SHA and an
owner PR-level rocket approval; all substantive review findings were fixed and
their threads resolved by the implementing agent before merge.

The sequence added focused race coverage around attachment admission,
selection, fork publication, maintenance locking, in-flight recovery, and GC.
Portability was exercised on Linux, Darwin, Windows, and AIX implementation
boundaries, including native file identity and lock implementations.

## Exit-gate decision

The P6.3 roadmap statement is satisfied:

- old images and logs are loaded only when explicitly selected or required by
  the current turn, never replayed as historical base64 by default;
- missing attachments produce bounded diagnostics and do not corrupt or block
  the coding thread;
- prompt accounting includes current and selected image media and selected
  text; and
- deleting one thread or reference cannot remove a blob still protected by an
  active, trashed, forked, or in-flight authority.

P6.3 does not add workspace checkpoints, filesystem rewind, git review and
change summaries, LSP integration, or live-agent/remote coding delegation.
Those remain owned by P6.4 and later roadmap packets.
