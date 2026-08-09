# Node Companion P4 proof matrix

Date: 2026-08-08

Status: complete; mandatory P4 stop applies

This record maps the admitted P4 requirements to reviewed implementation,
deterministic proof, a signed release, and a live deny-by-default deployment.
It is the P4 closeout record and does not authorize P5 or deferred work.

## Merged implementation

| PR | Merge commit | Scope |
| --- | --- | --- |
| #532 | `93505967` | P4 admission contract |
| #535 | `74e547da` | Four-tuple slim releases, signed manifest, offline verification, and deny-by-default policy |
| #544 | `ca9e3ee9` | Trusted-local-OS-principal amendment |
| #546 | `14fdc5d9` | Stable exact-instance coordinator, adoption, activation, rollback, and recovery |
| #555 | `1933d02a` | Model-facing `node.update.v1`, approval/invocation binding, status/cancel recovery, and redacted events |
| #559 | `1a7035d3` | Native Linux/macOS real-process update, rollback, disconnect, restart, and no-second-activation proof plus operator documentation |
| #565 | `e1afcec6` | Release archive contract correction and shared signer/coordinator executable validation |

Every implementation head passed Tests, Integration Tests, Linter, Security
Check, and Browser Windows. PR #555 completed three substantive review/fix
cycles. PR #565 completed one substantive review/fix cycle and an architecture
checkpoint after its production diff grew beyond the initial baseline. The
checkpoint retained one narrow invariant: the trusted signer must not
authenticate an archive the coordinator rejects. No new state, protocol,
transport, lifecycle, or generic updater abstraction was added.

## Requirement matrix

| Requirement | Implementation | Authoritative evidence | Deployment state |
| --- | --- | --- | --- |
| Authenticated slim artifacts for four tuples | #535, #565; `.goreleaser.yaml`, release workflows, `cmd/mintclaw-node-release`, `pkg/nodes/update` | manifest/signature, tuple, signer, expiry, compatibility, archive-contract, workflow-permission, and offline fixtures | `v0.1.0-p4.3` published from `e1afcec6`; signature, size, digest, single-entry executable mode, and ELF/Mach-O tuple independently verified |
| One target and one release alias | #535, #555; companion policy, target profile, invocation plan | disabled/missing/colliding/revoked policies; exact descriptor selector and changed-argument tests | Isolated `ab-p4-canary` bound to profile `p4-canary` and alias `candidate -> v0.1.0-p4.3`; all other targets unchanged |
| No caller-selected authority | #535, #555; descriptor projection and retained update authority | URL/key/digest/tag/tuple/version/path/service/downgrade/approval omission and replay-authorization tests | Live model selected only target and configured alias; source, key, digest, tuple, path, service, restart, downgrade, and approval remained operator policy |
| Exact durable approval and identity | #555; existing interaction and gateway invocation stores | actor, agent, route, session, tool call, execution ID, plan/catalog/profile revision, expiry, and continuation tests | Interaction `efbf3f9d` accepted `allow_once` in a separate operator turn; one resume consumed the retained plan; a different session could not read the invocation |
| Stable activation and health | #546; coordinator store/lifecycle/supervisor | native `TestRealProcessUpdateCanaries/user_scope_healthy_activation` plus authenticated wrong-version/node/tuple and malformed-catalog-digest tests | Linux CI and native macOS proof passed; live LaunchAgent canary committed `healthy` with one launch and authenticated `v0.1.0-p4.3` successor health |
| Verified rollback | #546; bounded supervisor and one previous payload | native `TestRealProcessUpdateCanaries/system_scope_verified_rollback`; three failed candidate processes and one healthy previous process | Linux CI and native macOS rollback proof passed; live success retained verified previous slot `v0.1.0-p4.1` without exercising destructive rollback |
| Crash, power loss, and cleanup | #546; atomic store and transaction fault points | store, stage, lifecycle, supervisor, and transaction fault-injection tests at publication boundaries | No live destructive fault injection required |
| Disconnect, status, restart, no replay | #546, #555 | native `TestRealProcessDisconnectRecoversWithoutSecondActivation`; invocation unknown/status/cancel/no-replay tests | Live dispatch returned `DISPATCH_UNCERTAIN`; the model recovered the same invocation through `nodes_status`; gateway stayed dispatched, node finished succeeded, and coordinator recorded `launch_attempts: 1` |
| Fresh installed-version truth | #555; `node.info.v1` and recovered invocation result | successor recovery, independent catalog/version health, exact result schema tests | Fresh catalog renewal and `node.info.v1` reported Darwin amd64 `v0.1.0-p4.3`; scoped `nodes_status` independently returned the same installed version |
| systemd and launchd user/system ownership | #546; lifecycle adoption/rendering and coordinator installation identity | Linux user/system unit tests, macOS LaunchAgent/LaunchDaemon tests, native user/system coordinator process canaries | Linux CI and native macOS proofs passed; live user LaunchAgent remained owned by the stable coordinator while payload slot changed |
| Redaction | #555; existing runtime event infrastructure | event/approval/tool projection tests exclude manifest, URL, digest, path, credential, and unrestricted output | Bounded live invocation-event scan found no source URL, public/signing key, redirect list, credential, secret, or manifest body; gateway error-level entries during the canary were zero |
| Unchanged non-update behavior | #555 and existing node/gateway packages | touched-package tests and broad exact-head CI | Main/web/reviewer and both shared and isolated companions remained active; reviewer was not restarted; a latest-main live gateway smoke returned `P4_FINAL_3D7014AE_OK` without tools |

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

## Signed release and archive correction

