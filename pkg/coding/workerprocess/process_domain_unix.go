//go:build linux || darwin

package workerprocess

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const unixProcessDomainScanInterval = 25 * time.Millisecond

type unixProcessIdentity struct {
	pid     int
	started uint64
}

type unixProcessInfo struct {
	identity unixProcessIdentity
	parent   int
	exited   bool
}

type processDomain struct {
	command *exec.Cmd
	state   *unixProcessDomainState
}

type unixProcessDomainState struct {
	command *exec.Cmd

	mu      sync.Mutex
	root    unixProcessIdentity
	owned   map[unixProcessIdentity]struct{}
	monitor chan struct{}
	done    chan struct{}
	running bool

	cleanupOnce sync.Once
	cleanupDone chan struct{}
	cleanupErr  error
}

func prepareProcessDomain(command *exec.Cmd) (processDomain, error) {
	if command == nil {
		return processDomain{}, errors.New("coding worker command is required")
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return processDomain{
		command: command,
		state: &unixProcessDomainState{
			command:     command,
			owned:       make(map[unixProcessIdentity]struct{}),
			monitor:     make(chan struct{}),
			done:        make(chan struct{}),
			cleanupDone: make(chan struct{}),
		},
	}, nil
}

func (domain processDomain) started() error {
	if domain.command == nil || domain.command.Process == nil || domain.state == nil {
		return errors.New("coding worker process domain started without a process")
	}
	table, err := snapshotUnixProcessTable()
	if err != nil {
		return fmt.Errorf("inspect coding worker process domain: %w", err)
	}
	root, ok := table[domain.command.Process.Pid]
	if !ok || root.exited {
		return errors.New("coding worker exited before process-domain ownership was established")
	}
	domain.state.mu.Lock()
	domain.state.root = root.identity
	domain.state.owned[root.identity] = struct{}{}
	domain.state.absorbLocked(table)
	domain.state.running = true
	domain.state.mu.Unlock()
	go domain.state.monitorProcesses()
	return nil
}

func (domain processDomain) stop(timeout time.Duration) error {
	return domain.cleanup(true, timeout)
}

func (domain processDomain) close() error {
	return domain.cleanup(false, DefaultStopTimeout)
}

func (domain processDomain) cleanup(terminateLeader bool, timeout time.Duration) error {
	if domain.state == nil {
		return nil
	}
	domain.state.cleanupOnce.Do(func() {
		domain.state.cleanupErr = domain.state.drain(terminateLeader, timeout)
		close(domain.state.cleanupDone)
	})
	<-domain.state.cleanupDone
	return domain.state.cleanupErr
}

func (state *unixProcessDomainState) monitorProcesses() {
	// A process group catches the normal case cheaply, while the identity-pinned
	// table retains descendants that call setsid or change groups. Workers are
	// task-scoped and short lived, so observation ends with the generation.
	ticker := time.NewTicker(unixProcessDomainScanInterval)
	defer ticker.Stop()
	defer close(state.done)
	for {
		select {
		case <-state.monitor:
			return
		case <-ticker.C:
			_ = state.capture()
		}
	}
}

func (state *unixProcessDomainState) capture() error {
	table, err := snapshotUnixProcessTable()
	if err != nil {
		return err
	}
	state.mu.Lock()
	state.absorbLocked(table)
	state.mu.Unlock()
	return nil
}

func (state *unixProcessDomainState) absorbLocked(table map[int]unixProcessInfo) {
	// Retain identities after reparenting. A PID only remains an ownership root
	// when its platform start token still matches, preventing PID-reuse capture.
	liveOwners := make(map[int]struct{}, len(state.owned))
	for identity := range state.owned {
		if process, ok := table[identity.pid]; ok && process.identity == identity && !process.exited {
			liveOwners[identity.pid] = struct{}{}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, process := range table {
			if process.exited {
				continue
			}
			if _, owned := liveOwners[process.identity.pid]; owned {
				continue
			}
			if _, parentOwned := liveOwners[process.parent]; !parentOwned {
				continue
			}
			state.owned[process.identity] = struct{}{}
			liveOwners[process.identity.pid] = struct{}{}
			changed = true
		}
	}
}

func (state *unixProcessDomainState) drain(terminateLeader bool, timeout time.Duration) error {
	state.stopMonitor()
	if state.command == nil || state.command.Process == nil || state.command.Process.Pid <= 0 {
		return nil
	}
	if timeout <= 0 {
		timeout = DefaultStopTimeout
	}
	deadline := time.Now().Add(timeout)
	emptyObservations := 0
	var observationErr error
	for {
		table, err := snapshotUnixProcessTable()
		if err == nil {
			observationErr = nil
			state.mu.Lock()
			state.absorbLocked(table)
			root := state.root
			live := state.liveOwnedLocked(table)
			state.mu.Unlock()

			for _, process := range live {
				if process.identity != root {
					_ = syscall.Kill(process.identity.pid, syscall.SIGKILL)
				}
			}
			if terminateLeader || len(live) > 1 {
				_ = syscall.Kill(-state.command.Process.Pid, syscall.SIGKILL)
			}
			if terminateLeader {
				leaderErr := state.command.Process.Kill()
				if leaderErr != nil && !errors.Is(leaderErr, os.ErrProcessDone) {
					return fmt.Errorf("terminate coding worker leader: %w", leaderErr)
				}
			}

			remaining := 0
			for _, process := range live {
				if process.identity != root || terminateLeader {
					remaining++
				}
			}
			if remaining == 0 {
				emptyObservations++
				if emptyObservations >= 2 {
					return nil
				}
			} else {
				emptyObservations = 0
			}
		} else {
			observationErr = err
			_ = syscall.Kill(-state.command.Process.Pid, syscall.SIGKILL)
			if terminateLeader {
				_ = state.command.Process.Kill()
			}
		}
		if time.Now().After(deadline) {
			if observationErr != nil {
				return fmt.Errorf("inspect coding worker process domain: %w", observationErr)
			}
			return errors.New("coding worker process domain remained alive after termination")
		}
		time.Sleep(unixProcessDomainScanInterval)
	}
}

func (state *unixProcessDomainState) liveOwnedLocked(
	table map[int]unixProcessInfo,
) []unixProcessInfo {
	live := make([]unixProcessInfo, 0, len(state.owned))
	for identity := range state.owned {
		process, ok := table[identity.pid]
		if ok && process.identity == identity && !process.exited {
			live = append(live, process)
		}
	}
	return live
}

func (state *unixProcessDomainState) stopMonitor() {
	state.mu.Lock()
	running := state.running
	if running {
		state.running = false
		close(state.monitor)
	}
	state.mu.Unlock()
	if running {
		<-state.done
	}
}
