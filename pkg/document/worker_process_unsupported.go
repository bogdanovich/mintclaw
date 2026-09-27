//go:build !((linux && amd64) || (darwin && (amd64 || arm64)) || (windows && amd64))

package document

import (
	"context"
	"time"
)

type processWorker struct{}

func newProcessWorker(_ time.Duration) *processWorker {
	return &processWorker{}
}

func processWorkerAvailable() bool { return false }

func (w *processWorker) run(
	_ context.Context,
	_ string,
	_ string,
	request WorkerRequest,
) WorkerResult {
	return workerFailure(
		request.OperationID,
		StateUnavailable,
		FailureUnsupportedPlatform,
		"document worker is unavailable on this platform",
	)
}
