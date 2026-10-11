package supervision

import (
	"context"
	"errors"
	"runtime"
	"testing"
)

func TestNetworkUnavailableStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, denied := range []context.Context{nil, ctx} {
		if _, err := OpenNetwork(denied); !errors.Is(err, errUnavailable) {
			t.Fatal("canceled network setup admitted")
		}
	}
	if runtime.GOOS != "linux" {
		if _, err := OpenNetwork(context.Background()); !errors.Is(err, errUnavailable) {
			t.Fatal("unsupported host admitted")
		}
	}
	var network *Network
	if _, _, err := network.Borrow(context.Background()); !errors.Is(err, errUnavailable) {
		t.Fatal("missing network borrowed")
	}
	if network.Close() != nil {
		t.Fatal("empty network close failed")
	}
}
