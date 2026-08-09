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

func drainJobProcessGroup(*exec.Cmd, bool) (bool, bool) { return false, false }

func jobProcessSignal(*os.ProcessState) string { return "" }
