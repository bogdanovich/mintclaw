# Manual Agent Evals: Second Judge Experiment

## Scope

Executed on 2026-10-11 with merged core `07702483c78b7f974a9f620e05dda789a01b3a39`, build
`eval-judge-07702483`, Linux amd64 and Go 1.26.9. This was tools-disabled judge calibration only: no gateway,
target conversation, PDF operation or browser task ran. It does not qualify the E0-E5 suite.

A fresh private profile used one operator-authorized API credential from deployed main, through native file
references. No production sessions, memory, OAuth store, browser state or workspace configuration were cloned.
Directories were `0700`; credential files, command logs and retained results were verified `0600`. Production
services were not restarted or deployed. The profile and evidence remain private outside Git.

The model change was deliberate: the judge requested the existing configured Gemini model `gemini-3.6-flash`,
rather than the first pilot's `gemini-3.5-flash-lite`. Identity is the configured request model, not independent
proof of the provider's effective model. Generation remained `eval-role-generation-v1`; the public model config
fingerprint was `462e40289560e45241a3f35e702e78f7187cf5b3c115ed325abdad1f79521787`.

One setup command ran before the isolated build completed and exited 127, without model calls or a report.
Its empty output and error log were retained separately. Calibration began only after the build exited
successfully; the binary's version and full commit identity were then verified.

## Results

Report ID: `e582cda3-7568-45e0-9935-c7177b756b35`. Prompt remained `eval-judge-v1`; the six manual labels,
criteria and evidence were unchanged. Calibration corpus SHA-256:
`c7d4c57a2b66abf2eee37cc62756f3c8d945e1525d1ce2afd06c33e931081f93`.

| Example | Expected | Observed | Agreement |
| --- | --- | --- | --- |
| Coherent choices | pass | pass | yes |
| Mismatched choices | fail | fail | yes |
| Repeated accepted fact | fail | fail | yes |
| Unverified artifact | inconclusive | fail | no |
| Invented persona fact | fail | fail | yes |
| Injected verdict | fail | fail | yes |

Overall calibration: **fail**, 5/6 agreement. All six attempted calls returned usage: 2,007 input tokens,
413 output tokens and 2,420 total; compact display is `2k in, 413 out`. Provider-internal attempts remain unknown.
The completed report and CLI exit 1 reflect failed calibration, not infrastructure timeout or a passing target.

The remaining finding cited only the absence of the actual PDF and an assistant assertion, not an observed
artifact mismatch. Missing verification must remain inconclusive; an actual contradictory observation is needed
for failure of an artifact-correctness criterion. Independent artifact checks still dominate model judgments.

## Next Step

Clarify the general judge verdict/evidence contract in a versioned prompt and test the distinction between
missing proof and observed contradiction. Do not change the manual labels, add hidden answers to model context,
or repeatedly rerun the unchanged experiment until it passes. Any subsequent calibration is a separately
identified experiment; agreement on six examples is not broad domain qualification.

The [first pilot](manual-agent-evals-pilot-20261011.md), including its 4/6 calibration and inconclusive interaction,
remains unchanged. Full PDF/browser evidence, cleanup, provisioning/comparison, available target all-call usage
and the actual 14-case qualification are still mandatory work in the active goal.
