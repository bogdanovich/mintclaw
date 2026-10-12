//go:build linux

package browserworker

import (
	"context"
	"net"
	"os"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw-credential/internal/approval"
	"github.com/bogdanovich/mintclaw/cmd/mintclaw-credential/internal/egress"
	"github.com/bogdanovich/mintclaw/cmd/mintclaw-credential/internal/runtimeimage"
	"github.com/bogdanovich/mintclaw/cmd/mintclaw-credential/internal/supervision"
)

type supervisedAllocation interface {
	EndpointFiles(context.Context) (*os.File, *os.File, error)
	Start(context.Context) error
	Retire(context.Context) error
	Close()
	Done() <-chan struct{}
}

type supervisedProxy interface {
	networkBoundary
	Serve(context.Context, net.Listener) error
}

type ownedProxy struct{ proxyBoundary }

func (proxy ownedProxy) Serve(ctx context.Context, listener net.Listener) error {
	return proxy.proxy.Serve(ctx, listener)
}

// StartSupervised composes the fixed Root allocation, actual approved lease,
// enforcing proxy and private channel. Trusted startup must separately qualify
// the protected host, named roles and authentic runtime before reaching it.
// It retrieves no secret, accepts no executable/path and exposes no agent tool.
func StartSupervised(ctx context.Context, lease approval.LoginLease,
	current CurrentScope, submit AdmitSubmission,
) (*Session, error) {
	return startSupervised(ctx, lease, current, submit,
		func(ctx context.Context) (supervisedAllocation, error) { return supervision.AllocateResources(ctx) },
		func(ctx context.Context) (supervisedProxy, error) {
			proxy, err := egress.NewLoginProxy(ctx, lease, egress.CurrentScope(current))
			if err != nil {
				return nil, errUnavailable
			}
			return ownedProxy{proxyBoundary{proxy, lease}}, nil
		})
}

// Test factories are private. The public entry uses only the fixed production
// allocation and actual signed lease/proxy, never caller-supplied resources.
func startSupervised(ctx context.Context, lease loginLease, current CurrentScope, submit AdmitSubmission,
	allocate func(context.Context) (supervisedAllocation, error),
	newProxy func(context.Context) (supervisedProxy, error),
) (result *Session, resultErr error) {
	var allocation supervisedAllocation
	var listener net.Listener
	var connection net.Conn
	var proxy supervisedProxy
	var cancel context.CancelFunc
	var watchDone chan struct{}
	var started bool
	var stop func(context.Context) error
	defer func() {
		if recover() != nil {
			result, resultErr = nil, errUnavailable
		}
		if resultErr == nil {
			return
		}
		if started && stop != nil {
			_ = terminate(stop)
		}
		if proxy != nil {
			_ = proxy.Close()
		}
		if listener != nil {
			_ = listener.Close()
		}
		if connection != nil {
			_ = connection.Close()
		}
		if allocation != nil {
			allocation.Close()
		}
		if cancel != nil {
			cancel()
		}
		if watchDone != nil {
			<-watchDone
		}
	}()
	if ctx == nil || ctx.Err() != nil || lease == nil || lease.Done() == nil || current == nil ||
		submit == nil || allocate == nil || newProxy == nil {
		return nil, errUnavailable
	}
	select {
	case <-lease.Done():
		return nil, errUnavailable
	default:
	}
	checkCtx, endCheck := context.WithTimeout(ctx, time.Second)
	defer endCheck()
	request, err := current(checkCtx)
	if err != nil {
		return nil, errUnavailable
	}
	if _, err = lease.Flow(checkCtx, request); err != nil || checkCtx.Err() != nil {
		return nil, errUnavailable
	}
	// This original caller lifetime remains attached to Root. Grant withdrawal
	// closes network authority immediately through the proxy/channel; their
	// bounded private close protocol can still ask Root to verify retirement.
	resourceCtx, endResources := context.WithCancel(ctx)
	cancel = endResources
	allocation, err = allocate(resourceCtx)
	if err != nil || allocation == nil || allocation.Done() == nil {
		return nil, errUnavailable
	}
	listenerFile, controlFile, err := allocation.EndpointFiles(resourceCtx)
	defer func() {
		if listenerFile != nil {
			_ = listenerFile.Close()
		}
		if controlFile != nil {
			_ = controlFile.Close()
		}
	}()
	if err != nil || listenerFile == nil || controlFile == nil {
		return nil, errUnavailable
	}
	listener, err = net.FileListener(listenerFile)
	if err != nil {
		return nil, errUnavailable
	}
	connection, err = net.FileConn(controlFile)
	if err != nil {
		return nil, errUnavailable
	}
	_ = listenerFile.Close()
	_ = controlFile.Close()
	proxy, err = newProxy(resourceCtx)
	if err != nil || proxy == nil {
		return nil, errUnavailable
	}
	watchDone = make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-allocation.Done():
		case <-resourceCtx.Done():
		}
		_ = proxy.Close()
		_ = listener.Close()
		_ = connection.Close()
		allocation.Close()
		cancel()
	}()
	var stopOnce sync.Once
	var retirementErr error
	stop = func(cleanupCtx context.Context) error {
		stopOnce.Do(func() {
			_ = proxy.Close()
			_ = listener.Close()
			_ = connection.Close()
			if allocation.Retire(cleanupCtx) != nil {
				retirementErr = errUnavailable
			}
			allocation.Close()
			cancel()
			<-watchDone
		})
		return retirementErr
	}
	ready := &servingListener{Listener: listener, ready: make(chan struct{})}
	serveDone := make(chan error, 1)
	go func() { serveDone <- proxy.Serve(resourceCtx, ready) }()
	setupCtx, endSetup := context.WithTimeout(resourceCtx, 20*time.Second)
	defer endSetup()
	select {
	case <-ready.ready:
	case <-serveDone:
		return nil, errUnavailable
	case <-setupCtx.Done():
		return nil, errUnavailable
	case <-lease.Done():
		return nil, errUnavailable
	}
	if proxy.Match(setupCtx, request) != nil {
		return nil, errUnavailable
	}
	if _, _, err = proxy.Endpoint(setupCtx); err != nil || allocation.Start(setupCtx) != nil {
		return nil, errUnavailable
	}
	started = true
	return connect(resourceCtx, connection, Config{ExecutablePath: "/" + runtimeimage.ChromiumPath},
		lease, current, submit, proxy, stop)
}

// First Accept proves Serve has adopted the listener before the fixed worker
// starts. A failed Serve returns separately; goroutine scheduling is no proof.
type servingListener struct {
	net.Listener
	once  sync.Once
	ready chan struct{}
}

func (listener *servingListener) Accept() (net.Conn, error) {
	listener.once.Do(func() { close(listener.ready) })
	return listener.Listener.Accept()
}
