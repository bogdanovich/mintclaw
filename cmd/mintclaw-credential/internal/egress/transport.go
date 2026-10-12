// Package egress owns private credential-consumer HTTPS connections and the
// enforcing private login proxy. It creates no listener, SDK client, or model
// tool. Startup must qualify process isolation and supply any proxy listener.
package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/internal/credentials/approval"
	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

var errUnavailable = errors.New("private credential connection unavailable")

// CurrentScope reads authenticated runtime identity, never request/model fields.
// Only the origin comes from this transport's immutable approved endpoint.
type CurrentScope func(context.Context) (credentialbroker.Request, error)

type loginLease interface {
	Binding(context.Context, credentialbroker.Request) (approval.LoginBinding, error)
	Done() <-chan struct{}
}

type (
	resolver func(context.Context, string) ([]netip.Addr, error)
	dialer   func(context.Context, string, string) (net.Conn, error)
)

// Transport admits one canonical HTTPS origin and one original grant generation.
// All response bytes stay inside a qualified private consumer; callers must not
// project responses, cookies, URLs, TLS state, or diagnostics into model output.
type Transport struct {
	ctx         context.Context
	cancel      context.CancelFunc
	lease       loginLease
	current     CurrentScope
	origin      string
	hostname    string
	address     string
	lookup      resolver
	dial        dialer
	roots       *x509.CertPool
	backend     *http.Transport
	mu          sync.Mutex
	connections map[*connection]struct{}
}

// New uses only the system certificate roots, system DNS resolver, and a direct
// TCP dialer. Environment proxies, custom roots, arbitrary dialers, redirects,
// HTTP/2 coalescing, and alternate protocols are not admitted by this API.
func New(ctx context.Context, lease approval.LoginLease, origin string, current CurrentScope) (*Transport, error) {
	for _, name := range []string{"SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if _, exists := os.LookupEnv(name); exists {
			return nil, errUnavailable
		}
	}
	return newTransport(ctx, lease, origin, current, net.DefaultResolver.LookupNetIP,
		(&net.Dialer{Timeout: 5 * time.Second, KeepAlive: -1}).DialContext, nil)
}

func newTransport(ctx context.Context, lease loginLease, origin string, current CurrentScope,
	lookup func(context.Context, string, string) ([]netip.Addr, error), dial dialer, roots *x509.CertPool,
) (*Transport, error) {
	canonical, err := browserpolicy.NormalizePublicOrigin(origin)
	parsed, parseErr := url.Parse(origin)
	if ctx == nil || ctx.Err() != nil || lease == nil || lease.Done() == nil || current == nil ||
		lookup == nil || dial == nil || err != nil || parseErr != nil || canonical != origin || parsed.Scheme != "https" {
		return nil, errUnavailable
	}
	port := parsed.Port()
	if port == "" {
		port = "443"
	}
	lifetime, cancel := context.WithCancel(ctx)
	transport := &Transport{
		ctx: lifetime, cancel: cancel, lease: lease, current: current,
		origin: origin, hostname: parsed.Hostname(), address: net.JoinHostPort(parsed.Hostname(), port),
		lookup: func(ctx context.Context, hostname string) ([]netip.Addr, error) { return lookup(ctx, "ip", hostname) },
		dial:   dial, roots: roots, connections: make(map[*connection]struct{}),
	}
	if transport.admit(lifetime) != nil {
		cancel()
		return nil, errUnavailable
	}
	transport.backend = &http.Transport{
		DialTLSContext: transport.connect, Proxy: nil,
		TLSNextProto:    make(map[string]func(string, *tls.Conn) http.RoundTripper),
		MaxConnsPerHost: 1, DisableKeepAlives: true, ResponseHeaderTimeout: 10 * time.Second,
		MaxResponseHeaderBytes: 32768, DisableCompression: true,
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		defer func() { _ = transport.Close() }()
		for {
			select {
			case <-lifetime.Done():
				return
			case <-lease.Done():
				return
			case <-ticker.C:
				if transport.admit(lifetime) != nil {
					return
				}
			}
		}
	}()
	return transport, nil
}

func (transport *Transport) admit(ctx context.Context) (result error) {
	result = errUnavailable
	defer func() {
		if recover() != nil {
			result = errUnavailable
		}
		if result != nil {
			_ = transport.Close()
		}
	}()
	if transport == nil || transport.ctx.Err() != nil || ctx == nil || ctx.Err() != nil {
		return result
	}
	select {
	case <-transport.lease.Done():
		return result
	default:
	}
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	request, err := transport.current(bounded)
	request.Origin = transport.origin
	if err != nil || bounded.Err() != nil {
		return result
	}
	if _, err = transport.lease.Binding(bounded, request); err != nil || bounded.Err() != nil ||
		transport.ctx.Err() != nil {
		return result
	}
	select {
	case <-transport.lease.Done():
		return result
	default:
	}
	return nil
}

