# Core C1 Qualification

Date: 2026-10-25. This is a bounded qualification of two new General contracts,
not a new full-suite baseline. The original fourteen-case record remains
[2 pass, 7 fail, 2 error, 3 inconclusive](manual-agent-evals-baseline-20261024.md).
PDF/browser were not executed, fixed or promoted. Ten C2/C3 contracts remain in
the [execution ledger](../architecture/agent-evals-core-execution.md).

## Candidate And Method

- Merged runtime/corpus: `c69659c5349ed07286e5b9a0a6ae1e73a984301f`, Go 1.26.10,
  Linux AMD64, `goolm,stdjson`, build label `core-eval-c1-c69659c5349e`.
- Runtime SHA-256: `99f510f05bfdbdfd46d854862bd6d059d2e383524aa197da18d714cc3a7f173a`.
- Native Gemini request model `gemini-3.5-flash-lite`, isolated alias `core-eval`
  for target/judge. Model users were not used. This is not an independent
  attestation of the effective backend or provider-internal work.
- One qualification attempt, then one fixed three-trial batch per case. All
  four fresh profiles and the campaign plan were prepared before model work.
  No prompt changes, regrades or rerun-until-green. Each batch used three distinct
  native session identities; no production workspace/conversation was copied.
- Explicit owned gateway, Seahorse, 8,192-token context, 2,048 requested target
  output tokens, four tool iterations and one configured LLM retry. Tools were
  default-deny except ordinary `request_user_input`; hooks, exec, cron, MCP,
  remote nodes, browser, document and external channels were disabled/refused.
- Each entire run shared a ceiling of 24 logical target adapter calls, 128 KiB
  serialized input per call, 2,048 requested output tokens, a 100k reported-token
  stopping threshold and two calls in flight. Limits do not reset per trial.
  Judge calls have their separate tools-disabled role bounds.

Selected native credentials were resolved read-only and copied only into new
private credential files/native references. Channel tokens were regenerated.
Credential values, transcripts and raw artifacts are not in Git or this report.
The one-off seed helper passed synthetic-source preparation/key-binding controls
locally and on Linux before actual credential use; it never called a model.

Private evidence root on `server@oc`:
`/home/server/mintclaw-core-evals-20261025.pKzJyDsX` (0700). The frozen campaign is
`campaign.json`; invocation output/stderr and native reports remain there.
This is not a production installation or a public/shared test credential profile.

## Results

Durations are the runner's post-admission report duration, not binary build,
profile preparation or gateway-startup wall time. Token counts are scoped
provider-reported observations, not complete billing totals.

| Case / Attempt | Result | Target Calls | Reported Target Tokens | Judge Calls / Tokens | Duration |
| --- | --- | --- | --- | --- | --- |
| Correction qualification | 1/1 pass | 3 | 5.7k | 1 / 800 | 3.7s |
| Correction fixed batch | 3/3 pass | 9 | 16.8k | 3 / 2.4k | 10.2s |
| Answer/resume qualification | 1/1 pass | 3 | 6.0k | 1 / 801 | 3.6s |
| Answer/resume fixed batch | 3/3 pass | 9 | 17.7k | 3 / 2.4k | 11.5s |

All eight trials passed execution, every mandatory hard check and the required
judge. All four owned children exited normally with sealed budgets, zero
in-flight calls and unknown-usage counters, and observed privacy scans without
known leaks. No forced cleanup, provider fallback or extra paid retry was used
by the runner; provider-internal attempts remain unobservable.

Correction required the corrected marker, absence of the old marker in the
last final and the same native session. Answer/resume required the exact
delivered prompt, a terminal answered record, exactly one resume/paired tool
result/assistant continuation, saved `paper` recall and the same native session.
Each answer-batch trial retained native committed history/metadata digests.
This is MintClaw wire/owner coverage, not Telegram buttons or PDF intake.

## Frozen Identities

| Case | Case SHA-256 | Qualification Run | Fixed Batch Run |
| --- | --- | --- | --- |
| `general.context.correction` | `2fd7760a9d5f54a530a48b3197f0440a02759c3ca42fc837d99af1823c82240b` | `894ffded-d058-4cb9-8a02-315b02bb8be5` | `4b625e5d-bbef-47fc-9b21-1a3a3dc61076` |
| `general.interaction.answer` | `a8eaee4e5515b36c0f320a30a96c7bfc2ea853d11f5670a54ec79ec50c1b20f4` | `bd592d35-18de-480e-9fde-998c89820e34` | `ea92109e-6306-45fd-8e1d-954e264614cb` |

Invocation metadata names the four separate `profiles/eval-UUID` homes. Native
JSON reports are under each home's `reports/<run-id>/`; case/trial IDs and
versions remain unchanged. Offline qualification-vs-batch `compare` succeeded
for both cases without target/model work and retained 1-vs-3 sampling counts.
It reported configuration/repetition and applicable native-environment changes,
not causal quality attribution.

## Limits And Disposition

Optional routed-model metadata is **partial** in the answer batch: seven
observed native request/response records and two `missing_trace` outcomes. The
independent native agent-provider budget retained nine completed calls with known
reported usage, and all mandatory answer/resume witnesses passed. No missing
trace was fabricated or called complete; `all_call_coverage` stays `unknown`.
Correction's three/nine and answer qualification's three routed records were
observed. This gap is not an instruction to repair/replay historical traces.

The two C1 cases satisfy the predefined 3/3 promotion threshold and join the two
original General defaults without changing any case definition/digest. Defaults
therefore become four General cases; all twelve PDF/browser cases stay pending.
The combined four-default suite was not run in this campaign. Original cases'
earlier evidence is preserved, not relabeled as new candidate measurements.
This small fixed smoke sample is not a statistical reliability estimate or a
claim about the ten unimplemented C2/C3 contracts.

The expanded default selection needs `bounded_calls`, `target_call_budget`, a
configured judge and `--start-target`; see the updated
[preset template](../../evals/presets/local.template.json) and
[budget contract](../reference/owned-eval-call-budget.md). An old attach preset
can still explicitly select `general.context.followup` and
`general.interaction.navigation` without enabling the new contracts.

No production binary/config/service was changed. Main PID remained `911387`;
all ten expected services were active with zero error entries in the closing
status window. Every owned profile's PID/ownership marker was absent after
clean settlement. The private campaign used about 153 MiB including binaries;
the host was already nearly full, so compilation was local and a 1 GiB free-space
reserve was checked before each attempt. Do not clean unrelated host data.

Next: C2's genuine active-turn barrier, steering/order modes and two-session
isolation. The overall goal is not complete.
