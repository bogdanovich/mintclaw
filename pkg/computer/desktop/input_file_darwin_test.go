//go:build darwin

package desktop

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

func privateDarwinInputRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal("private fixture directory permissions", err)
	}
	return root
}

func privateDarwinFileLease(t *testing.T, root string) *darwinFileLease {
	t.Helper()
	lease, err := openDarwinFileLease(t.Context(), root, uint32(os.Getuid()), 0o700)
	if err != nil {
		var info unix.Stat_t
		var filesystem unix.Statfs_t
		_ = unix.Lstat(root, &info)
		_ = unix.Statfs(root, &filesystem)
		t.Logf("owned fixture root: uid=%d mode=%#o fsflags=%#x", info.Uid, info.Mode, filesystem.Flags)
		t.Fatal("private native lock acquisition", err)
	}
	t.Cleanup(func() {
		if err := lease.close(); err != nil {
			t.Error(err)
		}
	})
	return lease
}

// This child is used only by our real-process tests. Its directory argument is
// not an exported constructor, production environment override or model input.
func TestDarwinInputLockChild(t *testing.T) {
	mode := os.Getenv("MINTCLAW_TEST_INPUT_LOCK_CHILD")
	if mode == "" {
		return
	}
	var lease *darwinFileLease
	var err error
	if os.Getenv("MINTCLAW_TEST_INPUT_LOCK_ROOT") == "" {
		lease, err = acquireDarwinFileLease(t.Context())
	} else {
		lease, err = openDarwinFileLease(t.Context(), os.Getenv("MINTCLAW_TEST_INPUT_LOCK_ROOT"),
			uint32(os.Getuid()), 0o700)
	}
	if mode == "busy" {
		if lease != nil || !errors.Is(err, computer.ErrBusy) {
			t.Fatal("other process entered an occupied native lock", err)
		}
		_, _ = io.WriteString(os.Stdout, "busy\n")
		return
	}
	if err != nil {
		t.Fatal("child native lock", err)
	}
	defer func() { _ = lease.close() }()
	_, _ = io.WriteString(os.Stdout, "owned\n")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func startDarwinLockChild(t *testing.T, root, mode string) (*exec.Cmd, io.WriteCloser) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	child := exec.CommandContext(ctx, executable, "-test.run=^TestDarwinInputLockChild$", "-test.timeout=12s")
	// Deliberately different ambient configuration cannot choose a second lock.
	child.Env = []string{
		"PATH=/usr/bin:/bin", "MINTCLAW_TEST_INPUT_LOCK_CHILD=" + mode,
		"MINTCLAW_TEST_INPUT_LOCK_ROOT=" + root, "TMPDIR=" + t.TempDir(),
		"MINTCLAW_HOME=" + t.TempDir(),
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = child.Process.Kill(); _ = child.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	want := "owned\n"
	if mode == "busy" {
		want = "busy\n"
	}
	if err != nil || line != want {
		t.Fatal("child did not prove native ownership decision", line, err)
	}
	return child, stdin
}

func TestDarwinInputFileExcludesSameAndOtherProcesses(t *testing.T) {
	root := privateDarwinInputRoot(t)
	first := privateDarwinFileLease(t, root)
	if second, err := openDarwinFileLease(t.Context(), root, uint32(os.Getuid()), 0o700); second != nil ||
		!errors.Is(err, computer.ErrBusy) {
		t.Fatal("same-process second open bypassed kernel exclusion", err)
	}
	child, _ := startDarwinLockChild(t, root, "busy")
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := first.check(); err != nil {
		t.Fatal("competing opens revoked original owner", err)
	}
	if err := first.close(); err != nil || first.close() != nil {
		t.Fatal("release was not idempotent", err)
	}
	if _, err := os.Stat(filepath.Join(root, first.name)); err != nil {
		t.Fatal("release deleted the shared namespace", err)
	}
	second := privateDarwinFileLease(t, root)
	if first.check() == nil || second.check() != nil {
		t.Fatal("released lease resurrected or newer owner was cleared")
	}
}

func TestDarwinInputFileCrashReleasesWithoutPIDRecovery(t *testing.T) {
	root := privateDarwinInputRoot(t)
	child, _ := startDarwinLockChild(t, root, "hold")
	if lease, err := openDarwinFileLease(t.Context(), root, uint32(os.Getuid()), 0o700); lease != nil ||
		!errors.Is(err, computer.ErrBusy) {
		t.Fatal("parent entered live child ownership", err)
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	lease := privateDarwinFileLease(t, root)
	if lease.check() != nil {
		t.Fatal("kernel did not release crashed owner's file description")
	}
}

func TestDarwinInputFileCancellationBoundsOnlyAcquisition(t *testing.T) {
	root := privateDarwinInputRoot(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if lease, err := openDarwinFileLease(ctx, root, uint32(os.Getuid()), 0o700); lease != nil ||
		!errors.Is(err, context.Canceled) {
		t.Fatal("canceled acquisition touched native namespace", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("pre-canceled admission created a file", err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	lease, err := openDarwinFileLease(ctx, root, uint32(os.Getuid()), 0o700)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.close() }()
	cancel()
	if lease.check() != nil {
		t.Fatal("acquisition context cancellation unlocked before input cleanup")
	}
	if next, err := openDarwinFileLease(t.Context(), root, uint32(os.Getuid()), 0o700); next != nil ||
		!errors.Is(err, computer.ErrBusy) {
		t.Fatal("post-acquisition context released original ownership", err)
	}
}

func TestDarwinInputFileReplacementAndRestorationNeverResurrect(t *testing.T) {
	for _, change := range []string{
		"replace", "rename_restore", "mode_restore", "link_restore", "contents", "contents_restore",
	} {
		t.Run(change, func(t *testing.T) {
			root := privateDarwinInputRoot(t)
			lease := privateDarwinFileLease(t, root)
			path := filepath.Join(root, lease.name)
			switch change {
			case "replace":
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				_ = file.Close()
			case "rename_restore":
				if os.Rename(path, path+".moved") != nil || os.Rename(path+".moved", path) != nil {
					t.Fatal("fixture rename failed")
				}
			case "mode_restore":
				if os.Chmod(path, 0o640) != nil || os.Chmod(path, 0o600) != nil {
					t.Fatal("fixture chmod failed")
				}
			case "link_restore":
				if os.Link(path, path+".link") != nil || os.Remove(path+".link") != nil {
					t.Fatal("fixture hard link failed")
				}
			case "contents", "contents_restore":
				if err := os.WriteFile(path, []byte("not a protocol lock"), 0o600); err != nil {
					t.Fatal(err)
				}
				if change == "contents_restore" {
					if err := os.Truncate(path, 0); err != nil {
						t.Fatal(err)
					}
				}
			}
			for range 2 {
				if !errors.Is(lease.check(), computer.ErrStale) {
					t.Fatal("changed native resource resurrected", change)
				}
			}
			if change == "replace" {
				newer := privateDarwinFileLease(t, root)
				_ = lease.close()
				if newer.check() != nil {
					t.Fatal("stale cleanup cleared newer file owner")
				}
			}
		})
	}
}

func TestDarwinInputFileRejectsUnsafeExistingObjects(t *testing.T) {
	for _, shape := range []string{"symlink", "directory", "fifo", "mode", "hardlink", "nonempty"} {
		t.Run(shape, func(t *testing.T) {
			root := privateDarwinInputRoot(t)
			name := "io.github.bogdanovich.mintclaw.input-control." + strconv.Itoa(os.Getuid()) + ".lock"
			path := filepath.Join(root, name)
			other := filepath.Join(root, "other")
			if err := os.WriteFile(other, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch shape {
			case "symlink":
				err = os.Symlink(other, path)
			case "directory":
				err = os.Mkdir(path, 0o700)
			case "fifo":
				err = unix.Mkfifo(path, 0o600)
			case "mode":
				err = os.WriteFile(path, nil, 0o644)
			case "hardlink":
				err = os.Link(other, path)
			case "nonempty":
				err = os.WriteFile(path, []byte("other protocol"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if lease, err := openDarwinFileLease(t.Context(), root, uint32(os.Getuid()), 0o700); lease != nil ||
				!errors.Is(err, computer.ErrDenied) {
				t.Fatal("unsafe resource was repaired or admitted", shape, err)
			}
		})
	}
}

func TestDarwinInputFileRetainsCloexecAndRootIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lease := privateDarwinFileLease(t, root)
	for _, fd := range []int{lease.file, lease.root, lease.queue} {
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil || flags&unix.FD_CLOEXEC == 0 {
			t.Fatal("owned handle could leak into an input worker", err)
		}
	}
	if os.Rename(root, root+".old") != nil || os.Mkdir(root, 0o700) != nil {
		t.Fatal("fixture root replacement failed")
	}
	if lease.check() == nil {
		t.Fatal("root namespace replacement was not terminal")
	}
}
