# Local Coding Agent P1.3 Cross-Process Thread Lease

Status: implemented

Roadmap packet: P1.3 in
[`local-coding-agent-roadmap.md`](local-coding-agent-roadmap.md)

## Decision

MintClaw admits `thread.Store.AcquireLease` as the exclusive writer boundary
for one existing coding thread. The returned `thread.Lease` must remain alive
for every transcript mutation and is released explicitly when the frontend or
delegated coding run stops owning that thread.

The operating-system advisory lock is the sole authority. `thread.lock`
contains a small diagnostic owner record, but the presence, age, PID, or
contents of that record never grants ownership and never causes MintClaw to
break a live lock.

This keeps three responsibilities separate:

- P1.2 catalog reads `thread.meta.json` without taking or inspecting a lease;
- P1.3 serializes future writers without opening canonical JSONL; and
- P1.4 and later runtime entrypoints must acquire the lease before append.

## On-disk contract

Each thread owns one file outside canonical transcript content:

```text
~/.mintclaw/coding/threads/<thread-id>/thread.lock
```

The bounded version-1 owner record is:

```json
{
  "schema_version": 1,
  "pid": 12345,
  "hostname": "operator-host",
  "acquired_at": "2026-08-10T09:30:00Z"
}
```

The record is limited to 4 KiB, rejects unknown fields when read, requires a
positive PID and UTC timestamp, and bounds the optional hostname to 255 bytes
of trimmed UTF-8. It deliberately omits argv, cwd, environment, project path,
prompt text, credentials, and transcript content.

The owner writes this record only after obtaining the OS lock and syncs it
before returning the lease. A contending process reads it only after the OS
reports that the lock is busy. Because the record shares the locked inode, a
contender may observe a partial or corrupt record during an owner rewrite; in
that case MintClaw still returns the typed busy error and safely omits owner
details.

Clean release leaves the last diagnostic record in place. This is intentional:
replacing or deleting the locked pathname would create a second inode and
weaken exclusivity. The next successful owner overwrites the stale record
while holding the same lock.

## Platform behavior

On Unix, MintClaw opens `threads/`, the UUID thread directory, and
`thread.lock` through directory handles with no-follow and nonblocking flags.
The lock file must be a regular file with exactly one link, is forced to mode
`0600`, and is acquired with nonblocking `flock(LOCK_EX)`. FIFOs, devices,
directories, symbolic links, and multiply linked files are rejected before
locking or writing.

On Windows, MintClaw opens the thread directory and lock relative to parent
handles through `NtCreateFile`, uses `OBJ_DONT_REPARSE` and
`FILE_OPEN_REPARSE_POINT`, and rejects reparse points from opened-handle
attributes. It also requires exactly one hard link before applying security or
writing diagnostics, so an NTFS hard link cannot redirect those mutations to
another file. `LockFileEx` acquires one byte nonblockingly. The file receives a
TokenUser owner SID and protected owner-only DACL in one handle-based security
update; both are validated from the opened handle before use. This remains
correct when an elevated token's default TokenOwner differs from TokenUser.
The locked byte sits immediately beyond the maximum owner-record range, so
Windows contenders can read diagnostics without crossing the mandatory
byte-range lock; delete sharing remains disabled while the lease is live.

Other Go targets compile with an explicit unsupported-platform result; they
do not silently pretend to provide a cross-process lock. The supported release
targets are Unix and Windows.

## Contention and recovery

Contention is immediate rather than waiting invisibly. Callers receive
`ErrLeaseBusy` through `LeaseBusyError`, including the canonical thread ID and,
when a valid record was readable, owner PID and optional hostname. CLI/TUI
code can therefore report who owns the thread and offer read-only inspection,
cancellation, or a later explicit takeover workflow without guessing that a
PID is dead.

No PID liveness probe or timestamp timeout participates in recovery. Those
checks race with PID reuse and cannot prove ownership. If a process crashes,
the kernel closes its file handle and releases the advisory lock. A successor
then acquires the same file and overwrites the stale diagnostic record. This
recovers automatically without ever allowing two cooperating writers.

`Lease.Release` is idempotent. It unlocks before closing the handle and returns
any release error. A failed owner-record write releases and closes the lock
before returning, so an initialization error cannot strand ownership.

## Evidence

Focused contract tests prove:

- a second independently opened handle cannot acquire a live lease;
- a typed busy error includes the exact owner record when safely readable;
- release is idempotent and a successor can reacquire immediately;
- a real child process excludes the parent, exposes its real PID, and a forced
  child crash releases the lock for the parent;
- malformed stale owner content is overwritten after a legitimate acquire;
- current-project read-only catalog listing succeeds while the thread is leased;
- missing threads and invalid owner records fail closed;
- Unix lock files are private and symbolic links, hard links, and FIFOs are
  rejected without blocking;
- Windows hard links are rejected before security or content mutation, with a
  native NTFS regression in the Windows CI job; and
- the package passes race tests plus Darwin, Linux, and Windows compile checks.

The tests exercise lease ownership itself rather than transcript writes
because P1.4 introduces the first coding-thread writer entrypoint. That
entrypoint's acceptance test must hold this lease across every append; it may
not add a second concurrency mechanism or infer ownership from `thread.lock`
JSON.
