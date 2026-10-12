# Local Coding Agent P8.3 State Lifecycle

Status: H4 implementation and qualification in progress. Production rollout
and its disposable-state canary remain required before H4 closes.

## Ownership and privacy audit

The machine-readable audit is
[`state_ownership_inventory.json`](../../pkg/coding/thread/testdata/state_ownership_inventory.json).
Its contract test compares every recognized disposable thread entry with the
deletion implementation and requires explicit privacy, export, retention and
authority facts. It is a test-only artifact; runtime code never reads it.

| Owner | Retained state | Lifecycle and export boundary |
| --- | --- | --- |
| Coding thread | Canonical JSONL, Seahorse SQLite and sidecars, memory, metadata/previews, repository/review evidence, presentation/plan, runtime registries/key, diagnostics, media, attachment references, document artifacts/write journal | Complete thread root moves to recoverable trash; explicit purge removes it. Local content remains private. Support export selects bounded metadata rather than copying this tree. |
| Shared attachment store | Immutable content-addressed blob bytes | Active, recoverable-trash and fork-quarantine references protect blobs. Separate explicit GC removes only old, unreferenced blobs. |
| Worktree owner | Allocation/handoff registry and isolated checkout | Separate conservative cleanup checks Git state and effects; thread purge has no worktree-cleanup authority. |
| Gateway and companion | Task generations, interaction registry, outbox, invocation/worker ledgers, remote thread | Each owner retains its own terminal/recovery evidence. Deleting a local thread does not erase a remote task or receipt. |
| Configured diagnostic store | Relative overrides and absolute shared trace directories | Separate age/count/byte policy applies. Only paths nested below recognized thread entries participate in automatic thread-root removal; custom top-level names fail closed. |
| User/operator | Model recents, config/auth, journals, prior support exports | Recents retain at most ten identities. Credentials, service logs and exported copies have separate removal/revocation policies. |
| Frontend/process | Picker/search/render views and pending paste/attachment drafts | In-memory views are bounded. Draft lifecycle releases temporary payloads; crash leftovers require inspection by their owner. |
| Project/external service | Source files, Git objects/refs, published commits, deployment/install effects | Their own lifecycle applies. Thread deletion cannot undo these effects. |

Canonical history and summaries are not anonymized files: an intentionally
submitted password or private source can remain in the private transcript.
Credential-bearing Git origins are rejected, protected document assignments
are replaced by value-free bindings, and runtime presentation uses the shared
redaction paths. These controls do not make the canonical transcript suitable
for public upload.

Passive diagnostic capture stays disabled in generic defaults. When enabled,
the default trace policy is bounded by 24 hours, count and byte limits.
Support bundles default to metadata, omit unrelated traces and credentials,
hash path/project identities, and declare truncation or missing evidence.
Rich redacted exports remain sensitive and require explicit selection. See
[diagnostic support bundles](../operations/diagnostic-support-bundles.md).

An absolute `diagnostics.trace_capture.state_dir` can select a shared store
outside the thread root; its traces remain governed by that store's retention
policy after thread purge. A relative override stays inside the thread, but an
unrecognized top-level directory causes delete/purge to refuse ownership.
Inspect that state and relocate only verified thread-owned data below the
standard `diagnostics` directory before retrying. Never delete a shared
directory as a workaround for an ownership refusal.
Telemetry does not add source-content export by default; provider requests
still contain the admitted task context needed to execute the user's task.

## Delete, recovery and retention

`mintclaw threads delete <thread-id>` plans the complete known external thread
root. Repeating the exact ID with `--confirm` takes the writer lease,
revalidates ownership/project boundaries, and atomically moves the root into
`coding/trash/threads/<trash-id>`. It removes the thread from the active
catalogue. A post-move durability error reports the recovery destination.

