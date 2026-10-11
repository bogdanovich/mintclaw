package egress

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

var proxyTestFlow = credentialbroker.LoginFlow{
	NavigationURL:   testOrigin + "/login",
	EntryOrigin:     testOrigin,
	FormID:          "signin",
	UsernameID:      "username",
	PasswordID:      "password",
	UsernameType:    "email",
	SubmitURL:       testOrigin + "/session",
	SuccessURL:      testOrigin + "/success",
	SuccessMarkerID: "signed-in",
	HandoffURL:      testOrigin + "/mfa",
	HandoffMarkerID: "mfa",
}

type proxyTestLease struct{ testLease }

func (lease proxyTestLease) Flow(
	ctx context.Context,
	request credentialbroker.Request,
) (credentialbroker.LoginFlow, error) {
	return proxyTestFlow, lease.Check(ctx, request)
}

func (lease proxyTestLease) Origins(ctx context.Context, request credentialbroker.Request) ([]string, error) {
	return []string{testOrigin}, lease.Check(ctx, request)
}

type proxyFixture struct {
	base      *fixture
	proxy     *LoginProxy
	client    *http.Client
	endpoint  string
	done      chan error
	getCount  atomic.Int32
	postCount atomic.Int32
}

func newProxyFixture(t *testing.T, handler http.HandlerFunc, witness ...func(string)) *proxyFixture {
	t.Helper()
	f := &proxyFixture{}
	f.base = newFixture(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			f.getCount.Add(1)
		}
		if request.Method == http.MethodPost {
			f.postCount.Add(1)
		}
		if handler != nil {
			handler(writer, request)
			return
		}
		switch request.URL.Path {
		case "/login":
			_, _ = io.WriteString(writer, "synthetic login page")
		case "/session":
			if request.Method != http.MethodPost {
				t.Error("synthetic submission method changed")
			}
			if len(request.Header.Values("Content-Type")) != 1 ||
				request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Error("synthetic submission content type changed upstream")
			}
			data, err := io.ReadAll(request.Body)
			if err != nil || string(data) != "username=synthetic&password=SYNTHETIC_PRIVATE_PASSWORD" {
				t.Error("synthetic form changed")
			}
			clear(data)
			writer.Header().Set("Set-Cookie", "session=SYNTHETIC_PRIVATE_COOKIE; Secure; HttpOnly; Path=/")
			writer.Header().Set("Location", "/success")
			writer.WriteHeader(http.StatusSeeOther)
		case "/success":
			_, _ = io.WriteString(writer, "synthetic success page")
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}, nil, nil)
	lease := proxyTestLease{f.base.transport.lease.(testLease)}
	current := func(ctx context.Context) (credentialbroker.Request, error) { return *f.base.current.Load(), ctx.Err() }
	proxy, err := newLoginProxy(context.Background(), lease, current, func(origin string) (*Transport, error) {
		return newTransport(context.Background(), lease, origin, current,
			func(ctx context.Context, _, hostname string) ([]netip.Addr, error) {
				return f.base.transport.lookup(ctx, hostname)
			},
			f.base.transport.dial, f.base.transport.roots)
	})
	if err != nil {
		t.Fatal("synthetic enforcing proxy unavailable")
	}
	f.proxy = proxy
	t.Cleanup(func() { _ = proxy.Close() })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("synthetic private listener unavailable")
	}
	if len(witness) > 0 {
		listener = &proxyWitnessListener{Listener: listener, witness: witness[0]}
	}
	f.done = make(chan error, 1)
	go func() { f.done <- proxy.Serve(context.Background(), listener) }()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		endpoint, _, endpointErr := proxy.Endpoint(context.Background())
		if endpointErr == nil {
			f.endpoint = endpoint
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("proxy readiness exceeded bound")
		case <-time.After(time.Millisecond):
		}
	}
	parsed, err := url.Parse(f.endpoint)
	if err != nil {
		t.Fatal("synthetic proxy endpoint invalid")
	}
	certificate, err := x509.ParseCertificate(proxy.certificates["login.example.org:443"].Certificate[0])
	if err != nil {
		t.Fatal("synthetic proxy certificate invalid")
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	transport := &http.Transport{Proxy: http.ProxyURL(parsed), TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: roots,
	}, DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	f.client = &http.Client{
		Transport: transport, Timeout: 3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return f
}

// Witness reports only a fixed classification of the first proxy request.
// Neither TLS records nor request headers/bodies are exposed by this fixture.
type proxyWitnessListener struct {
	net.Listener
	witness func(string)
}

func (listener *proxyWitnessListener) Accept() (net.Conn, error) {
	connection, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &proxyWitnessConnection{Conn: connection, witness: listener.witness}, nil
}

type proxyWitnessConnection struct {
	net.Conn
	witness  func(string)
	prefix   []byte
	reported bool
}

func (connection *proxyWitnessConnection) Read(data []byte) (int, error) {
	n, err := connection.Conn.Read(data)
	if !connection.reported {
		remaining := 1024 - len(connection.prefix)
		if remaining > n {
			remaining = n
		}
		connection.prefix = append(connection.prefix, data[:remaining]...)
		if index := bytes.IndexByte(connection.prefix, '\n'); index >= 0 || len(connection.prefix) == 1024 {
			classification := "unexpected"
			if index >= 0 && string(connection.prefix[:index]) == "CONNECT login.example.org:443 HTTP/1.1\r" {
				classification = "approved-connect"
			}
			connection.reported = true
			clear(connection.prefix)
			connection.prefix = nil
			connection.witness(classification)
		}
	}
	return n, err
}

func (proxy *proxyFixture) request(t *testing.T, method, target, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal("synthetic browser request invalid")
	}
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response, err := proxy.client.Do(request)
	if err != nil {
		t.Fatal("admitted synthetic browser request failed")
	}
	return response
}

