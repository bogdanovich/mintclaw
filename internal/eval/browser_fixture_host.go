package eval

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

// browserFixtureHost owns one stable listener across serial trials. The native
// profile can pin this origin without a bind/close/rebind gap between cases.
// Callers must retain the host until native work is quiescent; neither sealing
// a trial nor closing this local server proves native browser cleanup.
type browserFixtureHost struct {
	origin   string
	listener net.Listener
	server   *http.Server
	done     chan struct{}

	mu     sync.Mutex
	trials map[string]*browserFixture
	active string
	closed bool
	failed bool

	closeOnce sync.Once
	closeErr  error
}

func (*browserFixtureHost) MarshalJSON() ([]byte, error) { return nil, ErrBrowserFixture }

func newBrowserFixtureHost(address string) (*browserFixtureHost, error) {
	host, port, err := net.SplitHostPort(address)
	number, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || strconv.Itoa(number) != port ||
		host != "127.0.0.1" && host != "::1" || address != net.JoinHostPort(host, port) ||
		number != 0 && (number < 1024 || number > 65535) {
		return nil, ErrBrowserFixture
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, ErrBrowserFixture
	}
	origin := "http://" + listener.Addr().String()
	if _, err = config.NormalizeBrowserNetworkOrigin(config.BrowserNetworkLoopbackFixture, origin); err != nil {
		_ = listener.Close()
		return nil, ErrBrowserFixture
	}
	owner := &browserFixtureHost{
		origin: origin, listener: listener, done: make(chan struct{}), trials: make(map[string]*browserFixture),
	}
	owner.server = &http.Server{
		Handler: owner, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second,
		WriteTimeout: 2 * time.Second, IdleTimeout: 2 * time.Second, MaxHeaderBytes: 8192,
		ErrorLog: log.New(io.Discard, "", 0),
	}
	go owner.serve()
	return owner, nil
}

func (host *browserFixtureHost) serve() {
	defer close(host.done)
	if err := host.server.Serve(host.listener); !errors.Is(err, http.ErrServerClosed) {
		host.mu.Lock()
		host.failed = true
		for _, fixture := range host.trials {
			fixture.mu.Lock()
			fixture.state.failed = true
			fixture.mu.Unlock()
		}
		host.mu.Unlock()
	}
}

// Retain retired identities and witnesses for the run's bounded cohort. A
// completed path cannot be reused, replaced, or routed to a later trial.
func (host *browserFixtureHost) newTrial(sessionID string) (*browserFixture, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closed || host.failed || len(host.trials) >= MaxCorpusCases || host.trials[sessionID] != nil {
		return nil, ErrBrowserFixture
	}
	if previous := host.trials[host.active]; previous != nil {
		previous.mu.Lock()
		closed := previous.state.closed
		previous.mu.Unlock()
		if !closed {
			return nil, ErrBrowserFixture
		}
	}
	fixture, err := makeBrowserFixture(sessionID, host.origin)
	if err != nil {
		return nil, err
	}
	host.trials[sessionID], host.active = fixture, sessionID
	return fixture, nil
}

func (host *browserFixtureHost) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if request == nil || request.URL == nil || request.Host != strings.TrimPrefix(host.origin, "http://") ||
		request.URL.RawQuery != "" || request.URL.ForceQuery || request.URL.RawPath != "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	path, prefix := strings.CutPrefix(request.URL.Path, "/eval/")
	id, _, slash := strings.Cut(path, "/")
	if !prefix || !slash {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	host.mu.Lock()
	fixture, active, closed, failed := host.trials[id], id == host.active, host.closed, host.failed
	host.mu.Unlock()
	switch {
	case closed:
		w.WriteHeader(http.StatusGone)
	case failed:
		w.WriteHeader(http.StatusServiceUnavailable)
	case fixture == nil:
		w.WriteHeader(http.StatusNotFound)
	case !active:
		w.WriteHeader(http.StatusGone)
	default:
		fixture.ServeHTTP(w, request)
	}
}

func (host *browserFixtureHost) close() error {
	host.closeOnce.Do(func() {
		host.mu.Lock()
		host.closed = true
		for _, fixture := range host.trials {
			fixture.seal()
		}
		host.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := host.server.Shutdown(ctx); err != nil {
			host.closeErr = ErrBrowserFixture
			_ = host.server.Close()
		}
		<-host.done
		host.mu.Lock()
		if host.failed {
			host.closeErr = ErrBrowserFixture
		}
		if host.closeErr != nil {
			for _, fixture := range host.trials {
				fixture.mu.Lock()
				fixture.state.failed = true
				fixture.mu.Unlock()
			}
		}
		host.mu.Unlock()
	})
	return host.closeErr
}
