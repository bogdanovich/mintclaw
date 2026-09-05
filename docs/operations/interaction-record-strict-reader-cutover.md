# Interaction Record Strict-Reader Cutover

Status: deployed

## Production inventory

On 2026-09-04, a read-only inventory was taken from the active MintClaw
deployment at core commit `06c2c02fc9ad18fcd578bd9a96700919bed5e009`.
The inventory inspected only schema names and aggregate counts; it did not emit
record IDs, prompts, answers, routes, or configuration values.

| Snapshot | Schema | Records | Approvals | Missing argument hash | Missing execution context | Missing approval action | Active obsolete approvals |
|---|---:|---:|---:|---:|---:|---:|---:|
| `main/workspace/state/interaction_registry.json` | `interaction_snapshot.v1` | 13 | 12 | 0 | 0 | 0 | 0 |

No interaction registry existed in the other active profile trees. Because no
obsolete approval was present, the cutover requires no data conversion,
quarantine, or deletion.

## Contract change

The steady-state snapshot reader now applies the same authority requirements as
record creation: every approval must carry a valid canonical argument hash, an
immutable execution context, and a bounded non-empty action description. A
snapshot containing an obsolete approval fails closed and cannot be used to
recover or create authority.

Questions retain their existing contract and may carry a validated execution
context, but never an approval action.

## Rollout and rollback

Deploy the strict reader only after re-running the aggregate inventory against
all active profile trees. Stop if an obsolete approval appears; quarantine the
whole snapshot for audit and deliberately resolve its active interaction before
deployment instead of synthesizing missing authority metadata.

Rollback is a binary rollback only. Do not rewrite a current snapshot into the
obsolete shape. A rollback remains compatible with strict current records.

## Deployment evidence

Immediately before deployment on 2026-09-04 local time (2026-09-05 UTC), the
inventory was repeated with the same result: 13 records, 12 approvals, and no
missing argument hash, execution context, or approval action. The strict reader
was deployed as part of commit
`82d7b398106b283c74c2c810f560005a9282040a`; no record conversion or state
rewrite was performed.

All active configurations loaded under the deployed binary without schema or
load errors. The final service status and ten-minute error-level journal window
were clean, a live gateway smoke completed with a valid redacted diagnostic
trace, and all 47 deployed reviewer-runtime tests passed. The matching binary,
configuration, interaction snapshot, service-unit, and smoke evidence is in the
checksummed rollback bundle
`/home/server/mintclaw-code-health-backup-20260905T025452Z`.
