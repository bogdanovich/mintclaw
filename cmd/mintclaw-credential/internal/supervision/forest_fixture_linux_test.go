//go:build linux

package supervision

import (
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

var helperMode = flag.String("credential-forest-helper", "", "synthetic forest fixture subprocess")

func TestForestFixtureHelper(t *testing.T) {
	if *helperMode == "" {
		t.Skip("only the synthetic Linux fixture invokes this helper")
	}
	switch *helperMode {
	case "deny":
		if root() {
			os.Exit(2)
		}
		if supervisor, err := Open(); supervisor != nil || !errors.Is(err, errUnavailable) {
			os.Exit(2)
		}
		os.Exit(0)
	case "descendant":
		signal.Ignore(syscall.SIGTERM)
		for {
			time.Sleep(time.Hour)
		}
	case "parent":
		child := fixtureCommand(t, "descendant")
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if child.Start() != nil {
			os.Exit(2)
		}
		endpoint := os.NewFile(3, "private-fixture-ready")
		if _, err := endpoint.Write([]byte{1}); err != nil {
			os.Exit(2)
		}
		var exit [1]byte
		if _, err := io.ReadFull(endpoint, exit[:]); err != nil || exit[0] != 1 {
			os.Exit(2)
		}
		// Deliberately leave a setsid descendant. Root supervision must retire it
		// after the original leader exits; a PID/process-group wait is insufficient.
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

func fixtureCommand(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("fixture executable unavailable")
	}
	command := exec.Command(executable, "-test.run=^TestForestFixtureHelper$", "-credential-forest-helper="+mode)
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "TZ=UTC"}
	return command
}

func fixtureParent(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || !strings.HasPrefix(string(data), "0::/") || len(data) > 4096 {
		t.Fatal("fixture cgroup unavailable")
	}
	location := strings.TrimSuffix(strings.TrimPrefix(string(data), "0::"), "\n")
	if strings.Contains(location, "\n") || location == "/" ||
		!strings.Contains(location, "mintclaw-credential-forest-qualification-") {
		t.Fatal("fixture requires its dedicated disposable systemd unit")
	}
	parent := filepath.Join("/sys/fs/cgroup", location, "workers")
	if os.Mkdir(parent, 0o700) != nil {
		t.Fatal("fixture parent creation failed")
	}
	t.Cleanup(func() {
		if os.Remove(parent) != nil {
			t.Error("fixture parent was not empty after cleanup")
		}
	})
	return parent
}

func fixtureStart(t *testing.T, supervisor *Supervisor, ctx context.Context) (*Forest, net.Conn) {
	t.Helper()
	files, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal("fixture endpoint unavailable")
	}
	parent, child := os.NewFile(uintptr(files[0]), "private-fixture-parent"),
		os.NewFile(uintptr(files[1]), "private-fixture-child")
	endpoint, err := net.FileConn(parent)
	if parent.Close() != nil || err != nil {
		_ = child.Close()
		t.Fatal("fixture endpoint conversion failed")
	}
	t.Cleanup(func() {
		if endpoint.Close() != nil {
			t.Error("fixture endpoint cleanup failed")
		}
	})
	command := fixtureCommand(t, "parent")
	command.ExtraFiles = []*os.File{child}
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
	forest, startErr := supervisor.Start(ctx, command)
	if child.Close() != nil || startErr != nil {
		t.Fatal("atomic cgroup worker start failed")
	}
	if endpoint.SetReadDeadline(time.Now().Add(3*time.Second)) != nil {
		t.Fatal("fixture readiness deadline unavailable")
	}
	var ready [1]byte
	if _, err := io.ReadFull(endpoint, ready[:]); err != nil || ready[0] != 1 {
		t.Fatal("fixture descendant did not start")
	}
	processes, err := readControl(forest.group, "cgroup.procs")
	if err != nil || len(strings.Fields(processes)) < 2 {
		t.Fatal("fixture did not witness the complete live process forest")
	}
	return forest, endpoint
}

