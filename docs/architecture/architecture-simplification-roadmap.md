# Architecture Simplification Roadmap

Status: active; the implementation sequence is merged through P1, C2, and
X3.88. The corrected source-side Z1 audit passes at `e60b8e26` through PR
#976. The explicitly authorized R1 compatibility reset and the 2026-08-30 Z1
stopped-state session cutover, matched rollback, reapply, and observation are
complete. This closeout deletes the temporary copy-only converter from #978.
O1 shutdown ownership is merged through #1007 and deployed on `7e52c1dd`.
O2 SQLite ownership is merged through #1015 and deployed on `20cf7a18`.
O3 terminal-delivery ownership is merged through #1019 and deployed on
`6fc47a3e`, including the coordinated browser/node contract reset exposed by
the combined release. Only the program completion audit remains open.

Original audit baseline: `origin/main` at `f5c9afe9`, 2026-08-19

Re-audit baseline: `origin/main` at `d864bb7e`, 2026-08-24

## Current Execution Objective

Finish the simplification program with one canonical internal runtime and
persisted contract. Preserve only bounded additive current-plus-previous
first-party wire compatibility; remove historical readers and schema
generators, deprecated aliases, implicit old-state inference, duplicate
ownership, and service-locator layers. Preserve legitimate failover, product
aliases, and external protocols. Keep the completed R1 and Z1 reset and
rollback evidence closed, preserve the completed semantic source audit at
`e60b8e26`, and resolve the lifecycle, SQLite writer, and terminal-error
ownership defects exposed by the cutover as separate focused packets. Then
repeat the source and deployed completion audit without adding a steady-state
compatibility reader.

## Decision

MintClaw has one canonical internal implementation and a deliberately small
wire-compatibility budget.

First-party wire protocols should evolve additively when that is natural. A
gateway on the current release must normally interoperate with companions from
the immediately previous release. Older combinations may continue to work when
the protocol change was purely additive, but they are not guaranteed or tested.
An explicitly named rollout may temporarily support two previous releases; it
must include the removal release and may not become an open-ended support tail.

Compatibility must come from the shape of the canonical protocol, not copies
of historical implementations. New optional fields, stable field identities,
unknown-field tolerance at non-authority-bearing envelopes, and advertised
capability intersection are allowed. Historical schema reconstruction,
version-by-version execution switches, dual writes, deprecated aliases,
fallback engines, and no-op flags are not.

Breaking changes are allowed. They increment an explicit protocol major or
minimum-supported version and reject older peers clearly. Once all deployed
components have been upgraded, any temporary boundary adapter is deleted.

Persisted server state and configuration use one current format. An
incompatible storage release uses a coordinated cutover:

1. inspect and back up the deployed configuration and durable state;
2. stop all MintClaw processes that share those contracts;
3. convert or deliberately discard obsolete state outside the steady-state
   runtime;
4. install mutually compatible revisions on the server and first-party
   clients; and
5. restart and verify the current contract.

The product may retain a protocol major, minimum supported version, capability
set, or current storage fingerprint. Those markers select current features or
reject an incompatible peer or document; they must not select a historical
runtime implementation.

These rules follow the useful evolution properties of Protobuf, gRPC, and
Thrift without requiring MintClaw to adopt another RPC stack. The existing
transport can keep its current framing while applying the same discipline:

- add optional fields instead of renaming or changing field meaning;
- never reuse a removed field identity for a different meaning;
- ignore unknown informational envelope fields;
- keep authority-bearing action payloads strict for the action version the
  peer advertised;
- advertise supported actions and features rather than inferring them from a
  historical full schema; and
- use a major boundary for breaking semantic or security changes.

This decision does not remove independent product integrations merely because
they contain the word `compatible`. OpenAI-compatible model APIs, OneBot, OS
and architecture support, browser standards, and explicit import from another
product are current external contracts. They remain unless separately removed.

## Purpose

Recent work improved safety and durability, but several features now encode the
same fact in multiple packages. A small contract change consequently expands
across tool schemas, persisted records, gateway adapters, companion interfaces,
task projections, delivery state, and frontend reducers.

The program will reduce that change amplification while preserving these
properties:

- browser actions remain fail-closed and revalidated at the enforcing broker;
- protected values remain transient and redacted from durable records;
- outbound delivery distinguishes definite failure from ambiguous receipt;
- accepted work remains durable across restart;
- tool effects are never blindly replayed after an uncertain outcome;
- coding and channel runtimes remain separate authority profiles; and
- first-party peers negotiate the bounded current protocol and fail clearly
  when their protocol majors do not overlap.

The target is not fewer files by itself. The target is one owner and one
representation for each protocol, lifecycle, and durable fact.

## Evidence From Recent Changes

The audit identified the following change-amplification signals:

| Change | Signal |
| --- | --- |
| Browser `check` and `hover` | One action slice changed 19 files in PR #737 |
| Browser `drag` | One action slice changed 19 files in PR #738 |
| Browser file chooser | Historical schema mismatch blocked production startup and required PR #743 |
| Verified child objective outcomes | One result concept changed 33 files in PR #780 |
| Human interaction recovery | PRs #770 through #776 repeatedly crossed interaction, task, channel, pipeline, and delivery ownership |

At the original audit baseline, production also contained:

- two browser action representations plus repeated action-kind switches;
- complete generators for several historical browser schemas;
- separate prompt/final delivery state inside interactions and the durable
  outbox;
- duplicate completion, deliverable, objective, and receipt structures in
  tools and tasks;
- an `AgentLoop` that owns nearly every runtime subsystem, plus a `Pipeline`
  adapter layer rebuilt from that same object;
- compatibility config loaders for versions 0, 1, and 2 while version 3 is
  current;
- legacy session-key aliases and JSON-to-JSONL runtime migration;
- legacy subagent execution and `spawn_status` fallback paths; and
- a coding frontend with distributed-client synchronization machinery even
  though its current producer and TUI consumer are in one process.

## Progress And Re-audit Evidence

Roadmap status uses three distinct meanings:

- **merged** means the implementation is present on `origin/main`;
- **deployed** means the active server and dependent first-party clients run a
  mutually compatible merged revision; and
- **reset complete** means temporary rollout adapters have also been deleted
  and the removal release has been deployed and verified.

Merging a packet does not by itself satisfy its deployed-state or compatibility
reset criteria.

| Packet | Merged evidence | Re-audit status |
| --- | --- | --- |
| S0 | #784 | Merged |
| B1 | #787 | Merged; temporary browser schema-generation debt later introduced by #865 was deleted by #901 and the removal release was deployed in R1 |
| B2 | #788 and #790 | Merged; the canonical action contract remains, and #901 removed the temporary catalogue-admission bridge |
| T1 | #791, with correctness follow-ups #806 and #824 | Merged |
| T2a | #792 | Merged |
| T2b | #794 | Merged |
| D1 | #795 and #796 | Merged |
| D2 | #798 and #799 | Merged |
| A1 | #800 and #801, completed by X3.30-X3.42 | Merged |
| A2 | #808, #809, and #863 | Merged |
| C1 | #802; later coding work #836, #838, and #840 preserved the admitted boundary | Merged |
| Coding resume recovery and C2 | #908, #924-#927, and #929 | Merged; JSONL-authoritative recovery remains current, and its construction, checkpoint, and derived-engine lifecycle now have direct owners |
| X1 | #797, completed across the current-contract X3 packets | Merged; deployed config inspection passed |
| X2 | #803 and #807 | Merged |
| X3.1-X3.61 | #810-#816, #818-#823, #826-#835, #837, #839, #843, #845-#846, #848-#856, #858-#862, #864, #866, #868, #872, #878, #880, #885-#896, and #933 | Merged |
| X3.62 | #935 and #936 | Merged; production-unused public APIs and test-only constructor facades are gone, while current extension and injection seams remain |
| X3.63 | #937 | Merged; compaction execution mode is owner-supplied, and downstream inference plus test-only projector facades are gone |
| X3.64 | #938 | Merged; subagent, spawn, and delegate dependencies are complete and immutable at construction, while `SubTurnSpawner` remains the package seam |
| X3.65 | #940 | Merged; current defaults, resilience paths, provider error refinement, and standard platform behavior no longer use compatibility-shaped internal terminology |
| X3.66 | #942 | Merged; outbound messages directly own typed delivery metadata, async delivery no longer smuggles it through cloned inbound state, and outbox version 3 is the sole persisted contract |
| P1 | #881 | Merged and deployed; all five live configs and 20 personal workspaces use version 4 plus standard root `AGENTS.md`, with no root `AGENT.md` or `IDENTITY.md` reader debt |
| R1 compatibility reset | #899 and #901 | Complete; `vpn` was upgraded, P3 and obsolete identities were retired, strict revision `827e0f70` was deployed, and full backup plus rollback were verified |
| Pre-Z1 strict audit | #911, #914, #916, #919-#921, #940, and #901 | Complete and deployed in R1; browser schema generators, empty-algorithm admission, optional execution-profile admission, and runtime-less companion construction are gone |
| X3.67 | #948-#950 | Merged; Web model activation, channel type, and channel security are explicit current configuration contracts |
| X3.68 | #951 | Merged; channel runtimes are constructor-owned and runtime self-repair is gone |
| X3.69 | #953 | Merged; Seahorse engine collaborators are complete at construction |
| X3.70 | #954 | Merged; context-builder dependencies are complete at construction |
| X3.71 | #955 and #956 | Merged; current-contract terminology and channel-runtime documentation describe the actual ownership boundaries |
| X3.72 | #957 | Merged; channel instance identity is immutable and preserved across runtime paths |
| X3.73 | #959 | Merged; typed channel factories own construction without untyped registries or assertions |
| X3.74 | #960 | Merged; coding workspaces require one canonical explicit Git directory |
| X3.75 | #961 | Merged; browser profiles require an explicit network mode |
| X3.76 | #962 | Merged; `InboundContext` is the sole owner of inbound message identity and relation metadata |
| X3.77 | #963 | Merged; outbound retries are limited to definite rejection or a known untouched remainder, while ambiguous acceptance is preserved |
| X3.78 | #965 | Merged; coding forks require writer-owned root-turn markers and no longer infer historical roots from message shape |
| X3.79 | #966 | Merged; interaction snapshots require explicit current commit sequences and no longer synthesize ordering on read |
| X3.80 | #967 | Merged; task snapshots are validated symmetrically on read and write instead of repaired or partially skipped |
| X3.81 | #968 | Merged; session metadata has one strict identity-bearing shape and no filename-derived key fallback |
| X3.82 | #969 | Merged; persisted agent model selection uses the object contract only, without string/object dual serialization |
| X3.83 | #970 | Merged; the unused second model/tool protocol family in `pkg/tools/shared` is gone |
| X3.84 | #971 | Merged; internal and persisted tool calls use one flat contract, with nested external shapes isolated to provider adapters |
| X3.85 | #972 | Merged; runtime maintenance enumerates only the exact coding thread or owner-matched opaque scope-v2 sessions |
| X3.86 | #973 | Merged; the duplicated ephemeral session-store interface and repeated context checks are gone |
| X3.87 | #975 | Merged; session metadata uses one exact current decoder across store, fork, and Web readers, and successful canonical writes are validated against it before persistence |
| X3.88 | #976 | Merged; bounded history, coding fork, and Web reuse the canonical persisted-message and exact scope decoders instead of decoding those documents independently |
| Z1 source | #964-#976; final source `e60b8e26` | Passed after correcting the premature #973 proof; every persisted session document now has one reader owner, every surviving discovery match is classified, and no production historical reader, dual writer, deprecated callable facade, or version-selected runtime remains |
| Z1 converter preparation | #978 | Merged; a temporary copy-only external command inventories all configured session roots, emits disjoint retained/archive trees and a checksum manifest, strictly validates every retained record, and is registered for deletion after closeout |
| Z1 deployed closeout | Authorized operation on `edd8759b`; this closeout | Complete; 20 session roots were converted from a stopped-state snapshot, the retained current cohort was atomically installed, exact matched rollback and reapply passed, canaries and observation passed, and the temporary converter is deleted |
| O1 shutdown ownership | #1007 | Complete and deployed on `7e52c1dd`; five loaded VPN terminal stops completed in 1.99-4.51 seconds with no stop timeout or `SIGKILL`, and every remote child was gone after restart |

