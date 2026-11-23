//go:build linux

package isolation

import (
	"errors"
	"os/exec"
	"runtime"
	"sync"
)

// ParentBoundCommand retains the Linux thread that started a child with a
// parent-death signal. Its owner must call Wait after stopping the child or
// observing its exit.
type ParentBoundCommand struct {
	reap    chan struct{}
	reaped  chan struct{}
	wait    sync.Once
	waitErr error
}

// StartParentBoundCommand starts the command on a dedicated, locked thread.
// Linux delivers Pdeathsig on exit of the creating thread, so that thread
// stays alive until the caller has finished managing the original child.
func StartParentBoundCommand(command *exec.Cmd) (*ParentBoundCommand, error) {
	if command == nil || command.SysProcAttr == nil || command.SysProcAttr.Pdeathsig == 0 {
		return nil, errors.New("parent-bound command requires a parent-death signal")
	}
	owner := &ParentBoundCommand{reap: make(chan struct{}), reaped: make(chan struct{})}
	startup := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		err := command.Start()
		startup <- err
		if err != nil {
			return
		}
		<-owner.reap
		owner.waitErr = command.Wait()
		close(owner.reaped)
	}()
	if err := <-startup; err != nil {
		return nil, err
	}
	return owner, nil
}

// Wait reaps the original child. The owner can first drain an owned process
// group while the original leader's PID is still reserved.
func (owner *ParentBoundCommand) Wait() error {
	owner.wait.Do(func() { close(owner.reap) })
	<-owner.reaped
	return owner.waitErr
}
