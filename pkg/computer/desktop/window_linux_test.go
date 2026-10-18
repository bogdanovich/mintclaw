//go:build linux

package desktop

import (
	"context"
	"fmt"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPIDFDAliveRetriesInterruptedSameHandle(t *testing.T) {
	calls := 0
	alive := pidfdAlive(t.Context(), 23, func(descriptors []unix.PollFd, timeout int) (int, error) {
		calls++
		if len(descriptors) != 1 || descriptors[0].Fd != 23 || descriptors[0].Events != unix.POLLIN || timeout != 0 {
			t.Fatal("poll selected a different process or blocking timeout")
		}
		if descriptors[0].Revents != 0 {
			t.Fatal("interrupted poll retained partial event state")
		}
		if calls < 3 {
			descriptors[0].Revents = unix.POLLIN
			return -1, fmt.Errorf("interrupted fixture: %w", unix.EINTR)
		}
		return 0, nil
	})
	if !alive || calls != 3 {
		t.Fatal("signal interruption invalidated the retained live process", alive, calls)
	}
}

func TestPIDFDAliveRefusesExitAndOtherErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		events int16
		err    error
	}{
		{name: "exit", events: unix.POLLIN},
		{name: "hangup", events: unix.POLLHUP},
		{name: "error", events: unix.POLLERR},
		{name: "invalid descriptor", events: unix.POLLNVAL},
		{name: "syscall error", err: unix.EBADF},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			if pidfdAlive(t.Context(), 23, func(descriptors []unix.PollFd, _ int) (int, error) {
				calls++
				descriptors[0].Revents = test.events
				return 1, test.err
			}) || calls != 1 {
				t.Fatal("native loss was retried or accepted", calls)
			}
		})
	}
}

func TestPIDFDAliveCancellationStopsInterruptedPoll(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	if pidfdAlive(ctx, 23, func([]unix.PollFd, int) (int, error) {
		calls++
		cancel()
		return -1, unix.EINTR
	}) || calls != 1 {
		t.Fatal("canceled interruption loop continued", calls)
	}
	if pidfdAlive(ctx, 23, func([]unix.PollFd, int) (int, error) {
		t.Fatal("pre-canceled check performed native I/O")
		return 0, nil
	}) {
		t.Fatal("canceled process check accepted")
	}
}
