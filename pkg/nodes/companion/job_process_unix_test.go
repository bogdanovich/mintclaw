//go:build linux || darwin

package companion

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestDrainJobProcessGroupBoundsObservationFailure(t *testing.T) {
	observations := 0
	signals := 0
	kills := 0
	result := drainJobProcessGroupWithOps(123, true, jobProcessGroupOps{
		observe: func(int) (bool, int, error) {
			observations++
			return false, 0, errors.New("process table unavailable")
		},
		signal: func(int) bool {
			signals++
			return true
		},
		killLeader: func() error {
			kills++
			return nil
		},
		pause: func() {},
	})
	if !result.observationUnknown || observations != jobProcessObservationFailureLimit ||
		signals != 1 || kills != 1 {
		t.Fatalf(
			"bounded observation result = %#v, observations %d, signals %d, kills %d",
			result,
			observations,
			signals,
			kills,
		)
	}
}

func TestDrainJobProcessGroupDoesNotSignalZombieOnlyGroup(t *testing.T) {
	signals := 0
	kills := 0
	result := drainJobProcessGroupWithOps(123, true, jobProcessGroupOps{
		observe: func(int) (bool, int, error) { return true, 0, nil },
		signal: func(int) bool {
			signals++
			return true
		},
		killLeader: func() error {
			kills++
			return nil
		},
		pause: func() {},
	})
	if result != (jobProcessDrainResult{}) || signals != 0 || kills != 0 {
		t.Fatalf("zombie-only drain = %#v, signals %d, kills %d", result, signals, kills)
	}
}

func TestJobCompletionPreservesNaturalResultWhenCancelLosesRace(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestJobHelperProcess$")
	command.Dir = t.TempDir()
	command.Env = []string{
		jobHelperEnabled + "=1",
		jobHelperAction + "=success",
	}
	waitErr := command.Run()
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	completion := jobCompletionForProcess(command, waitErr, "cancel", jobProcessDrainResult{})
	if completion.State != JobSucceeded || completion.CancellationSignal ||
		completion.FailureCode != "" {
		t.Fatalf("cancel/natural completion = %#v", completion)
	}
}

func TestJobCompletionRecordsObservationFailureAsUnknown(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestJobHelperProcess$")
	command.Dir = t.TempDir()
	command.Env = []string{
		jobHelperEnabled + "=1",
		jobHelperAction + "=success",
	}
	waitErr := command.Run()
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	drain := jobProcessDrainResult{signalSent: true, observationUnknown: true}
	completion := jobCompletionForProcess(command, waitErr, "timeout", drain)
	if completion.State != JobUnknown || completion.FailureCode != "PROCESS_OBSERVATION_FAILED" ||
		!completion.CancellationSignal {
		t.Fatalf("observation failure completion = %#v", completion)
	}
}
