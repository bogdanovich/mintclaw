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
	workerProcessDetachedChild     = "MINTCLAW_WORKERPROCESS_DETACHED_CHILD"
)

func TestTerminateKillsUnixWorkerProcessGroup(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err = process.Terminate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = waitForUnixProcessExit(pid, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestDetachedUnixDescendantDoesNotBlockNormalCompletion(t *testing.T) {
	process, pid := launchProcessWithDetachedUnixDescendant(t)
	process.stopTimeout = 100 * time.Millisecond
	if err := process.Steer(t.Context(), "steer-1", "finish", nil); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	result, err := process.Wait(testTimeoutContext(t, 3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkerStop == nil || result.WorkerStop.Reason != worker.WorkerStopCompleted {
		t.Fatalf("process result = %#v", result)
	}
	if !errors.Is(result.ProcessError, ErrDiagnosticsDrainTimeout) || result.ClientError != nil {
		t.Fatalf("process errors = process=%v client=%v", result.ProcessError, result.ClientError)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("bounded completion took %v", elapsed)
	}
	if err = syscall.Kill(pid, 0); err != nil {
		t.Fatalf("detached process unexpectedly joined worker domain: %v", err)
	}
}

func TestDetachedUnixDescendantDoesNotBlockTermination(t *testing.T) {
	process, pid := launchProcessWithDetachedUnixDescendant(t)
	process.stopTimeout = 100 * time.Millisecond
	started := time.Now()
	if err := process.Terminate(testTimeoutContext(t, 3*time.Second)); err != nil {
		t.Fatal(err)
	}
	result, err := process.Wait(testTimeoutContext(t, time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(result.ProcessError, ErrDiagnosticsDrainTimeout) {
		t.Fatalf("process error = %v, want diagnostics drain timeout", result.ProcessError)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("bounded termination took %v", elapsed)
	}
	if err = syscall.Kill(pid, 0); err != nil {
		t.Fatalf("detached process unexpectedly joined worker domain: %v", err)
	}
}

func launchProcessWithDetachedUnixDescendant(t *testing.T) (*Process, int) {
	t.Helper()
	launcher, buildID := newTestLauncher(t)
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	launcher.environment = append(
		launcher.environment,
		workerProcessDescendantPIDFile+"="+pidFile,
		workerProcessDetachedChild+"=1",
	)
	binding := testProcessBinding(t, buildID, worker.ThreadOpenNew)
	process, err := launcher.Launch(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if err = process.StartTurn(t.Context(), "turn-start-1", "detach descendant", nil); err != nil {
		t.Fatal(err)
	}
	pid := waitForDescendantPID(t, pidFile)
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		_ = waitForUnixProcessExit(pid, 5*time.Second)
	})
	return process, pid
}

func TestWorkerProcessDescendantHelper(t *testing.T) {
	switch os.Getenv(workerProcessDescendantHelper) {
	case "sleep":
		time.Sleep(5 * time.Minute)
	case "detach":
		command := exec.Command(os.Args[0], "-test.run=^TestWorkerProcessDescendantHelper$")
		command.Env = append(os.Environ(), workerProcessDescendantHelper+"=sleep")
		command.Stderr = os.Stderr
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := command.Start(); err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(
			os.Getenv(workerProcessDescendantPIDFile),
			[]byte(strconv.Itoa(command.Process.Pid)),
			0o600,
		); err != nil {
			_ = command.Process.Kill()
			os.Exit(2)
		}
		os.Exit(0)
	}
}

func maybeStartProcessTestDescendant() error {
	pidFile := os.Getenv(workerProcessDescendantPIDFile)
	if pidFile == "" {
		return nil
	}
	mode := "sleep"
	if os.Getenv(workerProcessDetachedChild) == "1" {
		mode = "detach"
	}
	command := exec.Command(os.Args[0], "-test.run=^TestWorkerProcessDescendantHelper$")
	command.Env = append(os.Environ(), workerProcessDescendantHelper+"="+mode)
	command.Stderr = os.Stderr
	if mode == "detach" {
		return command.Run()
	}
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
