# Manual Agent Evals: Empty PDF V3 Pilot, 2026-10-19

## Result

One fresh `pdf.empty.required` attempt after merged #1840 returned **inconclusive**, not pass. All six observed
Gemini requests succeeded, including requests after protected document calls. Native `form.discover` returned the
expected source-bound field counts. The target then stopped at its protected-continuation guard because the model
did not call the required trusted `document` tool. No question or cancellation qualified, and the error-final lacked
agent/session identity. The harness retained `missing_route_identity`; judge, regrade and comparison were not run.

This is evidence of progress beyond the [v2 missing-signature failure](manual-agent-evals-pdf-empty-v2-pilot-20261019.md),
not empty-value understanding, a qualified PDF scenario or complete E3/E0-E5. No historical result is upgraded.
The task was attempted once, without prompt/rubric tuning, guard weakening or replay of a used profile.

## Frozen Experiment

| Item | Observed |
| --- | --- |
| Merged runtime | `e9deb0a87812f1c0ec332bbe4ce70f8f2b37b201`, PR #1840 |
| Reviewed PR head | `20c4cdadff699995d319ef006cbb767e485e3cfb`; exact-head green CI, substantive review and owner PR rocket |
| Platform/build | Linux AMD64, native CGO, Go 1.26.9, `goolm,stdjson`, version `eval-pdf-empty-v3` |
| Build time | `2026-10-19T11:42:25Z` |
| Binary SHA-256 | `787e672e8b6e863de7578f032219d8795ed8692c6c51959e3fde05fa3fd91942` |
| Source archive SHA-256 | `7a4e8c25e17b0b69e6a12ac74f677a385cfc6a4c062733b99d7d290b7f028f82` |
| Case hash | `59235b8f04bb8a46801df88afc96b3752adf5c965553ebc9a2463c607ab1f19b` |
| Source PDF SHA-256 | `d7c6b1143f11bba1ac75a4b9d63f540505c340946b214d308557891015d8942e` |
| Run / trial | `af070872-279e-4634-8048-3e7bebc350b0` / `7be2e374-1fc4-4839-bb2c-e33866f41ac2` |
| Fresh client session | `ab2869b3-8ed6-4a07-a993-59f458bacc99` |
| Repetition | 1; same v2 task, driver, rubric, manifest and source |
| Time | CLI `11:44:03-11:44:15 UTC`; run start `11:44:04.191077096Z`, duration 11770 ms |

The isolated binary was built from the confirmed merge, outside production. A new private HOME, prepared profile,
client session and source copy were used with a clean environment and an owned gateway. Native BM25, `document`
and `request_user_input` were explicitly admitted; unrelated tools remained denied. Metadata-only capture remained
opted in. Existing deployed-main credentials were copied privately, never printed or committed.

## Runtime Fix And Evidence

PR #1840 restores `ThoughtSignature` only on the protected live tool-call projection in `pkg/agent/pipeline_llm.go`.
Durable history, turn journal and context ingest still omit it. The existing native pipeline regression now checks
an independent signature canary on the actual second provider request and its absence from durable and diagnostic
payloads. It fails against the old normalization and passes with the fix. Focused macOS race validation passed in
3.463s, broader protected/diagnostic/canonical controls in 10.828s and Gemini HTTP adapter controls in 1.595s;
root formatting, affected lint, changed-package pre-push checks and CI passed. Cross-human-resume, restart and
compaction signature retention are not established by this live-only fix.

Native metadata trace `trace-turn-f0e6dd0908a7b843000a1d54` contains 33 records, root `main-turn-2`, agent `main`,
outcome `error`, with no reported truncation. Its independently checked byte SHA-256 is
`583f5435ce3d45742cd33fb250b2abb121be313eaa2e3f7947f4d09cbf14de7e`.

| Records | Native fact |
| --- | --- |
| 2/3, 8/9, 14/15, 20/21, 26/27, 28/29 | Six model request/successful-response pairs |
| 5/7 | Native BM25 call/successful result |
| 11/13, 17/19 | Successful generic document calls; operation/field use not inferred |
| 23/25 | Successful `form.discover` with source SHA and content-free field counts |
| 31 | Terminal error following the protected-continuation guard |

The field-read observation reports 5 schema fields, 5 returned fields, 3 required fields, 2 saved values and
4 defaults, matching the independent fixture expectation. It belongs to call `call_1048828`, runtime generation
`tool-call-generation-e6fda52d-77e7-4056-82a7-c2b79e384e83`. This proves that this native call returned the scoped
field inventory, not that the model understood empty values or asked the right question.

The request-bound final exactly reports `protected answer continuation did not call trusted tool "document"`.
It has the expected workspace/turn trace scope but no agent/session identity. No missing-signature HTTP error was
observed in this attempt. The protected tool-only continuation rejected the responses rather than publishing an
unvalidated continuation; do not disable that guard to make the eval pass. Whether model behavior or its native
continuation contract needs adjustment is separate target triage, not authority for an open-ended repair/retry loop.

The frozen source-fields and native-model observations remain unavailable: no eligible routed question/cancellation
binding was established. Passive trace observations are kept separate and cannot backfill those frozen projections.
All three hard checks remain inconclusive with missing evidence, and the judge is correctly ineligible.

## Usage, Privacy And Cleanup

Six native responses expose 53200 input / 581 output tokens (53.2k input). This is the known passive native subset,
not complete billing: provider-internal attempts, broader all-call coverage and cost remain unknown. The canonical
native-model totals have unknown coverage, so their zero-valued counters do not mean zero cost. Scripted user and
judge made no model calls.

The raw owned-profile observer inspected 50 files / 528007 bytes, one authorized PDF and 26 public system files,
with zero opaque files or known literal leaks. Native SQLite inspection covered 118 rows, 330 text values,
67 index values and 137161 value bytes. The separate credential-copy guard inspected 71 files / 549901 bytes,
exactly five authorized copies and no known literal leak. This is scoped coverage, not all-host/provider privacy.

Owned PID 336366 exited natively with code 0, without force or log truncation, and was independently absent.
The retained source copy still matched its SHA. Logical settlement and the routed census owner were unavailable,
so the fixture was not removed; two empty owned temporary directories remain. Physical shutdown is not proof of
logical no-write/cleanup. Do not restart this used profile or delete its retained evidence merely to make cleanup green.

All five owned credential copies were removed and independently confirmed absent. Production config/security
hashes were unchanged. A post-run read-only check found all ten services active, no failed units or ten-minute
error entries, and production core still at `cf8bf5dc5beff1a0ef92487077f981e3ca3072ee`. No production binary,
service, config, harness or workspace was deployed, restarted or changed; no rollback is needed.

## Private Packet And Remaining Work

Private evidence stays outside git under:

- `/home/server/mintclaw-eval-pdf-source-build-v3-20261019.A6D0mrAi`
- `/home/server/mintclaw-eval-pdf-empty-v3-20261019.L0WgrxJH`

The used profile ends in `eval-680e5fa0-e686-4861-b9a5-61da1560450d`. Its canonical run JSON SHA-256 is
`1769a860fdbab1e3a26de5b4234624e746d86f5ddea9e360629a53f1cdf18593`. No regrade/comparison receipt exists.

Preserve this negative checkpoint and every earlier result. The remaining PDF output/private/adaptive/correction/
large cases, browser submit/multiturn/ambiguity/private/adaptive admission, domain repeats and exit record remain
mandatory. The full goal stays active; neither the signature fix nor this pilot completes it.
