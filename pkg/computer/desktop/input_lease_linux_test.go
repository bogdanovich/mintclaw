//go:build linux

package desktop

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/jezek/xgb/xproto"
	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

type fakeSelectionTransport struct {
	calls      []string
	fail       string
	owners     []xproto.Window
	ownerCalls int
	claimed    xproto.Window
	grabbed    bool
	published  bool
	aborted    bool
}

func (protocol *fakeSelectionTransport) step(name string) error {
	protocol.calls = append(protocol.calls, name)
	if name == protocol.fail {
		return errors.New("synthetic native failure")
	}
	return nil
}

func (protocol *fakeSelectionTransport) grab() error {
	// A lost acknowledgement can mean the server accepted the grab.
	protocol.grabbed = true
	return protocol.step("grab")
}

func (protocol *fakeSelectionTransport) owner() (xproto.Window, error) {
	protocol.ownerCalls++
	name := "owner-before"
	if protocol.ownerCalls > 1 {
		name = "owner-after"
	}
	if err := protocol.step(name); err != nil {
		return 0, err
	}
	return protocol.owners[protocol.ownerCalls-1], nil
}

func (protocol *fakeSelectionTransport) claim(window xproto.Window) error {
	protocol.claimed = window
	return protocol.step("claim")
}

func (protocol *fakeSelectionTransport) publish() {
	protocol.calls = append(protocol.calls, "publish")
	protocol.published = true
}

func (protocol *fakeSelectionTransport) ungrab() error {
	if err := protocol.step("ungrab"); err != nil {
		return err
	}
	protocol.grabbed = false
	return nil
}

func (protocol *fakeSelectionTransport) abort() {
	protocol.calls = append(protocol.calls, "abort")
	protocol.aborted = true
	protocol.grabbed = false
}

func TestX11InputClaimIsAtomicAndPublishesBeforeEvents(t *testing.T) {
	protocol := &fakeSelectionTransport{owners: []xproto.Window{0, 123}}
	if err := claimX11InputSelection(protocol, 123); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(protocol.calls, []string{
		"grab", "owner-before", "claim", "owner-after", "publish", "ungrab",
	}) || protocol.grabbed || !protocol.published || protocol.aborted {
		t.Fatal("input lease claim escaped the atomic server transaction", protocol.calls)
	}
}

func TestX11InputClaimNeverStealsExistingSelection(t *testing.T) {
	protocol := &fakeSelectionTransport{owners: []xproto.Window{456}}
	if !errors.Is(claimX11InputSelection(protocol, 123), computer.ErrBusy) ||
		!reflect.DeepEqual(protocol.calls, []string{"grab", "owner-before", "ungrab"}) ||
		protocol.claimed != 0 || protocol.published || protocol.grabbed || protocol.aborted {
		t.Fatal("busy transaction stole, published or failed to release server grab", protocol.calls)
	}
}

func TestX11InputClaimAmbiguityAbortsOriginalConnection(t *testing.T) {
	for _, failure := range []string{"grab", "owner-before", "claim", "owner-after", "ungrab"} {
		t.Run(failure, func(t *testing.T) {
			protocol := &fakeSelectionTransport{owners: []xproto.Window{0, 123}, fail: failure}
			if !errors.Is(claimX11InputSelection(protocol, 123), computer.ErrUnavailable) ||
				!protocol.aborted || protocol.grabbed {
				t.Fatal("uncertain server transaction retained ownership/grab", protocol.calls)
			}
			if protocol.calls[len(protocol.calls)-1] != "abort" {
				t.Fatal("ambiguous input admission attempted more native work after abort")
			}
		})
	}
	protocol := &fakeSelectionTransport{owners: []xproto.Window{0, 999}}
	if !errors.Is(claimX11InputSelection(protocol, 123), computer.ErrUnavailable) || !protocol.aborted ||
		protocol.published || protocol.grabbed {
		t.Fatal("mismatched selection owner admitted ownership", protocol.calls)
	}
	busyUngrab := &fakeSelectionTransport{owners: []xproto.Window{456}, fail: "ungrab"}
	if !errors.Is(claimX11InputSelection(busyUngrab, 123), computer.ErrUnavailable) || !busyUngrab.aborted ||
		busyUngrab.grabbed || busyUngrab.claimed != 0 {
		t.Fatal("busy owner plus lost ungrab acknowledgement failed open")
	}
}