func (transport *Transport) connect(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" || address != transport.address || transport.admit(ctx) != nil {
		return nil, errUnavailable
	}
	bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	unwatch := context.AfterFunc(transport.ctx, cancel)
	defer unwatch()
	addresses, err := transport.lookup(bounded, transport.hostname)
	if err != nil || len(addresses) == 0 || len(addresses) > 32 || bounded.Err() != nil {
		return nil, errUnavailable
	}
	for _, ip := range addresses {
		if !ip.IsValid() || ip.Zone() != "" || !browserpolicy.IsPublicIP(net.IP(ip.AsSlice())) {
			return nil, errUnavailable
		}
	}
	_, port, _ := net.SplitHostPort(transport.address)
	for _, ip := range addresses {
		if transport.admit(bounded) != nil {
			return nil, errUnavailable
		}
		// No hostname reaches DialContext: this exact already-validated answer is
		// used for the socket, avoiding a second resolver lookup or DNS rebind.
		pinned := net.JoinHostPort(ip.Unmap().String(), port)
		raw, dialErr := transport.dial(bounded, "tcp", pinned)
		if dialErr != nil {
			continue
		}
		tracked := &connection{Conn: raw, owner: transport}
		transport.mu.Lock()
		if transport.ctx.Err() != nil {
			transport.mu.Unlock()
			_ = raw.Close()
			return nil, errUnavailable
		}
		transport.connections[tracked] = struct{}{}
		transport.mu.Unlock()
		secured := tls.Client(tracked, &tls.Config{
			MinVersion: tls.VersionTLS13, ServerName: transport.hostname,
			RootCAs: transport.roots, NextProtos: []string{"http/1.1"},
		})
		if secured.HandshakeContext(bounded) != nil || transport.admit(bounded) != nil {
			_ = tracked.Close()
			return nil, errUnavailable
		}
		return secured, nil
	}
	return nil, errUnavailable
}

func (transport *Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport == nil || transport.ctx == nil || transport.backend == nil || request == nil {
		return nil, errUnavailable
	}
	if request.Method == http.MethodConnect || upgradeRequested(request.Header) ||
		request.URL == nil || request.URL.Scheme != "https" || request.URL.User != nil ||
		request.URL.Opaque != "" || request.URL.Fragment != "" ||
		"https://"+request.URL.Host != transport.origin ||
		(request.Host != "" && request.Host != request.URL.Host) || request.RequestURI != "" ||
		transport.admit(request.Context()) != nil {
		if request.Body != nil {
			_ = request.Body.Close()
		}
		_ = transport.Close()
		return nil, errUnavailable
	}
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	unwatch := context.AfterFunc(transport.ctx, cancel)
	onCancel := context.AfterFunc(ctx, func() { _ = transport.Close() })
	release := func() { onCancel(); unwatch(); cancel() }
	response, err := transport.backend.RoundTrip(request.Clone(ctx))
	if err != nil || response == nil || response.StatusCode == http.StatusSwitchingProtocols ||
		transport.admit(ctx) != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		release()
		_ = transport.Close()
		return nil, errUnavailable
	}
	response.Body = &body{ReadCloser: response.Body, owner: transport, ctx: ctx, release: release}
	return response, nil
}

func upgradeRequested(header http.Header) bool {
	for name, values := range header {
		if strings.EqualFold(name, "Upgrade") {
			return true
		}
		if strings.EqualFold(name, "Connection") {
			for _, value := range values {
				for _, token := range strings.Split(value, ",") {
					if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
						return true
					}
				}
			}
		}
	}
	return false
}

// Close permanently denies reuse and destroys active as well as idle sockets.
func (transport *Transport) Close() error {
	if transport == nil || transport.cancel == nil {
		return errUnavailable
	}
	transport.cancel()
	transport.mu.Lock()
	connections := make([]*connection, 0, len(transport.connections))
	for connection := range transport.connections {
		connections = append(connections, connection)
	}
	transport.mu.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
	if transport.backend != nil {
		transport.backend.CloseIdleConnections()
	}
	return nil
}

func (*Transport) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private credential transport]")
}

func (*Transport) MarshalJSON() ([]byte, error) {
	return []byte(`"[private credential transport]"`), nil
}

type connection struct {
	net.Conn
	owner *Transport
	once  sync.Once
}

func (socket *connection) Close() error {
	socket.once.Do(func() {
		socket.owner.mu.Lock()
		delete(socket.owner.connections, socket)
		socket.owner.mu.Unlock()
		_ = socket.Conn.Close()
	})
	return nil
}

func (socket *connection) Read(data []byte) (int, error) {
	if socket.owner.admit(socket.owner.ctx) != nil {
		_ = socket.Close()
		return 0, errUnavailable
	}
	count, err := socket.Conn.Read(data)
	if socket.owner.admit(socket.owner.ctx) != nil {
		clear(data[:count])
		_ = socket.Close()
		return 0, errUnavailable
	}
	return count, err
}

func (socket *connection) Write(data []byte) (int, error) {
	if socket.owner.admit(socket.owner.ctx) != nil {
		_ = socket.Close()
		return 0, errUnavailable
	}
	return socket.Conn.Write(data)
}

type body struct {
	io.ReadCloser
	owner   *Transport
	ctx     context.Context
	release func()
	once    sync.Once
}

func (response *body) Close() error {
	response.once.Do(func() { response.release(); _ = response.ReadCloser.Close() })
	return nil
}

func (response *body) Read(data []byte) (int, error) {
	if response.owner.admit(response.ctx) != nil {
		_ = response.Close()
		return 0, errUnavailable
	}
	count, err := response.ReadCloser.Read(data)
	if response.owner.admit(response.ctx) != nil {
		clear(data[:count])
		_ = response.Close()
		return 0, errUnavailable
	}
	if err != nil {
		_ = response.Close()
		if !errors.Is(err, io.EOF) {
			return count, errUnavailable
		}
	}
	return count, err
}
