//go:build linux

package runtimeimage

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRuntimeImageFixture(t *testing.T) {
	if os.Getenv("MINTCLAW_SYNTHETIC_RUNTIME_IMAGE_FIXTURE") != "1" {
		t.Skip("disposable Root mount fixture only")
	}
	if os.Getenv("MINTCLAW_RUNTIME_IMAGE_CHILD") == "1" {
		if os.Getuid() != 65532 || os.Getgid() != 65532 {
			t.Fatal("synthetic child identity unavailable")
		}
		if _, err := Open(context.Background()); !errors.Is(err, errUnavailable) {
			t.Fatal("unprivileged startup admitted")
		}
		return
	}
	if !rootProcess() {
		t.Fatal("Root fixture required")
	}
	private, err := os.MkdirTemp("/run", "mintclaw-runtime-qual-")
	if err != nil {
		t.Fatal("synthetic root allocation failed")
	}
	defer func() {
		if os.RemoveAll(private) != nil {
			t.Error("synthetic root cleanup failed")
		}
	}()
	for _, scenario := range []string{
		"valid", "writable", "noexec", "digest", "extra", "missing", "symlink", "hardlink", "fifo",
		"owner", "mode", "acl", "nested-mount", "script-executable", "executable-manifest",
	} {
		t.Run(scenario, func(t *testing.T) { exerciseRuntimeImage(t, private, scenario) })
	}
	child := exec.Command(os.Args[0], "-test.run=^TestRuntimeImageFixture$", "-test.count=1")
	child.Env = []string{
		"PATH=/usr/bin:/bin",
		"MINTCLAW_SYNTHETIC_RUNTIME_IMAGE_FIXTURE=1",
		"MINTCLAW_RUNTIME_IMAGE_CHILD=1",
	}
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
	if child.Run() != nil {
		t.Fatal("unprivileged startup denial failed")
	}
}

func exerciseRuntimeImage(t *testing.T, private, scenario string) {
	t.Helper()
	root := filepath.Join(private, scenario)
	if os.Mkdir(root, 0o755) != nil ||
		unix.Mount("none", root, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID, "size=8m") != nil {
		t.Fatal("synthetic tmpfs allocation failed")
	}
	defer func() {
		if unix.Unmount(root, 0) != nil {
			t.Error("synthetic runtime unmount failed")
		}
	}()
	files := syntheticFiles()
	if scenario == "script-executable" {
		files[NodePath] = []byte("#!/bin/sh\nexit 0\n")
	}
	for name, data := range files {
		pathname := filepath.Join(root, name)
		if os.MkdirAll(filepath.Dir(pathname), 0o755) != nil || os.WriteFile(pathname, data, 0o444) != nil {
			t.Fatal("synthetic runtime file setup failed")
		}
		if name == NodePath || name == ChromiumPath {
			if os.Chmod(pathname, 0o555) != nil {
				t.Fatal("synthetic executable mode failed")
			}
		}
	}
	if os.WriteFile(filepath.Join(root, manifestName), syntheticManifest(files), 0o444) != nil {
		t.Fatal("synthetic manifest setup failed")
	}
	node := filepath.Join(root, NodePath)
	switch scenario {
	case "digest":
		err := os.WriteFile(node, []byte("\x7fELFchanged-node!!"), 0o555)
		requireFixture(t, err)
	case "extra":
		requireFixture(t, os.WriteFile(filepath.Join(root, "unlisted.cjs"), []byte("synthetic"), 0o444))
	case "missing":
		requireFixture(t, os.Remove(node))
	case "symlink":
		requireFixture(t, os.Remove(node))
		requireFixture(t, os.Symlink("chromium", node))
	case "hardlink":
		requireFixture(t, os.Link(node, filepath.Join(root, "link")))
	case "fifo":
		requireFixture(t, os.Remove(node))
		requireFixture(t, unix.Mkfifo(node, 0o444))
	case "owner":
		requireFixture(t, os.Chown(node, 65532, 65532))
	case "mode":
		requireFixture(t, os.Chmod(node, 0o777))
	case "acl":
		acl := make([]byte, 44)
		binary.LittleEndian.PutUint32(acl, 2)
		for i, item := range []struct {
			tag, permissions uint16
			id               uint32
		}{
			{1, 5, ^uint32(0)}, {2, 4, 65532}, {4, 0, ^uint32(0)}, {16, 5, ^uint32(0)}, {32, 5, ^uint32(0)},
		} {
			at := 4 + i*8
			binary.LittleEndian.PutUint16(acl[at:], item.tag)
			binary.LittleEndian.PutUint16(acl[at+2:], item.permissions)
			binary.LittleEndian.PutUint32(acl[at+4:], item.id)
		}
		requireFixture(t, unix.Setxattr(node, "system.posix_acl_access", acl, 0))
	case "executable-manifest":
		requireFixture(t, os.Chmod(filepath.Join(root, manifestName), 0o555))
	}
	if filepath.WalkDir(root, func(pathname string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.IsDir() {
			return os.Chmod(pathname, 0o555)
		}
		return nil
	}) != nil {
		t.Fatal("synthetic directory protection failed")
	}
	if scenario == "nested-mount" {
		// A read-only bind of the very same filesystem still has a distinct
		// mount ID; device identity alone must not admit this substitution.
		directory := filepath.Join(root, "bin")
		requireFixture(t, unix.Mount(directory, directory, "", unix.MS_BIND, ""))
		requireFixture(t, unix.Mount("", directory, "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY, ""))
		defer func() { requireFixture(t, unix.Unmount(directory, 0)) }()
	}
	if scenario != "writable" {
		flags := uintptr(unix.MS_REMOUNT | unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV)
		if scenario == "noexec" {
			flags |= unix.MS_NOEXEC
		}
		requireFixture(t, unix.Mount("", root, "tmpfs", flags, ""))
	}
	image, err := open(context.Background(), root)
	if scenario != "valid" {
		if image != nil {
			_ = image.Close()
		}
		if !errors.Is(err, errUnavailable) || strings.Contains(err.Error(), root) {
			t.Fatal("unsafe runtime admitted")
		}
		return
	}
	if err != nil {
		t.Fatal("protected read-only runtime denied")
	}
	defer func() { requireFixture(t, image.Close()) }()
	pin, err := image.BorrowRoot(context.Background())
	if err != nil {
		t.Fatal("admitted mount borrow failed")
	}
	requireFixture(t, pin.Close())
	requireFixture(t, image.Close())
	if _, err = image.BorrowRoot(context.Background()); !errors.Is(err, errUnavailable) {
		t.Fatal("closed image renewed")
	}
}

func requireFixture(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal("synthetic runtime setup or cleanup failed")
	}
}
