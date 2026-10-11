//go:build linux

package transport

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
)

func TestLoginIntentSurvivesShortControlDeadline(t *testing.T) {
	var calls atomic.Int32
	fixture := newTransportFixture(t, func(ctx context.Context, call Call) (intent.Response, error) {
		calls.Add(1)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 75*time.Second || call.Authorize() != nil {
			return intent.Response{}, errors.New("login authority was not bounded")
		}
		select {
		case <-time.After(5250 * time.Millisecond):
		case <-ctx.Done():
			return intent.Response{Code: intent.OutcomeUnknown}, nil
		}
		if err := call.Authorize(); err != nil {
			return intent.Response{}, err
		}
		return intent.Response{Code: intent.LoggedIn, SessionID: testReference}, nil
	})
	response, err := fixture.client.Call(context.Background(), intent.Request{
		Operation: intent.Login, Account: "facebook", GrantID: testReference,
	})
	if err != nil || response.Code != intent.LoggedIn || response.SessionID != testReference || calls.Load() != 1 {
		t.Fatal("one bounded login could not finish beyond the short control deadline")
	}
}

func TestIncompleteIntentKeepsShortReadDeadline(t *testing.T) {
	var calls atomic.Int32
	fixture := newTransportFixture(t, func(context.Context, Call) (intent.Response, error) {
		calls.Add(1)
		return intent.Response{Code: intent.Ready}, nil
	})
	connection, err := net.DialUnix("unixpacket", nil,
		&net.UnixAddr{Name: fixture.client.SocketPath, Net: "unixpacket"})
	if err != nil {
		t.Fatal("incomplete-request fixture connection failed")
	}
	defer func() { _ = connection.Close() }()
	started := time.Now()
	if connection.SetReadDeadline(started.Add(7*time.Second)) != nil {
		t.Fatal("incomplete-request fixture deadline failed")
	}
	var body [1]byte
	_, _ = connection.Read(body[:])
	elapsed := time.Since(started)
	if elapsed < 4*time.Second || elapsed >= 7*time.Second || calls.Load() != 0 {
		t.Fatal("an incomplete packet gained the login deadline or handler authority")
	}
}
