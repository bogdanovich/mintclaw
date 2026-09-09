//go:build linux || darwin

package workerprocess

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/worker"
)

const (
	workerProcessDescendantPIDFile = "MINTCLAW_WORKERPROCESS_DESCENDANT_PID_FILE"
	workerProcessDescendantHelper  = "MINTCLAW_WORKERPROCESS_DESCENDANT_HELPER"
)

func TestTerminateKillsUnixWorkerDescendants(t *testing.T) {
	launcher, buildID := newTestLauncher(t)
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	launcher.environment = append(launcher.environment, workerProcessDescendantPIDFile+"="+pidFile)
	binding := testProcessBinding(t, buildID, worker.ThreadOpenNew)
	process, err := launcher.Launch(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if err = process.StartTurn(t.Context(), "turn-start-1", "keep descendant alive", nil); err != nil {
		t.Fatal(err)
	}
	pid := waitForDescendantPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	waitForUnixDescendantTracking(t, process, pid)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err = process.Terminate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = waitForUnixProcessExit(pid, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestNormalCompletionDrainsUnixWorkerDescendantsHoldingStderr(t *testing.T) {
	launcher, buildID := newTestLauncher(t)
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	launcher.environment = append(launcher.environment, workerProcessDescendantPIDFile+"="+pidFile)
	binding := testProcessBinding(t, buildID, worker.ThreadOpenNew)
	process, err := launcher.Launch(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if err = process.StartTurn(t.Context(), "turn-start-1", "finish with descendant alive", nil); err != nil {
		t.Fatal(err)
	}
	pid := waitForDescendantPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	waitForUnixDescendantTracking(t, process, pid)
	if err = process.Steer(t.Context(), "steer-1", "finish", nil); err != nil {
		t.Fatal(err)
	}
	result, err := process.Wait(testTimeoutContext(t, 10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkerStop == nil || result.WorkerStop.Reason != worker.WorkerStopCompleted {
		t.Fatalf("process result = %#v", result)
	}
	if result.ProcessError != nil || result.ClientError != nil {
		t.Fatalf("process errors = process=%v client=%v", result.ProcessError, result.ClientError)
	}
	if err = waitForUnixProcessExit(pid, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerProcessDescendantHelper(t *testing.T) {
	if os.Getenv(workerProcessDescendantHelper) != "1" {
		return
	}
	time.Sleep(5 * time.Minute)
}

func maybeStartProcessTestDescendant() error {
	pidFile := os.Getenv(workerProcessDescendantPIDFile)
	if pidFile == "" {
		return nil
	}
	command := exec.Command(os.Args[0], "-test.run=^TestWorkerProcessDescendantHelper$")
	command.Env = append(os.Environ(), workerProcessDescendantHelper+"=1")
	command.Stderr = os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return err
	}
	return os.WriteFile(pidFile, []byte(strconv.Itoa(command.Process.Pid)), 0o600)
}

func waitForUnixDescendantTracking(t *testing.T, process *Process, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		process.domain.state.mu.Lock()
		tracked := false
		for identity := range process.domain.state.owned {
			if identity.pid == pid {
				tracked = true
				break
			}
		}
		process.domain.state.mu.Unlock()
		if tracked {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for escaped descendant tracking")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForDescendantPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr != nil || pid <= 0 {
				t.Fatalf("descendant PID = %q, %v", data, parseErr)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for descendant PID")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForUnixProcessExit(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("worker descendant remained alive after process-domain termination")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
