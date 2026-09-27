# Local Coding Agent P7.7 Exit Record

Roadmap packet: [P7.7 — Codex-style yolo execution profiles](local-coding-agent-roadmap.md#p77--codex-style-yolo-execution-profiles).

Status: completed and production-qualified on 2026-09-27.

P7.7 adds three explicit operator-owned execution profiles to the P7.4 durable
task, thread, and delivery foundation:

- `project-yolo` owns an isolated Git worktree and may commit, publish, open a
  pull request, or deploy when the objective requests it;
- `machine-yolo` starts in a configured plain directory and has the ambient
  authority of the companion account, without pretending that arbitrary host
  changes can be rolled back; and
- `machine-yolo-root` is a separate, disabled-by-default Linux profile backed
  by an exact node-local authority-broker binding. It is unavailable on macOS.

Paths, credentials, executables, privilege configuration, and scope policy
remain node-local. The gateway sees only aliases, immutable scope revisions,
profiles, targets, and exact requester grants.

## Merged implementation

| Packet | Evidence | Result |
| --- | --- | --- |
| Admission | [#1294](https://github.com/bogdanovich/mintclaw/pull/1294), merge `0bf3b2be` | Froze the scope kinds, authority matrix, incompatible v3-to-v5 migration, platform boundary, validation gates, canaries, and stop conditions. |
| Project yolo | [#1312](https://github.com/bogdanovich/mintclaw/pull/1312), merge `4f6ea11e` | Added isolated-worktree publication authority, external-effect receipts, terminal uncertainty, and local-remote coverage. |
| Machine yolo | [#1318](https://github.com/bogdanovich/mintclaw/pull/1318), merge `5df16a90` | Added persistent plain-directory coding identity with companion-user machine authority and an explicit no-generic-rollback contract. |
| Root profile | [#1337](https://github.com/bogdanovich/mintclaw/pull/1337), merge `f41b47d5` | Added the opt-in Linux authority broker, authenticated root identity checks, fail-closed unsupported-platform behavior, and no-replay settlement. |
| Publication receipt | [#1348](https://github.com/bogdanovich/mintclaw/pull/1348), merge `95f756df` | Required the requested remote ref to be verified from standalone command stdout before a push is reported as successful. |
| Slow preparation | [#1361](https://github.com/bogdanovich/mintclaw/pull/1361), merge `9f25e56b` | Separated the bounded dispatch/preparation window from the long-running task lifetime. |
| Queued reconciliation | [#1368](https://github.com/bogdanovich/mintclaw/pull/1368), merge `4c36f904` | Made durable queued retry and recovery observable without duplicating notifications. |
| Late runtime start | [#1373](https://github.com/bogdanovich/mintclaw/pull/1373), merge `5ae1d02a` | Reconciled restored queued tasks after gateway node tools become available and removed the pre-start background-context monitor leak. |

All implementation PRs passed their required formatting, lint, security,
focused, race or portability tests, GitHub CI, exact-head review, and owner
approval. Existing investigate/mutate grants were migrated directly to the v5
catalogue; no legacy v3 compatibility reader is active.

The final exact-main audit repeated the focused cross-layer matrix with
`goolm,stdjson` build tags:

- `pkg/config`: exact requester/profile grants, invalid scope rejection, and
  legacy configuration rejection;
- `pkg/nodes/companion`: Darwin root denial, privileged-parent rejection,
  direct-machine identity, project and machine no-replay recovery, and
  authenticated ephemeral task transport;
- `pkg/agent`: machine rollback truth, command-free privilege reporting,
  terminal delivery deduplication, restored queued dispatch, compaction
  continuity, and root-only privilege binding; and
- `pkg/tools`: durable preparation, dispatched-invocation recovery, privileged
  command/output redaction, and uncertain-outcome no-replay behavior.

All four focused commands passed on latest main.

## Production authority

The production gateway grants and the live `ab-2` companion catalogue agree:

| Gateway alias | Target | Node scope | Revision | Profiles |
| --- | --- | --- | --- | --- |
| `mintclaw-dev` | `ab-2` | `mintclaw` | `450dbc55c4ff447347f152938dbe833cb56728033c19fe3191bcb8a23cdd17c0` | `investigate`, `mutate`, `project-yolo` |
| `ab-2-machine` | `ab-2` | `ab-2-machine` | `86c85674185aaf75328ae9e05ef8a8091b7d06d3c93aaadcb787f1cb67b199f3` | `machine-yolo` |

Both aliases have one exact `main`/Telegram/sender requester grant. The
project source checkout is `/Users/ab/devel/mintclaw-remote-source/mintclaw`,
owned worktrees are allocated below
`/Users/ab/devel/mintclaw-remote-worktrees-v5`, and machine tasks start in
`/Users/ab/automation`.

The macOS companion publishes no root alias, no `machine-yolo-root` profile,
and no privileged executor. The gateway likewise has zero root-profile
grants. This is the intended production state: the Linux success path remains
covered by #1337's authenticated real-process authority-broker tests, while
Darwin rejects root admission because detached process-tree containment is not
proven.

## Production Telegram canaries

### Project-yolo publication and recovery

Telegram created durable task
`coding-05644b526b1424c2c8c839328a79496255de010926ebf6b0b8b0ad0b0b311433`.
It remained queued while target authority was unavailable, then the deployed
late-runtime-start reconciliation dispatched it after reconnect without a
second task or worker:

- task generation `fd662d05-e496-4714-aa9c-6812663504dd`;
- native thread `8fa110c7-a4cf-4747-ae1a-11518d3247ee` and worker
  `worker-a063091f-550b-4a72-b186-adef5ea8eb73`;
- owned worktree `wt-8fa110c7a4cf4747ae1a11518d3247ee` on branch
  `p7-7-production-canary-v3`;
- exactly one changed path,
  `docs/operations/p7-7-project-yolo-canary-v3.md`;
- successful `git diff --check`, exact content check, clean worktree, and
  commit `1061253d150015ab4367c53e0c6be9930101c987` with the requested message;
- push only to `p7-canary`, independently verified by standalone
  `git ls-remote --heads p7-canary refs/heads/p7-7-production-canary-v3`,
  which returned the exact commit and ref; and
- final `succeeded`/`delivered` projection with node result digest
  `d44016a36ba070164c1360289bfd1488cedb6f5b80d3ff6eab2fbc0797df8f4e`
  and handoff
  `c1e1d8612bdc98f0789194b71c2aac3a7ebe1326c8fe86259df2a81e9d86d5b1`.

The source checkout stayed clean at `9f25e56b58d3900adec469871d9ee8187fa02b8d`.
The retained worktree is clean at the canary commit. The node ledger contains
one `coding.task.start.v5` invocation for the task; the gateway ledger contains
one delivery decision and one delivered transition. No push reached `origin`,
and no pull request or deployment was created.

### Machine-yolo direct work

Telegram created durable task
`coding-5e33fdc5e21fd329f1bbf4cc44562224898ee31e800edd90dfb882be6850c916`.
It completed and delivered on native thread
`ff45ca08-2b84-4be4-a74d-672de448dc4d`, worker
`worker-501e65d6-b699-41bf-9198-3f9cea6ace0f`, and scope revision
`86c85674185aaf75328ae9e05ef8a8091b7d06d3c93aaadcb787f1cb67b199f3`.

Independent host inspection confirmed:

- `/Users/ab/automation/p7-7-production-machine-canary-v1` contains exactly
  one entry, `result.md`, and no `.git` directory;
- `result.md` has the requested exact text, SHA-256
  `a47b119ae716c485505ca5561cb06de05f4d2641a5ecdda13f0bade0937aa4ca`,
  and owner UID 501;
- the single short-lived user process reported PID 90720 and exit status 0;
  the PID no longer exists; and
- no sudo/root, network, Git commit, push, pull request, or deployment effect
  was reported or requested.

The node ledger contains one `coding.task.start.v5` invocation for this task.
The gateway ledger contains one delivery decision and one transition to
`delivered`; it records no replayed start.

### Privilege boundary

Static live-state inspection is complete: the gateway grant has zero
`machine-yolo-root` profiles, the node catalogue has zero root profiles, and
the node configuration has zero privileged executors. The Telegram admission
denial canary returned the exact gateway error
`remote coding scope or requester grant is unavailable`.

Post-canary inspection found zero gateway root tasks, zero native root coding
threads, zero node `coding.task.start.v5` invocations for the root profile, and
no active coding task. The request therefore failed before durable task
creation, thread allocation, node dispatch, or any host side effect, as
required for an unsupported macOS root target.

## Deployment health

The server source checkout, installed server binaries, running main gateway,
local companion source, installed companion, and running companion all resolve
to latest main `84ad205559cb758298cdbe7ba2fd8d887dd1e5e0`. The companion reconnected as
`ab-local-test` with software `v0.1.0-p8a.2-2388-g84ad20555`; its live and
approved catalogue hashes both equal
`bfd76505a80b80118324501ee68abd64b5a848b377469bd3254bea9d715b27eb`,
and the P7.7 scope revisions did not change.

The final status check found all ten expected MintClaw user services active,
zero product or global failed units, zero legacy processes, expected launcher
HTTP 302 and reviewer HTTP 404 responses, and zero error-level journal entries
in the ten-minute window. `doctor.v1` loaded all five active profile configs
with zero command or schema errors. Exit code 2 remains expected for the
existing plaintext-credential, broad local automation, permissive-channel,
external-skill, and recent-task findings; no P7.7 load error appeared.

A live gateway smoke returned exactly `MINTCLAW_P7_7_FINAL_OK` in session
`live-smoke-d696454c-32e5-44e9-b21d-d6ead29f52f4`, turn `main-turn-19`.
Fresh trace `trace-turn-ba7a7526ffbdd7445a1be190` completed with schema
`mintclaw.diagnostic_trace.v1`, eight records, redacted content, the configured
redactor, and no truncation.

## Backup and rollback

The atomic configuration migration was protected by server set
`/home/server/mintclaw-deploy-backup-20260922T075128Z` and macOS companion set
`/Users/ab/mintclaw-p7-7-deploy-backup-20260922T080946Z`. Their manifests were
reverified during closeout. The server set retains the prior binaries,
systemd state, main configuration/security references, and workspace state;
the companion set retains the prior binaries, exact config and LaunchAgent,
node state, coding threads/worktrees, and remote worktrees.

The final checksum-verifiable server snapshot is
`/home/server/mintclaw-p7-7-final-backup-20260927T215119Z`. Its 42-entry
manifest covers the exact binaries, user systemd units and drop-ins, five
active configs and gateway launchers, source/runtime identity, service state,
and a pointer to the retained pre-rollout set
`/home/server/mintclaw-deploy-backup-20260927T072133Z`.

The matching macOS snapshot is
`/Users/ab/mintclaw-p7-7-final-backup-20260927T214827Z`. Its verified manifest
covers the previous companion binary, exact companion config, local-test node
and tunnel LaunchAgents, source revision, and launchd state. Active
configuration did not change during the final binary-only alignment.

Rollback is disable-first: remove both gateway `remote_coding_scopes` grants
and restart `mintclaw-main` before stopping the companion. Restore matched
gateway and companion binaries and service definitions, then restart only the
owned services and repeat the health checks. Do not delete retained tasks,
threads, ledgers, worktrees, handoffs, or external-effect receipts to
manufacture a clean state. Machine and root effects have no generic filesystem
rollback.

## Residual limits

- `project-yolo` deliberately permits requested publication, pull request,
  release, and deployment effects through node-local credentials; objective
  review and the exact grant remain the policy boundary.
- `machine-yolo` has the companion user's ambient authority and no automatic
  rollback outside its initial directory.
- `machine-yolo-root` remains Linux-only until a separately qualified,
  root-owned macOS helper proves equivalent process-tree containment.
- Existing plaintext-secret and broad-automation `doctor` findings remain
  operational debt outside this packet.
- Codex and ACPX adapters remain available but are not part of the native
  Telegram-to-coding execution contract.

## Done-criteria audit

| Requirement | Evidence | Result |
| --- | --- | --- |
| Explicit scope/profile admission | Merged roadmap and closed validation matrix | Passed |
| Exact alias, requester, target, profile, and revision authority | Live gateway grant and matching node catalogue | Passed |
| Isolated project publication | Telegram project-yolo v3 worktree, commit, standalone remote-ref receipt, and clean source | Passed |
| Direct non-Git machine work | Telegram machine-yolo directory, exact file, UID/process receipt, and no orphan | Passed |
| Opt-in Linux root and default denial | #1337 real-process broker tests plus live macOS denial before dispatch | Passed |
| Durable lifecycle and no replay | One task/thread/worker per canary, one start per node task, one final delivery, and recovery regression matrix | Passed |
| Incompatible v5 migration | Active `coding_scopes`/`remote_coding_scopes`; legacy config rejected | Passed |
| Cross-platform validation | Merged Linux/macOS CI plus final exact-main focused audit | Passed |
| Review and merge gates | Every implementation PR merged after its required CI, review, and owner approval | Passed |
| Production rollout and rollback | Exact-main gateway/companion, three Telegram canaries, clean health/trace, and verified backup manifests | Passed |

P7.7 is complete. Later work may add a separately admitted macOS root helper,
but it must not weaken the current fail-closed boundary.