The X3 item-to-PR mapping is: 1-7 to #810-#816; 8-13 to #818-#823;
14 to #826; 15 to #827; 16-23 to #828-#835; 24 to #837; 25 to #839;
26 to #843; 27 to #845; 28 to #846; 29-37 to #848-#856; 38 to #858;
39-42 to #859-#862; 43 to #864; 44 to #866; 45 to #868; 46 to #872;
47 to #878; 48 to #880; 49 to #885; 50 to #886; 51 to #887; 52 to #888;
53 to #889; 54 to #890; 55 to #891; 56 to #892; 57 to #893; 58 to #894;
59 to #895; 60 to #896; 61 to #933; 62 to #935 and #936; 63 to #937; 64 to
#938; 65 to #940; 66 to #942; 67 to #948-#950; 68 to #951; 69 to #953; 70 to
#954; 71 to #955 and #956; 72 to #957; 73 to #959; 74 to #960; 75 to #961;
76 to #962; 77 to #963; 78 to #965; 79 to #966; 80 to #967; 81 to #968; 82 to
#969; 83 to #970; 84 to #971; 85 to #972; 86 to #973; 87 to #975; and 88 to
#976. PR #964 records the completed R1 evidence and opens the final
source-audit sequence; it is not a code packet.

The 2026-08-24 read-only deployed audit established these rollout facts at
that time:

- the installed server remains at `1adb08f7`, so merged architecture work after
  that revision is not yet deployed;
- all five active configurations use the current Delta Chat and nested channel
  security shapes;
- all five active personal profiles have root `AGENT.md`; one extra root
  `AGENTS.md` is shadowed by the current personal-profile reader;
- 5,570 retained messages across the active stores omit `created_at`, while the
  current writer assigns it to new messages; and
- all six retained node-registry records omit `key_algorithm` and therefore
  still rely on the empty-value-to-Ed25519 reader.

The 2026-08-25 read-only R1 preflight superseded the installed-revision fact
above and refined the reset scope:

- the gateway fleet is healthy but split across two effective revisions: the
  main gateway runs `4ecd74c3`, while four other gateways still run
  `631aaff8` from deleted executable inodes; rollback must therefore capture
  the effective binary for each process rather than only the current file at
  its configured path;
- the sole connected browser-capable companion already runs `4ecd74c3` and
  advertises the complete current browser catalogue. A temporary package test
  proved that all six retained node identities deterministically normalize to
  Ed25519 and that their derived identifiers remain unchanged;
- current Ed25519 companion construction still omits `key_algorithm`; only the
  gateway-side reader supplies Ed25519. R1 therefore needs a bridge packet that
  makes companions emit the explicit field while the gateway still accepts the
  omitted form, followed by a fleet upgrade before that reader is deleted;
- the node registry contains four connected, one pending-pairing, and one
  revoked record. Two connected Linux companions are local canary services at
  `631aaff8` and `74998a25`; another connected Linux companion remains on
  `03b08be2`. They must be upgraded or deliberately retired before the gateway
  requires explicit `key_algorithm`;
- the five version 3 configurations describe 21 agents across 20 distinct
  personal workspaces. Every workspace has `AGENT.md`; the machine metadata to
  transfer comprises 21 names, 21 descriptions, 20 tool policies, and one MCP
  policy, with no deployed Markdown model or skill override. A copy-free
  conversion rehearsal produced version 4 documents that passed current
  strict decoding and validation for every profile;
- one spouse workspace already contains a different, currently shadowed
  `AGENTS.md`. The cutover must back up both documents and resolve that collision
  explicitly instead of silently overwriting or concatenating instructions;
- active task events use `task_event.v2`, outbox entries use version 2, and the
  active interaction registry uses the version 1 snapshot and event contracts.
  Session JSONL and metadata plus Seahorse SQLite remain current stores, so the
  structural inventory found no additional persisted-shape conversion gate;
  and
- the active profile trees occupy about 6.3 GiB while the filesystem has about
  7.5 GiB free. Fifty-one earlier backup directories occupy about 26 GiB, so R1
  requires a capacity gate and explicit authorization before moving or deleting
  any retained backup.

The authorized 2026-08-25 local bridge rollout completed the first two
companion upgrades and is recorded in
[R1 Local Node-Identity Bridge Rollout](../operations/architecture-simplification-r1-node-bridge.md):

- `p5a-canary` and `p3-canary` now run the exact `71ad3e53` node build and are
  connected as `v0.1.0-p8a.2-814-g71ad3e53`;
- the privileged P3 service helper is a same-release deployment unit with the
  P3 node. A node-only attempt failed closed while loading the helper snapshot,
  the previous pair was restored and verified, and the coordinated current
  node/helper upgrade then succeeded;
- both local nodes remained stable with zero service restarts and empty
  warning-level journals after the final start, while the gateway fleet,
  profiles, and registry records were not manually changed;
- the connected Darwin `ab-local-test` companion remains at `4ecd74c3`, the
  connected Linux `vpn` companion remains at `03b08be2`, and the older pending
  Darwin and revoked Linux records still require an explicit upgrade,
  conversion, retirement, or removal decision; and
- all six persisted registry records still omit `key_algorithm`, including the
  two upgraded local peers. The external record conversion in R1 therefore
  remains mandatory before the empty-algorithm reader can be deleted.

The 2026-08-26/27 live R1 re-audit supersedes the rollout facts that changed
after that bounded local operation:

- all five gateways and the Web launcher now execute merged main `8418b021`,
  which includes the reviewed browser unknown-effect receipt correction in PR
  #913. `p5a-canary` and `ab-local-test` also report `8418b021`; this source
  re-audit found no historical browser schema, alternate runtime owner, or
  compatibility adapter introduced by that correction;
- a separate stopped preflight converted all five active configs to version 4,
  all 21 configured agent entries, and all 20 distinct personal workspaces.
  Every workspace now has root `AGENTS.md`, and none retains root `AGENT.md`;
- the same preflight converted all six retained node-registry records to
  explicit Ed25519 without changing their node identifiers. The registry still
  contains four connected records, one older pending-pairing record, and one
  older revoked record;
- `p5a-canary` and the Darwin browser companion `ab-local-test` now run
  `8418b021`, which contains the PR #899 bridge, and send explicit Ed25519.
  `p3-canary` last ran and was verified on the exact bridge build `71ad3e53`.
  The Linux `vpn` companion still runs pre-bridge `03b08be2` and has no
  advertised managed-update command;
- a later host operation stopped the P3 node/helper pair. The original local
  rollout authority was used to restart the same verified bridge artifacts in
  dependency order; both services returned active with zero restarts, empty
  warning/error journals, and `p3-canary` reconnected on `71ad3e53`;
- a subsequent current-main deployment stopped the system P3 node cleanly and
  did not restart it. Its privileged helper remains active, no companion node
  process is running, and the registry snapshot still carries the last bridge
  version and an aging connected state. Restarting or retiring that canary now
  requires a deliberate live-operation decision;
- `ab-local-test` advertises the complete current eight-command browser
  catalogue, so historical catalogue admission is no longer needed. On the
  earlier `55a656df` deployment, trace
  `trace-turn-325ced9441a30ff7be34004e` targeted `companion`, opened ready,
  observed, navigated successfully, remained ready, and closed cleanly. The
  trace is schema-valid, complete, and untruncated. Because `ab-local-test` is
  the only connected companion advertising browser commands, this satisfies
  the functional companion gate. The separately tested gateway-local driver
  also opened, observed, navigated, and captured successfully;
- the converted configs retain seven inert deny entries: the browser agent
  names four removed task tools, and three media agents deny `exec` even though
  it is not in their discovered catalogue. These current-data leftovers must be
  removed rather than turned into another compatibility or diagnostic path;
- capacity is no longer a blocker: the filesystem had about 100 GiB free at
  re-audit. The retained 67 MiB local bridge backup and 111 MiB conversion
  staging tree are not the required same-time full backup of every effective
  binary and roughly 6 GiB active durable state. A later 207 MiB current-main
  deployment backup contains binaries, units, launchers, and service-state
  markers only, so it does not satisfy that gate either; and
- strict removal PR #901 is refreshed at `f7defbb2`: in addition to the
  browser-catalogue and empty-algorithm reset, it now requires the current
  authenticated execution profile and deletes the unused runtime-less
  companion constructor. All nine checks pass on this exact head, but its last
  clean review covers `5c6a493a`, not the rebased head. It remains intentionally
  unmerged until the live R1 gates below are satisfied. Main has since gained
  coding resume recovery and PRs #918-#921, so the branch must receive one final
  refresh, exact-head validation, and review before merge.

The 2026-08-27 post-X3.64 read-only gate audit further established:

- all expected services are active except the deliberately stopped P3 node,
  the last ten minutes of service journals have no error entries, and no old
  product process is running. The gateways, Web launcher, and local P5a node
  still execute `8418b021`, and the installed CLI has the same revision;
  merged main is now `1b02363e` through PR #940;
- the six node records remain explicit Ed25519. `p5a-canary` and
  `ab-local-test` are connected on `8418b021`, `p3-canary` remains stopped on
  bridge build `71ad3e53` while its matching service helper stays active, and
  connected `vpn` remains on pre-bridge `03b08be2` with no managed-update
  command in its advertised catalogue. The old pending Darwin and revoked
  Linux records remain unchanged;
- all five configs load as version 4, still define 21 agent entries over 20
  distinct workspace roots, and every root has `AGENTS.md` with no root
  `AGENT.md`. One ignored root `IDENTITY.md` remains in the spouse workspace;
  current production source has no reader for it, but R1 must preserve it in
  the full backup, reconcile any unique prose into the current profile or
  prose files, and remove the ignored file rather than silently discarding its
  content or restoring a second reader;
- the seven inert policy entries are exactly the main browser's four removed
  task-tool denies plus the main, family, and spouse media agents' `exec`
  denies. Other deny rules remain current policy and are not cleanup targets;
- the filesystem has 80 GiB free and the five active profile trees occupy
  6,480,493,890 bytes. The three recent 207 MiB deployment backups still cover
  binaries, units, scripts, and service-state markers rather than the complete
  active durable state, so the same-time full-backup gate remains open; and
- all 811 retained outbox version 2 records are terminal: 777 delivered, 31
  definitely failed, two abandoned, and one ambiguous. There are no pending or
  interrupted deliveries. The three deployed task registries contain 250
  terminal tasks and 1,200 current `task_event.v2` events with no missing
  generation or invalid sequence, and the sole interaction registry contains
  28 terminal records plus 236 current `interaction_event.v1` events. The R1
  reset therefore has no live task, interaction, or outbound delivery to carry
  across the stopped-state boundary; and
- PR #901 is still at `f7defbb2` on base `4b875d41`; all nine recorded checks
  are green, but its clean review covers `5c6a493a` and its current head has
  only an eyes reaction. A read-only merge simulation against current main at
  `1b02363e` is conflict-free, but the branch still requires a real final refresh,
  validation, exact-head review, and new merge approval after the live gates.

The 2026-08-27 post-X3.66 read-only gate audit supersedes the mutable facts in
that snapshot:

- merged main is `72d07542` through PR #943, while the five gateways, Web
  launcher, installed CLI, and local P5a companion still run the previously
  deployed `8418b021` release. All expected user services are active, the
  deliberately stopped system P3 node is the only inactive product service,
  its matching helper remains active, the last ten minutes contain no service
  errors, and no old product process is running;
- the six retained node records still name Ed25519, an executor, and a policy
  revision. `p5a-canary` and `ab-local-test` are connected on `8418b021`, the
  stopped `p3-canary` record retains bridge build `71ad3e53`, connected `vpn`
  remains on pre-bridge `03b08be2` without a managed-update command, and the
  old pending Darwin and revoked Linux records remain unchanged;
- all five configs decode as version 4 and still define 21 agent entries over
  20 workspace roots. Every root has `AGENTS.md`, none has root `AGENT.md`, the
  single ignored root `IDENTITY.md` remains 301 bytes, and the same seven inert
  deny entries remain. `doctor` reports no load or schema errors; its exit-2
  results are independent policy and security findings;
- the filesystem has 83,558,346,752 bytes free and the five active profile
  trees occupy 6,482,271,509 bytes. No backup newer than the partial 2026-08-26
  deployment backups exists, so the same-time full-state backup gate remains
  open;
- all 811 outbox version 2 records remain terminal with the same status split.
  Retention pruning leaves 249 terminal tasks and 1,196 valid
  `task_event.v2` events with no missing generation or invalid sequence. The
  interaction inventory remains 28 terminal records and 236 valid
  `interaction_event.v1` events; and
- PR #901 remains at `f7defbb2` on base `4b875d41` with nine historical green
  checks. Its last clean review still names `5c6a493a`, the current head still
  has only an eyes reaction, and a corrected read-only merge simulation of the
  exact head against `72d07542` is conflict-free. It still requires a real
  final refresh, validation, exact-head review, and new merge approval after
  the live gates.

