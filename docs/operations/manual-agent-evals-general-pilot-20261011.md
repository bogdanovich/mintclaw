# Manual Agent Evals: General V2 Pilot

## Scope

One serial two-case run on 2026-10-11 used merged core `4f60d2eb0089266228e28008f8d59b4b1bf32ad6`, build
`eval-general-v2-4f60d2eb`, Linux amd64 and Go 1.26.9. Build identity was verified before starting the fresh
isolated gateway. No production service, binary or configuration was modified.

The profile copied only the operator-authorized API credential through native file references, not production
sessions, memory, OAuth/browser state or workspace configuration. The target requested `gemini-3.5-flash-lite`;
the tools-disabled judge requested `gemini-3.6-flash` with `eval-judge-v2` and `eval-role-generation-v1`.
The same judge configuration had [6/6 calibration agreement](manual-agent-evals-judge-v2-calibration-20261011.md).
Configured request identity does not prove the provider's effective model, internal attempts or billing.

Both scenarios were scripted, using a fresh session per trial. There were no LLM-user calls or repetitions.
Only ordinary question input was enabled; PDF/browser/exec/MCP and external channels were disabled.
Credentials, command logs, transcripts and result artifacts stay in private directories outside Git.

## Results

Report ID: `ab2900e6-3ad5-494d-acf8-cb38b5e76dc2`; complete report, overall **fail**, CLI exit 1.

| Case | Retained Verdict | Duration | Evidence |
| --- | --- | --- | --- |
| `general.context.followup` | pass | 3,026 ms | Marker retained across two turns in the same native session; all hard checks and judge passed. |
| `general.interaction.navigation` | fail | 2,750 ms | Native question observed; execution remained inconclusive with `domain_evidence_unavailable` before cancel dispatch. |

Context case SHA-256: `4703ef12118060ecbb7c4edfb2ee19cf4950f91e13b52ac29e01851c8797b69d`.
Interaction case SHA-256: `d5e6ab4c8732f27ef726ea3407e181d595b05ca8ac035236a35c2dfd2f7332e4`.
Runtime public-config SHA-256: `e11f30206ff57707c113aceab1b8c79debb5179f31eeaa41417843d57499176a`.

The context judge returned one usage record: 585 input, 90 output, 675 total tokens, displayed `585 in, 90 out`.
User calls were zero; target all-call usage and provider-internal attempts remain unknown.

The interaction's hard cancellation predicate failed because the last observed event was still a question.
Its execution verdict was inconclusive and its judge was ineligible. This retained failure is a harness failure,
not evidence that the target rejected an admitted cancel: no cancel was sent. The question was coherent, with
Email/Paper choices, and its owner-bound native prompt receipt was actually delivered.

## Diagnosis And Limits

Native gateway startup opens its instance-wide outbox with `cfg.WorkspacePath()`. The observer instead searched
`MINTCLAW_HOME/state/outbox`; no such directory existed. The delivered prompt and matching platform message ID
were in the configured workspace's `state/outbox`. The same incorrect root was used by the PDF outbox observer
and its synthetic fixtures. Document job/key/write-journal stores legitimately remain home-scoped; those roots
are not interchangeable and must not be changed with the outbox.

The forward fix reads only the admitted native workspace outbox, without a home-directory fallback, and moves
test receipts to the same native location. Negative controls refuse an otherwise matching receipt moved to the
non-native home directory. This report is preserved unchanged; a subsequent merged-code run must get a new ID.

After report completion, the gateway's `/proc` executable path was verified to belong to this profile, that PID
received SIGTERM, and its owned command completed successfully. The private workspace and waiting-question evidence
were preserved. Local process shutdown is not proof of question cancellation, whole-runtime privacy, domain
cleanup or remote quiescence. No uncertain target mutation was replayed.

This is not a passing full suite or adaptive-user qualification. The interaction must be exercised again after
the harness fix is reviewed and merged; PDF/browser evidence, fingerprints, available target all-call usage,
owned cleanup and all 14 cases remain required by the active E0-E5 goal.
