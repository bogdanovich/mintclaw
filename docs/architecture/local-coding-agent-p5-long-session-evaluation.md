# Local Coding Agent P5.6: Long-Session Evaluation

Status: implemented

This document records the deterministic evidence for the P5 compaction
evaluation contract. The gate is intentionally a suite of focused fixtures:
one canonical long-session harness proves the end-to-end continuity boundary,
while smaller tests isolate repository observation, failure handling, and TUI
projection without turning the gate into one timing-sensitive mega-test.

## Deterministic scenario

`TestCodingLongSessionCompactionContinuity` builds a 32-turn, 64-message coding
history in the canonical per-session JSONL store. Historical turns include
bounded pasted-log pressure, while the marker-free protected tail carries a
media reference. A deterministic Seahorse completion fixture validates that
each source prompt contains the required facts before returning the same coding
continuation record, including:

- the active parser-migration objective and done criteria;
- `parser.go`, `parser_test.go`, and `docs/parser.md` as changed paths;
- a passing unit-test result, a failing Windows-path result, and integration
  tests explicitly marked as not run;
- the unresolved `normalizeWindowsPath` failure and next action;
- the `--yolo` and no-backward-compatibility constraints;
- a rejected regex-only approach; and
- artifact and media references.

The harness forces leaf summaries and at least two condensation depths, then
appends one paired `apply_patch` call/result. It verifies the assembled token
envelope, exact provider-safe tool pairing, and thread isolation. It closes and
reopens Seahorse, then deletes the database (including WAL/SHM files), rebuilds
it from canonical JSONL, compacts again, and proves that the canonical
`apply-once` side effect still occurs exactly once.

Repository freshness is covered by a synthetic git fixture in
`TestCodingPromptReanchorsCompactedSummaryToFreshWorkspace`: an external branch
and file mutation supersede the deliberately stale branch claim retained by
the summary. `TestCodingProviderRetryRefreshesWorkspaceSnapshot` proves the
same precedence between provider attempts, and
`TestCodingWorkspaceSnapshotRefreshesPromptAndEmitsFrontendObservation` covers
tool-driven mutation and its frontend observation.

## Contract matrix

| Contract clause | Deterministic evidence |
| --- | --- |
| Objective, done criteria, changed paths, validation truth, unresolved failure, constraints, rejected approach | `TestCodingLongSessionCompactionContinuity` asserts every marker after initial compaction, reopen, and derived-state rebuild. |
| Live git/filesystem state supersedes stale summary text | `TestCodingPromptReanchorsCompactedSummaryToFreshWorkspace` and `TestCodingProviderRetryRefreshesWorkspaceSnapshot`. |
| Provider-safe tool-call/result pairing | `TestCodingLongSessionCompactionContinuity` requires exactly one call and one result in assembled context; `TestAssemblerProjectsRetentionWithoutBreakingToolPairing` covers retention projection. |
| Strict prompt bounds | The long-session harness asserts selected history and summary tokens against their independent absolute budgets; `TestCompactUntilAbsoluteBudgetsPreservesConfiguredRecentTurns` covers the lower-level boundary. |
| No cross-thread/project leakage | The long-session harness writes a secret to a second thread and rejects it from primary assembly; coding runtime layout tests independently cover project-scoped storage. |
| Rebuild after deleting derived state | The long-session harness removes the Seahorse DB/WAL/SHM and reconciles from JSONL; `TestNewCodingAgentLoopRebuildsMissingOrCorruptSeahorseFromCanonicalHistory` covers runtime construction. |
| No duplicate side effect on resume | The long-session harness counts the canonical `apply-once` call after rebuild; `TestNativeCodingCommandEditsAndResumesAcrossProcessBoundary` covers a real command/process boundary. |
| Compactor timeout | `TestCodingCompactorTimeoutProducesOneTerminalInterruption` requires one completion attempt and one terminal interrupted lifecycle. |
| Empty compactor output | `TestGenerateLeafSummaryEscalationToTruncation` and `TestGenerateCondensedSummaryEscalation` prove deterministic non-empty fallbacks. |
| No-progress output | `TestCompactUntilAbsoluteBudgetsFailsWhenProtectedTailExceedsCap` terminates with a bounded no-progress error; `TestSeahorseCompactLifecyclePairsNoopAndFailure` projects no-progress as one terminal lifecycle. |
| Large tool output, media, and pasted logs remain bounded | The long-session harness supplies media and pasted-log pressure; `TestCodingToolResultProjectionBoundsSuccessfulOutputAndPreservesEvidence` and `TestCodingToolResultProjectionRendersMultipartArtifactEvidence` isolate tool-result elision. |
| Lifecycle is visible and usable in the TUI | `TestCompactionSurfacesDistinguishModeAndReportMetrics`, `TestComposerRemainsUsableDuringBackgroundCompaction`, and the frontend adapter/projector compaction tests. |
| At least two condensation levels and restart reconciliation | The long-session harness requires summary depth `>= 2`, reopens the same derived store, then separately rebuilds and recompacts from canonical history. |

## Baselines

The scenario logs observations for regression comparison but does not assert on
wall-clock duration. A representative local run on 2026-08-27 recorded:

- 66 canonical messages;
- summary depth 2;
- first compaction from 107,112 to 6,976 tokens;
- rebuild compaction from 107,409 to 7,273 tokens; and
- 25 ms reported compaction duration, with roughly 8 seconds for the complete
  test including store construction and two rebuild paths.

Token counts and structural invariants are asserted. Durations are diagnostic
only because scheduler load, filesystem speed, race instrumentation, and CI
virtualization are not product correctness signals.
