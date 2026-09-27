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
) (func(), error) {
	if !workerOperationRequiresNativeIsolation(operation) {
		return func() {}, nil
	}
	return isolation.PrepareDocumentCommand(
		ctx,
		command,
		workerScratch,
		nativeBackendExecutablePaths(operation),
	)
}

func workerOperationRequiresNativeIsolation(operation string) bool {
	switch operation {
	case workerOperationExtract, workerOperationRender, workerOperationFillCandidate:
		return true
	default:
		return false
	}
}