func requireProxyDenied(t *testing.T, f *proxyFixture, request *http.Request) {
	t.Helper()
	response, err := f.client.Do(request)
	if response != nil {
		_ = response.Body.Close()
		if response.StatusCode < 400 {
			t.Fatal("unexpected browser operation admitted")
		}
	}
	if response == nil && err == nil {
		t.Fatal("browser denial lacked an outcome")
	}
	select {
	case <-f.proxy.Done():
	case <-time.After(time.Second):
		t.Fatal("denial did not retire proxy")
	}
}

func TestEnforcingProxyPrivateLoginFlow(t *testing.T) {
	f := newProxyFixture(t, nil)
	response := f.request(t, http.MethodGet, proxyTestFlow.NavigationURL, "")
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 || response.TLS == nil || response.TLS.Version != tls.VersionTLS13 ||
		f.getCount.Load() != 1 {
		t.Fatal("synthetic private preparation failed")
	}
	f.proxy.mu.Lock()
	old := f.proxy.transports[testOrigin]
	f.proxy.mu.Unlock()
	if f.proxy.LockForInjection(context.Background()) != nil || old == nil || old.ctx.Err() == nil {
		t.Fatal("preparation transport survived injection lock")
	}
	if f.proxy.PermitSubmit(context.Background()) != nil {
		t.Fatal("trusted submission gate failed")
	}
	response = f.request(
		t,
		http.MethodPost,
		proxyTestFlow.SubmitURL,
		"username=synthetic&password=SYNTHETIC_PRIVATE_PASSWORD",
	)
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Set-Cookie") == "" ||
		f.postCount.Load() != 1 {
		t.Fatal("private submission or cookie response failed")
	}
	response = f.request(t, http.MethodGet, proxyTestFlow.SuccessURL, "")
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 || f.getCount.Load() != 2 {
		t.Fatal("configured success request failed")
	}
	requireProxyDenied(t, f, getRequest(t, context.Background(), proxyTestFlow.SuccessURL))
	if f.base.dials.Load() != 3 {
		t.Fatal("completed proxy opened another upstream connection")
	}
	encoded, err := json.Marshal(f.proxy)
	if err != nil || string(encoded) != `"[private credential login proxy]"` ||
		strings.Contains(fmt.Sprintf("%#v", f.proxy), "login.example.org") {
		t.Fatal("proxy projected private state")
	}
}

