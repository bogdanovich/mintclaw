//go:build darwin && (amd64 || arm64)

package document

import (
	"context"
	"os/exec"
	"syscall"
)

func configureDocumentWorkerProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func prepareDocumentWorkerProcess(
	_ context.Context,
	_ *exec.Cmd,
	_ string,
	_ string,
) (func(), error) {
	return func() {}, nil
}
