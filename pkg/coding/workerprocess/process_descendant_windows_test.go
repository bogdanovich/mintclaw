//go:build windows

package workerprocess

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/bogdanovich/mintclaw/pkg/coding/worker"
)

const (
	workerProcessDescendantPIDFile = "MINTCLAW_WORKERPROCESS_DESCENDANT_PID_FILE"
	workerProcessDescendantHelper  = "MINTCLAW_WORKERPROCESS_DESCENDANT_HELPER"
)

func TestTerminateKillsWindowsWorkerDescendants(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err = process.Terminate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = waitForWindowsProcessExit(pid, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestNormalCompletionDrainsWindowsWorkerDescendantsHoldingStderr(t *testing.T) {
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
	if err = process.Steer(t.Context(), "steer-1", "finish", nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkerStop == nil || result.WorkerStop.Reason != worker.WorkerStopCompleted {
		t.Fatalf("process result = %#v", result)
	}
	if result.ProcessError != nil || result.ClientError != nil {
		t.Fatalf("process errors = process=%v client=%v", result.ProcessError, result.ClientError)
	}
	if err = waitForWindowsProcessExit(pid, 5*time.Second); err != nil {
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
	if err := command.Start(); err != nil {
		return err
	}
	return os.WriteFile(pidFile, []byte(strconv.Itoa(command.Process.Pid)), 0o600)
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

func waitForWindowsProcessExit(pid int, timeout time.Duration) error {
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	status, err := windows.WaitForSingleObject(process, uint32(timeout/time.Millisecond))
	if err != nil {
		return err
	}
	if status != windows.WAIT_OBJECT_0 {
		return errors.New("worker descendant remained alive after process-domain termination")
	}
	return nil
}
