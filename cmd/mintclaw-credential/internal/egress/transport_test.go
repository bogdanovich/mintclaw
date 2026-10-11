package egress

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw-credential/internal/approval"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

const testOrigin = "https://login.example.org"

// Only private test construction admits this CA1 adapter or synthetic DNS/dial
// mapping. The public constructor requires the actual signed LoginLease and
// system roots/DNS/direct TCP. These loopback tests qualify TLS/control behavior,
// not public DNS, deployed egress, Chromium proxying, or role isolation.
type testLease struct{ credentialbroker.GrantLease }

func (lease testLease) Binding(ctx context.Context, request credentialbroker.Request) (approval.LoginBinding, error) {
	return approval.LoginBinding{}, lease.Check(ctx, request)
}

type fixture struct {
	transport *Transport
	authority *credentialbroker.Authority
	grant     credentialbroker.Grant
	request   credentialbroker.Request
	current   atomic.Pointer[credentialbroker.Request]
	lookups   atomic.Int32
	dials     atomic.Int32
	server    *httptest.Server
}

func serverCertificate(t *testing.T, hostname string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal("synthetic TLS key unavailable")
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{hostname},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	encoded, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal("synthetic TLS certificate unavailable")
	}
	certificate, err := x509.ParseCertificate(encoded)
	if err != nil {
		t.Fatal("synthetic TLS certificate invalid")
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	return tls.Certificate{Certificate: [][]byte{encoded}, PrivateKey: key}, roots
}

func newFixture(t *testing.T, handler http.HandlerFunc, mutate func(*fixture, *tls.Config, **x509.CertPool),
	lookup func(context.Context, string, string) ([]netip.Addr, error),
) *fixture {
	t.Helper()
	principal := credentialbroker.Principal{
		OwnerID: "owner",
		ActorID: "operator",
		AgentID: "mintclaw",
		HostID:  "isolated",
	}
	policy, err := credentialbroker.NewPolicy([]credentialbroker.AccountRule{{
		Alias: "test-login", VaultID: "vault",
		ItemID: "login", Origins: []string{testOrigin}, Principals: []credentialbroker.Principal{principal},
	}})
	if err != nil {
		t.Fatal("synthetic policy unavailable")
	}
	authority, err := credentialbroker.NewAuthority(policy)
	if err != nil {
		t.Fatal("synthetic authority unavailable")
	}
	t.Cleanup(authority.Close)
	grant, err := authority.Approve(credentialbroker.Approval{
		Principal: principal, Accounts: []string{"test-login"},
		PolicyRevision: policy.Revision(), Lifetime: time.Minute,
	})
	if err != nil {
		t.Fatal("synthetic grant unavailable")
	}
	request := credentialbroker.Request{
		Principal: principal, Account: "test-login", Origin: testOrigin,
		PolicyRevision: policy.Revision(),
	}
	lease, err := authority.Lease(context.Background(), grant.ID, request)
	if err != nil {
		t.Fatal("synthetic lease unavailable")
	}
	f := &fixture{authority: authority, grant: grant, request: request}
	f.current.Store(&request)
	certificate, roots := serverCertificate(t, "login.example.org")
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate}, NextProtos: []string{"http/1.1"},
	}
	if mutate != nil {
		mutate(f, server.TLS, &roots)
	}
	server.StartTLS()
	f.server = server
	t.Cleanup(server.Close)
	transport, err := newTransport(context.Background(), testLease{lease}, testOrigin,
		func(ctx context.Context) (credentialbroker.Request, error) { return *f.current.Load(), ctx.Err() },
		func(ctx context.Context, network, hostname string) ([]netip.Addr, error) {
			f.lookups.Add(1)
			if network != "ip" || hostname != "login.example.org" {
				return nil, errUnavailable
			}
			if lookup != nil {
				return lookup(ctx, network, hostname)
			}
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}, func(ctx context.Context, network, address string) (net.Conn, error) {
			f.dials.Add(1)
			if network != "tcp" || address != "93.184.216.34:443" {
				return nil, errUnavailable
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
		}, roots)
	if err != nil {
		t.Fatal("private transport unavailable")
	}
	f.transport = transport
	t.Cleanup(func() { _ = transport.Close() })
	return f
}

func getRequest(t *testing.T, ctx context.Context, target string) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		t.Fatal("fixture request unavailable")
	}
	return request
}

func requireUnavailable(t *testing.T, response *http.Response, err error) {
	t.Helper()
	if response != nil || !errors.Is(err, errUnavailable) ||
		err.Error() != "private credential connection unavailable" {
		t.Fatal("private transport did not project fixed denial")
	}
}

func denyRequest(t *testing.T, transport *Transport, request *http.Request) {
	t.Helper()
	response, err := transport.RoundTrip(request)
	if response != nil {
		defer func() { _ = response.Body.Close() }()
	}
	requireUnavailable(t, response, err)
}