func TestX11InputTransactionBoundsNativeWaitAndCancellation(t *testing.T) {
	for _, cancelDuringCall := range []bool{false, true} {
		t.Run(
			map[bool]string{false: "socket deadline", true: "caller cancellation"}[cancelDuringCall],
			func(t *testing.T) {
				connection, peer := net.Pipe()
				t.Cleanup(func() { _ = connection.Close(); _ = peer.Close() })
				guard := &x11Window{socket: connection}
				lease := &x11InputLease{guard: guard, ended: make(chan struct{})}
				guard.input.Store(lease)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				started := time.Now()
				err := guard.inputTransaction(ctx, func() error {
					if cancelDuringCall {
						cancel()
					}
					_, err := connection.Write([]byte{1}) // peer intentionally never reads
					return err
				})
				if err == nil || time.Since(started) > time.Second || !guard.lost.Load() || !lease.lost.Load() {
					t.Fatal("uncertain native transaction retained authority or stalled", err, time.Since(started))
				}
				if cancelDuringCall && !errors.Is(err, context.Canceled) {
					t.Fatal("caller cancellation was not reported", err)
				}
				select {
				case <-lease.done():
				default:
					t.Fatal("failed transaction did not revoke lease")
				}
			},
		)
	}
}

func TestX11InputTransactionSuccessCannotPoisonLaterNativeCalls(t *testing.T) {
	for _, result := range []error{nil, computer.ErrBusy} {
		connection, peer := net.Pipe()
		t.Cleanup(func() { _ = connection.Close(); _ = peer.Close() })
		guard := &x11Window{socket: connection}
		ctx, cancel := context.WithCancel(t.Context())
		if err := guard.inputTransaction(ctx, func() error { return result }); !errors.Is(err, result) {
			t.Fatal("successful or busy transaction changed result", err)
		}
		cancel() // must not trigger the completed transaction's native abort callback
		written := make(chan error, 1)
		go func() { _, err := connection.Write([]byte{42}); written <- err }()
		_ = peer.SetReadDeadline(time.Now().Add(time.Second))
		var value [1]byte
		if _, err := peer.Read(value[:]); err != nil || value[0] != 42 || guard.lost.Load() {
			t.Fatal("late cancellation closed or poisoned the retained native connection", err)
		}
		if err := <-written; err != nil {
			t.Fatal(err)
		}
	}
}

func TestX11InputTransactionPreCanceledDoesNotTouchOriginal(t *testing.T) {
	connection, peer := net.Pipe()
	t.Cleanup(func() { _ = connection.Close(); _ = peer.Close() })
	guard := &x11Window{socket: connection}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := guard.inputTransaction(ctx, func() error {
		t.Fatal("pre-canceled transaction performed native I/O")
		return nil
	})
	if !errors.Is(err, context.Canceled) || guard.lost.Load() {
		t.Fatal("pre-canceled acquisition changed the original guard")
	}
}

func TestX11InputProcessLossUsesOriginalHandleNotWindowDestruction(t *testing.T) {
	for _, test := range []struct {
		name   string
		events int16
		err    error
		lost   bool
	}{
		{name: "alive"},
		{name: "interrupted", err: unix.EINTR},
		{name: "retained-window owner exited", events: unix.POLLIN, lost: true},
		{name: "broken handle", err: unix.EBADF, lost: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			guard := &x11Window{pidfd: 42}
			lease := &x11InputLease{guard: guard, ended: make(chan struct{})}
			guard.input.Store(lease)
			calls := 0
			guard.pollInputProcess(func(fds []unix.PollFd, timeout int) (int, error) {
				calls++
				if len(fds) != 1 || fds[0].Fd != 42 || fds[0].Events != unix.POLLIN || timeout != 0 {
					t.Fatal("input process observer changed native identity or blocked")
				}
				fds[0].Revents = test.events
				return 0, test.err
			})
			if calls != 1 || guard.lost.Load() != test.lost || lease.lost.Load() != test.lost {
				t.Fatal("process loss or interruption was misclassified")
			}
		})
	}
	(&x11Window{}).pollInputProcess(func([]unix.PollFd, int) (int, error) {
		t.Fatal("read-only observer without input changed its native polling behavior")
		return 0, nil
	})
}
