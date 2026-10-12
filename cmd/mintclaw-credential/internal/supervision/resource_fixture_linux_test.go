//go:build linux

package supervision

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Root, synthetic broker 65531, and synthetic browser 65532 are separate host
// principals. Production named-role admission is not substituted by this test.
func syntheticResourceBroker(path, mode string) int {
	if os.Getuid() != 65531 || os.Getgid() != 65531 {
		return 11
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	worker, err := allocateResources(ctx, path)
	if err != nil {
		return 12
	}
	defer worker.Close()
	if mode == "out-of-order" {
		if writeResourcePacket(ctx, worker.connection, worker.peer, allocateOperation, 0, nil) != nil {
			return 13
		}
		select {
		case <-worker.Done():
			return 0
		case <-ctx.Done():
			return 14
		}
	}
	if !errors.Is(worker.Start(ctx), errUnavailable) {
		return 15
	}
	listenerFile, controlFile, err := worker.EndpointFiles(ctx)
	if err != nil {
		return 16
	}
	if listener, control, err := worker.EndpointFiles(ctx); listener != nil || control != nil ||
		!errors.Is(err, errUnavailable) {
		return 17
	}
	if mode == "before-start" {
		worker.Close()
		return 0
	}
	listener, err := net.FileListener(listenerFile)
	if err != nil {
		return 18
	}
	defer func() { _ = listener.Close() }()
	control, err := net.FileConn(controlFile)
	if err != nil {
		return 19
	}
	defer func() { _ = control.Close() }()
	if worker.Start(ctx) != nil || !errors.Is(worker.Start(ctx), errUnavailable) {
		return 20
	}
	if control.SetDeadline(time.Now().Add(4*time.Second)) != nil {
		return 21
	}
	var ready [1]byte
	if _, err := io.ReadFull(control, ready[:]); err != nil {
		return 22
	}
	if ready[0] != 1 {
		return 100 + int(ready[0])
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 23
	}
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], uint16(address.Port))
	if _, err := control.Write(port[:]); err != nil {
		return 24
	}
	tcp, ok := listener.(*net.TCPListener)
	if !ok || tcp.SetDeadline(time.Now().Add(4*time.Second)) != nil {
		return 25
	}
	accepted, err := listener.Accept()
	if err != nil {
		return 26
	}
	_ = accepted.SetDeadline(time.Now().Add(4 * time.Second))
	var witness [1]byte
	_, err = io.ReadFull(accepted, witness[:])
	_ = accepted.Close()
	if err != nil || witness[0] != 42 {
		return 27
	}
	if mode == "peer-death" {
		os.Exit(0)
	}
	if mode == "server-loss" {
		if _, err := os.Stdout.Write([]byte{1}); err != nil {
			return 28
		}
		select {
		case <-worker.Done():
		case <-ctx.Done():
			return 29
		}
		return 0
	}
	_ = listener.Close()
	_ = control.Close()
	if mode == "cancellation" {
		cancel()
		select {
		case <-worker.Done():
		case <-time.After(time.Second):
			return 30
		}
		if !errors.Is(worker.Start(context.Background()), errUnavailable) {
			return 31
		}
		return 0
	}
	if mode != "explicit" || worker.Retire(ctx) != nil ||
		!errors.Is(worker.Start(ctx), errUnavailable) {
		return 32
	}
	select {
	case <-worker.Done():
		return 0
	default:
		return 33
	}
}

func TestResourceFixture(t *testing.T) {
	if os.Getenv("MINTCLAW_SYNTHETIC_WORKER_FOREST_FIXTURE") != "1" {
		t.Skip("disposable Root Linux systemd fixture only")
	}
	if !root() || os.Getenv("OP_SERVICE_ACCOUNT_TOKEN") != "" ||
		unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{}) != nil {
		t.Fatal("Root fixture without token/core material required")
	}
	for _, mode := range []string{"before-start", "explicit", "cancellation", "peer-death", "out-of-order", "server-loss"} {
		t.Run(mode, func(t *testing.T) {
			parent := fixtureParent(t)
			supervisor, err := open(parent)
			if err != nil {
				t.Fatal("resource fixture supervisor unavailable")
			}
			launcher := &Launcher{image: launcherFixtureImage(t, false), supervisor: supervisor, uid: 65532, gid: 65532}
			launcher.nsenter, err = protectedLauncherFile("/usr/bin/nsenter", true)
			if err != nil {
				t.Fatal("resource fixture nsenter unavailable")
			}
			launcher.bubblewrap, err = protectedLauncherFile("/usr/bin/bwrap", true)
			if err != nil {
				t.Fatal("resource fixture bubblewrap unavailable")
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if launcher.Close(ctx) != nil {
					t.Error("resource fixture cleanup failed")
				}
			})
			directory, err := os.MkdirTemp("/run", "mintclaw-resource-qualification-")
			if err != nil || os.Chown(directory, 0, 65531) != nil || os.Chmod(directory, 0o750) != nil {
				t.Fatal("resource fixture socket directory unavailable")
			}
			t.Cleanup(func() {
				if os.RemoveAll(directory) != nil {
					t.Error("resource directory cleanup failed")
				}
			})
			endpoint, err := openResourceListener(directory, 65531)
			if err != nil {
				t.Fatal("protected resource fixture listener unavailable")
			}
			serviceCtx, cancel := context.WithCancel(t.Context())
			serviceDone := make(chan error, 1)
			go func() { serviceDone <- serveResourceConnections(serviceCtx, endpoint.listener, launcher, 65531, 65531) }()
			t.Cleanup(func() {
				cancel()
				select {
				case err := <-serviceDone:
					if err != nil {
						t.Error("resource fixture service failed")
					}
				case <-time.After(4 * time.Second):
					t.Error("resource fixture service did not stop")
				}
				if endpoint.close() != nil {
					t.Error("resource fixture endpoint cleanup failed")
				}
			})
			// A Root connector is not the configured broker principal.
			if unauthorized, err := allocateResources(
				t.Context(),
				filepath.Join(directory, resourceSocketName),
			); unauthorized != nil ||
				!errors.Is(err, errUnavailable) {
				t.Fatal("wrong broker principal admitted")
			}
			self, err := os.Executable()
			if err != nil {
				t.Fatal("resource fixture executable unavailable")
			}
			command := exec.Command(self, "resource-broker", filepath.Join(directory, resourceSocketName), mode)
			command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
			command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65531, Gid: 65531}}
			if mode == "server-loss" {
				pipe, pipeErr := command.StdoutPipe()
				if pipeErr != nil || command.Start() != nil {
					t.Fatal("server-loss broker unavailable")
				}
				var ready [1]byte
				if _, err := io.ReadFull(pipe, ready[:]); err != nil || ready[0] != 1 {
					_ = command.Wait()
					t.Fatalf("server-loss broker did not start: fixed stage %d", command.ProcessState.ExitCode())
				}
				cancel()
				if command.Wait() != nil {
					t.Fatal("broker did not observe server loss")
				}
			} else if command.Run() != nil {
				t.Fatalf("synthetic broker lifecycle failed: fixed stage %d", command.ProcessState.ExitCode())
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				launcher.mu.Lock()
				active, failed := launcher.active != nil, launcher.quarantined
				launcher.mu.Unlock()
				if failed {
					t.Fatal("ordinary resource retirement quarantined launcher")
				}
				if !active {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("broker loss left active worker resources")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
