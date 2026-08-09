//go:build !linux && !darwin

package companion

import (
	"errors"
	"os"
	"os/exec"
)

func prepareJobProcess(*exec.Cmd) {}

func terminateJobProcess(*exec.Cmd) (bool, error) {
	return false, errors.New("durable node jobs are unsupported on this platform")
}

func jobProcessDomainEmpty(*exec.Cmd, JobCancelGuarantee) bool { return false }

func jobProcessSignal(*os.ProcessState) string { return "" }