func TestEnforcingProxyDeniesUnapprovedConnectionAndRequest(t *testing.T) {
	for _, change := range []func(*http.Request){
		func(r *http.Request) { r.Host = "untrusted.example.org" },
		func(r *http.Request) { r.Method = http.MethodDelete },
		func(r *http.Request) { r.Header.Set("Upgrade", "websocket") },
		func(r *http.Request) { r.Header.Set("Connection", "keep-alive, Upgrade") },
		func(r *http.Request) { r.Header.Set("Authorization", "SYNTHETIC_PRIVATE_HEADER") },
		func(r *http.Request) { r.Header.Set("Proxy-Authorization", "SYNTHETIC_PRIVATE_HEADER") },
	} {
		f := newProxyFixture(t, nil)
		request := getRequest(t, context.Background(), proxyTestFlow.NavigationURL)
		change(request)
		requireProxyDenied(t, f, request)
		if f.base.dials.Load() != 0 || f.base.lookups.Load() != 0 {
			t.Fatal("rejected request reached upstream dialing")
		}
	}
}

func TestEnforcingProxyRefusesBackgroundWithoutChangingLoginPhase(t *testing.T) {
	f := newProxyFixture(t, nil)
	for _, phase := range []proxyPhase{proxyBrowsing, proxyLocked, proxySubmitReady} {
		if phase == proxyLocked && f.proxy.LockForInjection(context.Background()) != nil {
			t.Fatal("synthetic injection lock failed")
		}
		if phase == proxySubmitReady && f.proxy.PermitSubmit(context.Background()) != nil {
			t.Fatal("synthetic submission gate failed")
		}
		for _, target := range []string{"https://untrusted.example.org/probe", "http://untrusted.example.org/probe"} {
			response, err := f.client.Do(getRequest(t, context.Background(), target))
			if response != nil {
				_ = response.Body.Close()
				if response.StatusCode != http.StatusForbidden {
					t.Fatal("foreign connection was not refused")
				}
			}
			if response == nil && err == nil {
				t.Fatal("foreign connection lacked fixed denial")
			}
			f.proxy.mu.Lock()
			unchanged := f.proxy.phase == phase
			f.proxy.mu.Unlock()
			if !unchanged || f.proxy.ctx.Err() != nil || f.base.dials.Load() != 0 || f.base.lookups.Load() != 0 {
				t.Fatal("foreign background connection changed admission or reached upstream")
			}
		}
	}
	response := f.request(
		t,
		http.MethodPost,
		proxyTestFlow.SubmitURL,
		"username=synthetic&password=SYNTHETIC_PRIVATE_PASSWORD",
	)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || f.postCount.Load() != 1 {
		t.Fatal("refused background altered the one permitted submission")
	}
}

func TestEnforcingProxyLocksExistingConnectionAndAllNewNetwork(t *testing.T) {
	f := newProxyFixture(t, nil)
	endpoint, err := url.Parse(f.endpoint)
	if err != nil {
		t.Fatal("synthetic endpoint invalid")
	}
	connection, err := net.DialTimeout("tcp", endpoint.Host, time.Second)
	if err != nil {
		t.Fatal("synthetic idle connection unavailable")
	}
	defer func() { _ = connection.Close() }()
	// Wait for the accepted connection to enter the proxy's ownership set.
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		f.proxy.mu.Lock()
		count := len(f.proxy.connections)
		f.proxy.mu.Unlock()
		if count == 1 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("idle connection ownership exceeded bound")
		case <-time.After(time.Millisecond):
		}
	}
	if f.proxy.LockForInjection(context.Background()) != nil {
		t.Fatal("injection lock failed")
	}
	if connection.SetDeadline(time.Now().Add(time.Second)) != nil {
		t.Fatal("idle connection read bound failed")
	}
	var buffer [1]byte
	if count, readErr := connection.Read(buffer[:]); count != 0 || readErr == nil {
		t.Fatal("idle preparation connection survived lock")
	}
	requireProxyDenied(t, f, getRequest(t, context.Background(), proxyTestFlow.NavigationURL))
	if f.base.dials.Load() != 0 {
		t.Fatal("injection-phase network reached upstream")
	}
}

