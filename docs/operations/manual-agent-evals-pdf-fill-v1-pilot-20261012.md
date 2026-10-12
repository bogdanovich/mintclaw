# Manual Agent Evals: Adaptive PDF Fill V1 Pilot

## Scope

One new adaptive `pdf.loan.fill` attempt used merged core `af0805034aa0940cfbf284bd627539c7b6009f83`,
build `eval-pdf-fill-v1-af080503`, build stamp `2026-10-12T01:54:00Z`, Linux amd64 and Go 1.26.9.
The runner, target and corpus came from that same archived commit. Offline case/preset validation passed, and
the native authenticated runtime admission completed before task dispatch. This run does not contain the
unmerged protected cancellation observer in [#1624](https://github.com/bogdanovich/mintclaw/pull/1624).

A fresh private profile allowed only `document` and `request_user_input`, configured native document audit,
and required approval. The target and separate tools-disabled user requested `gemini-3.5-flash-lite`;
the configured tools-disabled judge requested `gemini-3.6-flash`. Effective provider models and complete target
call usage remain unknown. The preset template retained ID `isolated-pdf-cancel-pilot`; the selected case was fill,
not cancel. No output confirmation or `allow_once` was sent.

Only an operator-authorized API credential was read from deployed main and placed behind native file references.
No production sessions, memory, workspace configuration or browser state were copied. Both processes used the
fresh profile as `HOME` and `MINTCLAW_HOME`, with an empty inherited environment except declared executable path,
locale and profile/oracle settings. Production services and configuration were not changed.

## Result

Report ID: `c0513354-f184-4dce-b3cb-a0b30e81949a`; complete report, overall **inconclusive**, CLI exit 1.
Total reported duration: **4,420 ms**; trial `b0b7387b-e7bc-4dda-b43a-0a43db70adc4` took **4,394 ms**.
Case SHA-256: `1e036844dc5047e71c0474f54a4cf629393221bf5cde2294ca53f27c455f4e5c`.
Runtime public-config SHA-256: `4c7b12460673e8d7329e01436bdefc25780e6fb74997e3e97427889b2badc220`.

Conversation execution is **inconclusive / domain_input_not_admitted**. The only retained task outcome is an
ordinary question whose content is `Sample question`, followed by native `/answer` and `/stop` controls.
The corresponding registry record names `request_user_input`, has no protected binding, and remains waiting at
revision 3. No protected document job, review, approval, delivery or artifact was established in eval evidence.
The runner refused the subsequent user-driver action; no second task input was sent or replayed.

One tools-disabled LLM-user call returned **679 input / 7 output / 686 total tokens**. No judge call occurred;
evidence is ineligible. The target's all-call usage is unknown, not inferred from the user-role call or final reply.
All four required checks (`private_surfaces`, `pdf_fields`, `pdf_structure`, `pdf_delivery`) remain
**inconclusive / missing_evidence**. The configured independent artifact oracle had no artifact to inspect.

This is evidence of a real adaptive attempt and a refused boundary, not successful PDF inspection/filling or
domain qualification. The placeholder is not present in the inspected production request-user-input source;
the exact reason the target produced it is not established. The original report does not retain the validated
driver action enum, so its precise choice cannot be recovered or inferred from output-token count. A value-free
decision-kind receipt is a focused harness diagnostics follow-up; it must not expose raw model output or facts,
weaken admission, or retroactively populate this historical report.

## Retention And Limits

Raw conversation, native state, credentials and fixtures remain outside Git in the fresh private profile.
The exact executable path of the operator-owned gateway PID was checked before SIGTERM. Its command exited
successfully and `/proc` confirmed absence. This does not mark the waiting question canceled or certify complete
native settlement/privacy/owned cleanup. The original report still retains `remote_state: unknown` and the owned
source fixture. No retry-until-green or rewriting of the failed trial was performed.

Both scripted general scenarios previously passed; this first adaptive fill attempt does not change that evidence.
It does not qualify protected intake, macOS live behavior, browser/companion, Telegram or the full suite. Native
idle, comprehensive private-surface/cleanup witnesses, six PDF and six browser case qualification, provisioning/
input fingerprints, available target usage and the E0-E5 exit report remain required by the active goal.
