#!/usr/bin/env bash
set -euo pipefail

case "${1:---all}" in
  --all) context_suite_integration=true ;;
  --unit-only) context_suite_integration=false ;;
  *) echo 'usage: test-context-cache-contracts.sh [--all|--unit-only]' >&2; exit 2 ;;
esac

cd "$(git rev-parse --show-toplevel)"

# Reject an accidentally renamed/missing test instead of accepting "no tests
# to run". Each named lifecycle gate must exist in the selected package.
run_context_contracts() {
  local package="$1"
  local tags="$2"
  shift 2
  local name available pattern='^('
  available="$(go test -tags "$tags" -list . "$package")"
  for name in "$@"; do
    if ! [[ $'\n'"$available"$'\n' == *$'\n'"$name"$'\n'* ]]; then
      echo "missing context contract: $package/$name" >&2
      return 1
    fi
    pattern+="$name|"
  done
  pattern="${pattern%|})$"
  echo "context contracts: $package"
  go test -count=1 -timeout=3m -tags "$tags" -run "$pattern" "$package"
}

run_context_contracts ./pkg/testharness/llmscenario goolm,stdjson \
  TestPrefixOracleRejectsHistoricalAndCompatibilityMutations \
  TestPrefixOracleRejectsBrokenToolEvidenceAndSidecars \
  TestPrefixOracleCanonicalizesOnlyObjectKeysAndDetachesCapture \
  TestPrefixOracleInstructionBoundaryStillRejectsHistoricalRewrite \
  TestPrefixOracleAllowsCompletedCallIDReuseAcrossTurns \
  TestPrefixCorpusRequiresReadFileToolSchema \
  TestScriptedProviderCapturesAreDetachedFromRuntimeAndReaders

run_context_contracts ./pkg/agent goolm,stdjson \
  TestGatewayContextPrefixCorpusAcrossRestart \
  TestSteeringEnvelopeReplaysSameProviderProjectionWithoutChangingDisplay \
  TestPromptCacheMediaBoundaryUsesAdmittedSnapshotAcrossRetries \
  TestPromptCacheMediaBoundaryTracksGenericOneShotContextWithoutRetainingIt \
  TestLiveToolContextReusedIDKeepsCacheIntentAndHistoricalResults \
  TestLiveToolContextMissingOccurrenceDoesNotBorrowHistoricalID \
  TestLiveToolContextIdentitySurvivesRebuildButNotProviderOrDurableProjection \
  TestLiveToolContextAmbiguousIdentityDisablesCacheIntent \
  TestLiveToolContextReusedIDsAcrossRealToolIterations \
  TestFallbackAttemptUsesActualProviderAndModelLineage \
  TestCodingPromptKeepsFrozenContextAcrossCrossProviderFallback \
  TestCodingProviderRetryKeepsWorkspaceFrozenUntilNextRootTurn \
  TestPromptCacheLineageRotatesForWorkspaceInstructionChanges \
  TestPromptCacheLineageChangesForEveryRoutingDimension \
  TestContextBuilderOrdersCheckpointBeforeRetainedHistory \
  TestPromptCacheScopeUsesCheckpointGeneration \
  TestContextCompactionWatermarksUseEffectiveWindowAndHysteresis \
  TestPostDeliveryCompactionRunsOnlyUnderPressure \
  TestSeahorseCompactLifecyclePairsNoopAndFailure \
  TestCodingLongSessionCompactionContinuity \
  TestSeahorseAdapterFailsClosedWhenMandatoryPromptCannotFit \
  TestSeahorseAdapterReportsAbsoluteBudgetPressureBelowContextWindow \
  TestSetupTurnPersistsAndReusesFrozenTurnEnvelope \
  TestSeahorseRealLoopNoDuplicateMessages

run_context_contracts ./cmd/mintclaw/internal/coding goolm,stdjson \
  TestNativeCodingContextPrefixCorpus \
  TestNativeCodingAttachmentsRemainLazySelectableAndDiagnosableAcrossRestart \
  TestNativeCodingCaptionedUnsupportedImagesStayOutOfProviderVision \
  TestNativeCodingCommandEditsAndResumesAcrossProcessBoundary \
  TestNativeControllerTranscriptPageHydratesOnlySafeDisplayContent

if [[ "$context_suite_integration" == false ]]; then
  echo 'context contracts: unit gates passed; subprocess/WSS gates not requested'
  exit 0
fi

readonly context_suite_root="$(mktemp -d /tmp/mintclaw-context-suite.XXXXXX)"
[[ "$context_suite_root" == /tmp/mintclaw-context-suite.* && -d "$context_suite_root" ]]
trap 'rm -rf -- "$context_suite_root"' EXIT
go build -tags goolm,stdjson -o "$context_suite_root/mintclaw" ./cmd/mintclaw
go build -o "$context_suite_root/mintclaw-node" ./cmd/mintclaw-node
export MINTCLAW_CODING_WORKER_TEST_BINARY="$context_suite_root/mintclaw"
export MINTCLAW_NODE_TEST_BINARY="$context_suite_root/mintclaw-node"
export MINTCLAW_REQUIRE_CODING_WORKER_E2E=1

run_context_contracts ./pkg/coding/workerprocess goolm,stdjson,integration \
  TestNativeMintClawWorkerContextPrefixCorpus \
  TestNativeMintClawWorkerStartsSteersResumesAndShutsDown \
  TestNativeMintClawWorkerProjectsAndAnswersDurableQuestion \
  TestNativeMintClawWorkerCrashReleasesLeaseWithoutBlindReplay

run_context_contracts ./pkg/gateway goolm,stdjson,integration \
  TestRemoteCodingTaskTelegramToNativeCompanionVerticalSlice

echo 'context contracts: all deterministic lifecycle gates passed'
