# Local Coding Agent P6.2 Historical Search

This record admits one read-only historical discovery path shared by
`mintclaw resume --search` and the interactive resume picker. It extends the
existing metadata search with bounded canonical transcript content; it does not
add a second index, mutate Seahorse, or acquire thread writer authority.

## Scope and privacy

The current canonical project key is mandatory by default. Search loads
transcript content only after valid thread metadata proves that the entry
belongs to that project. `--all` and the picker `A` action are the only ways to
include other projects. All-project results identify their project root and
remain non-resumable from the wrong current project; normal resume admission
still reloads metadata and project identity under the thread lease.

Active and archived views remain separate. `--archived` and the picker `Z`
action select archived threads; a search never silently combines both states.
Matching fields are thread ID, title, preview, project path, persisted branch,
and visible canonical message content.

## Bounded read model

One request scans at most the catalog's existing 10,000-entry bound. Candidates
are sorted newest first before content work. Metadata matches remain available
throughout that bounded catalog, while transcript search opens at most 200
newest non-metadata matches and reads at most 64 MiB total. One transcript is
limited to 32 MiB, 4,096 visible messages, the canonical 10 MiB JSONL record
limit, and a 512-byte UTF-8 excerpt. Page size, offset, query, skipped-entry
diagnostics, and rendered terminal width retain their existing bounds.

The store root, `threads`, target thread, `sessions`, session metadata, and
JSONL are opened through anchored no-follow handles. Search accepts only a
clean session whose key, count, skip, stable file identity, and canonical
records agree. Dirty, changing, corrupt, oversized, linked, missing, or
replaced transcript state is skipped without invoking JSONL recovery, opening
Seahorse, taking a writer lease, or changing any file. Cancellation is checked
through catalog iteration, transcript reads, and JSONL decoding.

Coverage fields distinguish catalog truncation from transcript thread/byte
truncation. A bounded request may therefore truthfully report partial content
coverage instead of claiming that no older match exists.

## Result and expansion contract

Every result carries the complete validated thread metadata plus match kind,
bounded excerpt, source time, and visible message number when content matched.
Metadata matches use the thread update time; transcript matches prefer the
canonical message timestamp. Results remain deterministically ordered by
thread update time and UUID, with stable offset pagination over the bounded
match set.

The picker renders the matching excerpt rather than an unrelated preview. The
`E` action expands the selected bounded excerpt and its exact source identity
without opening a controller or acquiring a lease. `Enter` retains existing
resume admission: only an available current-project result can open a writer,
and authoritative metadata, location, model, runtime layout, and lease are
rechecked after selection.

## Done criteria

- transcript-only text is discoverable from both JSON and interactive resume;
- default search returns no result content from another project;
- explicit all-project search identifies project, thread, match source, and
  time without bypassing resume project admission;
- active/archive scope, deterministic pagination, cancellation, and separate
  catalog/content truncation remain visible;
- dirty or unsafe transcript state is not repaired, followed, or exposed; and
- the selected bounded match can be expanded without mutating thread,
  transcript, derived context, or project files.
