# Manual Agent Evals: General V3 Pilot

## Scope

One serial two-case run on 2026-10-11 used merged core `c97d86cd63988016fa3bb9baf2fac0d4295d9c2e`, build
`eval-general-v3-c97d86cd`, Linux amd64 and Go 1.26.9. Source and corpus were archived from that same commit;
the binary version and isolated native runtime probe were checked before task dispatch. Build time and effective
provider model remain unknown. This run followed the native outbox observer fix in
[PR #1598](https://github.com/bogdanovich/mintclaw/pull/1598), not an unmerged branch.

A fresh private profile used one operator-authorized API credential through native file references. No production
sessions, memory, OAuth/browser state or workspace configuration were copied. Production services, binaries and
configuration were not modified. Profile/report directories were `0700`; credentials, logs and reports were `0600`.

The target requested `gemini-3.5-flash-lite`; the separate tools-disabled judge requested `gemini-3.6-flash`,
`eval-judge-v2` and `eval-role-generation-v1`. That judge configuration previously had
[6/6 calibration agreement](manual-agent-evals-judge-v2-calibration-20261011.md). Configured identities do not prove
effective provider model, provider-internal attempts or billing. Both cases were scripted, with one fresh native
session per case, no repeats and no LLM-user calls. Only ordinary question input was enabled; PDF/browser/exec/MCP,
external channels, cron, hooks and nodes were disabled.

## Results

Report ID: `ca5b54bb-50c3-43ab-9ae7-05e41a00782f`; complete report, overall **pass**, CLI exit 0.
Started at `2026-10-11T21:44:18.569615597Z`; total reported duration: **7,211 ms**.

| Case | Verdict | Trial Duration Including Judge | Independent Checks |
| --- | --- | --- | --- |
| `general.context.followup` | pass | 4,062 ms | Synthetic marker retained across two same-session turns; hard checks and judge passed. |
| `general.interaction.navigation` | pass | 3,110 ms | Fresh native ordinary question, delivered prompt and prompt-bound control; native terminal cancellation and judge passed. |

Context case SHA-256: `4703ef12118060ecbb7c4edfb2ee19cf4950f91e13b52ac29e01851c8797b69d`.
Interaction case SHA-256: `d5e6ab4c8732f27ef726ea3407e181d595b05ca8ac035236a35c2dfd2f7332e4`.
Runtime public-config SHA-256: `8ce4ec6da6a56df37ae8f4697e89ddf6f80ef99abb764e4aea6fbe01759a3f43`.
Judge configuration SHA-256: `462e40289560e45241a3f35e702e78f7187cf5b3c115ed325abdad1f79521787`.

The cancellation evidence retains the actual interaction, prompt/request/delivery IDs, owner route, original turn
and terminal revisions. The observed question advanced from waiting revision 3 to terminal revision 5, with a
matching `canceled` event and native remove-controls reply. This is not a prose-only assertion or socket closure.

Two judge calls returned 1,169 input and 188 output tokens (1,357 total), displayed **`1.2k in, 188 out`**.
Their reported durations were 1,454 ms and 1,151 ms. Target all-call usage and provider-internal attempts remain
unknown; the report does not invent a target total by subtracting role usage or infer a token budget.

## Retention And Limits

The [general v2 failure](manual-agent-evals-general-pilot-20261011.md) and all earlier pilot/calibration reports
remain unchanged. This is a new identified run after a reviewed harness fix, not a relabelled result or a
retry-until-green loop. Raw transcripts, native state, credentials and run artifacts stay outside Git.

After report completion, the owned gateway PID's executable path was checked against the fresh profile binary,
that PID received SIGTERM, its command exited successfully, and `/proc` confirmed its absence. Private evidence
was retained. The report truthfully records `local_transports_closed; remote_state_unknown`: one question's
terminal cancellation and local process shutdown do not establish whole-runtime quiescence or cleanup.

These are the two required general scripted cases on Linux, not adaptive-user, PDF, browser, Telegram, macOS live,
streaming-media, exhaustive privacy, skills-fingerprint or full-suite qualification. The E0-E5 goal remains active.
The native PDF repeated final-delivery instability remains separate required investigation; this passing general
run does not erase it. PDF/browser native evidence, independent hard oracles, owned cleanup, available target
usage, profile provisioning and the remaining actual minimum cases still need qualification.
