# Node JSON Canonicalization V2 Cutover

Status: protocol-v2 fleet deployed; retained-record compatibility remains.

## Deployment

On 2026-09-04 local time (2026-09-05 UTC), commit
`82d7b398106b283c74c2c810f560005a9282040a` was built from a clean,
fast-forwarded production checkout. The gateway, node companion, and launcher
binaries were installed atomically. Only their affected user services were
restarted.

The rollout followed gateway-first ordering. The old local companion first
reconnected to the new gateway with protocol v1 and an approved catalog,
demonstrating the bounded compatibility path. The companion was then stopped,
its eight terminal successful v1 ledger records were archived, and the v2
binary was started with an empty current ledger. Its previous authority was
reapproved without widening it: one alias and six allowed commands.

On 2026-09-05 UTC, the remaining macOS and Linux VPN companions were upgraded
to the same commit. Both retained their node identities, aliases, and approved
command surfaces: 24 commands for `ab-local-test` and 15 for `vpn`. The newly
advertised `system.which.v1` command on `vpn` was not approved, so the rollout
did not widen authority. Their v1 ledgers contained no live work and were
archived before the v2 processes started.

## Initial and final inventory

| Observation | Initial | Canary phase | Fleet closeout |
| --- | ---: | ---: | ---: |
| Connected nodes | 3 | 3 | 3 |
| Protocol v1 | 3 | 2 | 0 |
| Protocol v2 | 0 | 1 | 3 |
| Catalog-approved nodes | 3 | 3 | 3 |
| Nonterminal companion invocations | 0 | 0 | 0 |

At fleet closeout, the gateway invocation store contained no live nonterminal
work. Its opaque dispatched no-replay tombstones were preserved rather than
reset: 148 expired records were created under protocol v1 and four under
protocol v2. The connected fleet no longer needs v1, but the retained v1
records mean gateway v1 readers remain required until normal retention pruning
removes them.

## Verification

- the gateway reports the exact deployed commit and all expected services are
  active, with no failed units, legacy processes, or error-level journal entries
  in the final ten-minute window;
- the running core, launcher, and local companion executable hashes match the
  installed binaries, with zero service restarts after deployment;
- all three companions are connected with protocol v2 at `82d7b398`, and every
  catalog hash matches its approved hash;
- the macOS LaunchAgent and the Linux node, authority broker, and file helper
  are active with zero post-cutover restarts;
- the final Linux ten-minute journal window contains no error entries, and the
  macOS process completed its read-only smoke with no nonterminal ledger work;
- read-only `node.info.v1` calls succeeded through the ordinary gateway path on
  both upgraded companions;
- the smoke turns emitted completed, non-truncated, redacted
  `mintclaw.diagnostic_trace.v1` traces, and the durable interaction registry
  contains no pending interactions;
- all 47 deployed reviewer-runtime tests passed.

The initial gateway and canary rollback bundle remains
`/home/server/mintclaw-code-health-backup-20260905T025452Z`; all 64 of its
entries passed SHA-256 verification. The fleet closeout added four checksummed
rollback sets. The macOS set is
`/Users/ab/mintclaw-node-v2-backup-20260905T200634Z` with 32 files. Gateway
evidence is `/home/server/mintclaw-all-companions-v2-backup-20260905T200634Z`
with 62 files. The VPN host keeps the initial rollback and successful retry at
`/root/mintclaw-node-v2-backup-20260905T200634Z` and
`/root/mintclaw-node-v2-attempt2-20260905T205500Z`, with 20 and 24 files. Every
manifest passed SHA-256 verification.

The first VPN cutover rolled back automatically and exposed two
deployment-specific recovery prerequisites: stopping the node also stops its
`PartOf` authority broker, and a restored ledger must be owned by `deploy`.
The retry controlled both units and restored ownership explicitly; no state or
authority was lost.

## Rollback and remaining compatibility

Rollback must stop node admission, drain any accepted v2 work, restore the
matching companion binary and v1 ledger, and only then roll back the gateway
binary if needed. A v1 binary must never be attached to a v2 ledger, and a v2
plan hash must never be reused as v1 authority.

All connected companions now use v2. Remove gateway v1 readers only after
normal retention has removed the 148 expired v1 tombstones and a fresh deployed
inventory shows zero connected, active, or retained v1 work.
