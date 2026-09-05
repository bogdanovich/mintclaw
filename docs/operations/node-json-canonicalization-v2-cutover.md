# Node JSON Canonicalization V2 Cutover

Status: protocol-v2 local canary deployed; mixed-fleet compatibility remains.

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

## Before and after inventory

| Observation | Before | After |
| --- | ---: | ---: |
| Connected nodes | 3 | 3 |
| Protocol v1 | 3 | 2 |
| Protocol v2 | 0 | 1 |
| Catalog-approved nodes | 3 | 3 |
| Local canary aliases | 1 | 1 |
| Local canary allowed commands | 6 | 6 |
| Current local v2 ledger records | Not applicable | 0 |

The gateway invocation store contained no prepared work. Its opaque dispatched
no-replay tombstones were preserved rather than reset: normal retention pruning
reduced the count from 237 to 131 during the observation window. Two external
v1 nodes mean gateway v1 readers remain required; this deployment does not
authorize the final zero-v1 cleanup.

## Verification

- the gateway reports the exact deployed commit and all expected services are
  active, with no failed units, legacy processes, or error-level journal entries
  in the final ten-minute window;
- the running core, launcher, and local companion executable hashes match the
  installed binaries, with zero service restarts after deployment;
- the local canary is connected with protocol v2 and its catalog is approved;
- a live gateway turn completed and emitted a non-truncated, redacted
  `mintclaw.diagnostic_trace.v1` trace with eight records;
- a read-only canary invocation reached the ordinary human-approval boundary.
  It was not approved or executed; `/stop` durably cancelled the interaction,
  leaving zero nonterminal interactions;
- all 47 deployed reviewer-runtime tests passed.

The checksummed rollback bundle is
`/home/server/mintclaw-code-health-backup-20260905T025452Z`. It contains the old
and new binaries, user units, relevant state files, the archived v1 companion
ledger, smoke results, and test logs. All 64 entries passed SHA-256 verification.

## Rollback and remaining compatibility

Rollback must stop node admission, drain any accepted v2 work, restore the
matching companion binary and v1 ledger, and only then roll back the gateway
binary if needed. A v1 binary must never be attached to a v2 ledger, and a v2
plan hash must never be reused as v1 authority.

Remove gateway v1 readers only after both remaining external nodes have moved
to v2 and a fresh deployed inventory shows zero connected or retained v1 work.