The local deployed-operations helper also needs a pre-R1 path correction: its
topology and status script still probe `/home/server/src/mintclaw/default`,
which no longer exists, while the actual source checkout and gateway build are
under `/home/server/src/mintclaw`. The health fields above were verified from
the real units, processes, registry, and state files rather than treating the
helper's empty source SHA as valid evidence.

The completed 2026-08-28 R1 operation supersedes every mutable gate above:

- the operator explicitly authorized the full backup, cleanup, strict
  deployment, verification, rollback exercise, `vpn` upgrade, P3 retirement,
  and removal of obsolete pending and revoked identities;
- the stopped-state backup at
  `/home/server/mintclaw-r1-backup-20260828T053340Z` contains the full
  `.mintclaw` archive, host configuration and binaries, effective process
  executable images, strict release artifacts, retired outbox state, rollback
  state, service metadata, and checksums. A 2026-08-28 read-only recheck passed
  the archive, effective-binary, retired-outbox, rollback-state, and strict
  release SHA-256 manifests;
- `vpn` was upgraded to bridge-or-newer revision `0df185dc`. P3 was stopped and
  retired, and the obsolete pending and revoked records were removed. The
  three retained records (`p5a-canary`, `ab-local-test`, and `vpn`) are
  connected and explicitly carry Ed25519, an executor, and a policy revision;
- PR #901 merged as `049e337f`. Strict revision `827e0f70`, which also includes
  the separately approved browser-default correction in #946, is installed.
  The running main gateway, P5a node, and launcher exactly match the backed-up
  strict artifacts; all expected services are active, P3 is intentionally
  absent, and the verification journal has no warning or error entries;
- all five active configurations load as version 4 and describe 21 agents over
  20 distinct personal workspace roots. Every root uses standard `AGENTS.md`,
  no root retains `AGENT.md` or `IDENTITY.md`, and the seven inert deny entries
  were removed without changing other policy;
- 822 terminal version 2 outbox records were checksum-recorded and retired at
  shutdown, and five additional terminal version 2 records created before the
  strict cutover were separately checksum-recorded and retired. The 2026-08-28
  follow-up found only version 3 records, all delivered;
- the rollback exercise proved that the exact previous binary rejects current
  outbox version 3, then reached ready only while that state was safely
  quarantined. The current state and strict binaries were restored, and
  completed untruncated diagnostic trace
  `trace-turn-69eb624d37fb9d807837d947` records the post-rollback smoke; and
- later architecture packets #948-#976 are merged on `main` but are newer than
  the deployed R1 release. R1 remains complete; deploying the strict session
  contracts in #968, #971, #972, #975, and #976 requires the separate
  stopped-state closeout recorded below.

The 2026-08-29 final Z1 read-only audit supersedes the mutable deployed
inventory above without reopening the completed R1 reset:

- source `main` is `e60b8e26` through PR #976, while the installed gateway,
  CLI, and node still report `v0.1.0-p8a.2-928-g827e0f70`. All five gateway
  services and `mintclaw-node-p5a-canary` are active with zero restarts since
  the verified R1 start on 2026-08-28;
- the #975 read-only full-corpus rehearsal inspected all 3,570 metadata
  documents. After removing only the already registered `aliases` member in
  memory, all 742 retained current documents passed the exact root decoder and
  key-to-filename validation; no deployed byte was changed;
- all five configs are version 4. Their 21 agent entries use object-shaped
  model selections, the 20 distinct active personal roots all contain root
  `AGENTS.md`, and none contains root `AGENT.md` or `IDENTITY.md`;
- the current validators pass all deployed task and interaction snapshots.
  The three task stores contain 249 terminal tasks and 1,113 valid
  `task_event.v2` events with no active task. The interaction store contains
  19 terminal records and 153 valid events at commit sequence 4,801, with no
  active interaction;
- the outbox contains ten version 3 records, all delivered. The node registry
  contains exactly the three current connected identities (`p5a-canary`,
  `ab-local-test`, and `vpn`) with explicit Ed25519, executor, and policy
  revision fields; there is no P3 or pending identity. The two enabled gateway
  browser profiles explicitly use `any_http`, and all eight retained node
  browser profiles satisfy the current contract;
- service journals since the R1 start contain no recovery, reconciliation,
  panic, or fatal incident attributable to the architecture reset; and
- no deployed coding thread store exists under the active profile trees. The
  remaining deployment gate is therefore the normal-session corpus, not an
  unenumerated coding history.

The #975 point-in-time deployed session cutover cohort was:

| Cohort | Retain and convert: current opaque scope v2 | Archive: non-current identity or scope |
| --- | ---: | ---: |
| Metadata documents | 742 | 2,828 |
| Paired JSONL histories | 596 | 636 |
| Metadata bytes | 598,743 | 2,007,528 |
| JSONL bytes | 15,684,051 | 105,055,282 |
| Removed `aliases` members | 329 | 2,367 |
| Old nested tool calls | 3,728 | 16,455 |
| Google-specific old tool metadata cases | 2 | 118 |

The full inventory contains 3,570 metadata documents: 834 opaque keys, 2,219
`agent:*` keys, and 517 `task:*` keys. Scope classification found 742 opaque
scope-v2 records, 91 opaque scope-v1 records, 677 agent scope-v2 records,
1,542 agent scope-v1 records, 148 task scope-v2 records, one task scope-v1
record, and 369 records with no scope. Key, count, skip, and filename bounds
are valid; no unknown scope field or orphan JSONL file was found. Runtime
selection is nevertheless intentionally narrower than storage validity:
historical key families, scope v1, and missing scopes are archived rather than
translated, even when their generic metadata envelope is otherwise valid.

The 2026-08-29 copy-only `scripts/sessioncutover` rehearsal then loaded the
same five current configs and deduplicated the same 20 session roots. Its
source-stability re-read passed across all 4,802 emitted files. It retained
742 metadata documents and 596 histories, archived 2,828 metadata documents
and 636 histories byte-for-byte, removed 329 `aliases` members, flattened
3,728 nested tool calls plus two equal Google-signature cases, and validated
all 9,538 retained messages with the current runtime decoder. Independent
output inspection found zero retained aliases, zero nested tool calls, zero
duplicate source paths, and zero archive digest mismatches. Retained metadata
was 598,744 bytes, one byte above the earlier #975 point-in-time inventory;
all cohort counts and history byte counts were unchanged. The services remain
live, so the authorized stopped-state pass must take a new authoritative
snapshot rather than treating this rehearsal output as deployable state.

The exact-head #978 rehearsal later in the same live period completed
successfully after two additional empty current metadata documents appeared:
744 retained metadata documents, the same 596 retained histories, 2,828
archived metadata documents, 636 archived histories, and 4,804 emitted files.
All transformation and history totals were unchanged, and the independent
zero-alias, zero-nested-call, archive-digest, disjointness, and file-coverage
checks still passed. This live drift is expected and is not a converter error:
cohort counts are evidence, not hard-coded acceptance rules. The stopped-state
manifest, after the source-stability check passes, is the cutover authority.

The subsequent archive-framing audit found one scope-v1 archive whose metadata
records 1,297 messages while its JSONL contains 1,299 correctly framed, valid
JSON records. Because this cohort is never read by the new runtime, guessing
which two records to discard or rewriting its historical metadata would add an
unsafe repair policy. The converter therefore rejects every retained count
mismatch and every missing positive-count history, but preserves archived
histories byte-for-byte and records each archived metadata/physical count
divergence explicitly in the manifest and command result. The full stopped-state
backup remains the rollback authority.

The review-fix rehearsal completed over the full live corpus with 745 retained
metadata documents, 596 retained histories, 2,828 archived metadata documents,
636 archived histories, and 4,805 emitted files. It reported exactly the one
known archived count divergence above. The retained aliases, nested tool-call,
archive-digest, source-path disjointness, and file-coverage checks all remained
at zero failures. This remains rehearsal evidence only; writers were not
stopped and production state was not changed.

No production state was changed during that audit. Before a source release at
or after `e60b8e26` is installed, a newly authorized stopped-state operation
must back up the exact R1 binaries and full state, archive the non-current
cohort, remove `aliases` and flatten tool calls only in the retained cohort,
strictly validate the complete result, install mutually compatible first-party
binaries, restart and run canaries, and exercise rollback with the matching
pre-cutover state. The archive is evidence and recovery material; the new
runtime does not read it.

The 2026-08-29 post-deployment audit supersedes those deployment facts and
exposes an ordering violation without changing the source-side result:

- `origin/main` and the installed core report `e36a606f`, which includes the
  strict session readers. All expected first-party services are active, no
  product or global unit is failed, the HTTP probes return their expected
  status, and the ten-minute error window is empty;
- no eligible stopped-state manifest or installed converted corpus exists.
  The strict release was installed while the active session trees still
  contain the registered old fields and shapes. Current health does not prove
  that an older retained session can be opened;
- a new live copy-only rehearsal passed its source-stability check across all
  20 configured session roots. It retained 764 metadata documents and 606
  histories, archived 2,828 metadata documents and 636 histories, removed 329
  `aliases` members, flattened 3,755 nested tool calls plus two
  Google-specific cases, and validated 9,673 retained messages. It reported
  only the already documented archived 1,297-versus-1,299 count divergence;
  and
- no session decode error is visible in the bounded post-deployment journals,
  so this is an incomplete cutover and latent old-session access risk, not
  evidence of a current service outage.

The live rehearsal output was removed after its aggregate evidence was
captured. It is not a deployment candidate because writers were not stopped.
The authorized pass must take a fresh stopped-state snapshot, whose manifest
supersedes every live count above.

### Re-audit corrections

The original keyword inventory was useful for discovery but too broad as an
architectural rule. `alias`, `fallback`, and `compatible` also name legitimate
current concepts: configured model and node aliases, provider failover, default
values inside one current contract, and external OpenAI-compatible APIs. They
must not be removed merely because of their spelling.

The deciding question is whether a branch preserves a historical MintClaw
representation or creates a second owner for a current fact. Z1 therefore uses
semantic classification in addition to keyword search.

