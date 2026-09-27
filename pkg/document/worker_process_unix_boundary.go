//go:build (linux && amd64) || (darwin && (amd64 || arm64))

package document

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type unixDocumentWorkerProcessBoundary struct {
	command *exec.Cmd
	release func()
}

func configureDocumentWorkerInput(command *exec.Cmd, snapshot *os.File) (func(), error) {
	command.ExtraFiles = []*os.File{snapshot}
	return func() {}, nil
}

func newUnixDocumentWorkerProcessBoundary(
	command *exec.Cmd,
	release func(),
) documentWorkerProcessBoundary {
	return &unixDocumentWorkerProcessBoundary{command: command, release: release}
}

func (boundary *unixDocumentWorkerProcessBoundary) started() error {
	return nil
}

func (boundary *unixDocumentWorkerProcessBoundary) terminate() error {
	if boundary == nil || boundary.command == nil || boundary.command.Process == nil ||
		boundary.command.Process.Pid <= 0 {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-boundary.command.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func (boundary *unixDocumentWorkerProcessBoundary) close() error {
	if boundary != nil && boundary.release != nil {
		boundary.release()
		boundary.release = nil
	}
	return nil
}
