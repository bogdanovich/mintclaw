# Local Coding Agent P6.4 Exit Record

Roadmap packet: [P6.4 — Git review and change summaries](local-coding-agent-roadmap.md#p64--git-review-and-change-summaries).

The merge containing this record closes P6.4. Coding threads now share one
passive, bounded repository-evidence service for model tools, headless commands,
the TUI, and native read-only review. Repository observations remain distinct
from claims about which actor caused a change.

## Implementation sequence

The merged implementation is split into independently reviewed checkpoints:

- [#1004](https://github.com/bogdanovich/mintclaw/pull/1004), merge
  `bc15555b`, added the passive repository-evidence service, typed status and
  diff results, explicit targets, independent bounds, safe untracked-file
  reads, cancellation, and no-mutation coverage;
- [#1013](https://github.com/bogdanovich/mintclaw/pull/1013), merge
  `34cbc614`, added immutable per-thread repository baselines and conservative
  provenance comparison under the selected thread writer lease;
- [#1028](https://github.com/bogdanovich/mintclaw/pull/1028), merge
  `d5d3e63e`, removed resume-time baseline synthesis and the unused baseline
  diff target, making a stopped-state deployment cutover the only migration
  path for retained pre-contract threads;
- [#1032](https://github.com/bogdanovich/mintclaw/pull/1032), merge
  `5aaa4cdf`, exposed schema-versioned `repository_status` and
  `repository_diff` model tools with baseline-aware provenance;
- [#1041](https://github.com/bogdanovich/mintclaw/pull/1041), merge
  `c80fdf34`, integrated status and diff with the native runtime, controller,
  and frontend state without creating another transcript writer;
- [#1046](https://github.com/bogdanovich/mintclaw/pull/1046), merge
  `414fd13d`, added fresh `/status` and `/diff` TUI actions plus shared bounded,
  terminal-safe renderers;
- [#1055](https://github.com/bogdanovich/mintclaw/pull/1055), merge
  `24ba2da`, defined bounded review targets and findings, proved locations
  against frozen evidence, and published immutable per-review results;
- [#1064](https://github.com/bogdanovich/mintclaw/pull/1064), merge
  `a51afaa`, serialized review admission, progress, findings, completion,
  interruption, cancellation, and close behavior through the controller;
- [#1075](https://github.com/bogdanovich/mintclaw/pull/1075), merge
  `8101084e`, added the native reviewer over frozen evidence with a
  project-confined read/search toolset and linearized durable publication;
- [#1077](https://github.com/bogdanovich/mintclaw/pull/1077), merge
  `5f427ac8`, added `mintclaw review`, deterministic plain and versioned JSON
  output, `/review`, live progress, prioritized findings, paging, and
  interruption; and
- [#1078](https://github.com/bogdanovich/mintclaw/pull/1078), merge
  `6a0a0c09`, added the atomic latest-result pointer, display-only restore,
  current-evidence revalidation, and restart, fork, compaction, race, resource,
  and platform closeout.

The resulting behavior is specified by the
[P6.4 review contract](local-coding-agent-p6-4-review.md).

## Requirement evidence

### Passive and bounded repository evidence

- All coding callers use the same fixed-operation repository boundary. Git
  configuration that could invoke helpers, filters, external diffs, lazy
  network fetches, hooks, fsmonitor, or submodule worktree recursion is
  neutralized or produces an explicit unavailable result.
- Status distinguishes clean, dirty, unavailable, truncated, stale, and
  indeterminate evidence. Diff supports the current worktree, a verified local
  base through its merge base, or one locally available commit.
- Independent time, concurrency, path, file, hunk, line, line-byte, stderr,
  stdout, and rendered-byte limits prevent repository size or hostile output
  from becoming unbounded prompt or terminal input.
- Untracked content is read through pinned project authority. Symlinks and
  non-regular files are not followed, changing files are omitted explicitly,
  control characters are escaped, and cancellation terminates the Git process
  tree.

Representative coverage includes
`TestRepositoryDiffCurrentReturnsStructuredTrackedAndUntrackedChanges`,
`TestRepositoryDiffAppliesIndependentBounds`,
`TestRepositoryDiffDisablesConfiguredGitHelpers`,
`TestRepositoryDiffDisablesLazyFetchForMissingPromisorObject`, and
`TestRepositoryConcurrencyWaitHonorsCancellation`.

### Baseline ownership and truthful provenance

- A new thread publishes its immutable, versioned baseline outside the project
  before the first model turn. Resume requires that baseline under the selected
  thread lease; it never invents historical evidence at runtime.
- A fork captures a fresh child baseline rather than copying the parent's
  authority. Compaction may refer to baseline metadata but cannot replace the
  authoritative file.
- Comparison reports `pre_existing`, `first_observed_during_thread`,
  `resolved_since_baseline`, or `indeterminate`. It never translates temporal
  observation into a claim that MintClaw authored an edit.
- Changed repository authority, HEAD lineage, index identity, bounded-out
  evidence, and concurrent file changes fail closed to stale or indeterminate
  provenance.

Representative coverage includes
`TestRepositoryBaselineCapturesBoundedFingerprintsWithoutContents`,
`TestCompareBaselineClassifiesTruthfulPathTransitions`,
`TestCompareBaselineRejectsChangedAuthority`,
`TestRepositoryBaselineBoundsAggregateEncodedPaths`, and
`TestResumeRejectsThreadWithoutRepositoryBaseline`.

### Shared model, headless, and TUI surfaces

- Native coding turns expose typed `repository_status` and `repository_diff`
  tools instead of asking the model to assemble Git commands.
- `/status` and `/diff current|base <ref>|commit <ref>` request fresh evidence
  through the controller and render branch state, provenance, files, bounded
  hunks, line numbers, omissions, staleness, and truncation diagnostics.
- Plain rendering is deterministic and terminal-safe. Structured output is
  versioned. Frontend refresh and invalidation use cloned typed state rather
  than reinterpreting model-authored prose.
- Canonical JSONL and Seahorse remain the conversation authorities; repository
  evidence and baselines remain supporting thread state.

Representative coverage includes
`TestRenderStatusPlainIncludesProvenanceAndEscapesControls`,
`TestRenderDiffPlainIncludesBoundedHunksAndDiagnostics`,
`TestRepositoryEvidenceUpdatesDoNotAliasNestedState`, and
`TestWorkspaceAdvanceInvalidatesMutableRepositoryEvidence`.

### Native review lifecycle and findings

- Review is a controller-owned read-only operation over one frozen evidence
  generation. It supports current changes, a verified local base, a local
  commit, and bounded custom instructions attached to one of those scopes.
- The reviewer receives project-confined list, read, search, and repository
  evidence capabilities. It receives no mutation, network, GitHub,
  delegation, delivery, or canonical-transcript writer authority.
- Findings carry severity, confidence, bounded explanation, and a current,
  stale, or unlocated location. A current location must overlap a reviewed
  changed line; the runtime never guesses after evidence changes.
- The controller serializes review against turns, compaction, refresh, another
  review, cancellation, and shutdown. Only the owning runtime can publish the
  validated immutable result.

Representative coverage includes
`TestNativeControllerPublishesReadOnlyReviewResult`,
`TestNativeReviewReconcilesEvidenceImmediatelyBeforePublication`,
`TestNativeReviewerToolsetRestrictsReadsToProject`,
`TestReviewProjectionCorrelatesEventsAndCompletedResult`, and
`TestStorePublishesImmutableReviewResultUnderThreadLease`.

### Durable restore, fork, compaction, and resource safety

- Completed reviews publish immutable result files and then atomically advance
  a bounded `repository/reviews/latest.json` pointer. Resume projects the
  latest result without calling the model or recreating task authority.
- Restore revalidates mutable evidence. Changed repositories keep the result
  visible but downgrade unsupported locations to stale and remove unproven
  lines.
- Interrupted reviews are neither published nor rerun. Forks may retain
  visible canonical conversation context but never inherit the parent's live
  review authority. Compaction metadata does not alter the latest pointer.
- Pinned readers reject oversized, malformed, ambiguous, linked, replaced, and
  non-regular review state. Unix FIFO inputs do not block; Darwin and Windows
  compilation cover platform-specific boundaries.

Representative coverage includes
`TestStoreLatestReviewPointerAdvancesAndSurvivesCompactionMetadata`,
`TestInterruptedNativeReviewDoesNotRerunAfterRestart`,
`TestReviewRestoredProjectsCompletedStateWithoutLiveAuthority`,
`TestLatestReviewRestoreRejectsFIFOWithoutBlocking`, and
`TestNativeReviewerFilesystemToolsDoNotBlockOnFIFO`.

## Core and skill boundary

P6.4 deliberately keeps deterministic local evidence and review in core.
Project-specific checklists, reviewer personas, GitHub publication, CI
monitoring, branch management, rebase, merge, and deployment remain skill
policy. Removing all skills therefore leaves `status`, `diff`, and local
`review` usable without leaving a second repository scanner or transcript
writer behind.

Core performs no implicit fetch, pull, push, commit, checkout, reset, restore,
clean, stash, rebase, merge, or worktree cleanup. Review is observational and
cannot discard user changes.

## Validation and review gates

Every implementation PR above is merged. Each final implementation head passed
the repository's required CI matrix, including tests, race coverage, lint,
security, integration, browser validation, and supported-platform compilation.
Each merged exact head also received a reviewer result tied to that SHA and an
owner PR-level rocket approval; implementing agents addressed and resolved
substantive findings before merge.

Focused validation exercised repository mutation races, hostile Git
configuration, output and file bounds, cancellation, immutable baseline and
review publication, restart without duplicate execution, interrupted review,
fork isolation, compaction coexistence, FIFO handling, and Darwin/Windows
build boundaries.

## Exit-gate decision

The P6.4 roadmap statement is satisfied:

- the agent and UI label pre-existing and indeterminate evidence without
  claiming ownership;
- current review findings link only to proven current paths and changed line
  positions, otherwise becoming stale or unlocated;
- large statuses, diffs, reviewer inputs, results, and terminal views remain
  bounded and navigable; and
- no repository observation or review action implicitly resets, discards, or
  otherwise mutates project or Git state.

P6.4 does not add LSP-backed code intelligence, workspace checkpoints or
rewind, queued live-agent delegation, companion execution, autonomous GitHub
publication, or remote coding orchestration. Those remain owned by P6.5 and
later roadmap packets.
