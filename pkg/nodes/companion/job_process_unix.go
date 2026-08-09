//go:build linux || darwin

package companion

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const jobProcessObservationInterval = 10 * time.Millisecond

const jobProcessObservationFailureLimit = 50

type jobProcessGroupOps struct {
	observe    func(int) (bool, int, error)
	signal     func(int) bool
	killLeader func() error
	pause      func()
}

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
func drainJobProcessGroup(command *exec.Cmd, terminateLeader bool) jobProcessDrainResult {
	if command == nil || command.Process == nil || command.Process.Pid <= 0 {
		return jobProcessDrainResult{}
	}
	return drainJobProcessGroupWithOps(command.Process.Pid, terminateLeader, jobProcessGroupOps{
		observe:    observeJobProcessGroup,
		signal:     signalOwnedJobProcessGroup,
		killLeader: command.Process.Kill,
		pause: func() {
			time.Sleep(jobProcessObservationInterval)
		},
	})
}

func drainJobProcessGroupWithOps(
	processGroup int,
	terminateLeader bool,
	ops jobProcessGroupOps,
) jobProcessDrainResult {
	result := jobProcessDrainResult{}
	emptyObservations := 0
	observationFailures := 0
	for {
		leaderExited, members, err := ops.observe(processGroup)
		if err != nil {
			observationFailures++
			if observationFailures < jobProcessObservationFailureLimit {
				ops.pause()
				continue
			}
			// The unreaped leader still owns the PGID, so one final group signal
			// cannot target a recycled group. Kill the exact child as a reaping
			// backstop, then report unknown because membership was not proven.
			result.signalSent = ops.signal(processGroup) || result.signalSent
			_ = ops.killLeader()
			result.observationUnknown = true
			return result
		}
		observationFailures = 0
		if members > 0 {
			result.hadDescendants = true
		}
		if (terminateLeader || result.hadDescendants) && (!leaderExited || members > 0) {
			sent := ops.signal(processGroup)
			result.signalSent = sent || result.signalSent
			if sent && !leaderExited {
				result.signaledLiveLeader = true
			}
		}
		if members > 0 {
			emptyObservations = 0
		} else if leaderExited {
			emptyObservations++
			if emptyObservations >= 2 {
				return result
			}
		} else {
			emptyObservations = 0
		}
		ops.pause()
	}
}

func observeJobProcessGroup(processGroup int) (bool, int, error) {
	leaderExited, leaderErr := jobProcessLeaderExited(processGroup)
	members, membersErr := liveJobProcessGroupMembers(processGroup, processGroup)
	if leaderErr != nil || membersErr != nil {
		return false, 0, errors.Join(leaderErr, membersErr)
	}
	return leaderExited, members, nil
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

func jobProcessKilled(state *os.ProcessState) bool {
	if state == nil {
		return false
	}
	status, ok := state.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == syscall.SIGKILL
}
