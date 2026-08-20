# Browser Current-Contract Cutover

Status: pre-deployment evidence

Audit date: 2026-08-20

This record covers B1 of the
[Architecture Simplification Roadmap](../architecture/architecture-simplification-roadmap.md).
It verifies that historical browser schema implementations can be deleted
without converting executable production authority.

## Runtime Baseline

The read-only preflight inspected the installed `6a9ca37f` runtime and active
state under the main profile. All expected MintClaw services were active, no
failed units or legacy processes were present, and the ten-minute error window
was empty.

The live node registry contained six records. Its one browser-capable companion
advertised:

- protocol version 1;
- all seven current browser commands, including `browser.capture.v1`;
- all twelve current browser actions; and
- an approved catalog hash for that exact current catalog.

The browser state store was version 2 and contained:

- 214 sessions;
- 128 prepared actions;
- 138 invocations;
- zero prepared actions with the obsolete plaintext `dialog_message`; and
- three current dialog records carrying only digest and byte-count authority.

No browser state rewrite is required before rollout. The new reader rejects an
obsolete plaintext field instead of migrating it during startup.

## Dispatched Invocation Tombstones

The gateway invocation database contained 333 dispatched records. Of 319
browser records, 289 retained a descriptor from before the current observation
schema. These records are not prepared execution authority. They are bounded
seven-day tombstones that prevent an uncertain already-dispatched action from
being replayed.

B1 preserves that safety property without preserving any old browser schema:

- prepared records must validate against the one canonical current descriptor;
- a dispatched tombstone may retain an opaque descriptor only when its current
  execution plan, expected plan hash, command, risk, state, timestamps,
  ownership, bounded size, and canonical descriptor hash all agree; and
- normal retention deletes the tombstone after seven days.

The runtime does not interpret the opaque schema, translate it, or use it to
execute work. This boundary lets old no-replay evidence expire naturally while
all executable authority becomes current-only immediately.

## Rollout

After the implementation PR merges:

1. Create a timestamped backup of the node registry, browser state, invocation
   database, WAL and SHM files, binaries, and affected unit definitions.
2. Confirm again that the browser state contains no plaintext
   `dialog_message` field.
3. Confirm the connected browser companion advertises the current protocol-1
   command schemas. Upgrade it first if it does not.
4. Build and install the merged gateway and node binaries.
5. Restart only the affected gateway and companion units.
6. Verify node registry load, browser reconnect, current catalog approval,
   browser open/observe, and one safe read-only browser action.
7. Inspect error-level journals and a new passive diagnostic trace.
8. After seven days, confirm that no opaque pre-current browser tombstones
   remain.

## Rollback

Restore the previous binaries and the matching backed-up state together. Do not
restore mutable browser or invocation state automatically after the new runtime
has written to it; compare the backup and plan a forward recovery first.
