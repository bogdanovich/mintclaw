package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw-credential/internal/approval"
	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

type proxyLease interface {
	loginLease
	Flow(context.Context, credentialbroker.Request) (credentialbroker.LoginFlow, error)
	Origins(context.Context, credentialbroker.Request) ([]string, error)
	Remaining(context.Context, credentialbroker.Request) (time.Duration, error)
}

type proxyPhase uint8

const (
	proxyBrowsing proxyPhase = iota
	proxyLocked
	proxySubmitReady
	proxyPostPending
	proxyOutcomeReady
	proxyCompleted
	proxyRetired
)

// LoginProxy terminates private browser TLS and checks HTTP operations before
// dialing the approved destination. It is never a generic CONNECT byte tunnel.
// Startup must isolate the listener to the supervised browser's network role;
// these software checks do not establish that process or host boundary.
type LoginProxy struct {
	ctx          context.Context
	cancel       context.CancelFunc
	lease        proxyLease
	current      CurrentScope
	flow         credentialbroker.LoginFlow
	certificates map[string]tls.Certificate
	origins      map[string]string // Exact CONNECT authority -> approved HTTPS origin.
	pin          string
	factory      func(string) (*Transport, error)
	mu           sync.Mutex
	phase        proxyPhase
	listener     net.Listener
	connections  map[*proxyConnection]struct{}
	transports   map[string]*Transport
	serving      atomic.Bool
	once         sync.Once
}

// NewLoginProxy uses the actual approved lease and ordinary verified upstream
// HTTPS. No production caller can supply custom DNS, dialers, or trust roots.
func NewLoginProxy(ctx context.Context, lease approval.LoginLease, current CurrentScope) (*LoginProxy, error) {
	return newLoginProxy(
		ctx,
		lease,
		current,
		func(origin string) (*Transport, error) { return New(ctx, lease, origin, current) },
	)
}

func newLoginProxy(ctx context.Context, lease proxyLease, current CurrentScope,
	factory func(string) (*Transport, error),
) (result *LoginProxy, resultErr error) {
	var proxy *LoginProxy
	defer func() {
		if recover() != nil {
			result, resultErr = nil, errUnavailable
		}
		if resultErr != nil {
			_ = proxy.Close()
		}
	}()
	if ctx == nil || ctx.Err() != nil || lease == nil || lease.Done() == nil || current == nil || factory == nil {
		return nil, errUnavailable
	}
	bounded, cancelCheck := context.WithTimeout(ctx, time.Second)
	defer cancelCheck()
	request, err := current(bounded)
	if err != nil {
		return nil, errUnavailable
	}
	flow, err := lease.Flow(bounded, request)
	if err != nil {
		return nil, errUnavailable
	}
	origins, err := lease.Origins(bounded, request)
	if err != nil || len(origins) == 0 || len(origins) > 16 {
		return nil, errUnavailable
	}
	remaining, err := lease.Remaining(bounded, request)
	if err != nil || remaining < time.Millisecond || bounded.Err() != nil {
		return nil, errUnavailable
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, errUnavailable
	}
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, errUnavailable
	}
	digest := sha256.Sum256(public)
	lifetime, cancel := context.WithCancel(ctx)
	proxy = &LoginProxy{
		ctx:          lifetime,
		cancel:       cancel,
		lease:        lease,
		current:      current,
		flow:         flow,
		factory:      factory,
		pin:          base64.StdEncoding.EncodeToString(digest[:]),
		certificates: make(map[string]tls.Certificate),
		origins: make(
			map[string]string,
		),
		connections: make(map[*proxyConnection]struct{}),
		transports:  make(map[string]*Transport),
	}
	for index, origin := range origins {
		canonical, originErr := browserpolicy.NormalizePublicOrigin(origin)
		parsed, parseErr := url.Parse(origin)
		if originErr != nil || parseErr != nil || canonical != origin || parsed.Scheme != "https" {
			_ = proxy.Close()
			return nil, errUnavailable
		}
		port := parsed.Port()
		if port == "" {
			port = "443"
		}
		authority := net.JoinHostPort(parsed.Hostname(), port)
		if _, duplicate := proxy.origins[authority]; duplicate {
			_ = proxy.Close()
			return nil, errUnavailable
		}
		proxy.origins[authority] = origin
		template := &x509.Certificate{
			SerialNumber: big.NewInt(int64(index + 1)), DNSNames: []string{parsed.Hostname()},
			NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(remaining),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		der, certificateErr := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if certificateErr != nil {
			_ = proxy.Close()
			return nil, errUnavailable
		}
		proxy.certificates[authority] = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	}
	if proxy.admit(ctx) != nil {
		return nil, errUnavailable
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		defer func() { _ = proxy.Close() }()
		for {
			select {
			case <-lifetime.Done():
				return
			case <-lease.Done():
				return
			case <-ticker.C:
				if proxy.admit(lifetime) != nil {
					return
				}
			}
		}
	}()
	return proxy, nil
}

