//go:build linux || darwin

package companion

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

const jobProcessObservationInterval = 10 * time.Millisecond

func prepareJobProcess(command *exec.Cmd) {
	if command == nil {
		return
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// drainJobProcessGroup never reaps the leader. Its unreaped PID is the
// non-reusable ownership token for the process-group ID while signals and
// membership observations are in flight. Two empty observations after the
// last group signal close the fork-versus-scan race before Cmd.Wait releases
// that token.
func drainJobProcessGroup(command *exec.Cmd, terminateLeader bool) (bool, bool) {
	if command == nil || command.Process == nil || command.Process.Pid <= 0 {
		return false, false
	}
	processGroup := command.Process.Pid
	signalSent := false
	hadDescendants := false
	emptyObservations := 0
	if terminateLeader {
		signalSent = signalOwnedJobProcessGroup(processGroup) || signalSent
	}
	for {
		leaderExited, leaderErr := jobProcessLeaderExited(processGroup)
		members, membersErr := liveJobProcessGroupMembers(processGroup, processGroup)
		if leaderErr != nil || membersErr != nil {
			time.Sleep(jobProcessObservationInterval)
			continue
		}
		if members > 0 {
			hadDescendants = true
			emptyObservations = 0
			signalSent = signalOwnedJobProcessGroup(processGroup) || signalSent
		} else if leaderExited {
			emptyObservations++
			if emptyObservations >= 2 {
				return signalSent, hadDescendants
			}
		} else {
			emptyObservations = 0
		}
		if terminateLeader || hadDescendants {
			signalSent = signalOwnedJobProcessGroup(processGroup) || signalSent
		}
		time.Sleep(jobProcessObservationInterval)
	}
}

func signalOwnedJobProcessGroup(processGroup int) bool {
	return syscall.Kill(-processGroup, syscall.SIGKILL) == nil
}

func jobProcessSignal(state *os.ProcessState) string {
	if state == nil {
		return ""
	}
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return ""
	}
	return status.Signal().String()
}
