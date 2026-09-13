#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT

companion_binary="$test_root/mintclaw-node"
coding_binary="$test_root/mintclaw"
integration_tests='^('\
'TestCompanionProcessAuthenticatesAndInvokesOverWSS|'\
'TestCompanionProcessTransfersFilesOverAuthenticatedWSS|'\
'TestNodeInvocationVerticalSliceWithApprovalAndRealCompanion|'\
'TestCompanionBrowserLifecycleAndReconnectOverProductionWSS|'\
'TestNodeFileTransferVerticalSliceWithApprovalAndDelivery|'\
'TestNodeJobVerticalSliceWithRestartArtifactAndCancellation|'\
'TestNodeServiceStatusModelToSystemdRealProcessVerticalSlice|'\
'TestDocumentPDFTelegramVerticalSlice|'\
'TestNativeMintClawWorkerStartsSteersResumesAndShutsDown|'\
'TestNativeMintClawWorkerCrashReleasesLeaseWithoutBlindReplay|'\
'TestNativeMintClawWorkerDisconnectAndHardCancelAreExplicit'\
'|TestNativeMintClawWorkerMutatesOwnedWorktreeAndRecoversAfterCrash'\
')$'

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
