//go:build linux

package desktop

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestX11OriginalTransportLossRevokesWithoutPolling(t *testing.T) {
	for _, mode := range []string{"read EOF", "read deadline", "write failure", "explicit close"} {
		t.Run(mode, func(t *testing.T) {
			connection, peer := net.Pipe()
			t.Cleanup(func() { _ = connection.Close(); _ = peer.Close() })
			guard := &x11Window{}
			lease := &x11InputLease{guard: guard, ended: make(chan struct{})}
			guard.input.Store(lease)
			transport := &x11ObservedTransport{Conn: connection, guard: guard}
			guard.socket = transport
			var value [1]byte
			var err error
			switch mode {
			case "read EOF":
				_ = peer.Close()
				_, err = transport.Read(value[:])
				if !errors.Is(err, io.EOF) {
					t.Fatal("original read failure was changed", err)
				}
			case "read deadline":
				_ = transport.SetReadDeadline(time.Now())
				_, err = transport.Read(value[:])
				var timeout net.Error
				if !errors.As(err, &timeout) || !timeout.Timeout() {
					t.Fatal("original read deadline was changed", err)
				}
			case "write failure":
				_ = peer.Close()
				_, err = transport.Write(value[:])
				if err == nil {
					t.Fatal("failed original write reported success")
				}
			case "explicit close":
				if err = transport.Close(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-lease.done():
			default:
				t.Fatal("transport loss required an event poll or another input call to revoke")
			}
			if !guard.lost.Load() || !lease.lost.Load() {
				t.Fatal("transport loss retained native authority")
			}
			// Resetting a deadline cannot resurrect XGB's terminated reader.
			_ = transport.SetDeadline(time.Time{})
			_ = peer.SetReadDeadline(time.Now().Add(time.Second))
			if _, err = peer.Read(value[:]); err == nil {
				t.Fatal("failed observer connection remained open")
			}
		})
	}
}

func TestX11OriginalTransportSuccessDoesNotRevoke(t *testing.T) {
	connection, peer := net.Pipe()
	t.Cleanup(func() { _ = connection.Close(); _ = peer.Close() })
	guard := &x11Window{}
	lease := &x11InputLease{guard: guard, ended: make(chan struct{})}
	guard.input.Store(lease)
	transport := &x11ObservedTransport{Conn: connection, guard: guard}
	guard.socket = transport
	_ = connection.SetDeadline(time.Now().Add(time.Second))
	_ = peer.SetDeadline(time.Now().Add(time.Second))
	result := make(chan error, 1)
	go func() {
		var value [1]byte
		if _, err := transport.Read(value[:]); err != nil {
			result <- err
			return
		}
		_, err := transport.Write(value[:])
		result <- err
	}()
	if _, err := peer.Write([]byte{42}); err != nil {
		t.Fatal(err)
	}
	var value [1]byte
	if _, err := peer.Read(value[:]); err != nil || value[0] != 42 {
		t.Fatal("observed connection changed native bytes", err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if guard.lost.Load() || lease.lost.Load() {
		t.Fatal("successful native I/O revoked the lease")
	}
}