On Windows, cross-directory rename requires closing the contained writer
file. The operation retires its one-shot writer lease only after acquiring
the existing catalogue admission gate, and keeps that gate through move and
durability checks. Native writer admission cannot observe an unlocked active
thread during this handoff. A failed Windows publication attempt consumes the
lease; a later explicit retry must acquire a fresh one. NT rename uses a pinned
destination-directory handle and a simple leaf name, as required by the
[Windows rename contract](https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/ntifs/ns-ntifs-_file_rename_information).

Active canonical threads remain retained until explicitly deleted. Archiving
only changes visibility. Neither startup nor resume silently expires history.

Recoverable trash has a default eligibility age of 720 hours (30 days):

```json
{
  "coding": {
    "state": {
      "trash_retention_hours": 720
    }
  }
}
```

The supported range is 24 through 8760 hours; an omitted or zero value uses
720 hours. Eligibility is measured from the UTC timestamp in the generated
trash ID, rather than mutable filesystem modification time. Expiration makes
state eligible for explicit removal; it does not schedule an automatic purge.

```console
mintclaw threads trash purge --json
mintclaw threads trash purge --older-than 720h --confirm purge-expired-thread-trash --json
mintclaw threads gc --older-than 24h --json
mintclaw threads gc --older-than 24h --confirm delete-unreferenced-blobs --json
```

Purge is store-wide and can run outside a repository. The optional
`--older-than` overrides the configured window within the same bounds. The
confirmation phrase must match exactly; whitespace is not accepted. A dry run
does not create a store, lock directory or trash directory. A confirmed pass
rescans; it does not trust a previously displayed plan.

## Deletion contract and failure evidence

- Scans are bounded to 10,000 trash roots and 32 top-level entries per root.
  Exceeding a limit aborts the pass before deletion rather than silently
  treating a partial scan as complete.
- Each trash ID must contain canonical thread/nonce UUIDs and the generated
  UTC timestamp. Metadata must match the thread and validate normally.
  Unknown entries, unsafe metadata and linked top-level state fail closed.
- A store-wide attachment maintenance lease serializes confirmed purge with
  attachment admission, fork, trash moves and blob GC.
- Pinned directory handles and identity checks cover the store, trash
  hierarchy and candidates. All roots validate before the first removal;
  each candidate is revalidated immediately before its anchored removal.
- Project roots and Git directories are checked for overlap. Recursive
  removal remains anchored below the pinned trash directory and does not
  follow nested symlinks to external content.
- Each completed removal is directory-synced. JSON receipts contain cutoff,
  scan count, opaque candidate IDs/times, attempted IDs and completed IDs.
  Titles, project paths, prompts, commands and artifact bytes are omitted.
- A recursive removal can partly succeed before failing. The error is
  classified as committed once deletion starts; `incomplete_trash_id` names
  that uncertain root even if no complete root was removed. A sync or output
  failure also preserves the committed classification. Inspect the receipt
  and filesystem before retrying; partial roots may require owner recovery.

Purge removes thread manifests, allowing a later blob GC pass to collect
unreferenced content. It does not remove fork/GC quarantine, current threads,
credentials, previous exports, gateway/companion ledgers or project effects.
Physical secure erase and revocation of already copied bytes are outside this
filesystem lifecycle contract.

## Qualification

The focused suite checks old/recent trash, active and cross-project sentinels,
shared-blob preservation, exact confirmation, configured retention, unavailable
current project, missing-store no-op, scan limits, unknown/linked state,
maintenance contention, candidate replacement, recursive partial failure and
post-delete durability failure. Existing support-export and diagnostic tests
cover credential/header/URL/command/path/source redaction and unrelated-trace
exclusion. Native/race/full-package and Windows compile qualification are
required before publication.

Production qualification uses only a disposable synthetic coding home and
project with checksummed sentinels. It must show a dry run, explicit purge,
separate blob GC where applicable, unchanged project/source state, and a
privacy-safe receipt. Real user threads are not purge-canary targets.
