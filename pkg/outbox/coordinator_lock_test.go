package outbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func coordinatorHelper(t *testing.T, root, mode string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestCoordinatorLockProcessHelper$")
	cmd.Env = append(os.Environ(), "MINTCLAW_OUTBOX_LOCK_HELPER="+mode, "MINTCLAW_OUTBOX_LOCK_ROOT="+root)
	return cmd
}

func TestCoordinatorLockProcessHelper(t *testing.T) {
	mode := os.Getenv("MINTCLAW_OUTBOX_LOCK_HELPER")
	if mode == "" {
		return
	}
	coordinator, err := OpenCoordinator(os.Getenv("MINTCLAW_OUTBOX_LOCK_ROOT"))
	if mode == "blocked" {
		if !errors.Is(err, ErrCoordinatorOwned) {
			t.Fatalf("competing process error = %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := coordinator.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if mode == "hold" {
		fmt.Fprintln(os.Stdout, "owned")
		// The parent kills this process to exercise OS ownership release.
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}
}

func TestCoordinatorRejectsAnotherProcessAndReleasesOnClose(t *testing.T) {
	root := t.TempDir()
	first, err := OpenCoordinator(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	if second, err := OpenCoordinator(root); !errors.Is(err, ErrCoordinatorOwned) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("same-process duplicate = %v", err)
	}
	if output, err := coordinatorHelper(t, root, "blocked").CombinedOutput(); err != nil {
		t.Fatalf("competing process: %v; %s", err, output)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if output, err := coordinatorHelper(t, root, "acquire").CombinedOutput(); err != nil {
		t.Fatalf("acquire after Close: %v; %s", err, output)
	}
}

func TestCoordinatorLockReleasedWhenProcessDies(t *testing.T) {
	root := t.TempDir()
	cmd := coordinatorHelper(t, root, "hold")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- strings.TrimSpace(line) }()
	select {
	case line := <-ready:
		if line != "owned" {
			t.Fatalf("child admission = %q", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child did not acquire ownership")
	}
	if coordinator, err := OpenCoordinator(root); !errors.Is(err, ErrCoordinatorOwned) {
		if coordinator != nil {
			_ = coordinator.Close()
		}
		t.Fatalf("active child did not fence parent: %v", err)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	waited = true
	coordinator, err := OpenCoordinator(root)
	if err != nil {
		t.Fatalf("ownership after process death: %v", err)
	}
	t.Cleanup(func() { _ = coordinator.Close() })
}

func TestCoordinatorLockOpenFailureReleasesInProcessClaim(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(filepath.Join(state, "outbox", ".writer.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	if coordinator, err := OpenCoordinator(root); err == nil {
		_ = coordinator.Close()
		t.Fatal("opened coordinator with invalid lock file")
	}
	if err := os.Remove(filepath.Join(state, "outbox", ".writer.lock")); err != nil {
		t.Fatal(err)
	}
	coordinator, err := OpenCoordinator(root)
	if err != nil {
		t.Fatalf("retry after lock failure: %v", err)
	}
	t.Cleanup(func() { _ = coordinator.Close() })
}

func TestCoordinatorLocksSharedStoreThroughOutboxSymlink(t *testing.T) {
	root := t.TempDir()
	owner, err := OpenCoordinator(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	alias := t.TempDir()
	if err = os.MkdirAll(filepath.Join(alias, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(Path(root), Path(alias)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	other, err := OpenCoordinator(alias)
	if other != nil {
		_ = other.Close()
	}
	if !errors.Is(err, ErrCoordinatorOwned) {
		t.Fatalf("shared store admitted second owner: %v", err)
	}
}