The re-audit also found that `AGENTS.md` must not be described generically as a
legacy file. It is the [cross-tool coding-instruction convention](https://agents.md/)
supported by [Codex](https://learn.chatgpt.com/docs/agent-configuration/agents-md),
[Jules](https://jules.google/docs/), GitHub Copilot, and other coding agents,
and MintClaw's coding runtime already discovers it hierarchically. MintClaw's
singular `AGENT.md` is a separate PicoClaw-derived personal profile manifest
with typed frontmatter. Packet X3.44 removed a dual reader at that
personal-profile boundary; packet P1 below owns the final metadata/prose
separation and standards alignment.

### Compatibility and simplification debt register

| Debt | Classification | Current evidence | Status |
| --- | --- | --- | --- |
| Previous and older browser catalogue schemas | Temporary first-party wire adapter | #901 deleted the historical generators; the sole browser companion advertises the current catalogue, and strict deployment plus browser canaries passed | Closed in R1 |
| Empty node `key_algorithm` | Temporary first-party wire and persisted-state adapter | All retained identities and connected peers explicitly carry Ed25519; #901 deleted omitted-algorithm admission | Closed in R1 |
| Optional node execution profile and runtime-less companion constructor | Temporary first-party wire/API adapter | Every retained record carries the authenticated execution profile; #901 made it mandatory and deleted runtime-less construction | Closed in R1 |
| Deployed personal-profile cutover | Coordinated persisted-config and workspace cutover | All profiles use version 4 and standard `AGENTS.md`; the ignored files and inert policy entries are gone, and strict deployment plus rollback passed | Closed in R1 |
| Non-current session identities and scopes | Coordinated persisted-state archive, not a runtime adapter | The stopped-state manifest archived 2,828 metadata documents and 636 paired histories; the installed current corpus contains none of them | Closed in Z1 |
| Removed session metadata `aliases` | Coordinated current-state conversion | The stopped-state conversion removed 329 members; the post-observation verifier found no surviving member to transform | Closed in Z1 |
| Nested persisted tool calls | Coordinated current-state conversion | The stopped-state conversion flattened 3,721 calls plus two Google-specific cases; the post-observation verifier found no surviving nested call | Closed in Z1 |
| `scripts/sessioncutover` | Temporary external deployment tool, not a runtime adapter | Deployment, matched rollback, reapply, canaries, observation, and evidence capture passed; this closeout deletes the command and tests | Closed in Z1 |
| Restricted-policy browser catalogue transition | Coordinated first-party peer and persisted-registry reset, not a runtime adapter | The first O3 rollout strictly rejected the previously connected `ab-local-test` catalogue; the rollout restored service, upgraded that companion, archived and removed its one stale registry record, and re-approved the same identity against the current bounded command surface | Closed in O3 |

No registered R1 or Z1 compatibility adapter remains open, and the final
source Z1 audit found no new source adapter. The deployed-data gates were
resolved outside the running product under a stopped-state backup. No Z1
session reader, normalizer, migration hook, or converter remains in the
steady-state source.

### Post-Z1 runtime ownership register

The cutover also exercised shutdown, recovery, reconciliation, and live-client
error paths more aggressively than ordinary operation. These findings are not
compatibility adapters and do not reopen the completed data cutover. They are
kept as separate packets so the closeout does not become a mixed reliability
refactor:

| Packet | Finding | Required simplification and exit gate | Status |
| --- | --- | --- | --- |
| O1 | `mintclaw-main.service` exceeded its 30-second stop budget and was killed on all five stops observed in the cutover and observation window | Give gateway shutdown one bounded lifecycle owner; repeated loaded stops must exit cleanly without `SIGKILL`, abandoned child processes, or session corruption | Complete; #1007 deployed on `7e52c1dd`, with five loaded stops, five clean starts, no timeout or `SIGKILL`, and no surviving terminal child |
| O2 | Seahorse reconciliation/provenance writes and live ingest can collide with `SQLITE_BUSY`; the warning recurred after final reapply | Give provenance mutation one concurrency contract; startup reconciliation and live turns must complete without a database-lock failure | Complete; #1015 deployed on `20cf7a18`, with one connection owner per agent database, fail-fast rejection of external writers, successful concurrent live turns, WAL/integrity verification, and no database-lock error |
| O3 | A live-agent error was delivered through the gateway outbox, but the CLI kept waiting until its outer timeout | Make success and error finals share one terminal-delivery owner; the client must return promptly after either final and never wait for a second terminator | Complete; #1019 deployed on `6fc47a3e`, with correlated canonical error finals, immediate client completion regression coverage, a successful live turn and trace, and a coordinated current browser/node reset with no historical reader |

The full O1 build, recovery, rollout, five-cycle loaded shutdown evidence,
smokes, trace, and cleanup record is in the
[O1 shutdown deployment evidence](../operations/architecture-simplification-o1-shutdown.md).
The O2 build, compact recovery set, five-profile rollout, concurrent live-turn,
persisted-session, trace-correlation, and journal evidence is in the
[O2 Seahorse deployment evidence](../operations/architecture-simplification-o2-seahorse.md).
The O3 build, exact-head review, rollback, coordinated browser/node reset,
five-profile rollout, live-turn, trace, persistence, and observation evidence
is in the
[O3 live error-final deployment evidence](../operations/architecture-simplification-o3-live-error-final.md).

Diagnostic evidence also showed that `root_turn_id` alone is not globally
unique across restarts. Trace selection for these packets must therefore bind
time, profile, and session identity in addition to the turn identifier; this
is an evidence-selection rule, not another runtime compatibility layer.

C2 is closed. PR #924 moved durable checkpoint ownership to the coding
composition root; PRs #925-#927 made recovery reads and constructors explicitly
context-aware; and PR #929 made the coding runtime replace derived state as one
owned engine while delaying retrieval publication until reconciliation and
integrity verification succeed. The JSONL-authoritative recovery semantics from
PR #908 remain current product behavior rather than compatibility debt.

The current implementation also has one non-blocking observability follow-up,
not a compatibility adapter: an exact deny rule can be reported as an unknown
tool or MCP server after policy filtering removes the denied capability from
the discovered catalogue. A later agent-policy diagnostics packet should make
configured denies quiet while continuing to warn about genuinely unknown
allow or deny patterns.

PR #885 classifies the scope-less tool-feedback lookup as obsolete inference
left behind after session-stable ownership replaced the older turn-scoped
design. It deletes the active-entry scan; cleanup now requires the same
explicit session or trace identity used by publication.

PR #886 deletes the unused `ToolRegistry.SetAllowlist` API, its registration
filter, clone state, and discovery-tool exception. Typed
`AgentCapabilityPolicy` is the sole production owner of per-agent tool
authorization; the generic registry owns only catalogue membership and
lifecycle.

PR #887 deletes the unused exported `SaveConfig` alias and its private wrapper.
`Repository` is the sole config-persistence owner. Its unconditional `Save`
operation remains current at the explicit OpenClaw import and temporary
MCP-edit boundaries, while managed config mutation continues to use `Update`
or revision-checked `Replace`; tests use the same repository contract through
package-local setup helpers.

PR #888 deletes the caller-free `Manager.SendToChannel` API and its direct
transport fallback. Active synchronous and queued delivery continue through
the delivery runtime's canonical worker, which owns rate limiting, retries,
ordering, tool feedback, and outcome publication.

PR #889 deletes the caller-free Ed25519-only `IdentityProof.Verify` wrapper.
Node admission and current tests use the algorithm-aware `VerifyIdentity`
contract; Ed25519 remains supported, and the empty-algorithm wire and storage
adapter remains isolated for the coordinated R1 cutover.

PR #890 deletes six exported OpenClaw import-model helpers with no production
callers and the private provider type used only by that facade. The explicit
`LoadOpenClawConfig` to `ConvertToMintClaw` path remains the sole typed import
contract and continues to read channel, provider, and agent fields directly.

PR #940 renamed compatibility-shaped internal variables, helpers, comments,
and one test description according to their current semantics. It did not
remove provider failover, standard platform fallback behavior, the external
Kagi response-shape adapter, Bedrock's documented upstream deprecation, the
strict `legacy` context-manager rejection, or the benchmark baseline.

PR #942 resolves the raw outbound-metadata design debt. `OutboundMessage` and
`OutboundMediaMessage` directly own `OutboundMetadata`; tool calls and
interaction choices use bus-owned typed contracts; async user delivery passes
metadata explicitly instead of cloning a turn only to mutate its inbound
context; and the dead interaction `delivery_key` copy is gone. Supported
metadata values and their cross-field requirements are validated before outbox
persistence and replay. Outbox version 3 persists that direct contract and
strictly rejects version 2 rather than adding a dual reader. R1 archived the
terminal live version 2 inventory under the stopped-state backup before
installing this release.

The 2026-08-25 source-only pre-R1 audit found no additional adapter at that
time. The later pre-Z1 source and live-state audit corrected that conclusion:

- PR #911 removed an independently scoped dead Seahorse shutdown context,
  cancel function, `CompactionEngine.Close`, and lazy context allocation left
  after condensed compaction became synchronously joined. The real
  per-conversation join map and caller cancellation remain current ownership;
- PR #901 now also removes the optional execution-profile validator and the
  unused runtime-less companion constructor identified above;
- PR #914 removed the parent-only Telegram approval ordinal reservation, its
  sole `reserveOrdinal` helper, and the historical acknowledgement fixture.
  Current parent-only approvals now use ordinal zero, while question-control
  cleanup and normal durable outbox recovery remain unchanged;
- PR #916 removed the benchmark reporter's inference of missing
  `validF1Count` values from older evaluation JSON. Current writers persist the
  count explicitly, including a legitimate zero when every answer is invalid;
  the deliberately historical full-transcript comparison baseline remains a
  benchmark mode rather than a product input reader;
- PR #919 renamed the sole current prompt renderer and final-delivery path
  instead of letting `legacy` terminology imply a second implementation;
- PR #920 removed false provider-conversion terminology and documented the
  current explicit `provider` plus provider-native `model` contract;
- PR #921 removed TTS's order-dependent scan for the first API-backed model ID
  containing `tts`. `voice.tts_model_name` is now the sole selector, and the
  selected enabled entry must have explicit provider and model fields. A
  read-only deployed audit found no profile relying on the removed path;
- the post-merge review of PR #908 preserved its justified recovery behavior
  but found new composition debt. PR #924 removed durable checkpoint ownership
  from the frontend adapter; PRs #925-#927 removed optional and background
  recovery-context paths; and PR #929 replaced corrupt derived state as one
  unpublished engine before retrieval registration. C2 is complete without
  weakening fail-closed resume, typed corruption checks, or no-replay tool
  outcomes;
- the earlier Git-tracked zero-caller scan after PR #896 counted test references
  as sufficient evidence for a public API. The corrected production-only scan
  retained active entry points, `NamedHook`, clock and provider injection,
  test-harness helpers, and console restoration. PR #935 deleted the methods
  with no production caller, and PR #936 made the dependency-aware heartbeat,
  TTS, coding TUI, Web fetch, and exec constructors canonical while moving
  concise test defaults behind private helpers;
- PR #933 made `health.Server` handler-only. The channel manager's shared
  gateway server is now the sole HTTP listener and shutdown owner, and the
  unused readiness-check registry is gone without changing the health, ready,
  or reload routes;
- a focused re-audit of recent coding PRs found that the frontend compaction
  status type is a justified presentation projection and that the synchronous
  checkpoint bus tap keeps durable metadata with the coding composition root.
  PR #937 made the background scheduler emit the execution mode it already
  owns, projected that fact directly, and deleted the adapter heuristic plus
  three superseded projector convenience methods used only by tests;
- PR #938 made subagent, spawn, and delegate dependencies complete and
  immutable at construction. Missing task registries, child runners,
  allow-list and objective policies, or self identity now prevent tool
  publication, while `SubTurnSpawner` remains the real tool-to-agent package
  seam;
- PR #967 replaced task deliverable normalization on load with one validator
  shared by current reads and writes. The final deployed audit passes all
  retained task snapshots without repair; and
- the other production keyword matches are benchmark baselines, external
  provider or platform protocols, provider failover and current defaults, or
  strict rejection of removed inputs. PRs #965-#976 removed the remaining
  historical inference and duplicate-contract findings, and the final Z1
  table below records why each surviving discovery family remains.

## Canonical Contract And Bounded Compatibility Rules

Every implementation packet in this roadmap follows these rules.

### Wire protocols

- Keep one canonical protocol model in production code.
- Guarantee the current and immediately previous first-party release when
  their protocol majors overlap.
- Prefer additive optional fields and capability negotiation; do not add a
  branch for every release that omitted the field.
- Send an action or feature only when the peer advertises it.
- Ignore unknown informational envelope fields, but strictly validate the
  known authority-bearing payload for the advertised action.
- Reject a non-overlapping protocol major with an actionable upgrade error.
- Do not infer capabilities from an old generated schema or accept both an old
  and new field name.

### Runtime configuration and persisted inputs

- Accept only the current MintClaw config and persisted record shape.
- Reject a missing, older, or newer storage version with an actionable error.
- Do not infer omitted fields for compatibility. A default is allowed only
  when it is the documented current semantic default.

### Persistence

- Write only one representation.
- Read only the current representation.
- Do not keep dual-read, dual-write, alias, historical-schema, or lazy
  migration code in the running product.
- A coordinated deployment may use a one-time external conversion. That
  converter is not linked into normal startup and is removed after the
  cutover.
- State whose authority cannot be safely converted is invalidated and
  recreated rather than heuristically reinterpreted.

### APIs and tools

- Remove deprecated methods and fields after all in-repository callers move.
- Do not keep adapter methods solely for old tests; update the fixtures.
- Tool names are product APIs. When a replacement is chosen, remove the old
  tool and update prompts, docs, allowlists, schemas, and tests in the same
  packet.
- An exact current-version rejection check is allowed; a fallback execution
  path is not.

### Compatibility budget and deletion cadence

- The guaranteed rolling window is current plus one previous release.
- Supporting two previous releases requires a named rollout owner and deletion
  release in the implementing PR.
- Additive compatibility that requires no old implementation may continue
  naturally beyond the guarantee.
- A temporary adapter lives only at one process boundary, is covered by a
  removal test or tracking item, and is deleted as soon as the fleet is
  upgraded.
- Periodically declare a coordinated compatibility reset: upgrade every
  first-party component, delete all pre-reset adapters and fixtures, and begin
  the rolling window again from that release.
- Every release that changes an internal wire protocol audits expired adapters
  and old capability aliases.

### Deployment

- Dependent first-party binaries are normally upgraded as a set, but the
  current gateway may roll while immediately previous companions remain
  online.
- A deploy is blocked when a registered peer has no overlapping protocol major
  or requires an authority-bearing capability whose current semantics differ.
- Rollback restores the matching binary and its backed-up state as a unit. It
  does not require the new binary to understand the old state.

## Bird's-Eye View Of Coding Frontend Complexity

Before packet C1, the coding frontend was shaped like a small client/server
system even though no process boundary existed:

```text
AgentLoop runtime events
        |
        v
agent adapter
        |
        v
projector -> revisioned snapshot + retained deltas
        |                         |
        |                         +-> gap detection / replay window
        v
controller watch
        |
        v
reducer -> terminal UI model -> renderer
```

This architecture solves real distributed-client problems:

- a web or IPC client can disconnect and request changes since revision N;
- multiple consumers can rebuild the same authoritative state;
- a slow consumer can detect a missed delta and resynchronize; and
- runtime internals do not leak directly into a user interface.

Today, however, the runtime, projector, controller, reducer, and terminal UI
are in one process. There is no network connection to drop and no independent
web client to reconnect. The TUI receives a delta only to reconstruct state
that the producer already holds. That adds protocol versions, 23 delta kinds,
revision counters, retained change windows, snapshot resynchronization, and
two layers of state mutation before rendering.

The simpler current target is:

```text
AgentLoop runtime events
        |
        v
coding presentation store
        |  owns one current immutable view
        v
terminal UI subscription -> renderer
```

The store preserves the valuable boundary: the TUI still consumes coding
presentation state rather than `AgentLoop` internals. It publishes the latest
bounded view in process and does not retain a transport replay protocol.

If an actual web or IPC client is later admitted, that work starts at the
presentation-store boundary. It must justify and introduce a real transport,
authentication, connection lifecycle, consumer identity, backpressure, and
resynchronization contract together. The current runtime should not pay those
costs in anticipation.

## Ordered Implementation Packets

Each packet is one focused PR unless its exit criteria reveal a smaller safe
split. Dependent packets start from the merge of their prerequisite. Every PR
records production/test/documentation additions and deletions at first review.
Repeated cross-package fixes trigger an architecture checkpoint rather than
another compatibility layer.

### S0 — Admit the simplification policy and roadmap

Scope:

- publish this roadmap;
- make the canonical-contract and bounded-compatibility policy explicit; and
- record the coding frontend decision in plain language.

Exit criteria:

- the architecture index links this roadmap;
- later PRs can cite one compatibility budget and deletion policy; and
- no runtime behavior changes.

### B1 — Delete historical browser persistence compatibility

Scope:

- delete legacy browser input and output schema generators;
- delete stored-schema epoch enumeration and exact comparison against
  historical generated JSON;
- remove legacy prepared-dialog fields and lazy migration;
- retain one current browser capability schema under the existing protocol
  range;
- allow a companion to advertise a smaller current action set without
  reconstructing a historical schema;
- make startup reject obsolete persisted browser authority or prepared-action
  state;
- preserve old dispatched no-replay evidence only as bounded opaque tombstones;
  and
- document the deployed-state cutover and evidence.

Likely owners:

- `pkg/nodes/browser.go`
- `pkg/nodes/registry_file.go`
- `pkg/browser/types.go`
- `pkg/browser/file_store.go`

Tests:

- the current schema round-trips;
- an older or missing persisted authority version fails closed with an
  actionable error;
- a compatible companion can advertise its smaller current action set and
  continue serving those actions;
- current approvals and protected values retain their security properties; and
- no test constructs an old schema as accepted input.

Exit criteria:

- `rg` finds no legacy browser schema generator, epoch, or migration;
- adding a browser action does not require editing historical schemas; and
- the deployed system contains only current browser authority records before
  rollout, while connected companions may expose the bounded compatible action
  subset; old dispatched tombstones remain opaque until retention removes
  them.

### B2 — Establish one browser action protocol

Depends on: B1

Scope:

- create one dependency-light canonical action envelope used by browser, nodes,
  gateway, and companion code;
- replace repeated validation and capability switches with one action
  descriptor table or typed per-action implementation;
- replace per-action companion host interfaces with one `Act` request;
- derive model-visible schemas and capability metadata from the current
  protocol definition; and
- retain independent enforcement at the browser broker without translating
  into another action type.

Tests:

- every action has validation, schema, approval, effect, and host dispatch
  coverage;
- the descriptor set and emitted tool schema cannot drift;
- unknown or unadvertised actions fail closed at every trust boundary; and
- local and companion placements execute the same contract tests.

Exit criteria:

- exactly one internal browser action wire type;
- no action-kind conversion switch between browser, gateway, and nodes; and
- adding a non-special action changes its implementation, descriptor, and
  focused tests rather than a broad package chain.

### T1 — Make deliverable and objective outcome canonical

Scope:

- move deliverable, objective outcome, receipt, and artifact references into
  one leaf domain package;
- store those exact types in the task registry;
- delete `Completion` and all completion-to-deliverable conversion;
- delete duplicate tool/task projections; and
- make child agents, delegated tasks, interactions, and status tools consume
  the canonical result.

Tests:

- successful, partial, failed, and unverifiable outcomes round-trip through
  the task registry;
- receipts preserve verification semantics; and
- no caller can populate mutually inconsistent completion and deliverable
  fields.

Exit criteria:

- one definition for every task-result fact;
- no `completionPayloadForLegacyStorage`; and
- no persisted or rendered `Legacy completion` path.

### T2a — Remove legacy task and subagent APIs

Depends on: T1

Scope:

- remove the in-memory `RunToolLoop` subagent fallback;
- require the current `AgentLoop` child runner;
- remove `spawn_status` and use `task_status` as the sole status tool;
- remove spawn-only projections and compatibility wording;
- update tool allowlists, prompts, schemas, docs, and fixtures atomically.

Tests:

- spawn, delegate, wait, cancel, and status use only the durable task path;
- unavailable child execution fails explicitly instead of falling back.

Exit criteria:

- one task registry, one child execution path, and one status tool;
- no test-only compatibility adapter remains in production code.

### T2b — Separate tool output, control, and delivery

Depends on: T2a

Scope:

- split stable execution output from suspension/task control and delivery
  directives in `ToolResult`;
- make each layer consume only the part of the result it owns; and
- update tool schemas, prompts, and fixtures atomically.

Tests:

- task output cannot express impossible legacy/current combinations; and
- suspension and delivery directives do not mutate produced output.

Exit criteria:

- `ToolResult` does not contain deprecated delivery or completion fields; and
- output, control, and delivery each have one explicit owner.

Implemented shape:

- stable output remains on `ToolResult`;
- async and suspension directives live under `ToolResult.Control`;
- one enum plus prepared outbound state live under `ToolResult.Delivery`; and
- the unused message send callback and boolean delivery projections are
  deleted instead of retained as adapters.

### D1 — Make outbox the sole delivery lifecycle owner

Scope:

- remove prompt and final delivery attempt state from interaction records;
- store outbox identities on interactions;
- route interaction prompt, final answer, and resumed completion through the
  same durable delivery coordinator;
- remove direct definite-retry-only sends owned by interaction code; and
- centralize receipt, ambiguous failure, retry, and restart recovery policy.

Tests:

- crash before send, during ambiguous send, after receipt, and during retry;
- duplicate callback and duplicate channel update handling;
- restart during prompt and final delivery; and
- no interaction state transition can claim delivery without an outbox
  receipt.

Exit criteria:

- one delivery state machine;
- interaction records contain domain and routing state, not retry machinery;
  and
- channel-specific delivery outcomes enter one typed coordinator.

Implementation sequence:

- Prompt delivery binds one deterministic outbox ID to the interaction before
  publication. The outbox owns transport attempts, definite failure,
  ambiguity, retry exhaustion, abandonment, and restart recovery; the
  interaction moves to `waiting` only after the admission-specific receipt
  reports delivery. Retry deadlines are honored, and recovered prompts are
  revalidated against their current interaction immediately before replay and
  settled from that exact admission's terminal receipt.
- Final answers and resumed task completions now bind their exact deterministic
  outbox IDs before intent creation, publish through the same coordinator, and
  resolve only from exact terminal receipts. Interaction records retain only
  those IDs; retry counters, transport timestamps, ambiguity flags, and the
  direct channel-send path are deleted. Recovery derives every delivery
  decision from the outbox and revalidates stale final intents before replay.

### D2 — Unify interaction, task, and continuation ownership

Depends on: D1 and T1

Scope:

- replace copied interaction-to-task lifecycle projection with references to
  one authoritative interaction/task relation;
- create one continuation executor for approval and question resumes;
- remove partial pipeline construction and manual tool replay from inbound
  interaction handling;
- serialize session-scoped tool feedback through the owning continuation or
  delivery component; and
- remove generation counters, fallback routing, and compatibility claims made
  unnecessary by the single owner.

Tests:

- steering before, during, and after pending approval;
- cancel/answer/retry races;
- duplicate and stale callbacks;
- restart at every durable transition; and
- task status always derives from the same authoritative relation.

Exit criteria:

- one owner for continuation state;
- no best-effort projection between durable registries; and
- interaction resume does not instantiate an ad hoc pipeline.

### A1 — Replace the `AgentLoop` service bag with owned coordinators

Depends on: T2b and D2

Scope:

- extract `TurnRunner`, `InteractionCoordinator`, `IngressCoordinator`, and
  `DeliveryCoordinator` with explicit state ownership;
- construct them once during runtime initialization;
- remove pass-through `Pipeline` service wrappers whose only implementation is
  `AgentLoop`;
- retain narrow interfaces only at a real consumer or implementation boundary;
  and
- keep behavior unchanged while moving ownership.

Tests:

- current turn, retry, compaction, steering, delivery, and restart contract
  suites run against the extracted coordinators; and
- ownership tests prove mutable registries are not shared by unrelated
  components.

Exit criteria:

- `AgentLoop` composes coordinators instead of directly owning every mutable
  subsystem;
- `NewPipeline(al)` no longer reconstructs dependency bags per run; and
- pass-through interfaces and nil-check wrappers are deleted.

### A2 — Replace `processOptions` with explicit turn requests

Depends on: A1

Scope:

- make user turns, child turns, interaction resumes, coding turns, and recovery
  select one explicit current turn mode;
- normalize that mode once into a small internal turn specification;
- remove boolean combinations that describe modes indirectly; and
- make invalid combinations unrepresentable or rejected at construction.

Tests:

- a table covers every admitted entrypoint and its normalized semantics;
- no mode depends on an omitted boolean's historical default; and
- ordering hooks such as turn readiness belong to one explicit phase.

Exit criteria:

- callers select a turn mode instead of constructing a boolean bag; and
- `processOptions` is removed.

Implementation sequence:

1. Remove the compatibility-shaped field mirrors from `processOptions` and
   make `DispatchRequest` the only owner of routing, session, inbound-message,
   and media facts. Rename the normalized runtime shape to `turnSpec` so it is
   not mistaken for an entrypoint API.
2. Make each admitted entrypoint select one explicit `turnMode` and construct
   its `turnSpec` from the mode's canonical behavior. An architecture checkpoint
   rejected separate request wrappers because they added more production
   scaffolding than they removed.
3. Retain the one selected `turnMode` on the normalized specification and
   derive command dispatch, scheduled agent projection, and initial steering
   polling from it. Delete the three independent booleans that could disagree
   with that mode.
4. Retain only fields whose values genuinely vary within a mode, plus
   value-bearing data such as the caller's empty-response text. Do not add
   separate request wrappers or another turn-policy object.

### C1 — Collapse the in-process coding frontend protocol

Depends on: A1

Scope:

- replace projector, retained revision deltas, gap recovery, and reducer
  replay with one in-process coding presentation store;
- let the TUI subscribe to immutable current views through one bounded,
  coalescing subscription;
- retain thread persistence and runtime events as source systems, not as a
  speculative IPC protocol;
- remove current protocol-version compatibility and `ChangesSince`; and
- update the coding roadmap to require a separate admission before adding a
  web or IPC transport.

Tests:

- initial render, streaming progress, tool updates, workspace refresh,
  compaction, cancellation, and terminal restoration;
- slow UI subscribers converge to the latest view without replay; and
- durable thread resume remains independent from frontend presentation state.

Exit criteria:

- one current presentation state in process;
- no retained delta log, gap recovery, or frontend protocol version; and
- future remote clients have a clear boundary but no speculative runtime cost.

Implemented shape:

- `Projector` owns one bounded `ThreadSnapshot` and atomically returns that
  view with a subscription to later views;
- each subscriber has one coalescing slot, so a slow UI receives the newest
  view without blocking the running turn;
- the TUI replaces its presentation view directly instead of reconstructing
  it through a reducer; and
- protocol versions, revisions, delta variants, retained changes, and gap
  recovery are absent from this in-process boundary.

Downstream P4 integration rules:

- the resume picker is a separate pre-controller catalogue screen with one
  replaceable bounded page; it does not extend `ThreadSnapshot`, introduce a
  reducer, or treat discovery observations as write authority;
- selected threads still pass through canonical metadata reload, project
  validation, and OS-backed lease acquisition before an active presentation
  store is constructed; and
- in-app commands consume the subscribed current view and the typed controller
  sink instead of recreating command-specific mirrors or a delta protocol.

### C2 — Simplify coding resume recovery composition

Depends on: C1 and merged PR #908

Status: complete through PRs #924-#927 and #929

PR #908 added justified recovery behavior: canonical JSONL can rebuild missing
or corrupt derived Seahorse state, resume fails closed until reconciliation,
and completed compactions checkpoint their canonical source revision. C2 keeps
those semantics while removing the composition machinery introduced around
them.

Implementation sequence:

1. PR #924 kept durable compaction checkpoint observation in the coding
   composition root, removed it from `pkg/coding/frontend/agentadapter`, and
   returned the adapter to presentation-only projection.
2. PRs #925-#927 made the recovery deadline an explicit construction
   dependency, retained one current context-aware constructor for each recovery
   owner, removed context from `CodingRuntimeProfile`, and threaded it through
   canonical history reads.
3. PR #929 rebuilt corrupt derived state before engine and retrieval references
   become visible, made the coding recovery owner replace the engine/store as
   one unit, and deleted the exported replacement factory and field-by-field
   mutation of an already-published `seahorse.Engine`.

Tests:

- resume reconciliation and cursor inspection share one deadline;
- typed `SQLITE_CORRUPT` and `SQLITE_NOTADB` remain the only destructive reset
  authorization;
- frontend projection failure or absence cannot suppress a durable compaction
  checkpoint;
- a rebuilt engine publishes retrieval tools only after successful complete
  reconciliation; and
- interrupted and unknown tools remain visible without replay.

Exit criteria:

- `CodingRuntimeProfile` contains layouts and admitted store construction only,
  never request-scoped context;
- presentation adapters perform no durable writes or durability callbacks;
- one context-aware constructor exists for each coding recovery owner; and
- derived-store rebuild replaces one owned unit without copying private engine
  internals across instances.

### X1 — Require the current configuration only

Scope:

- remove V0, V1, and V2 runtime config migration;
- remove deprecated field stripping, legacy bindings, old GitHub skill
  settings, provider/model shorthand, old channel field names, and legacy
  defaults;
- require the current version and current field names;
- update example configs, documentation, fixtures, and deployed config before
  rollout; and
- retain only explicit current semantic defaults.

Exit criteria:

- startup reads one config schema;
- no read path writes a migration or backup;
- no deprecated config field exists in current Go structures; and
- the deployed config passes strict validation before the new binary starts.

### X2 — Require current session and state storage only

Scope:

- remove legacy `agent:...` session-key parsing and alias resolution;
- remove runtime JSON-session to JSONL migration;
- remove old state-location discovery and move-on-start behavior;
- remove compatibility fallbacks between `SessionManager` and the current
  store; and
- convert or retire deployed legacy state during the coordinated cutover.

Implementation sequence:

1. Make the current opaque key and structured scope the only session identity:
   delete textual-key parsing, generated aliases, metadata alias scans, and
   history promotion transactions. Existing alias history is intentionally not
   imported at runtime after this cutover.
2. Make JSONL and the current state directory the only startup storage:
   delete JSON-to-JSONL migration, the in-memory store fallback, old-location
   discovery, and move-on-start behavior.

Exit criteria:

- one session-key format and one persistent runtime store;
- startup never searches for or rewrites old locations;
- no `.migrated` lifecycle in runtime code; and
- current state survives restart and compaction contract tests.

Implemented shape:

- opaque keys and the current structured scope version are the only runtime
  session identities;
- JSONL is the only persistent session store, and initialization failure stops
  checked startup and reload construction;
- the removed JSON snapshot backend has been replaced by an explicitly
  non-persistent test and benchmark store;
- startup ignores removed JSON snapshots and the old workspace-level
  `state.json` location; and
- frontend session lookup uses only current `ClientSessionIDs` metadata.

### X3 — Remove remaining internal compatibility surfaces

Scope:

- inventory remaining production references to `legacy`, `deprecated`,
  backward compatibility, fallbacks, aliases, and migrations;
- remove legacy cron defaults, ASR discovery fallback, old metadata maps,
  remote-workspace argument aliases, old channel delivery mappings, and
  deprecated helper APIs;
- classify every remaining use of `compatible` as either a current external
  integration, platform support, or an error;
- update tests that preserve an old contract; and
- delete obsolete migration documentation after its operational evidence is
  no longer needed, retaining historical PR records in the archive when
  useful.

Implementation sequence:

1. Delete the test-only in-agent event subscription adapter, legacy event
   envelope, and event-kind aliases. Tests subscribe to the canonical
   `pkg/events` bus directly.
2. Delete the producerless synthetic async-completion system-message reader.
   Current producers pass typed `AsyncCompletionInput` directly to the
   delivery coordinator.
3. Delete the channel-name ASR capability fallback. Voice-capable channels
   advertise ASR through `VoiceCapabilityProvider`; TTS remains derivable from
   the current `MediaSender` contract.
4. Delete the redundant `InjectSteering` and `InjectFollowUp` names plus the
   unsafe session-ambiguous `InterruptHard`; callers use `Steer` and
   `HardAbort(sessionKey)` directly.
5. Delete unused exported source aliases for Seahorse ingestion and Google
   schema sanitization; tests and callers use the canonical method names.
6. Delete ASR auto-discovery and the empty audio-channel default. Current
   configurations select one named voice model, and current audio producers
   supply their source channel explicitly.
7. Delete unstructured and multi-format channel allowlist matching. Every
   inbound producer supplies `SenderInfo`, and `allow_from` contains exact
   platform IDs or the explicit `*` wildcard.
8. Delete Seahorse startup schema upgrades and metadata backfill. Active
   databases have the current schema, and every conversation has a current
   canonical-history reconciliation watermark.
9. Delete frontend readers for the superseded detail-visibility preference,
   content-encoded tool calls, and task completion result. Consolidate session
   history media into the current attachment response at the API boundary.
10. Delete the completed launcher-token migration and the diagnostic-only
    boolean-origin state. The active launcher config has no token, and its
    SQLite credential store is initialized; runtime behavior depends only on
    the current effective config values.
11. Delete the gateway invocation JSON engine, startup importer, marker
    protocol, and downgrade exporter. The active SQLite database passes the
    current schema and integrity checks; runtime and tests use that one store,
    while rollback restores a matching binary and same-time SQLite backup.
12. Delete the pre-`kind` thought boolean from MintClaw Protocol reads and
    writes. Current gateways, the web frontend, CLI, and client channel use the
    typed `kind: "thought"` discriminator as the single message contract.
13. Delete arbitrary-turn `GetActiveTurn` and `InterruptGraceful` APIs. Command
    runtimes bind active-turn inspection to their known workspace and session;
    graceful interruption is session-scoped and fails closed on ambiguity.
14. Make skill registries one name-keyed current contract. Delete the sibling
    GitHub settings, list-shaped registry input, direct security entries,
    merge/PATCH fallbacks, and the JSON-first metadata parser; configuration,
    security overlays, and runtime lookup use the same registry map.
15. Make cron mutations one error-returning current contract. Delete implicit
    legacy defaults and ensure failed persistence cannot leave an in-memory job
    mutation committed.
16. Make configured `model_name` the sole model selector identity. Reject
    dangling static references during config load, reject unknown workspace
    frontmatter at agent construction, and delete raw model-ID,
    `provider/model`, provider-alias, and default-provider inference from agent
    resolution and gateway restart signatures.
17. Require every model entry to store one explicit provider plus its
    provider-native model ID. Delete steady-state Web API normalization,
    provider-prefix/default-provider inference, and the unused fallback parser
    APIs that preserve those alternate representations.
18. Make the canonical descriptor the sole source of optional provider
    capabilities and the descriptor-based image interface the sole image
    operation. A provider without an optional descriptor advertises no optional
    features; runtime code no longer infers streaming, thinking, search, or
    image support from legacy method shapes.
19. Make event streaming the sole provider streaming contract. Delete the
    accumulated-text operation, its wrapper adapters, and the event-mode flag;
    providers declare one streaming boolean and emit `StreamChunk` values
    directly.
20. Make typed text delivery the sole channel text contract. Delete the
    tuple-returning channel method and optional typed sender; every channel
    returns confirmed IDs, retryable remainder, acceptance, and errors through
    `DeliveryResult`.
21. Make typed media delivery the sole optional channel media contract. Delete
    the tuple-returning media method and optional typed fallback; every
    media-capable channel returns confirmed IDs, retryable remainder,
    acceptance, and errors through `DeliveryResult`.
22. Make `tools.image_generate.model` the sole image-generator selector.
    Delete the unused agent-default image fallback fields and the cross-section
    runtime fallback; removed fields are rejected by current-schema decoding.
23. Make `model_list[].enabled` the sole model-activation contract. Delete
    API-key and `local-model` inference, persist the boolean explicitly, and
    carry it through every model creation and multi-key expansion path.
24. Make `session.dimensions` the sole persisted session-partition contract.
    Delete the bidirectional scope synchronization machinery, keep only a
    one-release Web boundary adapter, and remove that adapter at the next
    coordinated compatibility reset.
25. Make JSON string arrays the sole persisted list shape. Delete the
    `FlexibleStringSlice` scalar, mixed-array, and custom text decoders; retain
    friendly scalar normalization only at the current Web request boundary and
    use the standard environment parser for comma-separated overrides.
26. Make the node registry validate stored command schemas directly. Persist
    compact current JSON at the writer, delete load-time command-schema
    compaction and browser-schema reconstruction, and reject catalogs whose
    authority-bearing descriptors no longer match the current contract.
27. Make MCP configuration one explicit current contract. Require exactly one
    of `stdio`, `http`, or `sse`, reject fields from another transport, delete
    transport aliases and inference, and make every interrupted session-loss
    call uncertain after reconnect without configurable replay.
28. Delete unused compatibility helper APIs and implicit internal call modes.
    Remove the uncalled browser click-effect and Web local-IP aliases, and make
    every Seahorse leaf-compaction caller select its current force mode
    explicitly instead of relying on a variadic default.
29. Scope runtime profiles to the coding frontend that actually uses their
    isolated roots and restart-only lifecycle. Delete the unused personal
    profile path, its policy switches, and its storage-cutover plan; keep the
    current hot-reloadable gateway workspace lifecycle explicit.
30. Delete the single-implementation `turnRuntimeHost` callback interface and
    its nil-safe pass-through wrappers. The turn runner retains process and
    spool-settlement lifecycle; in-turn abort, steering, filtering, events,
    and final rendering use the immutable pipeline snapshot directly.
31. Flatten `PipelineRuntimeServices` into direct pipeline-owned dependencies.
    Keep narrow bus and event interfaces where alternate implementations
    exist; use concrete active-request and abort owners, and delete their
    single-implementation interfaces and test-only `AgentLoop` pass-throughs.
32. Keep the semantic `PipelineContextServices` grouping, but replace its
    background-compaction, model-execution, and terminal-task interfaces with
    concrete runtime-generation owners. Preserve the context-runtime,
    steering, and media seams because they have real substitutes, and delete
    model-execution `AgentLoop` pass-throughs made unused by the concrete owner.
33. Keep the semantic `PipelineInteractionServices` grouping, but make fallback
    execution and asynchronous tool completion concrete. Delete their
    single-implementation interfaces and the optional fallback-observer type
    assertion; preserve interaction seams with real test substitutes.
34. Confirm that inbound coordination is already constructed once per
    `AgentLoop.Run` and remains correctly run-scoped. Delete the test-only
    `AgentLoop` reasoning component factory and pass-through façade; test the
    pipeline-owned reasoning component directly.
35. Make constructor-owned active-request, background-compaction, and
    model-execution components explicit. Delete their lazy `AgentLoop`
    accessors, the runtime-event adapter factory, and the unused synchronous
    delivery factory/pass-through; preserve nil handling at actual call
    boundaries without creating alternate owners.
36. Complete the planned Web session-contract compatibility reset. Delete the
    one-release `session.dm_scope` response alias and PUT/PATCH translator;
    emit and accept only `session.dimensions`, with removed fields rejected by
    current-schema validation.
37. Replace the pointer-backed `tool_feedback.subagents` compatibility default
    with the current boolean contract. Keep the default explicit in
    `DefaultConfig`, apply the same defaults at file and API decode boundaries,
    preserve explicit `false` during serialization, and remove the runtime nil
    fallback.
38. Make `bus.Streamer` the sole channel streaming interface. Delete the
    `channels.Streamer` source alias retained for older implementations and
    update every current channel and fixture to name the canonical owner.
39. Make one concrete `turnRuntime` own admission, active session and route
    claims, pending stops, request tracking, sequencing, inbound spool
    settlement, and runner generations. Delete the corresponding mutable
    `AgentLoop` fields and runner/admission pass-throughs, and remove the unused
    context API that exposed `AgentLoop` as a child-tool service locator.
40. Make each `turnRunner` generation construct and retain one reasoning,
    feedback, synchronous delivery, asynchronous delivery, and interaction
    component set. Give `Pipeline` those same instances, route host-side work
    through the current runner owner, and delete the `AgentLoop` factories and
    delivery pass-throughs that rebuilt equivalent components per call.
41. Extract one process-wide `interactionCoordinator` to own the interaction
    registry cache, resolution callbacks, resume-flight deduplication,
    workspace catalog serialization, and recovery admission. Delete those six
    mutable fields from `AgentLoop`, and make every reloadable interaction
    component reference the same stable owner.
42. Extract one process-wide `taskCoordinator` to own task registries,
    restart reconciliation, terminal-task context, durable delivery state, and
    async-completion admission. Make it depend directly on the interaction
    owner, delete the two remaining task/completion maps from `AgentLoop`,
    replace the pipeline's terminal-task `AgentLoop` service locator, and
    remove its three task-state callbacks, config callback, and duplicate
    user-delivery callback.
43. Make `TaskPrompt` the sole child-turn instruction field. Delete the unused
    `ActualSystemPrompt` shadow, its producerless runtime override and prompt
    source, and the historically inverted `SystemPrompt` name from both sides
    of the in-repository tool/agent boundary.
44. Remove the dual reader from the MintClaw-specific personal workspace
    profile boundary. Make root `AGENT.md` the sole immediate structured
    personal definition, delete the personal `AGENTS.md` and `IDENTITY.md`
    readers, the definition-source enum, and source-dependent prompt and cache
    branches, and translate the current OpenClaw import product's personal
    `AGENTS.md` at that explicit import boundary. A read-only deployed audit
    confirmed that all five active profiles already have root `AGENT.md`; an
    extra root `AGENTS.md` was shadowed and unused. This packet does not
    classify the standard as legacy: project and skill-level coding
    `AGENTS.md` files remain the current cross-tool instruction contract. P1
    owns the later separation of profile metadata from standard prose.
45. Make the Delta Chat account store the sole owner of mailbox credentials.
    Delete MintClaw's password, IMAP, and SMTP settings plus its account
    configuration and drift-reconciliation paths. Full email addresses must
    identify an already configured account; the explicit chatmail bootstrap
    flow remains the current account-creation path.
46. Require timestamp evidence before classifying inbound media as an adjacent
    follow-up. A missing or zero `created_at` remains readable history but does
    not prove recency. Delete the redundant current-turn relation aliases,
    input wrapper, and second classifier layer so one canonical relation type
    and classifier own the decision.
47. Make prompt-source registration fail closed. Register every current static
    and dynamic contributor before collection, reject an unknown source instead
    of warning and accepting it in compatibility mode, and retain placement
    validation on the same registry owner.
48. Remove historical objective-result inference. A successful child outcome
    must carry the current bounded `result`; do not reinterpret an old
    `explanation` as terminal output merely because a receipt exists. Reconcile
    this packet with the focused objective-receipt accounting work before
    changing the shared parser.
49. Delete scope-less tool-feedback target inference. Publication, pause,
    terminalization, and cleanup use one explicit session key or trace scope;
    missing identity must not scan concurrent coordinator state and guess the
    only currently active scoped carrier.
50. Delete the unused generic tool-registry allowlist. Per-agent authorization
    remains at the typed agent-policy boundary; the registry no longer owns a
    second policy map, registration branch, clone path, or discovery-tool
    exception.
51. Make `Repository` the sole config-persistence owner. Delete the unused
    exported `SaveConfig` alias and its private wrapper, retain unconditional
    `Save` only at explicit import and temporary-config boundaries, migrate
    test setup to the repository contract, and simplify enforcement to reject
    only direct config-file mutation outside the repository package.
52. Delete the unused `Manager.SendToChannel` entrypoint and its private direct
    transport fallback. Keep synchronous and queued delivery on the canonical
    per-channel worker so no callable path bypasses its rate limiting, retries,
    ordering, tool-feedback, or outcome-publication ownership.
53. Delete the unused Ed25519-only `IdentityProof.Verify` compatibility
    wrapper. Keep `VerifyIdentity` as the sole algorithm-aware proof verifier;
    this callable API cleanup does not remove Ed25519 or the separately tracked
    empty-algorithm wire and storage adapter before R1.
54. Delete the zero-caller OpenClaw import-model helper facade: its generic
    enabled, directory-load, provider, channel, allow-list, and agent helpers
    plus their facade-only tests. Keep the explicit source-path loader and
    typed `ConvertToMintClaw` conversion as the one import boundary.
55. Delete the zero-caller OpenAI-compatible provider constructor wrappers and
    provider-name option. Keep `NewProvider` with functional options as the
    sole constructor, retain direct option coverage, and keep the factory's
    current `SetProviderName` path instead of a second configuration API.
56. Make image-generation provider resolution one direct current dependency.
    Delete the unused exported resolver type and option plus the per-tool
    callback field. When no concrete provider is injected for testing, lazily
    call the canonical provider factory once on first execution.
57. Delete the caller-free `pkg/agent/adapters` forwarding package. Concrete
    message-bus and channel-manager owners already implement the retained
    agent interfaces directly, and tests use real substitutes; the two wrapper
    types add a second implementation layer without providing substitutability.
58. Delete the caller-free turn and tool-feedback helper facades: the exported
    accessor returning a private turn-state type, the superseded feedback
    finalization classifier, and the no-title working-summary wrapper. Keep the
    private context owner, live dismissal classifier, and title-aware renderer.
59. Delete caller-free convenience wrappers at external integration
    boundaries: contextless Anthropic usage, default-option browser login,
    provider-specific Groq transcription, pre-buffered HTTP error normalization,
    default self-update, and tag/nightly release URL lookup. Keep the
    context-aware, configurable, and canonical factory entry points used by
    current callers.
60. Finish the tracked zero-caller utility cleanup. Delete unused package-level
    logger variants and console-level setter, adaptive-any host helpers, the
    Discord voice-receive predicate, the Web language getter, and two LOCOMO
    selectors. Retain documented channel-registry discovery, the console
    restoration test seam, active loopback helpers, and third-party logger
    interface methods.
61. Make the health package handler-only. Delete its unused private
    `http.Server`, direct start and stop methods, and test-only readiness-check
    registry. Accept only the reload token at construction, register the three
    current routes on an explicit mux, and keep the channel manager's shared
    gateway HTTP server as the sole listener and shutdown owner.
62. Delete production-unused public APIs and test-only constructor facades.
    Remove `Config.SecurityCopyFrom`, `Registry.FindWaitingBySession`,
    `ProcessSession.SetStatus`, `ProcessSession.SetExitCode`,
    `MessageBus.ReplayInboundSpool`, and `logger.InfoF`. Collapse the heartbeat,
    OpenAI TTS, coding TUI, Web fetch, and exec test conveniences onto the
    context-, state-, option-, proxy-, and config-aware constructors used by
    production, give those constructors the canonical names, and use private
    test helpers where concise defaults improve fixtures. Retain deliberate
    extension, injection, test-harness, logger-console, and third-party
    interface seams.
63. Make compaction execution mode one owner-supplied fact. The background
    scheduler marks its requests, the lifecycle payload carries that value,
    and the coding adapter projects it directly. Delete reason/turn-based mode
    inference and the three production-unused `Projector` compaction facades;
    keep the distinct agent lifecycle and frontend presentation status types.
64. Make subagent, spawn, and delegate construction complete. Require the
    current task registry, child runner, LLM defaults, allow-list policy,
    objective policy, and self identity at their constructors, remove the
    setter sequence and immutable-field mutex, and reject missing required
    dependencies before tool publication. Keep `SubTurnSpawner` because it is
    the real package-cycle and substitution boundary.
65. Remove compatibility-shaped terminology from current internal behavior.
    Name package defaults, numeric IPv4 handling, Telegram text delivery,
    provider error refinement, and rejected removed arguments by their actual
    semantics. Keep external API adapters, provider failover, strict rejection
    messages, and development benchmarks classified rather than deleting them
    by keyword.
66. Make outbound messages own typed delivery metadata directly. Remove the
    cloned-inbound-state transport path, and make outbox version 3 the sole
    persisted delivery contract.
67. Require current configuration to name Web model activation, channel type,
    and channel security explicitly. Delete value-derived channel type and
    security inference instead of rebuilding omitted authority-bearing facts.
68. Construct each channel runtime once at the composition root and pass its
    owner explicitly. Delete lazy owner repair and manager paths that silently
    recreate missing runtime state.
69. Construct Seahorse engine collaborators eagerly. Require the current
    store, embedder, and related engine dependencies rather than retaining
    optional setters or late self-assembly.
70. Require every context-builder dependency at construction and delete
    optional runtime lookups that act as a service locator.
71. Name current channel contracts and ownership boundaries by their actual
    semantics, and keep the maintained channel guide aligned with the runtime
    rather than preserving refactor-era terminology.
72. Give every channel instance one immutable canonical identity and preserve
    its configured alias across runtime, health, routing, and delivery paths.
73. Centralize channel construction behind typed factories. Delete untyped
    registry values, assertions, and parallel construction branches.
74. Require the coding runtime to receive one canonical explicit Git directory
    instead of rediscovering or inferring repository control paths.
75. Require every enabled browser profile to declare its network mode. Do not
    use deployment location or target selection to infer network authority.
76. Make `InboundContext` the sole owner of inbound message identity, routing,
    and relation metadata. Delete duplicated message-ID fields and projection
    fallbacks in channel and bus layers.
77. Retry outbound delivery only after definite rejection or for a known
    untouched remainder. Preserve ambiguous acceptance as terminal uncertainty
    so generic chunks, Telegram media, and streaming finalization cannot replay
    a possibly visible effect.
78. Make coding root turns explicit in every current transcript writer. Remove
    fork-time inference from unmarked user-shaped messages.
79. Require interaction snapshots and events to persist their commit sequence.
    Remove reader-side ordering synthesis and snapshot fallbacks.
80. Validate the sole current task snapshot on both read and write. Reject
    incomplete terminal state, reports, identities, and event ordering instead
    of repairing or partially loading them.
81. Require session metadata to carry its exact key and current fields. Remove
    filename-derived identity and make direct reads and enumeration share one
    strict decoder.
82. Persist agent model selection only as the object contract containing the
    primary model and ordered fallbacks. Remove string/object dual serialization.
83. Delete the unreferenced model and tool-call protocol mirror from
    `pkg/tools/shared`; keep `pkg/providers/protocoltypes` as the sole owner.
84. Flatten the canonical internal and persisted tool call. Convert nested
    functions and serialized arguments once at external provider boundaries,
    and remove internal normalizers, precedence rules, and dual-shape tests.
85. Enumerate active runtime sessions at the composition boundary. Normal
    agents receive only owner-matched opaque scope-v2 identities, and coding
    runtimes receive only their exact admitted thread.
86. Remove the local copy of `SessionStore` used by ephemeral sub-turns and the
    repeated JSONL context checks. Compile directly against the canonical
    interface and validate each boundary once.

Exit criteria:

- the zero-legacy audit below passes;
- no production branch selects a historical implementation based on the peer's
  release; bounded feature subsets come from current capability negotiation;
  and
- current third-party integrations are explicitly named and do not depend on
  internal compatibility shims.

### P1 — Separate personal profile metadata from standard instructions

Depends on: X3.44

Scope:

- make the current agent configuration the sole owner of machine-interpreted
  identity, model, skill, tool, and MCP policy fields;
- make standard Markdown `AGENTS.md` the prose instruction document rather
  than embedding a MintClaw-only configuration schema in it;
- preserve `SOUL.md` and `USER.md` as explicitly MintClaw-specific personal
  context layers;
- keep personal-profile roots and coding-project instruction roots explicit in
  their respective runtime profiles so identical filenames do not imply shared
  authority; and
- advance the sole accepted config schema to version 4 so a version 3 profile
  cannot start after silently losing Markdown-owned identity or deny policy;
  the coordinated profile conversion writes all current fields before the new
  binary is started; and
- cut every configured agent and distinct personal workspace in the five
  deployed profiles over once, delete the singular `AGENT.md` parser and
  frontmatter ownership, and avoid a steady-state dual reader.

Tests:

- agent identity and policy resolve from one current configuration owner;
- personal prose and hierarchical coding-project instructions load only in
  their admitted runtime scopes;
- an unknown machine field in Markdown cannot affect runtime authority; and
- the OpenClaw importer preserves prose at its explicit import boundary without
  adding another runtime format.

Exit criteria:

- `AGENTS.md` has standard prose semantics;
- one typed config contract owns every machine-interpreted profile field;
- version 3 configuration is rejected rather than interpreted under version 4
  authority rules;
- no runtime reads personal `AGENT.md` or parses Markdown frontmatter as
  authority; and
- deployed personal workspaces contain the current prose filename before a
  binary without the old reader is installed.

Implemented shape:

- PR #881 makes config version 4 the sole owner of personal identity, model,
  skills, tool policy, and MCP-server policy;
- root `AGENTS.md`, `SOUL.md`, and `USER.md` are loaded as prose without
  machine-interpreted frontmatter;
- the runtime no longer reads root `AGENT.md` or `IDENTITY.md`, and onboarding
  preserves existing workspace prose while creating missing templates; and
- the live preflight now satisfies the data-conversion criterion for all five
  active configs, 21 agent entries, and 20 distinct personal workspaces. R1
  later removed the inert entries, captured the full stopped-state backup, and
  deployed the strict reader-free release.

### R1 — Execute the coordinated first-party compatibility reset (complete)

Depends on: P1 and X3.46-X3.66

Deployment required explicit user authorization. That authorization was given,
and the reset completed on 2026-08-28.

Scope:

1. satisfy the capacity gate, then back up the current configuration, the
   effective running binary for every process, all personal Markdown, and all
   MintClaw durable state, explicitly including sessions, tasks, interactions,
   outbox, cron, node registry, browser state, media indexes and assets, and
   invocation state; do not assume that the file currently present at a
   configured executable path matches a process running a deleted inode;
2. treat the PR #865 browser bridge cycle as satisfied for the sole connected
   browser-capable companion, verify that it remains on the current
   streamed-snapshot contract, and require every enabled gateway and companion
   browser profile to declare `network_mode` before installing a strict binary;
3. stage the merged PR #899 node-identity bridge release, which makes Ed25519
   companions send explicit `key_algorithm` while the gateway still accepts
   the omitted form, then upgrade or deliberately retire every older connected
   companion;
4. convert every retained node record to explicit Ed25519 or deliberately
   remove it, and verify every connected companion sends `key_algorithm`;
5. perform the P1 cutover for all 21 configured agent entries and 20 distinct
   personal workspaces, resolve the one pre-existing `AGENTS.md` collision
   explicitly, reconcile unique prose from ignored root personal-profile files
   into the current contract before removing them, and validate every active
   profile before restart;
6. delete previous and older browser schema generators, empty-algorithm
   normalization, optional execution-profile admission, runtime-less companion
   construction, expired wire aliases, and production fixtures that keep those
   paths callable in one removal release. Retain test-only exact old-contract
   fixtures that prove the strict runtime rejects them; and
7. deploy and verify the removal release, then exercise rollback using the
   matching binary and same-time state backup.

Completion evidence:

1. the stopped-state backup records the complete active state, effective
   binaries, host configuration, rollback material, and checksum manifests;
2. every enabled browser profile declares `network_mode`, the only connected
   browser companion advertises the current catalogue, and companion plus
   gateway-local browser canaries passed;
3. `p5a-canary`, `ab-local-test`, and upgraded `vpn` are the only retained
   nodes. P3 and the obsolete pending and revoked records were deliberately
   retired;
4. all retained persisted and wire identities explicitly name Ed25519, their
   executor, and their policy revision;
5. every active profile uses version 4, all 20 personal roots use standard
   `AGENTS.md`, no old personal-profile file remains, and the seven inert deny
   entries were removed;
6. PR #901 merged, strict revision `827e0f70` was installed, and the running
   artifacts plus active services were verified; and
7. the exact previous release was exercised against safely quarantined current
   state, the strict release and current state were restored, and the final
   smoke trace completed without truncation.

Satisfied exit criteria:

- every registered first-party peer uses the current protocol major and current
  authority-bearing capabilities;
- every enabled gateway and companion browser profile declares `network_mode`;
- no production code reconstructs an older browser catalogue;
- every persisted and wire node identity names its key algorithm;
- no pre-reset personal-profile reader remains; and
- deployed verification and rollback evidence are recorded in the final audit.

### Z1 — Final zero-legacy audit

Depends on: all preceding packets, including R1

Audit every production match for:

```text
legacy
deprecated
backward compatibility
compatibility fallback
migration
old schema
old version
```

Also search semantically for historical-schema generators, omitted-field
normalization, version-selected implementations, dual readers or writers,
deprecated callable entrypoints, and names such as `previous*Schema`. Search
for `alias` and `fallback` as discovery aids, but do not treat a match as debt
until its semantics are classified.

Each surviving match must be one of:

- a current external protocol or import product;
- a current product concept such as configured aliases, provider failover, or
  a documented default inside the sole current contract;
- additive first-party wire compatibility inside the current rolling window,
  with no historical implementation and with any temporary adapter registered
  with an owner and removal gate;
- current operating-system or architecture portability;
- a development benchmark whose historical baseline is the behavior being
  measured rather than a product input contract;
- historical documentation under an archive; or
- a strict rejection message for an unsupported version.

No surviving match may:

- parse historical persisted state or select a historical runtime
  implementation; bounded wire peers may omit current optional fields;
- translate old state during normal startup;
- write both old and new representations;
- silently infer current meaning from an obsolete field;
- keep a deprecated API callable; or
- exist only to keep an old fixture passing.

The final report records deleted production lines, removed types and entrypoints,
the remaining current external integrations, full test and lint results, and
deployed cutover evidence.

Source result: **passed** at `e60b8e26` on 2026-08-29, after PRs #975 and #976
corrected the premature proof recorded at `9ad623a1`.

- PRs #965-#976 closed the final source findings: inferred coding roots,
  synthesized interaction ordering, task repair on load, session identity
  reconstruction, model-config dual serialization, a second protocol type
  family, dual-shape tool calls, historical runtime-session enumeration, a
  duplicated session-store interface, ambiguous or incomplete root metadata,
  and independent persisted-message and scope readers;
- compared with post-#963 source `7944edc7`, the final packet sequence changed
  53 production Go files with 887 additions and 847 deletions, a net addition
  of 40 lines. The final 103-line increase after the premature #973 proof is
  the exact metadata boundary and shared decoder surface from #975 and #976,
  not a compatibility reader. The sequence removed the eight unused
  `pkg/tools/shared` protocol types, the nested internal tool-call types and
  normalizers, both custom `AgentModelConfig` JSON methods, the local 18-method
  ephemeral session-store interface, and the historical reader/inference
  branches named above;
- the additional test surface is deliberate rejection and current-writer
  coverage, not old-format fixtures that keep a reader callable. Every code PR
  in #965-#976 passed the repository's nine CI jobs, focused package tests,
  required race or integration tests, formatting, lint, exact-head review, and
  merge-head protection; and
- no production background recovery, Seahorse reconciliation, or coding repair
  path bypasses the current runtime-session selector. The generic storage
  enumeration remains only where the backend legitimately stores both normal
  and coding histories.

Surviving discovery matches are classified as follows:

| Match family | Classification and reason retained |
| --- | --- |
| Kagi legacy array response | Current external upstream response shape at the Kagi adapter |
| OpenAI-compatible, OneBot, browser, remote-workspace, OS, and architecture compatibility | Current external or platform contract, not a MintClaw historical implementation |
| OpenClaw migration command | Explicit import product boundary; it does not run during normal startup |
| `legacy` memory benchmark | Development comparison baseline whose historical behavior is the measurement target |
| `legacy` context-manager value | Strict rejection of a removed configuration input |
| Bedrock deprecated temperature and Cobra deprecated help rendering | Upstream provider behavior and generic CLI presentation, not a callable MintClaw compatibility API |
| Model, node, service, release, CLI, and workspace aliases | Current user- or operator-visible product identity |
| Provider failover, retry delay, SPA routing, non-seekable streams, and delivery fallbacks | Current resilience or platform behavior with one owner; ambiguous external effects remain terminal |
| `ensureMessageCreatedAt` and canonical timestamp helpers | Current write and mutation invariants, not inference while loading historical state |
| Provider identifier compatibility sanitation | Current provider-boundary name constraints, not a persisted-state reader |
| Old browser/node fixtures in tests | Exact rejection evidence for the strict current runtime; no production old-path implementation remains |

No surviving match reads historical persisted state, writes two
representations, selects an implementation by old version, silently maps a
removed field into current authority, or keeps a deprecated MintClaw entrypoint
callable. Product aliases, external protocols, and genuine failover therefore
remain without weakening the simplification rule.

Deployed closeout result: **passed** on 2026-08-30 under running release
`edd8759b`. The stopped-state authority retained 819 metadata documents and
640 histories, archived 2,828 metadata documents and 636 histories, removed
329 root `aliases` members, flattened 3,721 tool calls plus two Google-specific
cases, and validated 10,232 messages. All 20 configured session roots passed
source-stability, disjointness, exact-decoder, filename, and checksum checks.

The retained candidate was installed by same-filesystem rename. All profiles
loaded, the old retained session resumed, a new message completed, the VPN PTY
smoke closed cleanly, all three expected nodes remained connected, and an
actual browser child trace completed open-observe-close without truncation.
The exact pre-cutover binary, configs, and untouched session snapshot were
then restored together and passed a baseline smoke. The candidate was reapplied
by inode-verified rename and the canaries passed again.

After the observation window, a final copy-only verification found 828 current
metadata documents, 649 histories, 10,400 valid messages, and zero archived
documents, aliases, nested calls, or Google cases to transform. All expected
services were active, no unit had failed, no legacy product process existed,
and the ten-minute error journal was empty. The verified targeted archive and
manifests remain in the compact recovery set; 15 rollback-observed trees and
the operational staging and preflight copies were pruned. Full commands,
digests, canaries, rollback evidence, and retained artifacts are recorded in
the [Z1 session cutover evidence](../operations/architecture-simplification-z1-session-cutover.md).

The compatibility-reset objective is therefore complete without adding a
steady-state old-state reader. O3 is also complete. The roadmap remains active
only for the final requirement-by-requirement audit.

## Validation For Every Code Packet

Each code PR must run:

1. focused tests for the changed package and contract;
2. race tests when lifecycle, registry, delivery, or concurrency changes;
3. integration tests for every crossed process or persistence boundary;
4. `make fmt` for changed Go files;
5. `scripts/pre-push-lint.sh --changed` or the broader required lint target;
6. the affected build and CLI smoke tests; and
7. a final diff audit that separates production deletion from new scaffolding.

A simplification PR is not successful when it only moves compatibility logic
behind a new interface. It should normally delete more production branches,
fields, conversions, or state transitions than it adds.

## Stop Conditions

Pause a packet and redesign it when:

- one current fact still needs two durable owners;
- removal requires a new general-purpose framework larger than the code being
  deleted;
- review fixes spread into two unrelated subsystems;
- security or delivery correctness is weakened to reduce line count;
- deployed current state has not been inspected before deleting its reader;
  or
- the first real web or IPC coding client appears before C1 merges.

The last condition does not automatically preserve the current coding
protocol. It triggers a fresh admission based on the real client's transport
and lifecycle requirements.

## Completion Definition

The roadmap is complete when:

- every first-party component implements the canonical internal contract and
  bounded wire peers interoperate through additive fields and capabilities;
- browser actions and capabilities have one definition and no historical
  schema machinery;
- tasks have one result model, one registry, one child runner, and one status
  tool;
- interactions reference the one delivery owner and the one continuation
  owner;
- `AgentLoop` composes explicit coordinators and has explicit turn entrypoints;
- the coding TUI consumes one in-process presentation store;
- configuration and sessions accept only their current formats;
- typed configuration owns personal profile metadata while standard
  `AGENTS.md` owns prose instructions;
- the compatibility-debt register is empty after the coordinated reset;
- the zero-legacy audit passes; and
- the coordinated deployment and rollback procedure has been exercised with
  the current server and clients.
