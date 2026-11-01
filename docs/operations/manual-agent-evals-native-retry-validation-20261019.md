# Native Retry And Provider Evidence Validation

## Scope And Merge

[PR #1820](https://github.com/bogdanovich/mintclaw/pull/1820) fixes two consumer assumptions exposed by the
[retained v1 experiment](manual-agent-evals-native-models-v1-20261019.md): native provider names may be absent,
and a completed turn may contain an unanswered attempt followed by its bound retry and successful replacement.
The [operator contract](manual-agent-evals-native-models.md) describes the resulting evidence and unknowns.

- Final head: `a6dbe19b47c5cf910c46f614e2f865a5624f3d12`.
- Confirmed merge: `6397ffc204e29313a43f4ef735da40faf8267a76`, 2026-10-19 06:13:19 UTC.
- Exact-head CI `39205860259`: ten successful jobs, two policy skips (Frontend and PDFium WASM).
- Actual reviewer comment `6260726061`, writeback `f911107b0116c0f9958b`, 05:58:33 UTC: no must-fix findings.
- Owner PR-description rocket `578671999`, 05:58:34 UTC. All green checks, ready state, clean mergeability and
  no new/actionable feedback were confirmed at 06:13:06 UTC. An earlier `UNKNOWN` mergeability state was waited out.

No review/fix cycle or production scope expansion was needed. Production stayed in three `internal/eval` files
(+63/-12), tests in three files (+182/-6), and one operator document (+11/-2). No AgentLoop, provider, native trace
schema, approval, delivery or privacy authority changed. Capture remains default-off and owned-profile-only.

## Semantics

An absent provider stays unknown, not a provider inferred from configuration or model spelling. Known model names
remain comparable independently. Provider comparisons require complete evidence on both sides; coverage changes
alone are not observed provider changes. Existing request/response pairing and response-model unknowns remain.

A native `model.retry` must bind to a unique still-unanswered request in the same admitted scope, with valid retry
payload/ordering. `retry_observed: true` preserves that receipt while usage remains `not_returned`. Orphan, duplicate,
post-response, wrong-scope and post-terminal retries cannot explain a missing response. A fallback count alone is
not such a receipt. Frozen grade/compare also validate retry receipt counts and reject contradictory returned usage.
Only admitted response usage is summed; no failed-attempt estimate, provider cost or full-call census is invented.

## Validation

| Observation | Result |
| --- | --- |
| Initial macOS projection/run/grade/compare race controls | Pass, 13.320 seconds |
| Expanded macOS native-model race controls | Pass, 14.508 seconds |
| Broader macOS grade/compare/native / CLI controls | Pass, 52.727 / 4.423 seconds |
| Isolated Linux grade/compare/native / CLI race controls | Pass, 110.236 / 4.287 seconds |
| Actual retained v1 source with old projector overlaid | Expected failure, 1.592 seconds |
| Actual retained v1 source with corrected head projector | Pass, 2.301 seconds |
| Actual retained v1 source with exact merged-source projector | Pass, 2.657 seconds; finished 06:16:31 UTC |
| Root formatting, affected lint, docs lint, diff checks | Pass; zero final lint findings |
| Post-commit changed-package formatting/lint | Pass, 5 / 3 seconds; zero findings |

Two local staticcheck suggestions in new tests were corrected before publication. No lint or test gate was
weakened. Linux used Go 1.26.9, `CGO_ENABLED=1`, `-race -tags goolm,stdjson`, `umask 077` and a cleared environment
with private test home/temporary roots. Tests use stubs/loopback or retained metadata, never a live CI model.

The retained-source control is a private, outside-Git Go test supplied through the native Go build overlay. It reads
the original canonical trial/route bindings and bounded native metadata files, validates source/record integrity,
projects with the tested consumer and checks the frozen result. It does not construct providers, load credentials,
start a gateway, send a model request, replay a task or write the original profile. Old-projector and corrected-
projector controls consume the same source, not differently coached model attempts.

The exact merge source accepted both actual scopes: four requests, two returned responses and two retry receipts.
Available response usage was 4,707 input / 28 output (`4.7k` / `28`), with `partial_native_responses` coverage. Provider
names, failed-attempt usage and all-call coverage stayed unknown. Native model names and effective request hashes
were retained from source records, not reconstructed from the workspace or transcript.

This is passive consumer/source-contract qualification, **not a new live run or canonical report backfill**.
The original v1 `run.json` stayed byte-identical and `partial`; its task/judge/regrade passes and native unknowns
are unchanged. It was not promoted into a new all-call/native-observer live pass. No paid task was retried.

## Evidence And Operations

Private validation root: `/home/server/mintclaw-eval-native-retry-validation-20261019.UduzKMwd`. It retains the
outside-Git probe/overlays, `retained-before.log`, `retained-after.log`, `focused.log`, `retained-merged.log`, and source
archives. The original captured profile remains in the separate v1 packet; none of its five removed credential
copies was restored or read by these controls.

- Reviewed-head source archive SHA-256: `e2ab2931b898eb4bccae4da1427ed00dc066425aad27e927fd05512d67921fed`.
- Exact merge-source archive SHA-256: `6ef5f164988f72dba140bb946d591cd8b21efc67f29f208d2e20504ce2076736`.

These identify operator archive-pinned source checks, not an independent native runtime VCS attestation. Shared Go
compiler caches were used; no production key, configuration, service, binary or runtime state was modified by this
packet. Production core advanced externally from `973d36b0a` to `b00976e76`. A health snapshot during that external
change saw `spouse` inactive with no failed units/errors; a subsequent read-only check showed it active/running with
`Result=success`, without intervention here. Production main config/security byte hashes were still unchanged at
06:18 UTC. The final 06:19 UTC snapshot found all ten services active, zero failed units/errors and about 6.6 GiB
disk free; future builds must recheck their own disk admission.

The full E0-E5 goal remains active. Actual per-trial isolation/effective loaded-input evidence, remaining admitted
PDF/browser/adaptive cases, domain repeats and the final exit are still required. No production rollback/deploy or
new model-task run is necessary for this source-contract fix.