func TestEnforcingProxyRejectsEarlySubmissionAndChangedForm(t *testing.T) {
	for _, mutation := range []string{
		"early", "path", "method", "type", "empty", "oversized", "connection-type", "connection-type-list", "duplicate-type",
	} {
		f := newProxyFixture(t, nil)
		if mutation != "early" {
			if f.proxy.LockForInjection(context.Background()) != nil ||
				f.proxy.PermitSubmit(context.Background()) != nil {
				t.Fatal("synthetic phase setup failed")
			}
		}
		body := "username=synthetic&password=SYNTHETIC_PRIVATE_PASSWORD"
		if mutation == "empty" {
			body = ""
		}
		if mutation == "oversized" {
			body = strings.Repeat("a", 16385)
		}
		request, err := http.NewRequestWithContext(
			context.Background(),
			http.MethodPost,
			proxyTestFlow.SubmitURL,
			strings.NewReader(body),
		)
		if err != nil {
			t.Fatal("synthetic form request invalid")
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		switch mutation {
		case "path":
			request.URL.Path = "/unapproved"
		case "method":
			request.Method = http.MethodPut
		case "type":
			request.Header.Set("Content-Type", "text/plain")
		case "connection-type":
			request.Header.Set("Connection", "Content-Type")
		case "connection-type-list":
			request.Header.Add("Connection", "keep-alive")
			request.Header.Add("Connection", "cookie, cOnTeNt-TyPe")
		case "duplicate-type":
			request.Header.Add("Content-Type", "text/plain")
		}
		requireProxyDenied(t, f, request)
		if f.postCount.Load() != 0 || f.base.dials.Load() != 0 || f.base.lookups.Load() != 0 {
			t.Fatal("changed submission reached upstream")
		}
	}
}

func TestEnforcingProxyRejectsRedirectSubstitution(t *testing.T) {
	for _, location := range []string{
		"http://login.example.org/success", "https://untrusted.example.org/success",
		testOrigin + "/unapproved", proxyTestFlow.SuccessURL + "#private", "",
	} {
		f := newProxyFixture(t, func(w http.ResponseWriter, _ *http.Request) {
			if location != "" {
				w.Header().Set("Location", location)
			}
			w.WriteHeader(http.StatusSeeOther)
		})
		if f.proxy.LockForInjection(context.Background()) != nil || f.proxy.PermitSubmit(context.Background()) != nil {
			t.Fatal("synthetic submission setup failed")
		}
		request, err := http.NewRequestWithContext(
			context.Background(),
			http.MethodPost,
			proxyTestFlow.SubmitURL,
			strings.NewReader("synthetic"),
		)
		if err != nil {
			t.Fatal("synthetic submission invalid")
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		requireProxyDenied(t, f, request)
		if f.base.dials.Load() != 1 {
			t.Fatal("redirect was followed upstream")
		}
	}
}

func TestEnforcingProxyRetiresOnScopeWithdrawalAndRevocation(t *testing.T) {
	for _, mode := range []string{"scope", "revoke"} {
		f := newProxyFixture(t, nil)
		if mode == "scope" {
			changed := f.base.request
			changed.Principal.HostID = "untrusted"
			f.base.current.Store(&changed)
		} else {
			if f.base.authority.Revoke(f.base.request.Principal.OwnerID, f.base.grant.ID) != nil {
				t.Fatal("synthetic revocation failed")
			}
		}
		select {
		case <-f.proxy.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("withdrawn proxy remained active")
		}
		f.base.current.Store(&f.base.request)
		if _, _, err := f.proxy.Endpoint(context.Background()); err == nil {
			t.Fatal("restored scope revived old proxy")
		}
		if f.base.dials.Load() != 0 {
			t.Fatal("withdrawn scope reached upstream")
		}
	}
}

func TestEnforcingProxyClaimsAtMostOneConcurrentSubmission(t *testing.T) {
	f := newProxyFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/success")
		w.WriteHeader(303)
	})
	if f.proxy.LockForInjection(context.Background()) != nil || f.proxy.PermitSubmit(context.Background()) != nil {
		t.Fatal("synthetic phase setup failed")
	}
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			request, err := http.NewRequestWithContext(
				context.Background(),
				http.MethodPost,
				proxyTestFlow.SubmitURL,
				strings.NewReader("synthetic"),
			)
			if err != nil {
				return
			}
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response, _ := f.client.Do(request)
			if response != nil {
				_ = response.Body.Close()
			}
		}()
	}
	workers.Wait()
	if f.postCount.Load() > 1 || f.base.dials.Load() > 1 {
		t.Fatal("concurrent submission replay reached upstream")
	}
	select {
	case <-f.proxy.Done():
	case <-time.After(time.Second):
		t.Fatal("submission replay did not retire proxy")
	}
}

