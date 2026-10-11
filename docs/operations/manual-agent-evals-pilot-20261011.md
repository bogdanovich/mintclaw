# Manual Agent Evals: First Live Pilot

## Scope

Executed on 2026-10-11 against merged core `b143bc01d5fdc0796f894ec537a2d36723e96f80`, not an unmerged branch
or the production gateway. Build version: `eval-pilot-b143bc01`; Linux amd64, Go 1.26.9. Corpus and calibration
came from an archive of that same commit. This is diagnostic evidence, not completion of the E0-E5 goal.

Two fresh private profiles used numeric-loopback gateways and authenticated channels, deny-default tools/MCP,
and only `request_user_input`. Production main supplied one operator-authorized API credential; no production
sessions, memory, OAuth store, browser state or runtime configuration were cloned. Profile directories were
`0700`; credentials, logs and retained reports were `0600`. Both owned gateways were stopped and their process
sessions exited; profile evidence was retained privately. No production service was restarted or deployed.

Target and judge requested the existing configured Gemini model `gemini-3.5-flash-lite` in separate contexts.
The two general cases are scripted, so no LLM-user call occurred. Same-model grading is correlated and the
configured request identity is not independent proof of an effective provider model or billing total.

## Judge Calibration

Calibration report ID: `39a3ee6c-c9cc-402d-a053-d69aee98f90d`; prompt `eval-judge-v1` and six unchanged manual labels.

| Example | Expected | Observed | Agreement |
| --- | --- | --- | --- |
| Coherent choices | pass | pass | yes |
| Mismatched choices | fail | pass | no |
| Repeated accepted fact | fail | fail | yes |
| Unverified artifact | inconclusive | fail | no |
| Invented persona fact | fail | fail | yes |
| Injected verdict | fail | fail | yes |

Overall calibration: **fail**, 4/6 agreement. Six attempted calls returned usage: 2,007 input tokens and 337
output tokens (2,344 total); compact display is `2k in, 337 out`. Provider-internal attempts remain unknown.
The judge missed a known bad choices example; its passing judgments are not reliable qualification evidence.
Labels and first outcomes were not changed or repeatedly rerun until green.

## General Cases

Initial report `195eb4ba-5208-499a-be57-14c9aed91cb0` retained both cases as inconclusive. An empty native
`allow_from` denied the sender, but eval preflight admitted that unusable profile. Only the owned gateway was
stopped after identifying the admission defect. The first attempted case recorded `disconnected_remote_unknown`;
the second recorded `target_changed` without task dispatch. Neither was treated as success or implicit cancellation.

A fresh profile with only `allow_from: ["mintclaw-user"]` produced report
`160233c4-85fc-4fa0-91a4-f7d413321cb9` in **3.625 seconds**:

| Case | Verdict | Time | Evidence / Limit |
| --- | --- | --- | --- |
| `general.context.followup` | pass | 2.351s | Same-session two-turn marker checks passed; judge is not calibrated |
| `general.interaction.navigation` | inconclusive | 1.256s | `missing_route_identity` on the response to unsupported navigation |

One judge call returned 498 input and 70 output tokens (568 total). No LLM-user calls occurred; target all-call
usage remains unknown, not zero. Overall run verdict: **inconclusive**, with one pass and one inconclusive.

The initial question had agent/session/trace identity. The next response rejected `Clarify`: ordinary
`request_user_input` does not admit that protected-question action. The notice had agent/session identity but no
trace scopes. The current case incorrectly expected another question. It never reached its cancellation step.
Do not enable unsupported actions globally or infer task success from this notice. Correct the scenario's native
action admission and separately assess whether notices need a distinct evidence contract.

## Follow-Up

The [second judge experiment](manual-agent-evals-judge-experiment-20261011.md) used a separately identified model
configuration with unchanged labels. Agreement improved to 5/6, but calibration still failed; it is not a replacement
for these first outcomes or proof of target qualification.

1. Fix prepared sender admission and the template regression test: [PR #1576](https://github.com/bogdanovich/mintclaw/pull/1576).
2. Use a valid native interaction/navigation scenario, retain unsupported/stale/foreign actions as negative controls,
   and keep real runtime evidence requirements. Do not silently relabel the original result as a pass.
3. Establish a reliable judge configuration and rerun pinned calibration as a new, identified experiment.
4. Finish PDF/browser native evidence, privacy/cleanup, provisioning/comparison and the actual 14-case qualification.

Private report JSON, transcripts, credentials and logs are not committed. This report does not qualify PDF,
browser, Telegram, macOS live execution, independent visual output, complete remote cleanup, or target token totals.