func TestPinnedTLSUsesValidatedAddressAndKeepsResponsePrivate(t *testing.T) {
	var calls atomic.Int32
	const canary = "SYNTHETIC_EGRESS_PRIVATE_COOKIE"
	f := newFixture(t, func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Host != "login.example.org" || request.TLS.Version != tls.VersionTLS13 ||
			request.TLS.ServerName != "login.example.org" {
			t.Error("TLS endpoint authority changed")
		}
		writer.Header().Set("Set-Cookie", "auth="+canary+"; Secure; HttpOnly")
		_, _ = io.WriteString(writer, canary)
	}, nil, nil)
	for range 2 {
		response, err := f.transport.RoundTrip(getRequest(t, context.Background(), testOrigin+"/login"))
		if err != nil {
			t.Fatal("qualified TLS request denied")
		}
		content, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil || string(content) != canary {
			t.Fatal("private TLS response mismatch")
		}
	}
	if calls.Load() != 2 || f.lookups.Load() != 2 || f.dials.Load() != 2 {
		t.Fatal("request reused a connection or resolved again in the dialer")
	}
	encoded, err := json.Marshal(f.transport)
	if err != nil || strings.Contains(string(encoded), canary) ||
		strings.Contains(fmt.Sprintf("%#v", f.transport), canary) || strings.Contains(string(encoded), testOrigin) {
		t.Fatal("generic transport projection exposed private state")
	}
}

func TestDNSRejectsEntireUnsafeOrAmbiguousAnswerSetBeforeDial(t *testing.T) {
	for _, answer := range []string{
		"127.0.0.1", "169.254.169.254", "10.0.0.1", "100.64.0.1", "::1",
		"::ffff:127.0.0.1", "64:ff9b::7f00:1", "fe80::1%eth0", "2001:db8::1", "invalid", "empty", "oversize",
	} {
		t.Run(answer, func(t *testing.T) {
			f := newFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unsafe DNS reached fixture") }, nil,
				func(context.Context, string, string) ([]netip.Addr, error) {
					if answer == "empty" {
						return nil, nil
					}
					public := netip.MustParseAddr("93.184.216.34")
					if answer == "oversize" {
						addresses := make([]netip.Addr, 33)
						for index := range addresses {
							addresses[index] = public
						}
						return addresses, nil
					}
					address, _ := netip.ParseAddr(answer)
					return []netip.Addr{public, address}, nil
				})
			denyRequest(t, f.transport, getRequest(t, context.Background(), testOrigin+"/login"))
			if f.dials.Load() != 0 {
				t.Fatal("partial public DNS answer was used despite unsafe sibling")
			}
		})
	}
}

func TestTLSRejectsUntrustedWrongNameAndOldProtocol(t *testing.T) {
	for _, mode := range []string{"untrusted", "wrong-name", "tls12"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid TLS reached HTTP") },
				func(_ *fixture, config *tls.Config, roots **x509.CertPool) {
					switch mode {
					case "untrusted":
						*roots = x509.NewCertPool()
					case "wrong-name":
						certificate, trusted := serverCertificate(t, "different.example.org")
						config.Certificates = []tls.Certificate{certificate}
						*roots = trusted
					case "tls12":
						config.MinVersion = tls.VersionTLS12
						config.MaxVersion = tls.VersionTLS12
					}
				}, nil)
			denyRequest(t, f.transport, getRequest(t, context.Background(), testOrigin+"/login"))
		})
	}
}

func TestRedirectIsReturnedWithoutFollowingAndChangedOriginDenies(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		http.Redirect(writer, request, "https://different.example.org/changed", http.StatusFound)
	}, nil, nil)
	response, err := f.transport.RoundTrip(getRequest(t, context.Background(), testOrigin+"/login"))
	if err != nil || response.StatusCode != http.StatusFound {
		t.Fatal("redirect response was lost")
	}
	_ = response.Body.Close()
	denyRequest(t, f.transport, getRequest(t, context.Background(), "https://different.example.org/changed"))
	if calls.Load() != 1 || f.dials.Load() != 1 {
		t.Fatal("redirect bypassed exact-origin admission")
	}
}

func TestCurrentPrincipalChangeDeniesCachedTransport(t *testing.T) {
	for _, dimension := range []string{"owner", "actor", "agent", "host", "account", "revision"} {
		t.Run(dimension, func(t *testing.T) {
			f := newFixture(
				t,
				func(http.ResponseWriter, *http.Request) { t.Error("changed scope reached HTTP") },
				nil,
				nil,
			)
			changed := f.request
			switch dimension {
			case "owner":
				changed.Principal.OwnerID = "changed"
			case "actor":
				changed.Principal.ActorID = "changed"
			case "agent":
				changed.Principal.AgentID = "changed"
			case "host":
				changed.Principal.HostID = "changed"
			case "account":
				changed.Account = "changed"
			case "revision":
				changed.PolicyRevision = "changed"
			}
			f.current.Store(&changed)
			denyRequest(t, f.transport, getRequest(t, context.Background(), testOrigin+"/login"))
			if f.dials.Load() != 0 {
				t.Fatal("changed scope reached a connection")
			}
		})
	}
}

