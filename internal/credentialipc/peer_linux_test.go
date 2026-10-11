//go:build linux

package credentialipc

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestLifetimeCopyRejectsExitedProcessAndSharedClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/sleep", "2")
	command.Env = []string{"PATH=/usr/bin:/bin"}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	lifetime, err := OpenProcess(int32(command.Process.Pid))
	if err != nil || !lifetime.Alive() {
		t.Fatal("live process was not pinned")
	}
	defer lifetime.Close()
	copy := lifetime
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	if copy.Alive() || lifetime.Alive() {
		t.Fatal("exited process retained lifetime authority")
	}
	copy.Close()
	if lifetime.Alive() || (Lifetime{}).Alive() {
		t.Fatal("closed or zero handle acquired authority")
	}
}
