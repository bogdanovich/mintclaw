# Node Companion P4 proof matrix

Date: 2026-08-08

Status: implementation proof complete; merged-main deployment evidence pending

This record maps the admitted P4 requirements to reviewed implementation and
deterministic proof. It intentionally does not claim deployment completion
until the final proof PR is merged, merged main is built, and the deny-by-
default deployment checks in the P4 runbook are recorded.

## Merged implementation

| PR | Merge commit | Scope |
| --- | --- | --- |
| #532 | `93505967` | P4 admission contract |
| #535 | `74e547da` | Four-tuple slim releases, signed manifest, offline verification, and deny-by-default policy |
| #544 | `ca9e3ee9` | Trusted-local-OS-principal amendment |
| #546 | `14fdc5d9` | Stable exact-instance coordinator, adoption, activation, rollback, and recovery |
| #555 | `1933d02a` | Model-facing `node.update.v1`, approval/invocation binding, status/cancel recovery, and redacted events |
| Current proof PR | pending | Native real-process canaries and operator documentation |

Every merged implementation head passed Tests, Integration Tests, Linter,
Security Check, and Browser Windows. PR #555 completed three substantive
review/fix cycles. Its final reviewer found no high-confidence issues; scope
remained one update command using the existing transport, ledgers, approval,
events, and no-replay path.

## Requirement matrix

| Requirement | Implementation | Authoritative evidence | Deployment state |
| --- | --- | --- | --- |
| Authenticated slim artifacts for four tuples | #535; `.goreleaser.yaml`, release workflows, `pkg/nodes/update` | manifest/signature, tuple, signer, expiry, compatibility, workflow-permission, and offline fixtures | Pending final release/deployment inventory |
| One target and one release alias | #535, #555; companion policy, target profile, invocation plan | disabled/missing/colliding/revoked policies; exact descriptor selector and changed-argument tests | Deny-all until final canary profile is configured |
| No caller-selected authority | #535, #555; descriptor projection and retained update authority | URL/key/digest/tag/tuple/version/path/service/downgrade/approval omission and replay-authorization tests | No update target enabled by default |
| Exact durable approval and identity | #555; existing interaction and gateway invocation stores | actor, agent, route, session, tool call, execution ID, plan/catalog/profile revision, expiry, and continuation tests | Pending routed canary approval |
| Stable activation and health | #546; coordinator store/lifecycle/supervisor | native `TestRealProcessUpdateCanaries/user_scope_healthy_activation` plus authenticated wrong-version/node/tuple and malformed-catalog-digest tests | macOS native proof passed; Linux native CI pending current PR |
| Verified rollback | #546; bounded supervisor and one previous payload | native `TestRealProcessUpdateCanaries/system_scope_verified_rollback`; three failed candidate processes and one healthy previous process | macOS native proof passed; Linux native CI pending current PR |
| Crash, power loss, and cleanup | #546; atomic store and transaction fault points | store, stage, lifecycle, supervisor, and transaction fault-injection tests at publication boundaries | No live destructive fault injection required |
| Disconnect, status, restart, no replay | #546, #555 | native `TestRealProcessDisconnectRecoversWithoutSecondActivation`; invocation unknown/status/cancel/no-replay tests | macOS native proof passed; Linux native CI pending current PR |
| Fresh installed-version truth | #555; `node.info.v1` and recovered invocation result | successor recovery, independent catalog/version health, exact result schema tests | Pending final live canary observation |
| systemd and launchd user/system ownership | #546; lifecycle adoption/rendering and coordinator installation identity | Linux user/system unit tests, macOS LaunchAgent/LaunchDaemon tests, native user/system coordinator process canaries | macOS native proof passed; Linux native CI pending current PR |
| Redaction | #555; existing runtime event infrastructure | event/approval/tool projection tests exclude manifest, URL, digest, path, credential, and unrestricted output | Pending final bounded log/trace scan |
| Unchanged non-update behavior | #555 and existing node/gateway packages | touched-package tests and broad exact-head CI | Pending merged-main health check |

## Native macOS proof

On 2026-08-08 the proof head ran the coordinator canaries natively on macOS
`amd64`. The candidate and previous payloads were real Mach-O test processes
launched through the production socketpair, fixed child environment, payload
verification, coordinator codec, and authenticated health path.

- User-scope success launched candidate slot B once and committed `healthy`
  with expected version `v1.1.0`.
- System-scope rollback launched candidate slot B exactly three times, then
  previous slot A once, and committed `rolled_back` only after authenticated
  health for `v1.0.0`.
- Disconnect proof stopped the supervisor after one real candidate launch,
  closed the coordinator and store, reopened the same durable directory into
  new coordinator and supervisor objects, and queried status twice without
  changing generation, attempts, payload, or release-download count. The
  reopened supervisor then committed healthy on the second process attempt.
  Staging and activation were not repeated.
- The ordinary package tests and focused real-process race tests passed. Linux
  `amd64` and `arm64` test binaries cross-compiled as native ELF executables;
  native Linux execution remains an exact-head CI gate.

The helper payload contains no product bypass. It is the native Go test binary
copied through the same archive, executable inspection, slot publication, and
launch path as a companion payload. In child mode it emits only a bounded
authenticated health frame, deliberately exits, or holds the control channel
according to a private deterministic test fixture.

The successor health boundary checks exact node identity, release version,
platform, and architecture plus a well-formed catalog digest. It does not
require that digest to equal the pre-update catalog: a successor can
legitimately advertise a different catalog. Signed manifest compatibility is
the pre-staging protocol/config gate; semantic catalog equality is not claimed
as P4 coordinator evidence.

## Residual completion work

After this proof PR merges:

1. fast-forward a clean deployment checkout to merged `origin/main`;
2. validate the merged commit and build the gateway, companion, and stable
   coordinator with repository tags;
3. inventory a signed four-tuple manifest without exposing the signing seed;
4. deploy with update authority absent or disabled, keeping rollback backups;
5. run the bounded target/profile discovery and routed-approval canary only on
   a reversible node;
6. verify `nodes_status`, fresh `node.info.v1`, health, logs, redaction, and no
   duplicate activation; and
7. replace this pending section with exact merged/deployed evidence.

P4 is not complete until those checks are recorded. Completion must then stop;
it does not authorize fleet work, key rotation, bootstrap, coordinator
self-update, package managers, additional platforms, privilege separation, or
P5.
