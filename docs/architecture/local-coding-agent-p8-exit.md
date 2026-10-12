# Local Coding Agent P8 Exit Record

Status: complete on 2026-10-12. The admitted coding surface is stable within
the declared platform and trust boundaries. P6.5 LSP remains unadmitted and
P7.6 workspace rewind remains deferred.

This closes [P8](local-coding-agent-p8-hardening.md), not every later MintClaw
feature. The frozen qualification/production source is
`b1b4c0384c32c95bf246223810e496aa401af7e1`. Subsequent main merges for credential
approval/private-browser transactions and Codex SSE heartbeat handling belong
to separate packets. They do not change the coding ownership architecture and
are not silently included in these release/deployment receipts.

## Completed gates

| Gate | Evidence |
| --- | --- |
| H0 | [Admission and deferral](local-coding-agent-p8-hardening.md): staged scope, stop conditions and P7.6 decision |
| H1 | [Diagnostic inventory](local-coding-agent-p8-diagnostic-inventory.md) and [support-bundle production proof](../operations/local-coding-agent-p8-support-bundle-production-evidence.md): exact passive joins, bounded owner snapshots and safe export |
| H2 | [Performance qualification](local-coding-agent-p8-performance.md): repeated measurements, deterministic counters and refreshed budgets below |
| H3 | [Platform qualification](local-coding-agent-p8-platform-qualification.md), PR #1604: native terminal/path/lock/process contracts and declared release tuples |
| H4 | [State lifecycle](local-coding-agent-p8-state-lifecycle.md), PR #1618 and [production proof](../operations/local-coding-agent-p8-state-lifecycle-production-evidence.md): owned-thread trash, bounded explicit purge, separate blob GC and no project deletion |
| H5 | PRs #1627/#1628, private workspaces #40 and [production proof](../operations/local-coding-agent-p8-documentation-production-evidence.md): onboarding, migration/rollback and aligned diagnostic skills |
| H6 | PRs #1632/#1636/#1638 and the exact-source integrated, release and production evidence below |

There remains one authority per domain: canonical coding JSONL, Seahorse
derivatives, task/interaction/delivery registries, gateway invocations,
companion ledgers and worktree ownership. Diagnostics are bounded passive
evidence; collection never resumes, repairs or replays work. No second task
owner, transcript, generic remote RPC or universal evaluator was added.

## Incident-to-regression closure

### Streamed authentication failure

