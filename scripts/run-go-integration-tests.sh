#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT

companion_binary="$test_root/mintclaw-node"
coding_binary="$test_root/mintclaw"
companion_tests='^('\
'TestCompanionProcessAuthenticatesAndInvokesOverWSS|'\
'TestCompanionProcessTransfersFilesOverAuthenticatedWSS|'\
'TestNodeInvocationVerticalSliceWithApprovalAndRealCompanion|'\
'TestCompanionBrowserLifecycleAndReconnectOverProductionWSS|'\
'TestNodeFileTransferVerticalSliceWithApprovalAndDelivery|'\
'TestNodeJobVerticalSliceWithRestartArtifactAndCancellation|'\
'TestNodeServiceStatusModelToSystemdRealProcessVerticalSlice|'\
'TestNativeMintClawWorkerStartsSteersResumesAndShutsDown|'\
'TestNativeMintClawWorkerContextPrefixCorpus|'\
'TestNativeMintClawWorkerProjectsAndAnswersDurableQuestion|'\
'TestNativeMintClawWorkerCrashReleasesLeaseWithoutBlindReplay|'\
'TestNativeMintClawWorkerDisconnectAndHardCancelAreExplicit'\
'|TestNativeMintClawWorkerMutatesOwnedWorktreeAndRecoversAfterCrash|'\
'TestRemoteCodingTaskTelegramToNativeCompanionVerticalSlice'\
')$'

document_tests='^('\
'TestDocumentPDFTelegramVerticalSlice|'\
'TestGatewayDocumentIntakeUsesProductionCompositionAndOrdinaryTransport|'\
'TestCodingDocumentReadOnlyVerticalSlice|'\
'TestCodingDocumentLocalWriteToolLinuxIntegration'\
')$'
if (( $# > 1 )); then
  echo 'usage: run-go-integration-tests.sh [all|companion|documents]' >&2
  exit 2
fi
case "${1:-all}" in
  all) integration_tests="${companion_tests}|${document_tests}" ;;
  companion) integration_tests="$companion_tests" ;;
  documents) integration_tests="$document_tests" ;;
  *) echo 'usage: run-go-integration-tests.sh [all|companion|documents]' >&2; exit 2 ;;
esac

cd "$repository_root"
go build -o "$companion_binary" ./cmd/mintclaw-node
go build -tags goolm,stdjson -o "$coding_binary" ./cmd/mintclaw

MINTCLAW_NODE_TEST_BINARY="$companion_binary" \
MINTCLAW_CODING_WORKER_TEST_BINARY="$coding_binary" \
MINTCLAW_REQUIRE_CODING_WORKER_E2E=1 \
MINTCLAW_REQUIRE_DOCUMENT_AGENT_E2E=1 go test \
  -count=1 \
  -tags goolm,stdjson,integration \
  -run "$integration_tests" \
  ./cmd/mintclaw-node \
  ./pkg/agent \
  ./pkg/coding/workerprocess \
  ./pkg/gateway