The first live candidate, `v0.1.0-p4.2`, provided a useful fail-closed canary.
Its signed manifest and archive digest were valid, but GoReleaser placed
`LICENSE` and `README.md` before `mintclaw-node`. The coordinator rejected the
archive as `artifact_invalid` before activation. The gateway reported the
terminal operator-action-required result and did not replay it.

PR #565 corrected the producer contract and made the trusted signer validate
the same archive and executable format accepted by the coordinator before it
can sign a manifest. Release `v0.1.0-p4.3` was then built from merge
`e1afcec6`, published as a prerelease, and independently verified with the
pinned public key whose SHA-256 fingerprint is
`e768631c24365428b2199bc8ddf8dcd5409ca01507b5f1c64bb2725505c49849`.
The signing seed remained only in operator-controlled secret storage and the
GitHub Actions secret; it was not installed on a gateway or companion.

| Tuple | Archive bytes | SHA-256 |
| --- | ---: | --- |
| Darwin amd64 | 4,972,404 | `3e9cf5435d90ebe2db1af0903473e29c15adf880ec0f8e1619a5659c64300092` |
| Darwin arm64 | 4,601,797 | `90ab9f207ce9fc1346edb1dc8c1d4f32ea51ee412a6417233ef3fe84c74bc7ae` |
| Linux amd64 | 4,933,497 | `700609ff2e94c229d81ae24bada6b1fd35982df56082c679a626cd930f8a8bfb` |
| Linux arm64 | 4,476,308 | `1ae9f51e25d41284bbea3082915b41a805ffb62111f3fec42d37c62371debfa6` |

Every archive matched the signed manifest size and digest, contained exactly
one regular executable entry named `mintclaw-node` with mode `0755`, and
identified as the expected Mach-O or ELF architecture. The manifest channel
is `nightly`, minimum coordinator version is `v1.0.0`, and expiry is
2026-11-07T00:38:00Z.

## Live macOS update canary

The shared `ab-local-test` target was being used concurrently for browser
validation and advertised a newer development payload. It was not downgraded
or disturbed. A separate user LaunchAgent, target alias `ab-p4-canary`, state
directory, identity, and stable coordinator were created from current main.
Its bootstrap payload reported `v0.1.0-p4.1`; only `node.info.v1` and
`node.update.v1` were approved. Unexpected advertised commands remained
unapproved.

The model obtained fresh discovery for exact alias `candidate ->
v0.1.0-p4.3`, Darwin amd64, privileged risk, mandatory approval, and no
downgrade. The only update invocation was
`inv_b84853131c4ba10bdc03fc47767da468187ff93e77d1cb3a3b20a83ea93c5b95`.
After a separate `allow_once`, the gateway crossed the dispatch boundary and
returned `DISPATCH_UNCERTAIN` while the payload restarted. No second update
invocation was created. Scoped `nodes_status` recovered:

- gateway state `dispatched`, node state `succeeded`;
- installed version `v0.1.0-p4.3`;
- activation attempted and successor verified;
- rollback not attempted and not verified; and
- no error code or recovery action.

The durable coordinator state is `healthy`, active slot B is `v0.1.0-p4.3`,
previous slot A is `v0.1.0-p4.1`, and `launch_attempts` is exactly one. The
stable coordinator PID did not change. After the successor catalog changed,
the gateway again hid commands until an operator renewed the catalog while
allowing only the same two commands. Fresh `node.info.v1` then recovered
Darwin amd64 `v0.1.0-p4.3`. A status request from a different routed session
received `INVOCATION_UNAVAILABLE`, proving session isolation.

## Deployment and rollback record

The gateway, web launcher, companion, and coordinator were built in an
isolated checkout from merged main `3d7014ae` and installed atomically. That
head includes the concurrently merged browser documentation and tool-format
refactor after the `p4.3` release commit. Installed SHA-256 values were:

- gateway: `4834d7844a5339d827005a28d8b5ae33adbf0283808bb2fed5e59072c5060797`;
- companion: `1bb402a6b559d353066b6b57172ae3182a78b456cf843de643e9f1d7fa64e3bf`;
- coordinator: `f4e4fa751bf7fcecb0988a54f514ebc58f3af5ec2d33939171eb8be7d1ba1fd1`; and
- web launcher: `bf778d9a9136ceb00d38b7b96f62b81093c933617e03b6c5d7e7671c60400102`.

The bounded rollback backups are:

- `/home/server/mintclaw-p4-backup-20260808T221055Z` for the initial P4
  deployment and config;
- `/home/server/mintclaw-p4-backup-20260809T001600Z` for the latest-main
  binaries and isolated-canary gateway config; and
- `/Users/ab/mintclaw-p4-canary-backup-20260808T223756Z` for the original
  shared macOS companion.

Only `mintclaw-main` and `mintclaw-main-web` were restarted during gateway
deployment. The reviewer process retained its original PID/start timestamp so
pending reviews were not disturbed. Product failed units were zero, launcher
HTTP returned the expected redirect, recent main/web/reviewer errors were
zero, and a live agent smoke returned `P4_FINAL_3D7014AE_OK`. Both the shared
browser target and isolated update target remained connected. The final
docs-only merge changes no runtime code; deployment health is rechecked after
it lands before the goal is closed.

## Completion and mandatory stop

All P4 Definition-of-Done requirements are evidenced by merged code, CI,
native Linux/macOS tests, signed four-tuple release `v0.1.0-p4.3`, and the live
macOS routed-approval canary. P4 is complete when this closeout record merges.
Stop after that merge. Do not begin fleet or batch updates, key rotation,
bootstrap, coordinator self-update, package-manager integration, additional
platforms, same-UID privilege isolation, P5, or other deferred work.
