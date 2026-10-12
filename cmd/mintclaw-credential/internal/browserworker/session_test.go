package browserworker

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/internal/credentials/approval"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

func TestConnectWithoutEnforcingProxyRetiresOwnedWorker(t *testing.T) {
	connection, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	var stops atomic.Int32
	session, err := Connect(context.Background(), connection, Config{}, approval.LoginLease{},
		func(context.Context) (credentialbroker.Request, error) { return credentialbroker.Request{}, nil },
		func(context.Context, credentialbroker.LoginFlow) error { return nil }, nil,
		func(ctx context.Context) error { stops.Add(1); return ctx.Err() })
	if session != nil || !errors.Is(err, errUnavailable) || stops.Load() != 1 {
		t.Fatal("missing enforcing proxy retained private worker authority")
	}
	_ = peer.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err = peer.Write([]byte{1}); err == nil {
		t.Fatal("missing enforcing proxy left its inherited channel open")
	}
}