func (proxy *LoginProxy) admit(ctx context.Context) (result error) {
	result = errUnavailable
	defer func() {
		if recover() != nil {
			result = errUnavailable
		}
		if result != nil {
			_ = proxy.Close()
		}
	}()
	if proxy == nil || proxy.ctx == nil || proxy.ctx.Err() != nil || ctx == nil || ctx.Err() != nil {
		return result
	}
	select {
	case <-proxy.lease.Done():
		return result
	default:
	}
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	request, err := proxy.current(bounded)
	request.Origin = proxy.flow.EntryOrigin
	if err != nil || bounded.Err() != nil {
		return result
	}
	if _, err = proxy.lease.Binding(bounded, request); err != nil || bounded.Err() != nil || proxy.ctx.Err() != nil {
		return result
	}
	select {
	case <-proxy.lease.Done():
		return result
	default:
	}
	return nil
}

// Match binds the browser channel to this exact original grant generation.
func (proxy *LoginProxy) Match(ctx context.Context, lease approval.LoginLease, request credentialbroker.Request) error {
	if proxy.admit(ctx) != nil {
		return errUnavailable
	}
	if lease.Done() != proxy.lease.Done() {
		_ = proxy.Close()
		return errUnavailable
	}
	flow, err := lease.Flow(ctx, request)
	if err != nil || flow != proxy.flow {
		_ = proxy.Close()
		return errUnavailable
	}
	return nil
}

// Endpoint returns only public connection metadata for the private launcher.
func (proxy *LoginProxy) Endpoint(ctx context.Context) (string, string, error) {
	if proxy.admit(ctx) != nil {
		return "", "", errUnavailable
	}
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if proxy.listener == nil || proxy.ctx.Err() != nil {
		return "", "", errUnavailable
	}
	return "http://" + proxy.listener.Addr().String(), proxy.pin, nil
}

func (proxy *LoginProxy) Done() <-chan struct{} {
	if proxy == nil || proxy.ctx == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	return proxy.ctx.Done()
}

// LockForInjection seals old connections before any password reaches the page.
// Previously admitted preparation GETs own retired transports; they cannot
// obtain a later connection or forward a password after this returns.
func (proxy *LoginProxy) LockForInjection(ctx context.Context) error {
	if proxy.admit(ctx) != nil {
		return errUnavailable
	}
	proxy.mu.Lock()
	if proxy.phase != proxyBrowsing || proxy.ctx.Err() != nil {
		proxy.mu.Unlock()
		_ = proxy.Close()
		return errUnavailable
	}
	proxy.phase = proxyLocked
	connections := proxy.connections
	transports := proxy.transports
	proxy.connections = make(map[*proxyConnection]struct{})
	proxy.transports = make(map[string]*Transport)
	proxy.mu.Unlock()
	for connection := range connections {
		_ = connection.Close()
	}
	for _, transport := range transports {
		_ = transport.Close()
	}
	return proxy.admit(ctx)
}

// PermitSubmit is called only after the separate trusted external-effect gate.
func (proxy *LoginProxy) PermitSubmit(ctx context.Context) error {
	if proxy.admit(ctx) != nil {
		return errUnavailable
	}
	proxy.mu.Lock()
	if proxy.phase != proxyLocked || proxy.ctx.Err() != nil {
		proxy.mu.Unlock()
		_ = proxy.Close()
		return errUnavailable
	}
	proxy.phase = proxySubmitReady
	proxy.mu.Unlock()
	return proxy.admit(ctx)
}