func TestForestFixture(t *testing.T) {
	if os.Getenv("MINTCLAW_SYNTHETIC_WORKER_FOREST_FIXTURE") != "1" {
		t.Skip("run scripts/qualify-credential-browser-forest.sh on disposable Linux")
	}
	if !root() || os.Getenv("OP_SERVICE_ACCOUNT_TOKEN") != "" {
		t.Fatal("fixture requires Root without token material")
	}
	parent := fixtureParent(t)
	denied := fixtureCommand(t, "deny")
	denied.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
	if denied.Run() != nil {
		t.Fatal("non-Root public constructor was admitted")
	}
	supervisor, err := open(parent)
	if err != nil {
		t.Fatal("protected cgroup supervisor did not open")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if supervisor.Close(ctx) != nil {
			t.Error("supervisor cleanup was not verified")
		}
	})
	for _, mode := range []string{"explicit", "leader-exit", "cancellation"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			forest, endpoint := fixtureStart(t, supervisor, ctx)
			if next, err := supervisor.Start(
				ctx,
				fixtureCommand(t, "deny"),
			); next != nil ||
				!errors.Is(err, errUnavailable) {
				t.Fatal("second worker admitted before verified retirement")
			}
			switch mode {
			case "leader-exit":
				if _, err := endpoint.Write([]byte{1}); err != nil {
					t.Fatal("fixture leader exit request failed")
				}
				select {
				case <-forest.done:
				case <-time.After(3 * time.Second):
					t.Fatal("leader exit left an orphaned worker forest")
				}
			case "cancellation":
				cancel()
				select {
				case <-forest.done:
				case <-time.After(3 * time.Second):
					t.Fatal("cancellation did not retire the worker forest")
				}
			}
			stopCtx, stopCancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer stopCancel()
			if forest.Retire(stopCtx) != nil {
				t.Fatal("whole-forest retirement was not verified")
			}
			if forest.Retire(stopCtx) != nil {
				t.Fatal("verified retirement was not idempotent")
			}
			if _, err := os.Stat(filepath.Join(parent, forest.name)); !os.IsNotExist(err) {
				t.Fatal("retired cgroup could be populated again")
			}
		})
	}
	t.Run("token-environment-denied", func(t *testing.T) {
		command := fixtureCommand(t, "deny")
		command.Env = append(command.Env, "OP_SERVICE_ACCOUNT_TOKEN=synthetic-only")
		if forest, err := supervisor.Start(t.Context(), command); forest != nil || !errors.Is(err, errUnavailable) {
			t.Fatal("ambient token delivery was admitted")
		}
	})
	t.Run("cleanup-failure-quarantines", func(t *testing.T) {
		faultParent := filepath.Join(filepath.Dir(parent), "fault-workers")
		if os.Mkdir(faultParent, 0o700) != nil {
			t.Fatal("fault fixture parent creation failed")
		}
		faultSupervisor, err := open(faultParent)
		if err != nil {
			t.Fatal("fault fixture supervisor unavailable")
		}
		forest, _ := fixtureStart(t, faultSupervisor, t.Context())
		killFile := filepath.Join(faultParent, forest.name, "cgroup.kill")
		if os.Chmod(killFile, 0o666) != nil {
			t.Fatal("fault fixture unsafe control metadata failed")
		}
		stopCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		if !errors.Is(forest.Retire(stopCtx), errUnavailable) {
			t.Fatal("unsafe control metadata was accepted as cleanup evidence")
		}
		if next, err := faultSupervisor.Start(
			t.Context(),
			fixtureCommand(t, "deny"),
		); next != nil ||
			!errors.Is(err, errUnavailable) {
			t.Fatal("uncertain retirement admitted another worker")
		}
		if reopened, err := open(faultParent); reopened != nil || !errors.Is(err, errUnavailable) {
			t.Fatal("restart silently adopted leftover groups")
		}
		// Protected fixture repair restores the known synthetic control file and
		// verifies removal without restoring the quarantined supervisor's admission.
		if os.Chmod(killFile, 0o644) != nil || forest.killAndVerify(stopCtx) != nil ||
			faultSupervisor.parent.Close() != nil || os.Remove(faultParent) != nil {
			t.Fatal("protected fault fixture cleanup failed")
		}
	})
}
