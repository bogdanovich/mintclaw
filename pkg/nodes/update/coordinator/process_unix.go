//go:build linux || darwin

package coordinator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/nodes/update/control"
)

const (
	childStopTimeout = 10 * time.Second
	fixedChildPath   = "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
)

type managedChild struct {
	command  *exec.Cmd
	file     *os.File
	codec    *control.Codec
	incoming chan control.Incoming
	done     chan error
	stopOnce sync.Once
}

func (child *managedChild) incomingFrames() <-chan control.Incoming { return child.incoming }
func (child *managedChild) completion() <-chan error                { return child.done }

func (store *Store) launchSelected(ctx context.Context, state State) (*managedChild, error) {
	if err := store.verifyPayload(state.Active, state.Installation); err != nil {
		return nil, err
	}
	payloadName, err := payloadFileName(state.Active.Slot)
	if err != nil {
		return nil, err
	}
	payloadPath := filepath.Join(store.root.Name(), payloadName)
	home, err := coordinatorHomeDirectory()
	if err != nil {
		return nil, err
	}
	sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("create companion control channel: %w", err)
	}
	unix.CloseOnExec(sockets[0])
	unix.CloseOnExec(sockets[1])
	parent := os.NewFile(uintptr(sockets[0]), "coordinator-parent")
	childFile := os.NewFile(uintptr(sockets[1]), "coordinator-child")
	if parent == nil || childFile == nil {
		if parent != nil {
			_ = parent.Close()
		}
		if childFile != nil {
			_ = childFile.Close()
		}
		return nil, errors.New("open companion control channel")
	}
	command := exec.CommandContext(ctx, payloadPath, "run", "--config", state.Installation.ConfigPath)
	command.Env = []string{
		"HOME=" + home,
		"PATH=" + fixedChildPath,
		"TMPDIR=/tmp",
		control.EnvironmentFD + "=3",
	}
	command.ExtraFiles = []*os.File{childFile}
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	configureChildProcess(command)
	if err = command.Start(); err != nil {
		_ = childFile.Close()
		_ = parent.Close()
		return nil, errors.New("start selected companion payload")
	}
	if err = childFile.Close(); err != nil {
		_ = command.Process.Kill()
		_ = parent.Close()
		_ = command.Wait()
		return nil, fmt.Errorf("close coordinator child descriptor: %w", err)
	}
	codec, err := control.NewCodec(parent, parent)
	if err != nil {
		_ = command.Process.Kill()
		_ = parent.Close()
		_ = command.Wait()
		return nil, err
	}
	child := &managedChild{
		command: command, file: parent, codec: codec,
		incoming: make(chan control.Incoming, 1), done: make(chan error, 1),
	}
	go child.readIncoming()
	go func() {
		child.done <- command.Wait()
		close(child.done)
		_ = parent.Close()
	}()
	return child, nil
}

func coordinatorHomeDirectory() (string, error) {
	account, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil || account == nil || !filepath.IsAbs(account.HomeDir) ||
		filepath.Clean(account.HomeDir) != account.HomeDir {
		return "", errors.New("resolve coordinator account home directory")
	}
	return account.HomeDir, nil
}

func (child *managedChild) readIncoming() {
	defer close(child.incoming)
	for {
		incoming, err := child.codec.ReadIncoming(time.Now().UTC())
		if err != nil {
			return
		}
		child.incoming <- incoming
	}
}

func (child *managedChild) respond(response control.Response) error {
	return child.codec.WriteResponse(response)
}

func (child *managedChild) stop() {
	child.stopOnce.Do(func() {
		if child.command.Process == nil {
			return
		}
		_ = child.command.Process.Signal(syscall.SIGTERM)
		timer := time.NewTimer(childStopTimeout)
		defer timer.Stop()
		select {
		case <-child.done:
		case <-timer.C:
			_ = child.command.Process.Kill()
			<-child.done
		}
	})
}
