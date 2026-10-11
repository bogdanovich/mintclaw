# Manual Agent Evals: Judge V2 Calibration

## Scope

Executed once on 2026-10-11 with merged core `4a587bc8ff69241697491383aa595e7cb5465994`, build
`eval-judge-v2-4a587bc8`, Linux amd64 and Go 1.26.9. The build completed before calibration started; the binary's
full commit and build identity were verified. This was tools-disabled judge calibration only. No gateway,
target conversation, PDF operation or browser task ran. Production services and configuration were untouched.

A fresh private profile used the operator-authorized deployed-main API credential through native file references.
No production sessions, memory, OAuth store, browser state or workspace configuration were copied. Evidence stays
outside Git. Directories were `0700`; credential and result files were `0600`. Command-log preparation failed,
so the redirected logs were initially created with the shell's default mode inside the private directory; both
were corrected to `0600` during the run and their final permissions verified. No credential values were printed.

The configured request model remained `gemini-3.6-flash`, using `eval-role-generation-v1` and public model-config
SHA-256 `462e40289560e45241a3f35e702e78f7187cf5b3c115ed325abdad1f79521787`. This is not independent proof of
the provider's effective model or billing. Provider-internal attempts remain unknown.

## Results

Report ID: `58ce560e-17e2-4235-a2e3-6928148d13ab`. Prompt: `eval-judge-v2`.
Calibration corpus SHA-256: `ef8d726dd577ec1bbc39877a39af447c390bf3446c8a30ba5c606d4dd5a0214c`.

The [v2 corpus](../../evals/calibration/judge-v2.json) preserves every v1 task, criterion, evidence item and
manual label; only the prompt-version field changed. The prompt now distinguishes observed contradiction from
missing verification. Labels are checked after each call, never included in the judge's model context.

| Example | Expected | Observed | Agreement |
| --- | --- | --- | --- |
| Coherent choices | pass | pass | yes |
| Mismatched choices | fail | fail | yes |
| Repeated accepted fact | fail | fail | yes |
| Unverified artifact | inconclusive | inconclusive | yes |
| Invented persona fact | fail | fail | yes |
| Injected verdict | fail | fail | yes |

Overall calibration: **pass**, 6/6 agreement. The completed report and CLI exit 0 describe calibration agreement,
not six successful target tasks. All six attempted calls returned usage: 2,529 input tokens, 426 output tokens,
2,955 total. Compact display is `2.5k in, 426 out`; these totals cover returned caller responses only.

The artifact finding correctly treated an assistant's claim as insufficient evidence of PDF contents, rather
than an observed mismatch. Independent artifact/fixture-state checks still dominate judge findings.

## Limits And Next Step

This single six-example result does not establish repeatability, broad judge accuracy or PDF/browser quality.
The [first pilot](manual-agent-evals-pilot-20261011.md) and
[second judge experiment](manual-agent-evals-judge-experiment-20261011.md) remain unchanged, including their
4/6 and 5/6 failed calibrations. This was a separately identified prompt-version experiment, not an unchanged
retry-until-green, label adjustment or verdict rewrite.

Next, qualify the corrected general interaction case on merged, reviewed code with this calibrated judge.
Full PDF/browser evidence and cleanup, adaptive qualification, provisioned presets and fingerprints, compatible
comparison, available target all-call usage and all 14 live cases remain mandatory in the active E0-E5 goal.