func TestEndpointAndProtocolConfusionDeniesBeforeDial(t *testing.T) {
	for _, mode := range []string{"host", "userinfo", "http", "fragment", "opaque", "request-uri", "connect", "upgrade"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(
				t,
				func(http.ResponseWriter, *http.Request) { t.Error("confused endpoint reached HTTP") },
				nil,
				nil,
			)
			request := getRequest(t, context.Background(), testOrigin+"/login")
			switch mode {
			case "host":
				request.Host = "different.example.org"
			case "userinfo":
				request.URL.User = url.UserPassword("synthetic-private-user", "synthetic-private-password")
			case "http":
				request.URL.Scheme = "http"
			case "fragment":
				request.URL.Fragment = "synthetic-private-value"
			case "opaque":
				request.URL.Opaque = "//different.example.org/login"
			case "request-uri":
				request.RequestURI = "/login"
			case "connect":
				request.Method = http.MethodConnect
			case "upgrade":
				request.Header.Set("Upgrade", "websocket")
			}
			denyRequest(t, f.transport, request)
			if f.dials.Load() != 0 {
				t.Fatal("endpoint confusion reached dial")
			}
		})
	}
}

func TestRevocationCancelsBlockedTLSResponseAndClearsRetainedConnections(t *testing.T) {
	started := make(chan struct{})
	f := newFixture(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		close(started)
		<-request.Context().Done()
	}, nil, nil)
	response, err := f.transport.RoundTrip(getRequest(t, context.Background(), testOrigin+"/login"))
	if err != nil {
		t.Fatal("stream fixture unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	<-started
	result := make(chan error, 1)
	go func() { var buffer [16]byte; _, readErr := response.Body.Read(buffer[:]); result <- readErr }()
	if f.authority.Revoke(f.request.Principal.OwnerID, f.grant.ID) != nil {
		t.Fatal("fixture revocation failed")
	}
	select {
	case readErr := <-result:
		if !errors.Is(readErr, errUnavailable) {
			t.Fatal("blocked response did not retire with fixed denial")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revocation did not interrupt active TLS socket")
	}
	_ = f.transport.Close()
	f.transport.mu.Lock()
	retained := len(f.transport.connections)
	f.transport.mu.Unlock()
	if retained != 0 {
		t.Fatal("retired transport retained a connection")
	}
}

func TestCancellationDuringDNSPermanentlyRetiresTransport(t *testing.T) {
	started := make(chan struct{})
	f := newFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("canceled lookup reached HTTP") }, nil,
		func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		response, callErr := f.transport.RoundTrip(getRequest(t, ctx, testOrigin+"/login"))
		if response != nil {
			_ = response.Body.Close()
		}
		result <- callErr
	}()
	<-started
	cancel()
	select {
	case callErr := <-result:
		if !errors.Is(callErr, errUnavailable) {
			t.Fatal("DNS cancellation lost fixed denial")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled DNS operation remained active")
	}
	if f.dials.Load() != 0 {
		t.Fatal("canceled DNS reached dial")
	}
	denyRequest(t, f.transport, getRequest(t, context.Background(), testOrigin+"/login"))
}

func TestUncertainPOSTIsNeverAutomaticallyReplayed(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		connection, _, err := writer.(http.Hijacker).Hijack()
		if err == nil {
			_ = connection.Close()
		}
	}, nil, nil)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, testOrigin+"/session",
		strings.NewReader("SYNTHETIC_PRIVATE_PASSWORD"))
	if err != nil {
		t.Fatal("POST fixture unavailable")
	}
	request.Header.Set("Idempotency-Key", "synthetic-key")
	denyRequest(t, f.transport, request)
	if calls.Load() != 1 || f.dials.Load() != 1 {
		t.Fatal("uncertain POST was replayed")
	}
}

func TestZeroAndForgedEndpointCannotCreatePrivateTransport(t *testing.T) {
	request := getRequest(t, context.Background(), testOrigin+"/login")
	denyRequest(t, &Transport{}, request)
	if (&Transport{}).Close() == nil {
		t.Fatal("zero transport closed as admitted")
	}
	for _, origin := range []string{testOrigin, "http://login.example.org", "https://127.0.0.1", "https://login.example.org/", "https://login.example.org?"} {
		transport, callErr := New(context.Background(), approval.LoginLease{}, origin,
			func(context.Context) (credentialbroker.Request, error) { return credentialbroker.Request{}, nil })
		if transport != nil || !errors.Is(callErr, errUnavailable) {
			t.Fatal("unsigned lease or endpoint admitted")
		}
	}
}
