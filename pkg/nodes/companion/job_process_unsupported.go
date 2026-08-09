//go:build !linux && !darwin

package companion

import (
	"os"
	"os/exec"
	"time"
)

const jobProcessObservationInterval = time.Second

func jobProcessSupported() error { return ErrJobPlatformUnsupported }

func prepareJobProcess(*exec.Cmd) {}

func jobProcessLeaderExited(int) (bool, error) {
	return false, ErrJobPlatformUnsupported
}

func drainJobProcessGroup(*exec.Cmd, bool) jobProcessDrainResult { return jobProcessDrainResult{} }

func jobProcessSignal(*os.ProcessState) string { return "" }

func jobProcessKilled(*os.ProcessState) bool { return false }
