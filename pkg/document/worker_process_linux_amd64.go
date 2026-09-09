//go:build linux && amd64

package document

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const workerWaitDelay = 500 * time.Millisecond

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
		if request.Operation == workerOperationExtract || request.Operation == workerOperationRender {
			timeout = defaultReadWorkerTimeout
		}
	}
	processCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	command := exec.CommandContext(processCtx, executable, w.args...)
	command.Dir = workerScratch
	command.Env = []string{
		"HOME=" + workerScratch,
		"TMPDIR=" + workerScratch,
		"LANG=C",
		"LC_ALL=C",
		"PATH=",
	}
	command.Stdin = bytes.NewReader(requestBytes)
	command.ExtraFiles = []*os.File{snapshot}
	command.WaitDelay = workerWaitDelay
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	command.Cancel = func() error {
		return killWorkerProcessGroup(command)
	}

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
	waitErr := command.Wait()
	if terminateErr := killWorkerProcessGroup(command); terminateErr != nil &&
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

func killWorkerProcessGroup(command *exec.Cmd) error {
	if command == nil || command.Process == nil || command.Process.Pid <= 0 {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
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
