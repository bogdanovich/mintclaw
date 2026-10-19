# Manual Agent Evals: Empty PDF V2 Pilot, 2026-10-19

## Result

One fresh `pdf.empty.required` attempt on merged source-use code returned **inconclusive**, not pass. Native BM25
discovery and one `document` invocation succeeded, but the next Gemini request failed with HTTP 400 reporting a
missing `thought_signature`. The final error message omitted agent/session identity, so the harness retained
`missing_route_identity`. No question, cancellation, field-read proof, judge, offline regrade or comparison qualified.
No task was retried, and neither this result nor the [v1 false positive](manual-agent-evals-pdf-empty-v1-pilot-20261019.md)
was upgraded. This does not finish E3 or the full E0-E5 goal.

## Frozen Experiment

| Item | Observed |
| --- | --- |
| Merged runtime | `e292a5c4913113a5ac5ab07e7daa5ae650ebf6b6`, PR #1838 |
| Reviewed PR head | `2b7e73a2eab3a2974ac2c70ecbbdeaf6dc3d1e80`; 14 green checks, SHA-bound review and owner PR rocket |
| Prerequisites | #1836 native discovery admission; #1837 content-free native field-read producer |
| Platform/build | Linux AMD64, native CGO, Go 1.26.9, `goolm,stdjson`, version `eval-pdf-empty-v2` |
| Build time | `2026-10-19T10:49:59Z` |
| Binary SHA-256 | `582023476431a68d0597b69ed5573094f4561ccb7171fecd050246462445e45e` |
| Source archive SHA-256 | `e13a065dd324335c27aec2e80d090efeeb08e95392b00e4b59e8f91c885bc33f` |
| Case hash | `59235b8f04bb8a46801df88afc96b3752adf5c965553ebc9a2463c607ab1f19b` |
| Source PDF SHA-256 | `d7c6b1143f11bba1ac75a4b9d63f540505c340946b214d308557891015d8942e` |
| Run / trial | `1b87bfe7-bde2-424c-849b-dc38bc5e3f55` / `9468e8ca-f928-4e12-bffd-a16321371dc7` |
| Fresh client session | `6932b850-50e1-40b3-85d3-0d022ab87380` |
| Repetition | 1; original task, driver and rubric unchanged |
| Time | CLI `10:52:24-10:52:28 UTC`; run start `10:52:26.067278885Z`, duration 2671 ms |

The new manifest requires the exact-source field-read hard check, independently inspected counts (5 schema fields,
3 required, 2 saved values, 4 defaults), and a returned window covering all 5. A new unused public seed explicitly
admitted native BM25 alongside `document` and `request_user_input`; unrelated tools remained denied. Metadata-only
capture was explicitly enabled. The operator used a clean environment/private HOME, a fresh prepared profile and
an owned target. Existing deployed-main scoped credentials were copied privately, never printed or committed.

This changes the case contract, code and discovery configuration, not the prompt. V1/V2 are not a controlled model
quality comparison, and changing the contract cannot retroactively qualify v1.

## Native Evidence And Triage

Trace `trace-turn-5ca09849837bd14ae16f2033` contains 19 metadata-only records, root turn `main-turn-2`, agent `main`,
outcome `error`, with no reported truncation. Its independently checked byte SHA-256 is
`ac7d35a9cacb821f6a392cccbd28c2034146ccac3efe9daeff038c9d6052f8d1`.

| Record | Fact |
| --- | --- |
| 2 / 3 | First model request / successful response |
| 5 / 7 | Native `tool_search_tool_bm25` call / successful result |
| 8 / 9 | Second model request / successful response |
| 11 / 13 | Native `document` call / successful result, without a field-read observation |
| 14 | Third model request; no successful response recorded before the terminal error |

The request-bound final contains the provider's missing-signature HTTP 400 error and the expected workspace/turn scope,
but no agent/session identity. The harness correctly does not infer a trusted route from error prose or a trace
hash. The missing route code is therefore secondary to the target failure. A successful generic document result
does not prove `fields`, `form.discover`, source identity or empty-value recognition; none is claimed here.

The runtime candidate is concrete but still needs its own focused provider-facing regression: in
`pkg/agent/pipeline_llm.go`, protected calls clear `ThoughtSignature` in the durable projection, copy that projection
to the live call and restore only live arguments. The existing
`TestLLMNormalizationPersistsProjectionButRetainsLiveTurnArguments` also expects an empty live signature. The native
Gemini adapter forwards `ToolCall.ThoughtSignature` when constructing function-call parts. This is a code-level
candidate for losing required provider replay metadata; metadata-only traces cannot reveal the actual opaque
provider signature or prove every transport hop. No raw signature, provider wire body or private source is published.

The [output v1 pilot](manual-agent-evals-pdf-output-v1-pilot-20261018.md) already observed the same error family
after an ordinary-question continuation. This attempt shows a protected tool loop can hit it before any question.
Triage the runtime separately: preserve required opaque metadata only in the authorized live provider exchange,
retain durable/log/trace privacy boundaries, cover actual provider requests with stubs, then use a new bounded
experiment. Do not bypass signature validation, fabricate signatures, loosen eval identity checks or retry until green.

## Usage, Privacy And Cleanup

Two native responses report 14255 input and 156 output tokens (14.3k input). A third request and any provider-internal
attempts remain unknown. These are passive trace facts, not complete billing or a replacement for the frozen report:
its native model observation is unavailable because a complete routed trial binding was never established. Its
zero-valued unknown totals are not zero cost. Scripted user and judge made no calls; grading was correctly skipped.

The raw owned-profile observer inspected 50 files / 425455 bytes, one authorized PDF and 26 public system files,
with zero opaque files or known literal leaks. Native SQLite inspection covered 66 rows, 258 text values, 35 index
values and 73821 value bytes. The separate credential-copy guard observed 71 files / 447654 bytes, exactly five
authorized copies and no known literal leak. This is scoped coverage, not a claim about all host/provider surfaces.

Owned PID 301808 exited natively with code 0, without force or log truncation, and was independently absent. The
source copy still matched its SHA. However, no routed census owner or logical settlement proof qualified, so the
harness retained the used fixture and reported cleanup uncertainty. Two empty owned temporary directories remain;
do not treat an absent process as a complete no-write/cleanup proof. Do not restart this used profile.

All five owned credential copies were removed and independently confirmed absent. Production main config/security
hashes were unchanged. A read-only post-run check found all ten services active, no failed units or ten-minute error
entries, and the separately deployed core still at `cf8bf5dc5beff1a0ef92487077f981e3ca3072ee`. No production binary,
service, config, harness or workspace was deployed, restarted or changed; no rollback is needed.

## Private Packet

Private operator evidence remains outside git under:

- `/home/server/mintclaw-eval-pdf-source-build-v2-20261019.nKlYFpja`
- `/home/server/mintclaw-eval-pdf-empty-v2-20261019.FJLU3o7F`

The latter retains scripts, guarded credential-copy cleanup, preparation, original run and independent verification;
the used profile ends in `eval-2f684212-9f24-4212-aeb3-1dbec5be0e9e`. The original canonical run JSON SHA-256 is
`8b5de4a2f6d39e6efe23f135f2af1d6bdc8152588a932a2059128406f120f795`. No regrade/comparison receipt exists for this
unqualified attempt. Follow-up fixes must preserve this packet and all prior failures, not rewrite them as success.