// Serve owns one startup-qualified loopback listener. No model-facing command
// or current executable startup opens or passes this endpoint.
func (proxy *LoginProxy) Serve(ctx context.Context, listener net.Listener) error {
	if listener == nil {
		_ = proxy.Close()
		return errUnavailable
	}
	if proxy.admit(ctx) != nil || !proxy.serving.CompareAndSwap(false, true) {
		_ = listener.Close()
		return errUnavailable
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.Equal(net.IPv4(127, 0, 0, 1)) || address.Port < 1024 {
		_ = listener.Close()
		_ = proxy.Close()
		return errUnavailable
	}
	proxy.mu.Lock()
	if proxy.ctx.Err() != nil {
		proxy.mu.Unlock()
		_ = listener.Close()
		return errUnavailable
	}
	proxy.listener = listener
	proxy.mu.Unlock()
	defer func() { _ = proxy.Close() }()
	unwatch := context.AfterFunc(ctx, func() { _ = proxy.Close() })
	defer unwatch()
	server := proxyHTTPServer(proxy.ctx, http.HandlerFunc(proxy.connectBrowser))
	err := server.Serve(&proxyListener{Listener: listener, proxy: proxy})
	if proxy.ctx.Err() != nil || ctx.Err() != nil {
		return nil
	}
	_ = err
	return errUnavailable
}

func proxyHTTPServer(ctx context.Context, handler http.Handler) *http.Server {
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8192,
		ErrorLog: log.New(io.Discard, "", 0), BaseContext: func(net.Listener) context.Context { return ctx },
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
	}
	server.SetKeepAlivesEnabled(false)
	return server
}

func (proxy *LoginProxy) connectBrowser(writer http.ResponseWriter, request *http.Request) {
	defer func() {
		if recover() != nil {
			_ = proxy.Close()
		}
	}()
	origin, ok := proxy.origins[request.RequestURI]
	if proxy.admit(request.Context()) != nil {
		proxy.deny(writer)
		return
	}
	// Chromium has browser-owned background requests outside the page routes.
	// Refuse their connection without DNS, dialing, or admitting any TLS/body.
	// They cannot change the approved operation's phase or original lease.
	if !ok || request.Method != http.MethodConnect {
		proxy.refuse(writer)
		return
	}
	if request.Host != request.RequestURI || request.URL == nil || request.URL.Host != request.RequestURI ||
		request.URL.User != nil || request.URL.RawQuery != "" || request.URL.Fragment != "" ||
		request.ContentLength != 0 || len(request.TransferEncoding) != 0 || upgradeRequested(request.Header) ||
		request.Header.Get("Proxy-Authorization") != "" || request.Header.Get("Cookie") != "" {
		proxy.deny(writer)
		return
	}
	proxy.mu.Lock()
	phase := proxy.phase
	proxy.mu.Unlock()
	if phase == proxyLocked || phase == proxyCompleted || phase == proxyRetired {
		proxy.deny(writer)
		return
	}
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		proxy.deny(writer)
		return
	}
	connection, buffered, err := hijacker.Hijack()
	if err != nil {
		_ = proxy.Close()
		return
	}
	defer func() { _ = connection.Close() }()
	if connection.SetDeadline(time.Now().Add(5*time.Second)) != nil {
		_ = proxy.Close()
		return
	}
	if _, err = buffered.WriteString(
		"HTTP/1.1 200 Connection Established\r\n\r\n",
	); err != nil ||
		buffered.Flush() != nil {
		_ = proxy.Close()
		return
	}
	parsed, _ := url.Parse(origin)
	secured := tls.Server(&bufferedProxyConnection{Conn: connection, reader: buffered.Reader}, &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{proxy.certificates[request.RequestURI]},
		NextProtos: []string{"http/1.1"}, GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			if hello.ServerName != parsed.Hostname() {
				return nil, errUnavailable
			}
			return nil, nil
		},
	})
	bounded, cancel := context.WithTimeout(proxy.ctx, 5*time.Second)
	err = secured.HandshakeContext(bounded)
	cancel()
	if err != nil || proxy.admit(proxy.ctx) != nil || connection.SetDeadline(time.Time{}) != nil {
		_ = proxy.Close()
		return
	}
	ended := make(chan struct{})
	owned := &singleProxyConnection{Conn: secured, ended: ended}
	server := proxyHTTPServer(
		proxy.ctx,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxy.forward(w, r, origin) }),
	)
	_ = server.Serve(&singleProxyListener{connection: owned})
}

