# Browser B4 Phase 1 Deployment Evidence

## Status

Browser B4 Phase 1, **Profile Authority And Production Cutover**, is merged,
deployed, and complete. Phase 2, **Managed Alias And Revocation Parity**, is the
active dependency-ordered phase.

The production gateway and configured Darwin companion run artifacts built
from merged `main` commit
`fa4f4100efb555b6b372f8c37612b882cf95695c`. This record closes the Phase 1
acceptance criteria in
[Browser B4 Execution Goal](../architecture/browser-b4-execution-goal.md) under
the authority and stop conditions in
[Browser Capability B4 Admission](../architecture/browser-capability-b4-admission.md).

Credential injection, password-manager integration, and secret-manager
integration remain explicitly outside B4. Persistent managed profile state and
visible handoff remain the deployed authentication mechanism.

## Merged implementation

The principal Phase 1 implementation and cutover-correction pull requests are:

| Slice | Pull request | Merge commit |
| --- | --- | --- |
| Canonical profile authority | [#1034](https://github.com/bogdanovich/mintclaw/pull/1034) | `5b0722c3` |
| Strict canonical cutover | [#1100](https://github.com/bogdanovich/mintclaw/pull/1100) | `d1b9653f` |
| Companion runtime authority | [#1101](https://github.com/bogdanovich/mintclaw/pull/1101) | `fe7a3449` |
| Durable browser-action projection | [#1105](https://github.com/bogdanovich/mintclaw/pull/1105) | `ce8610d0` |
| Recoverable context authority | [#1108](https://github.com/bogdanovich/mintclaw/pull/1108) | `f822d39b` |
| Delegated result-shape contract | [#1116](https://github.com/bogdanovich/mintclaw/pull/1116) | `4890b803` |
| Authoritative objective projection | [#1121](https://github.com/bogdanovich/mintclaw/pull/1121) | `9da312a8` |
| Final-handled trace settlement | [#1125](https://github.com/bogdanovich/mintclaw/pull/1125) | `fa4f4100` |

The first three pull requests own the profile schema and cutover. The remaining
focused corrections were defects exposed by real end-to-end validation of the
same first-party browser route; none adds a browser-specific delivery bypass.
Every code pull request passed its required CI and autonomous reviewer gates
before merge.

## Canonical authority and cutover

The active gateway configuration publishes only the safe target aliases
`gateway` and `companion`. Each currently grants one `managed` profile at
revision `managed-v1` with `any_http`, `full_access`, `model_requested`, and
non-dry-run policy. Actor grants, storage paths, lock paths, driver arguments,
and endpoint details remain private and are not part of discovery or this
record.

The Darwin companion is connected under its operator alias, advertises the
current typed browser command catalog, and reports policy revision
`local-test-browser-policy-evaluate-v10`. Its software version matches the
gateway artifact exactly.

The strict reader removed the transitional single-profile and reserved-driver-
argument fallback before this phase closed. Invalid, conflicting, duplicate,
ungranted, and path-unsafe configurations are covered by the merged validation
tests and fail before runtime publication. Gateway and companion share the same
profile authority contract; the companion receives only its typed private
runtime mapping.

The production `managed` identity was reused in place. No profile directory was
copied, exported, deleted, recreated, or included in a deployment backup. The
post-cutover gateway canary reached an authenticated Marketplace route in that
same identity, proving that the retained login survived the schema and binary
cutover.

## Exact deployment

The final artifacts report `v0.1.0-p8a.2-1584-gfa4f4100` and have these
SHA-256 digests:

| Artifact | SHA-256 |
| --- | --- |
| Linux gateway and CLI | `e2b56657333e429f37f268c221f41212a0259de2309575ade22bcaca410cf928` |
| Linux companion CLI | `c95e2a5f908294d8d6dd039b34c7b11779b732e3eea7780d1e74f575f1c2d0e0` |
| Linux launcher | `fe41e9bda529fd3b7221b4b66f356102a611757933f7b1b79c1121d325f6809d` |
| Darwin amd64 companion | `da3b2142bb26148d91c110db68ba3265ae1c66bc18e45756d87b9afc4d340ce8` |

Before restart, old and new `doctor` runs produced identical output digests and
the same expected policy-finding exit status for both active gateway profiles.
The rollout restarted only the main and spouse gateway units and the configured
Darwin companion LaunchAgent. Both gateway processes received new PIDs and the
companion reconnected with the exact deployed version.

## Live managed-profile canaries

Both canaries entered through the running main gateway, delegated exactly once
to the browser specialist with `delivery_mode=user_only`, and used only the
first-party browser tools. They opened `managed`, observed fresh state,
navigated once, observed the result, and explicitly closed the session.

The gateway canary used request
`c8b0a0cf-7042-4e15-8969-412ec130b5d8` and returned exactly one JSON result:

```json
{"target_status":"ready","dry_run":false,"initial_url":"about:blank","navigate_state":"succeeded","final_url":"https://www.facebook.com/marketplace/selling/","final_title":"Facebook","truncated":false,"close_state":"closed","safe_error":null}
```

It did not extract listing details or perform an external action.

The companion canary used request
`d5a6aa88-da52-41b0-b514-089cc0288ff7` and returned exactly one JSON result:

```json
{"target_status":"ready","dry_run":false,"initial_url":"about:blank","navigate_state":"succeeded","final_url":"https://example.com/","final_title":"Example Domain","truncated":false,"close_state":"closed","safe_error":null}
```

No canary required approval, credential injection, profile migration, or an
external commit.

## Trace and lifecycle evidence

The corresponding parent traces are:

- gateway: `trace-turn-e80c5d118327316578ab4e18`;
- companion: `trace-turn-1a26cd7b5eef3bc5cd37bf79`.

Both use schema `mintclaw.diagnostic_trace.v1`, have redacted-content policy,
contain 13 ordered records, end with status `completed`, have no truncation or
dropped-record reason, and record the final delivery outcome before turn end.
This specifically proves that the corrected final-handled delivery settles the
parent trace instead of timing out after successful user delivery.

The browser child traces are
`trace-turn-1f44e61ff16dbf90863d064c` for gateway and
`trace-turn-5b7f6ea39ef2c2e8aba9b67a` for companion. Each completed with 36
records and no truncation. Each recorded successful calls for target discovery,
session open, initial observation, typed navigation, final observation, and
session close.

After both canaries, the browser ledger contained 26 closed sessions and two
historical terminal lost sessions, with zero nonterminal sessions. All 40
retained invocations were terminal `succeeded`; there were zero nonterminal
invocations. Forty bounded prepared-action records remained as audit history.
The final ten-minute service audit found every expected unit active, zero
failed units, zero legacy product processes, and zero error-level journal
entries. No `delivery_settlement_timeout` appeared in the bounded main-gateway
journal.

## Rollback and retention

The checksum-verified immediate pre-deployment recovery sets are:

```text
/home/server/mintclaw-recovery-browser-b4-phase1-trace-pre-20260908T091134Z
/Users/ab/.mintclaw-deploy-backups/browser-b4-phase1-trace-pre-20260908T091147Z
```

They restore the prior `9da312a8` gateway and Darwin binaries through only the
named service manager. Mutable browser records and the persistent managed
profile are not rolled back over newer state.

The checksum-verified previous known-good `4890b803` recovery sets are retained
beside them. Older superseded Phase 1 compact sets were pruned on the gateway;
the corresponding Darwin sets were moved to the local Trash and remain
recoverable there. Monthly and non-Phase-1 recovery artifacts were untouched.

## Phase conclusion

Every Phase 1 acceptance criterion is satisfied without reaching a mandatory
stop condition. Canonical profile authority is now the only runtime path, the
existing managed identity remains intact, gateway and companion use the same
first-party contract, and rollback does not require copying or restoring
profile data.

Phase 2 may now implement multiple arbitrary managed aliases, lease isolation,
capacity behavior, and revision-bound revocation on both placements. Ephemeral
profiles and attached-user Chrome remain dependent later phases.
