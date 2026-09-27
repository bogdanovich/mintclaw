//go:build linux && amd64

package document

import (
	"context"
	"os/exec"
	"syscall"

	"github.com/bogdanovich/mintclaw/pkg/isolation"
)

func configureDocumentWorkerProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

func prepareDocumentWorkerProcess(
	ctx context.Context,
	command *exec.Cmd,
	workerScratch string,
	operation string,
) (documentWorkerProcessBoundary, error) {
	if !workerOperationRequiresNativeIsolation(operation) {
		return newUnixDocumentWorkerProcessBoundary(command, func() {}), nil
	}
	release, err := isolation.PrepareDocumentCommand(
		ctx,
		command,
		workerScratch,
		nativeBackendExecutablePaths(operation),
	)
	if err != nil {
		return nil, err
	}
	return newUnixDocumentWorkerProcessBoundary(command, release), nil
}

func workerOperationRequiresNativeIsolation(operation string) bool {
	return operation == workerOperationFillCandidate
}