func TestEnforcingProxyDeniesWrongSNIWithoutClientHostnameShortcut(t *testing.T) {
	f := newProxyFixture(t, nil)
	// The synthetic certificate deliberately matches the incorrect client SNI.
	// Thus the server's exact-name check, rather than client hostname verification,
	// must prevent this connection from reaching approved upstream HTTP.
	certificate, roots := serverCertificate(t, "untrusted.example.org")
	f.proxy.certificates["login.example.org:443"] = certificate
	transport := f.client.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.RootCAs = roots
	transport.TLSClientConfig.ServerName = "untrusted.example.org"
	defer transport.CloseIdleConnections()
	f.client.Transport = transport
	requireProxyDenied(t, f, getRequest(t, context.Background(), proxyTestFlow.NavigationURL))
	if f.base.dials.Load() != 0 {
		t.Fatal("incorrect browser SNI reached upstream")
	}
}

func TestEnforcingProxyFactoryPanicRetiresWithoutOwnershipDeadlock(t *testing.T) {
	f := newProxyFixture(t, nil)
	f.proxy.mu.Lock()
	f.proxy.factory = func(string) (*Transport, error) { panic("SYNTHETIC_PRIVATE_FACTORY_DIAGNOSTIC") }
	f.proxy.mu.Unlock()
	requireProxyDenied(t, f, getRequest(t, context.Background(), proxyTestFlow.NavigationURL))
	if f.base.dials.Load() != 0 {
		t.Fatal("panicked proxy factory reached upstream")
	}
}

func TestEnforcingProxyStartupFailureClosesOwnedListener(t *testing.T) {
	f := newProxyFixture(t, nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("synthetic listener unavailable")
	}
	if f.proxy.Serve(context.Background(), listener) == nil {
		t.Fatal("proxy served a second listener")
	}
	if connection, dialErr := net.DialTimeout("tcp", listener.Addr().String(), time.Second); dialErr == nil {
		_ = connection.Close()
		t.Fatal("rejected listener remained available")
	}
	lease := proxyTestLease{f.base.transport.lease.(testLease)}
	proxy, denied := newLoginProxy(context.Background(), lease,
		func(context.Context) (credentialbroker.Request, error) { panic("SYNTHETIC_PRIVATE_STARTUP_DIAGNOSTIC") },
		func(string) (*Transport, error) { return nil, errUnavailable })
	if proxy != nil || !errors.Is(denied, errUnavailable) {
		t.Fatal("startup panic escaped fixed denial")
	}
}