The [original canary](local-coding-agent-p8-stream-failure.md) made two
loopback POSTs for one HTTP 401 and lacked safe failure classification/model
correlation. PR [#1636](https://github.com/bogdanovich/mintclaw/pull/1636), final
head `904cbf7cd287c11a008ffab7959d701e2b4fdb7a`, merged as the qualified
source. All 17 checks, clean exact-head review and owner rocket passed. The
cancellation finding was answered and resolved after its regression reproduced
the old extra Chat call; the full agent rerun passed in 351.178 seconds.

Ordinary regressions prevent redundant auth/cancel fallback, preserve eligible
pre-output fallback, prefer structured auth over timeout wording and retain
safe terminal facts with the known model attempt.

Fresh production thread `426866b0-e517-462b-940d-bc467330d0f1` retained trace
`trace-turn-c73749e708d7b30131076946`: one HTTP POST, one model request, no
retry, HTTP 401, `auth`, `authentication`, `provider_structured`, and the same
nonempty model-attempt ID on request/error. Its terminal status is `error`,
without truncation/drops. The disposable project retained only its unchanged
sentinel; no production credential or external endpoint was used.

All three metadata-only bundle files passed independent size/digest checks.
The synthetic secret, prompt, sentinel and absolute fixture path are absent.
Manifest SHA-256:
`62ffd3f150097af79e51b606e06b3b489abf65b268e542e5b0bc5126c3f4c594`.

### Local-to-companion task identity

The [producer finding](local-coding-agent-p8-local-task-correlation.md) was
resolved by PR [#1638](https://github.com/bogdanovich/mintclaw/pull/1638), head
`60aed303cf5b943966f05d7043dc5050942f9373`, merge
`ce99b69162778bf330a83c3455fe79c6c8749173`. All 17 checks, clean exact-head
review and owner rocket passed. Three production lines populate existing
validated task/generation metadata without changing async/delivery behavior.

The fresh read-only production canary retained:

- local owner `034c854a-3b97-40c7-a251-91e65609d4aa`;
- task `coding-dae2e9389abdb45f4ed77f8eddf8a0f0b84f59c8bc8c307e7109f1b6a6734d08`;
- generation `5e78dd0a-acb6-487d-bfb2-473415999aeb`;
- parent trace `trace-turn-1a9ecaf058b20645e997dd22`;
- successful status trace `trace-turn-58fa15bef614e4dd0f69b057`;
- remote thread `e6dffb17-1cf2-44dd-ac26-89107384366e`;
- worker `worker-61459857-e4e9-4a2e-a6c2-8dd8abc0375d`;
- aliases/profile `mintclaw-dev` / `ab-2` / `investigate`.

An exact lookup joins both traces and the succeeded/completed durable owner by
the same task/generation. Local-owner delivery is intentionally
`silent`/`not_applicable`, not a missing Telegram message. The remote README
heading and commit `a70bc520eb23ce71835a2c8ca0701ec35b7e1669` matched independent
source inspection; the source remained clean. One malformed model-generated
status call was rejected with `INVALID_ARGUMENTS`; the exact two-field status
call succeeded without creating another task.

Select the local store explicitly: a bare task selector searches profile
stores, not every private coding thread.

```console
mintclaw diagnostics correlate --thread <local-thread> --task <task-id> --json
```

The four-file/two-trace metadata-only bundle passed independent digest and
privacy checks. Manifest:
`c8a9d186c479b49c4ea62898217d27403a8eb0c08887be2e46835b3d54d75fff`.
Repository text, objectives and absolute fixture paths are absent.

An earlier attempt retained `BROKER_UNAVAILABLE` before task preparation.
Discovery exceeded its 3/5-second budgets; later read-only probes completed in
1.2–1.4 seconds. Failure trace `trace-turn-b7f0e044292442bc656d4272` remains
preserved. No task effect was replayed and no grant/timeout was broadened.
Arbitrary host load can still make discovery unavailable; that path is bounded
and fail-closed, not a reason to silently start a second runtime.

## Integrated and release evidence

Tests ran on the installed exact-source production binary with
`MINTCLAW_REQUIRE_CODING_WORKER_E2E=1`:

- five native worker lifecycle tests: 152.771 seconds, covering question/answer,
  steering, interrupt/resume, crash/no replay, disconnect, hard cancel and
  worktree ownership;
- complete channel → gateway → native companion fixture: 148.325 seconds,
  covering investigate, isolated mutation, question/delivery, project-yolo
  publication receipts, machine-yolo and owned-process cancellation;
- remote/task/yolo contracts in agent, tools and companion: passed.

Fixtures use temporary projects, loopback providers, a local Git remote and
fake publication/package/service adapters. No real PR, application deploy,
package installation, sudo or production-grant change was performed. Previous
live Telegram/P7 canaries remain in their exit records; the integrated fixture
is not presented as a fresh human Telegram interaction. Receipt manifest:
`adfc9d269529436869095e367f8f56579329ebc3e2ad5c1ef1763efa55e489b2`.

[Release Qualification run 38207008279](https://github.com/bogdanovich/mintclaw/actions/runs/38207008279)
passed on the exact source: 16 binaries, 10 archives, six Linux packages,
16 SBOMs and four distinct local AMD64/ARM64 core/launcher images. Every
downloaded payload checksum was recomputed; metadata confirms the full commit.
Artifact `11727393948`, 519,109,785 bytes, has digest
`sha256:464525749f67720897c9593a059aaeef80fc77dec40344f34888448a1c4195eb`.
GitHub evidence expires after three days; local receipts were retained.
This was an unsigned non-publishing snapshot, not a release tag, registry push,
signing or notarization qualification.

## Refreshed performance

Three-run synthetic benchmarks and ten separate CLI processes used H2's
macOS/Intel/Go 1.26.9 environment on the exact source. All admitted budgets
passed; enforceable token measurements exclude provider/network latency.

| Path | Observed range | Maximum allocation/RSS |
| --- | --- | --- |
| Post-build / warm CLI help | 3.66 s / 90–100 ms | 45.2 MiB RSS |
| Empty catalogue processes | 100 ms | 45.4 MiB RSS |
| Warm runtime construction | 40.4–41.2 ms | 3.25 MiB/op |
| Streaming turn / first token | 296–307 / 150–159 ms | 18.9 MiB/op |
| 2,000-thread first page | 150–157 ms | 12.6 MiB/op |
| Forty-message compaction | 1.43–1.56 ms | 110 KiB/op |
| Clean revision / reconciliation | 93–98 us / 14.9–16.6 ms | 6.5 / 437 KiB |
| TUI paint / update / resize | 3.38–3.63 / 0.305–0.307 / 2.83–2.85 ms | 3.08 / 0.212 / 2.15 MiB |
| Hydration / viewport / overlay / search | 2.49–2.53 / 0.125 / 7.06–7.41 / 3.01–3.06 ms | 0.83 / 0.034 / 2.77 / 3.25 MiB |

Measurement manifest:
`fcf5d09301796f31c11af8f25b065ce34509b7450b1c1db3b193495c3726442e`.
Deterministic catalogue, hydration, rendering, retention and allocation guards
remain the correctness gates; no flaky single-run wall-clock assertion was added.

## Production and rollback

Clean server source advanced from `10fa246de67f67fed8099d97587c32e5a9116121`
to the qualified source. All five configs loaded with unchanged existing
findings and no new load/error/fail IDs. No config/schema/data migration was
needed. Restart completed at 05:15:53 UTC. The post-ten-minute observation
found all ten services active, zero failed/legacy processes, zero error-level
journal entries and expected HTTP 302/404 responses.

Live-gateway trace `trace-turn-4238e807e77bb1fe964c05f5` has the qualified
source, completed outcome, eight records, no tools/truncation/drops and
`queued` → `sent` delivery. It returned the P8 marker; channel presentation
adds the configured token footer. CLI receipt:
`380d623eda73020b150d2f8f866f14e8d722f0ff591c1dce04d44eb14609723a`.

Rollback set `/home/server/mintclaw-p8-h6-backup-gPg5BQ` retains previous
binaries, units/drop-ins, configs/run scripts/auth snapshot and source/service
receipts. Its manifest was reverified after rollout:
`49bf69e23cb21e82dd61a6474f23f260e6f86f9a9f77f7f94f80c7d952187b90`.
Core/node/launcher SHA-256 values are respectively
`9a5d1b94402a1f3bbd71f588529bee3ef16a1cb69237ba9cc3bec398b9643397`,
`2566a1946c31133ef4da4e1da43926a931cf86c1cabf7b85ac8c57d236648f37`,
`63127e5e0794c3b6317785091a457c3062e4c22704bb23314d819c5a9ef718ab`.

Rollback stops only affected units and restores matched prior binaries/config,
then checks health and a fresh trace. Canonical histories, task ledgers,
worktrees and external effects are never blindly restored or deleted. H4/H5
retain their separate state/skill backup and isolation evidence.

## Residual boundaries

- Stable means the [declared matrix](local-coding-agent-p8-platform-qualification.md):
  Unix interactive coding and Windows AMD64 headless, not Windows interactive
  PTY/companions or every cross-built device's hardware behavior.
- Existing exact grants and pinned companion/worker binaries were preserved;
  this is not a fleet upgrade or new root authorization.
- Yolo is trusted ambient authority, not an OS sandbox. Machine effects have
  no general filesystem rollback; publication requires an admitted profile.
- Existing deployment policy fail counts remain 14/7/7/8/9 for main/family/
  nutrition/reviewer/spouse. Plaintext storage and broad automation posture
  are disclosed, not silently certified secure by P8.
- Historical missing fields, disabled capture, scan/retention limits,
  corruption and incomplete/dropped traces still constrain evidence. Remote
  full transcripts/ledgers remain on their authoritative host.
- New credential transactions, binary/image secret transformations, public
  signed releases and subsequent provider changes need separate qualification.
- LSP/DAP, workspace rewind and universal replay/evaluator are outside this
  release. Next work is normal usage/soak and narrow incident-led regressions.
