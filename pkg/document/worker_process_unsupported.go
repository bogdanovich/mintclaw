//go:build !linux || !amd64

package document

import (
	"context"
	"time"
)

type processWorker struct{}

func newProcessWorker(_ time.Duration) *processWorker {
	return &processWorker{}
}

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
		"document worker is initially admitted only on linux/amd64",
	)
}
