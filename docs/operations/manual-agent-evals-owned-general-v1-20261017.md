# Manual Agent Evals: Owned General V1 Pilot

## Scope

One serial two-case run on 2026-10-17 exercised `eval run --start-target` from merged
`1420fb9739db8c6bcba0038ab12449a70ffcf8de` ([PR #1639](https://github.com/bogdanovich/mintclaw/pull/1639)).
The binary and corpus came from an archive of that commit, on Linux amd64 with Go 1.26.9 and
`goolm,stdjson` tags. Build identity was explicitly injected with `-buildvcs=false`, not inferred from a live
checkout; build version was `eval-owned-general-v1-1420fb973`.
Binary SHA-256: `65c0c25efac8a720ea47f8906164f013ac59f5ff2b23e2e81b335cc556617376`.

A fresh private profile reused only an authorized API credential and the selected public model request settings
from deployed main. Instructions were synthetic; production configuration, sessions, memory, OAuth/browser state,
services and binaries were not copied or modified by this operation. Credentials used native file references under
the profile's private `.security` directory. The preset and raw runner output stayed under private `reports`.
Only the native loopback channel and ordinary question tool were admitted; PDF/browser/exec/MCP, external channels,
nodes, cron, hooks, hot reload and deployment were disabled, with default-deny tool and approval policy.

The target requested `gemini-3.5-flash-lite`. The separate tools-disabled judge requested `gemini-3.6-flash`,
`eval-judge-v2` and `eval-role-generation-v1`; its recorded configuration hash was
`462e40289560e45241a3f35e702e78f7187cf5b3c115ed325abdad1f79521787`, matching the
[previous calibrated configuration](manual-agent-evals-judge-v2-calibration-20261011.md).
Requested identities are not provider-confirmed effective models or billing evidence. Both cases were scripted,
with fresh sessions, one trial per case, no retries and no LLM-user calls.

## Results

Report ID: `107a9f02-f14b-4504-bb62-3d86749229a2`; complete report, overall **pass**, CLI exit 0.
Reported duration: **6,440 ms**, including scenario work, judging and owned shutdown, but excluding gateway startup
before admission and the separate cold build. It is not the complete build-to-result wall time.

| Case | Verdict | Trial Duration Including Judge | Independent Checks |
| --- | --- | --- | --- |
| `general.context.followup` | pass | 3,861 ms | Synthetic marker retained across two same-session turns; hard checks and judge passed. |
| `general.interaction.navigation` | pass | 2,495 ms | Native question and same-session checks passed; prompt-bound native cancellation and coherent-controls judge passed. |

Context trial: `4738aab4-4838-4654-82d0-17c561a803cb`;
case SHA-256: `4703ef12118060ecbb7c4edfb2ee19cf4950f91e13b52ac29e01851c8797b69d`.
Interaction trial: `b6cf4779-6a5a-4c3e-b0c5-21505945e365`;
case SHA-256: `d5e6ab4c8732f27ef726ea3407e181d595b05ca8ac035236a35c2dfd2f7332e4`.
Runtime public-config SHA-256: `53f3c4d16e25f8d975c6b5fcebc1f0ca5ba4a3f2f1da9bb03f2b97fb6ecf66de`.

The interaction trial references native-question evidence `event-000002` and native-cancellation evidence
`event-000004`; same-session checks bind both. Its required judge returned `pass / judge_completed`, with the
recorded judge call taking 1,035 ms. Its retained remote state remains `unknown`; a passing cancellation check
does not rewrite whole-runtime remote state.

Two judge calls returned **1,169 input, 156 output, 1,325 total tokens**, displayed **`1.2k in, 156 out`**.
Target all-call usage, provider-internal attempts and costs remain unknown. Zero LLM-user calls do not mean zero
target usage, and judge totals are not substituted for target totals.

## Lifecycle And Privacy

The runner admitted its exact child PID `311776`, requested native stop, observed and reaped exit 0, recorded
`native_exit_zero`, and did not force termination or truncate the bounded child-output log. A subsequent `/proc`
check found no such process; the owned marker was absent. This exercised the product-owned lifecycle, not an
external launcher or a shutdown inferred from WebSocket closure.

Before sanitization or deletion, a bounded operational inspector scanned 56 raw files, 330,417 bytes, outside the
authorized credential directory. It found no exact API/channel credential literals. The scan withheld contents
and filenames from output; its value-free receipt was retained privately. This narrow literal check is not a
general privacy oracle, escaped/derived-value proof, or PDF/browser privacy qualification. Authorized credential
source files were deliberately excluded, not relabelled as leaks or sanitized before scanning.

After confirmed process exit and the raw observation, only the two generated API/channel credential copies were
removed; independent absence checks passed. Production credential sources were untouched. Native state, reports,
raw logs, source archive and the identified binary remain private outside Git. The retained profile intentionally
needs explicit credential reprovisioning for future grading; it is not fresh or resumable as an owned target.

## Reproduction And Limits

Use the [operator guide](manual-agent-evals.md) to prepare a new admitted private profile; keep model credential
files under `.security` and the preset under `reports`, not unknown top-level home entries. From the identified
binary/corpus revision, run once:

```sh
MINTCLAW_HOME=/absolute/PRIVATE_HOME MINTCLAW_CONFIG=/absolute/PRIVATE_HOME/config.json \
  /absolute/identified/mintclaw eval run --start-target --root /absolute/identified/evals \
  --preset /absolute/PRIVATE_HOME/reports/preset.json --out /absolute/PRIVATE_HOME/reports \
  --suite general --suite-timeout 5m --json
```

Retain the first result, including failures. Never adopt a PID, reopen a used profile, replay an unsettled task,
or publish raw logs/credentials. Earlier passing and nonpassing pilot reports remain unchanged.

This qualifies two scripted general cases and their owned native process lifecycle on Linux, not adaptive-user,
PDF, browser, Telegram, macOS live, full private-surface/domain cleanup or the 14-case suite. The workspace file
observations in [PR #1648](https://github.com/bogdanovich/mintclaw/pull/1648) merged after this run and were not
exercised by its older identified binary. That PR separately passed macOS/Linux full race suites, all 17 CI checks
and actual review; none of those tests are additional live-model scenarios. The full E0-E5 goal remains unfinished.
