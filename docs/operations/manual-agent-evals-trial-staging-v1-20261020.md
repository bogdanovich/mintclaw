# Per-Trial Evidence Staging V1

## Reviewed Source

[PR #1857](https://github.com/bogdanovich/mintclaw/pull/1857) adds private per-trial evidence staging, not another
native resource/approval engine. Reviewed head `db3cadb25f5e2d4df8b2935c88829a2e5501e721` merged as
`daf7b239de08f8384ae534184ab2f483b1926f24` at `2026-10-20T02:47:09Z`. Both trees were independently equal:
`4c8c4f5d7b50afcb3e1d38cefb0572ce105e9702`.

At the 02:46:50 UTC gate, exact-head CI run `39358390194` had ten successful jobs and two policy skips
(unaffected Frontend/PDFium WASM). Actual SHA-bound review comment `6282034903`, writeback
`6e198a673c1faee16029`, reported no actionable findings; two docs files were excluded. Owner PR-description rocket
`581404135`, clean mergeability and zero unresolved/new actionable feedback were checked before merge. Eyes alone
were not approval. There were no substantive review/fix cycles.

Root formatting, affected lint, changed-package pre-push checks, docs lint and diff checks passed. The exact-head
full Linux `internal/eval` race suite passed in **624.200s**, Go 1.26.9, CGO enabled, `goolm,stdjson`, without models.
CI macOS Portability independently ran the complete eval package successfully. Local expanded/full Mac binaries
held before Go startup at `_dyld_start`, 12 KB footprint/zero CPU; only those owned processes were stopped. They
are not passes. An earlier focused Mac set passed in 4.822s before the final test-setup simplification.

The implementation is confined to `internal/eval`: fresh private staging before each admitted trial probe/task,
canonical evidence before retirement, exact owned-file/directory checks, and frozen receipts consumed by offline
grade/compare. Tests cover repetitions, pre-task allocation failure, unavailable/retained cleanup, changed roots,
aliases/files/permissions/content, unknown extra entries, canonical-write failure and no target replay. Existing
PDF/browser finalization remains authoritative and does not acquire new cleanup proof from this receipt.

## Isolated Experiment

The current static Linux AMD64 binary was built from the reviewed head, then used only after merge/equal-tree
confirmation. Go 1.26.9, CGO disabled, `goolm,stdjson`, version `eval-trial-staging-v1`, build time
`2026-10-20T02:21:30Z`. Revision metadata was explicitly injected with `buildvcs=false`, not independently attested.

| Input | SHA-256 |
| --- | --- |
| Source archive | `1355ea5f3156d1e18b80f600c8a3644f7ada68189f1df4977154610cfee1472a` |
| Executable | `171512d70ce67afc06331ff5b51b8f745173b63d4ec789ec27abb2b62ebd3819` |
| General canonical run | `8ab948ab6fa56a04b04d9f0f865b5c9427bb4429c0bd1c45da0e894d73f65837` |
| General canonical regrade | `c31a2762499e6da5348465594e2433b9137fb400332dcbb613bf659c57236f88` |
| General comparison | `90079bc93b31af2fdfc07017b8685cf12fd0adb0dd25cec0f48b58c999ed9609` |
| PDF canonical run | `40d46fd598b62e2041221057e97e8594b77e14477d788c92f33f371b172af3d7` |

Each case used native `eval prepare`, a fresh private home/channel, one allowlisted synthetic `AGENTS.md`,
default-deny tool/approval policy and opt-in metadata capture. Only authorized deployed-main Gemini model settings
and its scoped API credential were reused through private native file references. No production session, memory,
browser/OAuth state or workspace was cloned. Target requests used `gemini-3.5-flash-lite`; separate tools-disabled
judging used `gemini-3.6-flash`. These are request identities, not complete provider/billing attestation.

The general profile allowed ordinary questions only. PDF preparation additionally admitted native document/BM25
discovery and prepared an empty write journal. Neither case granted an output write or broad approval. Runs were
serial; the two general repetitions were declared independent trials, not retries. No failed target was rerun.

| Experiment | Result | Run / Duration After Admission |
| --- | --- | --- |
| `general.context.followup`, repeat 2 | 2 pass; all hard checks and separate judges pass | `a238a0e0-564e-45b9-a0fb-a6fea1732f33`, 10618 ms |
| General judge-only regrade | 2 pass; original trial IDs and receipts retained | `31443f3d-2bd0-41c1-9a2a-81bb7ebe3c03`, 2122 ms |
| General offline compare | Compatible; 2 pass versus 2 pass; grading mode changed | No target/tool work; attribution not established |
| `pdf.cancel.no-write`, repeat 1 | Inconclusive, `missing_route_identity`; no judge/regrade | `0e4ea7dc-1e3f-4b71-857d-5e43e706c48f`, 11159 ms |

General CLI window: 02:47:51-02:48:03 UTC; trial durations 5035/5382 ms. Trial IDs are
`ec47f537-a4a8-44c4-8222-1ea895f8aa83` and `51baf86c-2460-475e-9a78-076d783d86db`; sessions are
`6a71f8a3-e151-4fd0-b78e-7b44afb99a90` and `5b5030e5-6c76-48ad-a2cd-c48a83ea875c`.
General case hash: `4703ef12118060ecbb7c4edfb2ee19cf4950f91e13b52ac29e01851c8797b69d`.

PDF CLI window: 02:48:17-02:48:29 UTC; trial `c8f7c9a5-f394-45d9-8821-e1c56aec06d6`, 10970 ms,
session `8093e302-009f-4395-9c54-9695df308f31`.
PDF case hash: `231ade22ace6d089ed2f823209dc18e8e0f88ab0453e2ce26f364f3ced38bbdc`.

All three `private_trial_staging_v1` receipts record `removed`, unique session identity, creation time in Unix
milliseconds and checkpoint SHA-256. Independent inspection found zero staging directories, retained canonical
trial files with mode `0600`, and canonical run content equal to the original CLI result excluding `report_dir`.
General regrade retained exactly equal scratch/native-model observations; it did not recreate a target or staging.

## Preserved Negative And Limits

The new PDF final reports the protected document continuation guard, has one trace scope but no agent/session
identity, and establishes neither a cancellation nor an eligible PDF owner. Both required hard checks remain
inconclusive; logical settlement is unknown and the source fixture remains retained. Two empty directories, zero
files, remain under owned `eval-tmp`. No positive PDF cleanup/worker/no-write qualification is inferred. The earlier
[passing ordinary cancellation path](manual-agent-evals-no-write-v1-20261018.md) remains unchanged. This new result
has different runtime/configuration observations; no causal regression is attributed to staging. Native document
continuation behavior and routed error-final metadata are separate target triage, not a reason to retry or relax
the harness. A removed local checkpoint never upgrades native settlement or the task verdict.

General native/final observations returned **9224 input / 55 output** tokens, displayed `9.2k` / `55`, from four
requests/responses/finals. Original judges returned **1170 / 163**; regrade judges **1170 / 144**, separately.
PDF canonical native/final usage is unknown: zero-valued counters are not zero cost. Provider-internal attempts,
complete all-call coverage, effective provider identities and comprehensive loaded-skill provenance remain unknown.
Comparison preserves those unknowns and does not claim statistical quality drift from rejudging the same trials.

Scoped raw-profile observation passed: general 58 files / 360866 bytes, PDF 50 / 527341; zero opaque files or known
leaks. Native SQLite witnesses were observed within their documented bounds. Independent literal credential guards
observed 78 files / 410028 bytes and 70 / 547681 bytes respectively, with five authorized copies each and no known
literal leaks. All ten owned copies were removed and independently absent. These are bounded observations, not
universal encoding/provider/host privacy or secure erasure.

Both exact admitted child PIDs, 863857/864890, exited natively with code 0, no force/truncation, and were independently
absent. General owned temp was empty; the PDF retained state above was not recursively deleted. Production
config/security hashes stayed unchanged; all ten services remained active with zero failed units/ten-minute errors.
Production core stayed `cf8bf5dc5beff1a0ef92487077f981e3ca3072ee`; no deploy/restart/rollback occurred.

Private build/source root: `/home/server/mintclaw-eval-trial-staging-20261020.xPV8mkVj`. Experiment roots:
`/home/server/mintclaw-eval-trial-staging-live-20261020.56bcKnHy` (general) and
`/home/server/mintclaw-eval-trial-staging-live-20261020.s7Cxl0r6` (PDF). Keep raw evidence outside Git; do not restart
these used profiles. Reproduction uses fresh profiles and the [operator guide](manual-agent-evals.md), not restored
credential copies. The host has about 3.6 GiB free at 99% use; future work must recheck capacity, not delete another
agent's evidence. This qualifies local staging on actual passing and nonpassing trials, not a green 14-case suite.
