//go:build (linux && amd64) || (darwin && (amd64 || arm64)) || (windows && amd64)

package document

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	workerWaitDelay             = 500 * time.Millisecond
	documentWorkerGoMemoryLimit = "384MiB"
)

type processWorker struct {
	executable string
	args       []string
	timeout    time.Duration
	maxOutput  int
}

func newProcessWorker(timeout time.Duration) *processWorker {
	return &processWorker{
		args:      []string{"document", "_worker"},
		timeout:   timeout,
		maxOutput: defaultWorkerOutputSize,
	}
}

func processWorkerAvailable() bool { return true }

func (w *processWorker) run(
	ctx context.Context,
	snapshotPath string,
	workerScratch string,
	request WorkerRequest,
) WorkerResult {
	if err := ctx.Err(); err != nil {
		return workerFailure(request.OperationID, StateCanceled, FailureCanceled, "document worker was canceled")
	}

	executable := w.executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return workerFailure(
				request.OperationID,
				StateUnavailable,
				FailureWorkerUnavailable,
				"document worker executable is unavailable",
			)
		}
	}
	requestBytes, err := jsonMarshalWorkerRequest(request)
	if err != nil {
		return workerFailure(
			request.OperationID,
			StateFailed,
			FailureWorkerProtocol,
			"document worker request is invalid",
		)
	}
	snapshot, err := os.Open(snapshotPath)
	if err != nil {
		return workerFailure(request.OperationID, StateFailed, FailureInternal, "immutable snapshot is unavailable")
	}
	defer func() { _ = snapshot.Close() }()

	timeout := w.timeout
	if timeout <= 0 {
		timeout = defaultWorkerTimeout
		switch request.Operation {
		case workerOperationExtract, workerOperationRender:
			timeout = defaultReadWorkerTimeout
		case workerOperationFillCandidate:
			timeout = defaultFormWriteTimeout
		}
	}
	processCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	command := exec.CommandContext(processCtx, executable, w.args...)
	command.Dir = workerScratch
	command.Env = []string{
		"HOME=" + workerScratch,
		"TEMP=" + workerScratch,
		"TMP=" + workerScratch,
		"TMPDIR=" + workerScratch,
		"XDG_CONFIG_HOME=" + filepath.Join(workerScratch, workerBackendConfigDir),
		"GOMEMLIMIT=" + documentWorkerGoMemoryLimit,
		"LANG=C",
		"LC_ALL=C",
		"PATH=",
	}
	command.Stdin = bytes.NewReader(requestBytes)
	command.WaitDelay = workerWaitDelay
	configureDocumentWorkerProcess(command)
	releaseInput, inputErr := configureDocumentWorkerInput(command, snapshot)
	if inputErr != nil {
		return workerFailure(
			request.OperationID,
			StateUnavailable,
			FailureWorkerUnavailable,
			"document worker input transport is unavailable",
		)
	}
	defer releaseInput()
	processBoundary, boundaryErr := prepareDocumentWorkerProcess(
		processCtx,
		command,
		workerScratch,
		request.Operation,
	)
	if boundaryErr != nil {
		if ctx.Err() != nil {
			return workerFailure(
				request.OperationID,
				StateCanceled,
				FailureCanceled,
				"document worker was canceled",
			)
		}
		if errors.Is(processCtx.Err(), context.DeadlineExceeded) {
			return workerFailure(
				request.OperationID,
				StateFailed,
				FailureWorkerTimeout,
				"document worker exceeded its runtime limit",
			)
		}
		return workerFailure(
			request.OperationID,
			StateUnavailable,
			FailureBackendUnavailable,
			"document worker process boundary is unavailable",
		)
	}
	defer func() { _ = processBoundary.close() }()
	command.Cancel = processBoundary.terminate

	maximum := w.maxOutput
	if maximum <= 0 {
		maximum = defaultWorkerOutputSize
	}
	stdout := newBoundedWorkerBuffer(maximum)
	stderr := newBoundedWorkerBuffer(maximum)
	command.Stdout = stdout
	command.Stderr = stderr

	if startErr := command.Start(); startErr != nil {
		if ctx.Err() != nil {
			return workerFailure(request.OperationID, StateCanceled, FailureCanceled, "document worker was canceled")
		}
		if errors.Is(processCtx.Err(), context.DeadlineExceeded) {
			return workerFailure(
				request.OperationID,
				StateFailed,
				FailureWorkerTimeout,
				"document worker exceeded its runtime limit",
			)
		}
		return workerFailure(
			request.OperationID,
			StateUnavailable,
			FailureWorkerUnavailable,
			"document worker executable is unavailable",
		)
	}
	releaseInput()
	if startedErr := processBoundary.started(); startedErr != nil {
		_ = processBoundary.terminate()
		_ = command.Wait()
		if ctx.Err() != nil {
			return workerFailure(request.OperationID, StateCanceled, FailureCanceled, "document worker was canceled")
		}
		if errors.Is(processCtx.Err(), context.DeadlineExceeded) {
			return workerFailure(
				request.OperationID,
				StateFailed,
				FailureWorkerTimeout,
				"document worker exceeded its runtime limit",
			)
		}
		return workerFailure(
			request.OperationID,
			StateUnavailable,
			FailureWorkerUnavailable,
			"document worker process boundary is unavailable",
		)
	}
	waitErr := command.Wait()
	if terminateErr := processBoundary.terminate(); terminateErr != nil &&
		!errors.Is(terminateErr, os.ErrProcessDone) {
		return workerFailure(
			request.OperationID,
			StateFailed,
			FailureInternal,
			"document worker process cleanup failed",
		)
	}
	if stdout.exceeded || stderr.exceeded {
		return workerFailure(
			request.OperationID,
			StateFailed,
			FailureWorkerOutputLimit,
			"document worker exceeded its output limit",
		)
	}
	if ctx.Err() != nil {
		return workerFailure(request.OperationID, StateCanceled, FailureCanceled, "document worker was canceled")
	}
	if errors.Is(processCtx.Err(), context.DeadlineExceeded) {
		return workerFailure(
			request.OperationID,
			StateFailed,
			FailureWorkerTimeout,
			"document worker exceeded its runtime limit",
		)
	}
	if waitErr != nil {
		return workerFailure(
			request.OperationID,
			StateFailed,
			FailureWorkerCrashed,
			"document worker terminated unexpectedly",
		)
	}
	result, err := decodeWorkerResult(stdout.Bytes(), request)
	if err != nil {
		return workerFailure(
			request.OperationID,
			StateFailed,
			FailureWorkerProtocol,
			"document worker returned an invalid response",
		)
	}
	return result
}

func jsonMarshalWorkerRequest(request WorkerRequest) ([]byte, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if len(data) > maxWorkerRequestSize {
		return nil, errors.New("document worker request exceeds limit")
	}
	return append(data, '\n'), nil
}

type boundedWorkerBuffer struct {
	buffer   bytes.Buffer
	maximum  int
	exceeded bool
}

func newBoundedWorkerBuffer(maximum int) *boundedWorkerBuffer {
	return &boundedWorkerBuffer{maximum: maximum}
}

func (b *boundedWorkerBuffer) Write(data []byte) (int, error) {
	remaining := b.maximum - b.buffer.Len()
	if remaining <= 0 {
		b.exceeded = true
		return 0, errors.New("document worker output limit exceeded")
	}
	if len(data) <= remaining {
		return b.buffer.Write(data)
	}
	written, _ := b.buffer.Write(data[:remaining])
	b.exceeded = true
	return written, errors.New("document worker output limit exceeded")
}

func (b *boundedWorkerBuffer) Bytes() []byte {
	return b.buffer.Bytes()
}
