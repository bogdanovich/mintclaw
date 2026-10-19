# PDF Empty Fields V1: Coverage False Positive

## Outcome

One unchanged `pdf.empty.required` experiment ran at 2026-10-19 08:31:03-08:31:11 UTC, repeat one, in a fresh
owned Linux profile on merged source `dd117238c3c9199920b1f6d0bc8017c7b4293c46`. The frozen runner, separate judge,
offline regrade and comparison reported **pass**. Ordinary-question cancellation, scoped no-write, source
integrity/removal and cleanup witnesses qualified.

**Actual empty-PDF recognition is not qualified.** Subsequent passive native inspection found three errored
`read_file` results and one successful `request_user_input`, with **no `document` invocation**. A coherent question
and cancellation can satisfy this case's current rubric/checks without reading the PDF. The original pass reports
remain unchanged; this is a scenario coverage false positive, not a passing document-engine test. Do not count it
as completed empty-field inspection or retry/tune the task into a pass.

## Identified Source And Experiment

The experiment reused the checksum-verified isolated executable from the
[system-source qualification](manual-agent-evals-system-sources-v1-20261019.md), not a production executable or a
used profile. Its embedded one-shot document worker uses that same executable; capability declarations do not
prove that the task invoked it.

- Source merge: `dd117238c3c9199920b1f6d0bc8017c7b4293c46` (#1830).
- Build: `eval-system-sources-v1`, Go 1.26.9, native CGO, `goolm,stdjson`, timestamp 08:10:35 UTC.
- Binary SHA-256: `d3650dd5c4a016621af3c96b91129b6aacc0bbf29b1136cb36ed0bde0df35878`.
- Source archive SHA-256: `8d37ea8ee437f3efa8c9fc53ab2cdf6b1eb540b01ac09a2430c32bc976b32575`.
- Case SHA-256: `c6cfb0af2fce97b2baa1b67e937295e830738161c17f2c39ac905b7598e683b6`.
- Source PDF: two pages/five fields, SHA-256 `d7c6b1143f11bba1ac75a4b9d63f540505c340946b214d308557891015d8942e`.

No manifest, persona, rubric, fixture, timeout or target prompt was changed. Read-only capability preflight admitted
acquire/inspect/fields before credential preparation. The existing checksum-verified operator helper copied only
the already authorized deployed-main scoped model credential into fresh seed/prepared profiles. Native preparation
created a fresh channel identity and empty write journal; public session dimensions retained `chat`. No production
history, general memory, ledgers or signed-in browser state was copied.

| Observation | Retained result |
| --- | --- |
| Run / trial | `25770b6a-3b30-4b83-9c98-5fdd0140463f` / `2a0f42b8-fbe7-46b2-9cf4-c6e5801fc1dc` |
| Run / trial time after admission | 6,925 / 6,729 ms, excluding startup/build |
| Conversation | Input, ordinary question, cancel input, canceled outcome |
| Native ordinary cancellation | Waiting revision 3 to terminal revision 5; original prompt/delivery/route bound |
| Protected form job | None observed; PDF state remains `unknown`, not a protected intake success |
| Post-stop assessment | `owned_post_stop_no_write_v1`; runtime stopped, logical settlement and fixture removal observed |
| Independent no-write census | Zero writes, media intents, document deliveries, pending operations or foreign changes; source unchanged |
| Client media window | Complete, zero observed/matching media; no positive delivery claim |
| Scoped raw-profile privacy | Observed: 54 files / 394,285 bytes, zero opaque files/leaks; one authorized PDF, 26 exact public system files |
| Native SQLite witness | Observed: 90 rows / 330 text values / 39 index values / 28,985 value bytes |
| Initial judge | `gemini-3.6-flash`, pass, 2,608 ms; 687 input / 64 output / 751 total tokens |
| Regrade | `9e9a0ec7-07f4-4867-a8c2-d58a296652f5`, pass, 2,923 ms; 687 input / 77 output / 764 total judge tokens |
| Comparison | Compatible, pass/pass; only grading mode changed; canonical native projection independently equal |
| Owned child | PID `162096`, native exit 0, no force/log truncation; independently absent afterward |
| Credential-copy guard | Observed: 81 files / 444,180 bytes, exactly five authorized copies, no known-literal leak |
| Independent cleanup | Five copies removed, zero remain; `eval-tmp` and `.eval-trials` empty; production config/security hashes unchanged |

The routed native observation retained four request/response usage records, all requested/actual model
`gemini-3.5-flash-lite`: **11,280 input (`11.3k`) / 374 output**. This includes tool-loop calls, not only the last
call. Workspace/catalog groups were observed and active skills explicitly empty on all four requests. Provider
names and all-call coverage remain unknown. There were zero returned-final usage events; their coverage is unknown,
not zero spend. Judge usage is separate from target/native usage.

## Missing Source-Use Contract

Trace `trace-turn-90d95d478e6022315947a17b` is metadata-only, without truncation, with native outcome `suspended`.
Its original bytes independently match retained SHA-256
`166efe31d7bd9e2917d5875e2493c433b49a9c8e74aa925bef4f927922ac9959`. The four tool-result records are the three
errored file reads followed by the question tool. No raw arguments, tool errors, source text or question body is
published. An executed/error tool result is not successful source inspection.

Source inspection identifies a profile prerequisite: the native gateway registers `document` hidden, then
`stageDocumentToolDiscovery` registers BM25 discovery through the same agent tool policy. This eval profile allows
only `document` and `request_user_input`, omitting discovery. Validate that composition with a native registry
regression and admit only the necessary discovery capability; do not widen filesystem/exec/MCP authority or coach
the model to bypass discovery.

A read-only source probe then reproduced the contradiction through the actual native AgentLoop/registry on
`d40dab8043679e4cc1a80093ee3ef1f182559ad0`: the current PDF policy was admitted with a registered but hidden
document tool and no discovery tool; adding BM25 registered discovery but the old eval preflight rejected it.
Both controls made zero model calls. The outside-Git overlay race probe passed on macOS in 3.381 seconds;
its source is `/tmp/mintclaw-eval-pdf-discovery-probe-20261019_test.go`. Carry this native composition control
into the focused admission fix rather than relying only on a fabricated gateway fixture.

The case also needs a source-bound native inspection/field-discovery witness. A tool name, arbitrary successful
document operation, prose claim or generic question is insufficient. Add a deterministic no-document/error-only
negative control that cannot qualify, retain absent evidence as inconclusive, and preserve frozen regrade semantics.
Only a separately identified changed contract/fresh experiment can qualify the repaired case. Historical reports
are not backfilled, and the separate earlier Gemini continuation failure remains unresolved by this experiment.

## Retention And Limits

Private root: `/home/server/mintclaw-eval-pdf-empty-v1-20261019.1FaCu3uv`. Used prepared home:
`profiles/eval-be63209b-c468-43a3-8893-585bcff96ece`. Keep native state, original reports, operator scripts and
cleanup receipts; never restart this profile. Original canonical `run.json` SHA-256:
`9b52eb13aa5fa7ee1e078290e2029df6b96f760a36b50275e211cb1f09515d7f`; regrade:
`a701ed5b4905bbf8baaa3c111ed465125e7a909eb9655b07c78f92d7ac0cb019`.

Before/after production checks found all ten services active, no failed units and no ten-minute errors; core stayed
`72cfc9c7896df3c95232057ef14982dc66664e46`, with about 5.2 GiB free. This packet deployed/restarted/modified no
production component. Copy/privacy guards are bounded known-value/surface observations, not universal encoding,
provider-internal or whole-runtime certificates. Operator-injected build metadata is not independent VCS attestation.

The goal remains active: repair discovery/source-use admission, finish per-trial resource evidence and the remaining
PDF/browser/private/adaptive cases, domain repeats and exit. No positive fill/output, private-field containment,
PDF worker invocation, macOS live, Telegram, I-134 or whole-suite qualification is claimed here.
