//go:build darwin

package desktop

import (
	"context"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDarwinProcessWatchDoesNotOwnApplicationLifetime(t *testing.T) {
	process, err := watchDarwinProcess(uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	if !process.alive() {
		t.Fatal("current process not initially live")
	}
	if err = process.close(); err != nil || process.alive() || process.close() != nil {
		t.Fatal("kernel watch close was not idempotent", err)
	}
	if err = unix.Kill(os.Getpid(), 0); err != nil {
		t.Fatal("watch release affected owning process", err)
	}
}

func TestDarwinProcessWatchRejectsExitAndExec(t *testing.T) {
	for _, action := range []string{"exit", "exec"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, "/bin/sh", "-c",
				"printf 'ready\\n'; IFS= read -r line; exec /bin/sleep 60")
			stdout, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := child.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = stdin.Close(); _ = child.Process.Kill(); _ = child.Wait() })
			ready := make([]byte, 6)
			if _, err = io.ReadFull(stdout, ready); err != nil || string(ready) != "ready\n" {
				t.Fatal("owned process did not become ready", err)
			}
			process, err := watchDarwinProcess(uint32(child.Process.Pid))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = process.close() })
			if !process.alive() {
				t.Fatal("new process watch is not live")
			}
			if action == "exit" {
				_ = child.Process.Kill()
				_ = child.Wait()
			} else if _, err = stdin.Write([]byte("\n")); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(2 * time.Second)
			for process.alive() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			for range 2 {
				if process.alive() {
					t.Fatal("original process incarnation became valid again")
				}
			}
			if action == "exec" && unix.Kill(child.Process.Pid, 0) != nil {
				t.Fatal("exec case did not preserve its numeric PID")
			}
		})
	}
}