func (proxy *LoginProxy) forward(writer http.ResponseWriter, request *http.Request, origin string) {
	defer func() {
		if recover() != nil {
			_ = proxy.Close()
		}
	}()
	parsed, _ := url.Parse(origin)
	if proxy.admit(request.Context()) != nil || request.URL == nil || request.URL.IsAbs() || request.URL.Opaque != "" ||
		request.URL.User != nil || request.URL.Fragment != "" || request.Host != parsed.Host ||
		request.URL.RawPath != "" || request.Method == http.MethodConnect || upgradeRequested(request.Header) ||
		request.Header.Get("Authorization") != "" || request.Header.Get("Proxy-Authorization") != "" {
		proxy.deny(writer)
		return
	}
	target := origin + request.URL.RequestURI()
	proxy.mu.Lock()
	phase := proxy.phase
	allowed := false
	switch phase {
	case proxyBrowsing:
		allowed = request.Method == http.MethodGet && request.ContentLength == 0 && len(request.TransferEncoding) == 0
	case proxySubmitReady:
		allowed = request.Method == http.MethodPost && target == proxy.flow.SubmitURL &&
			request.Header.Get("Content-Type") == "application/x-www-form-urlencoded" && request.ContentLength > 0 &&
			request.ContentLength <= 16384 && len(request.TransferEncoding) == 0
		if allowed {
			proxy.phase = proxyPostPending
		}
	case proxyOutcomeReady:
		allowed = request.Method == http.MethodGet && request.ContentLength == 0 &&
			len(request.TransferEncoding) == 0 &&
			(target == proxy.flow.SuccessURL || target == proxy.flow.HandoffURL)
		if allowed {
			proxy.phase = proxyCompleted
		}
	}
	transport := proxy.transports[origin]
	factory := proxy.factory
	expectedPhase := proxy.phase
	proxy.mu.Unlock()
	if !allowed {
		proxy.deny(writer)
		return
	}
	if transport == nil {
		created, createErr := factory(origin)
		if createErr != nil || created == nil {
			if created != nil {
				_ = created.Close()
			}
			proxy.deny(writer)
			return
		}
		proxy.mu.Lock()
		valid := proxy.ctx.Err() == nil && proxy.phase == expectedPhase
		transport = proxy.transports[origin]
		if valid && transport == nil {
			transport = created
			proxy.transports[origin] = created
		}
		proxy.mu.Unlock()
		if !valid || transport != created {
			_ = created.Close()
		}
		if !valid {
			proxy.deny(writer)
			return
		}
	}
	data, err := io.ReadAll(io.LimitReader(request.Body, 16385))
	defer clear(data)
	if err != nil || len(data) > 16384 || int64(len(data)) != request.ContentLength ||
		proxy.admit(request.Context()) != nil {
		proxy.deny(writer)
		return
	}
	upstream, err := http.NewRequestWithContext(request.Context(), request.Method, target, bytes.NewReader(data))
	if err != nil {
		proxy.deny(writer)
		return
	}
	upstream.Header = request.Header.Clone()
	stripProxyHeaders(upstream.Header)
	response, err := transport.RoundTrip(upstream)
	if err != nil {
		proxy.deny(writer)
		return
	}
	defer func() { _ = response.Body.Close() }()
	responseData, err := io.ReadAll(io.LimitReader(response.Body, 4194305))
	defer clear(responseData)
	if err != nil || len(responseData) > 4194304 || proxy.admit(request.Context()) != nil {
		proxy.deny(writer)
		return
	}
	if len(response.Header.Values("Location")) > 1 {
		proxy.deny(writer)
		return
	}
	if phase == proxySubmitReady &&
		(response.StatusCode < 200 || response.StatusCode >= 300) && response.StatusCode != 302 && response.StatusCode != 303 {
		proxy.deny(writer)
		return
	}
	if phase == proxySubmitReady && (response.StatusCode == 302 || response.StatusCode == 303) &&
		response.Header.Get("Location") == "" {
		proxy.deny(writer)
		return
	}
	if location := response.Header.Get("Location"); location != "" {
		destination, parseErr := upstream.URL.Parse(location)
		if parseErr != nil || destination.User != nil || destination.Fragment != "" || destination.Opaque != "" {
			proxy.deny(writer)
			return
		}
		if phase == proxySubmitReady {
			if (response.StatusCode != 302 && response.StatusCode != 303) ||
				(destination.String() != proxy.flow.SuccessURL && destination.String() != proxy.flow.HandoffURL) {
				proxy.deny(writer)
				return
			}
		} else {
			approved := false
			for _, configured := range proxy.origins {
				if destination.Scheme+"://"+destination.Host == configured {
					approved = true
				}
			}
			if !approved {
				proxy.deny(writer)
				return
			}
		}
	}
	proxy.mu.Lock()
	if phase == proxySubmitReady && proxy.phase == proxyPostPending {
		proxy.phase = proxyOutcomeReady
	}
	retired := proxy.ctx.Err() != nil || proxy.phase == proxyRetired
	proxy.mu.Unlock()
	if retired {
		proxy.deny(writer)
		return
	}
	stripProxyHeaders(response.Header)
	for key, values := range response.Header {
		writer.Header()[key] = append([]string(nil), values...)
	}
	writer.Header().Set("Connection", "close")
	writer.WriteHeader(response.StatusCode)
	if _, err = writer.Write(responseData); err != nil {
		_ = proxy.Close()
	}
}

func stripProxyHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			header.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{
		"Connection", "Proxy-Connection", "Proxy-Authenticate", "Proxy-Authorization",
		"Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		header.Del(name)
	}
}

func (proxy *LoginProxy) deny(writer http.ResponseWriter) {
	proxy.refuse(writer)
	_ = proxy.Close()
}

func (proxy *LoginProxy) refuse(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Connection", "close")
	writer.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(writer, "credential connection unavailable")
}

func (proxy *LoginProxy) Close() error {
	if proxy == nil {
		return nil
	}
	proxy.once.Do(func() {
		if proxy.cancel != nil {
			proxy.cancel()
		}
		proxy.mu.Lock()
		proxy.phase = proxyRetired
		listener := proxy.listener
		connections := proxy.connections
		transports := proxy.transports
		proxy.connections = nil
		proxy.transports = nil
		proxy.mu.Unlock()
		if listener != nil {
			_ = listener.Close()
		}
		for connection := range connections {
			_ = connection.Close()
		}
		for _, transport := range transports {
			_ = transport.Close()
		}
	})
	return nil
}

type proxyListener struct {
	net.Listener
	proxy *LoginProxy
}

func (listener *proxyListener) Accept() (net.Conn, error) {
	connection, err := listener.Listener.Accept()
	if err != nil {
		return nil, errUnavailable
	}
	if listener.proxy.admit(listener.proxy.ctx) != nil {
		_ = connection.Close()
		return nil, errUnavailable
	}
	owned := &proxyConnection{Conn: connection, proxy: listener.proxy}
	listener.proxy.mu.Lock()
	if listener.proxy.ctx.Err() != nil || len(listener.proxy.connections) >= 16 {
		listener.proxy.mu.Unlock()
		_ = connection.Close()
		return nil, errUnavailable
	}
	listener.proxy.connections[owned] = struct{}{}
	listener.proxy.mu.Unlock()
	return owned, nil
}

type proxyConnection struct {
	net.Conn
	proxy *LoginProxy
	once  sync.Once
}

func (connection *proxyConnection) Close() error {
	connection.once.Do(func() {
		_ = connection.Conn.Close()
		connection.proxy.mu.Lock()
		delete(connection.proxy.connections, connection)
		connection.proxy.mu.Unlock()
	})
	return nil
}

func (connection *proxyConnection) Read(data []byte) (int, error) {
	if connection.proxy.admit(connection.proxy.ctx) != nil {
		return 0, errUnavailable
	}
	count, err := connection.Conn.Read(data)
	if connection.proxy.admit(connection.proxy.ctx) != nil {
		clear(data[:count])
		return 0, errUnavailable
	}
	return count, err
}

func (connection *proxyConnection) Write(data []byte) (int, error) {
	if connection.proxy.admit(connection.proxy.ctx) != nil {
		return 0, errUnavailable
	}
	count, err := connection.Conn.Write(data)
	if connection.proxy.admit(connection.proxy.ctx) != nil {
		return 0, errUnavailable
	}
	return count, err
}

type bufferedProxyConnection struct {
	net.Conn
	reader *bufio.Reader
}

func (connection *bufferedProxyConnection) Read(data []byte) (int, error) {
	return connection.reader.Read(data)
}

type singleProxyConnection struct {
	net.Conn
	ended chan struct{}
	once  sync.Once
}

func (connection *singleProxyConnection) Close() error {
	connection.once.Do(func() { _ = connection.Conn.Close(); close(connection.ended) })
	return nil
}

type singleProxyListener struct {
	connection *singleProxyConnection
	accepted   bool
}

func (listener *singleProxyListener) Accept() (net.Conn, error) {
	if !listener.accepted {
		listener.accepted = true
		return listener.connection, nil
	}
	<-listener.connection.ended
	return nil, net.ErrClosed
}
func (listener *singleProxyListener) Close() error   { return listener.connection.Close() }
func (listener *singleProxyListener) Addr() net.Addr { return listener.connection.LocalAddr() }

func (*LoginProxy) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private credential login proxy]")
}

func (*LoginProxy) MarshalJSON() ([]byte, error) {
	return []byte(`"[private credential login proxy]"`), nil
}

func (*LoginProxy) MarshalText() ([]byte, error) {
	return []byte("[private credential login proxy]"), nil
}
