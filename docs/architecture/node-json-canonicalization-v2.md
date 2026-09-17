# Node JSON Canonicalization V2

Status: protocol-v2 cutover complete. The gateway and companions accept only
v2; persisted snapshots and execution plans must carry the version explicitly.
Omitted and v1 values fail closed instead of selecting a compatibility reader.

All three connected companions were deployed on v2 before the legacy reader
was removed. At fleet closeout, 148 expired v1 no-replay invocation tombstones
still remained; their retention gate and deployment evidence are recorded in
the
[Node JSON Canonicalization V2 Cutover](../operations/node-json-canonicalization-v2-cutover.md).

## Numeric representation

Node protocol v2 canonical JSON keeps the existing UTF-8 JSON encoding,
lexicographic object-member order, exact decimal parsing, and duplicate-member
rejection. It changes only number rendering:

- zero is `0`, including negative zero and exponent spellings;
- every mathematical integer is emitted as an optional `-` followed by plain
  base-10 digits, without a decimal point or exponent;
- every non-integer is emitted as one non-zero digit, an optional fractional
  significand, and an `e` exponent when that exponent is non-zero;
- equivalent JSON spellings have one representation (`60`, `60.0`, `6e1`, and
  `0.6e2` all become `60`);
- a number may contain at most 4,096 significant digits, its normalized
  exponent magnitude may not exceed 1,000,000, and expanding an integer may
  not produce more than 4,096 digits.

These limits prevent a short exponent spelling from causing unbounded output.
They are protocol validation errors, not rounding: accepted values remain
exact. Canonical schemas and model examples must also remain within their
existing byte limits after number expansion, and the final canonical catalog
must remain within the catalog byte limit.

## Affected authority and persistence

The cutover changes bytes, and therefore digests, anywhere the node subsystem
uses canonical JSON:

| Surface | Binding or storage | Cutover treatment |
| --- | --- | --- |
| Capability descriptors and catalogs | `DescriptorHash`, `CatalogHash`, identity proof, node registry approval | Compute with the sole v2 representation. A former v1 approval never authorizes a changed v2 digest. |
| Execution plans | `PlanHash`, separately retained expected hash, approval binding | Require explicit protocol v2; never reinterpret or rehash an older plan. |
| Invocation input | Canonical request input and `PlanHash` | Validate and canonicalize with v2 before plan preparation. |
| Invocation output | Canonical result in the companion ledger and gateway result | Validate and canonicalize with v2 before persistence or typed decoding. |
| Gateway invocation store | `<workspace>/state/node_invocations.db` | Reject omitted or v1 plans. Reader removal therefore requires retention to reach zero first. |
| Companion invocation ledger | `<state_dir>/invocations.json` | Stop the companion, back up the v1 ledger, and start an empty v2 ledger. |
| Node registry | `<workspace>/state/nodes/registry.json` | Preserve identity keys and pairing state, but require the connected v2 catalog digest to match before command approval is usable. |

Unrelated JSON users (`evaltrace`, configuration documents, task records, and
provider payloads) do not use the node canonicalizer and are outside this
cutover.

## Protocol boundary

The representation is node protocol major version 2. The gateway advertises
only v2 and rejects peer ranges that do not include it. Invocation and transfer
dispatch remain bound to the exact authenticated session generation through
the durable dispatched transition and first frame write.

The negotiated version is persisted explicitly on the node snapshot. Every
execution plan carries `"protocol_version":2` and uses v2 catalog, descriptor,
input, plan, and output canonicalization end to end.

## Rollout

1. Inventory the node registry, gateway invocation-store report, connected
   protocol versions, and each companion ledger. Save checksummed backups.
2. Deploy the dual-protocol v2 gateway. It continues serving existing v1
   companions with v1 bindings.
3. Disable new node invocation admission and drain every prepared, dispatched,
   and running v1 invocation. An incomplete drain blocks the cutover.
4. Stop each companion, archive its v1 invocation ledger, deploy the v2
   companion, and reconnect. Reapprove commands whose v2 catalog digest differs.
5. After every companion reports protocol v2, audit the gateway invocation
   store. Preserve expired v1 no-replay tombstones and the reader needed to
   load them until normal retention removes them.
6. Record the inventory and backup locations in the deployment log. Remove v1
   readers only after a later audit shows zero connected, active, or retained
   v1 work; keep the removal change unmerged until that gate passes.

## Rollback

Disable admission and drain v2 work first. Roll companions back while the
dual-protocol gateway is still running, restoring their matching v1 ledgers
only if no v2 invocation was accepted. Then restore the gateway v1 invocation
store and gateway binary together. Never attach a v1 binary to a v2 ledger or
reuse a v2 plan hash as v1 authority.
