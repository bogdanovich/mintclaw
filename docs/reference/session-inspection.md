# Passive Session Inspection

`pkg/memory.JSONLSessionFiles(key)` returns the canonical history and metadata
filenames used by the native JSONL store and snapshot builder. It performs no
filesystem access and grants no access authority. Invalid UTF-8, blank keys and
NUL bytes are rejected; the caller must still anchor access to an admitted
session directory and verify the expected native session/owner scope.

`pkg/memory.InspectJSONLSnapshot(ctx, key, metadata, history)` decodes supplied
bytes without constructing a store, creating directories, taking runtime locks,
running recovery or writing files. It shares the committed-message decoder with
the active native reader. Scope and freshness are caller responsibilities, not
properties established by this pure parser.

Admission is bounded to 2 MiB of combined metadata/history and 256 raw records,
including logically skipped records. The metadata key must match the expected
key; dirty state is unavailable, never repaired. Metadata validation, record
decoding, skip behavior, transient-thought filtering and exact record-count
matching follow the native reader. Missing, malformed or count-mismatched input
returns an error and no partial inspection. Cancellation is honored. Clean empty
snapshots are supported. Input buffers are not modified.

For a live passive observer:

1. Establish ownership and the physical directory through native admission.
2. Read bounded regular files using anchored access; do not construct a store to
   discover paths or accidentally trigger its recovery behavior.
3. Inspect raw bytes for applicable private values before decoding/projection.
4. Capture stable metadata before/after the history read. A concurrent write,
   dirty state or incomplete capture remains unavailable rather than successful.
5. Bind the decoded metadata/history to the observed native session, owner and
   operation; retain only the necessary bounded evidence.

The API does not certify hostile filesystem mutation, quiescence, complete model
usage, interaction completion or tool execution by itself. Core eval consumers
must independently join the delivered question, terminal interaction record and
ordered canonical tool-call/result evidence. It is also a prerequisite for the
bounded restart/compaction observation packs, not their implementation.
