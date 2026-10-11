//go:build linux

package startup

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const approvalFixtureRoot = "/qual-approval-service"

// This fixture executes the public startup entry point in a disposable Linux
// identity. It cannot select operator paths or import a real credential.
func TestApprovalServiceFixture(t *testing.T) {
	mode := os.Getenv("MINTCLAW_SYNTHETIC_APPROVAL_SERVICE_FIXTURE")
	if mode == "" {
		t.Skip("disposable Linux service fixture only")
	}
	marker, err := os.ReadFile(approvalFixtureRoot + "/synthetic-only")
	if err != nil || string(marker) != "mintclaw-approval-service-fixture-v1\n" {
		t.Fatal("synthetic service marker unavailable")
	}
	if _, err = os.Stat("/.dockerenv"); err != nil {
		t.Fatal("disposable container required")
	}
	switch mode {
	case "setup":
		if os.Getuid() != 0 || os.Getgid() != 0 {
			t.Fatal("synthetic setup requires Root")
		}
		setupApprovalFixture(t)
	case "serve":
		if os.Getuid() != 65532 || os.Getgid() != 65532 {
			t.Fatal("synthetic service identity required")
		}
		runApprovalFixture(t)
	default:
		t.Fatal("unknown synthetic service mode")
	}
}

func setupApprovalFixture(t *testing.T) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal("synthetic TLS generation failed")
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "approval.example.org"},
		DNSNames:     []string{"approval.example.org", "untrusted.example.org"},
		NotBefore:    now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, IsCA: true,
		BasicConstraintsValid: true, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal("synthetic TLS certificate failed")
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal("synthetic TLS key encoding failed")
	}
	defer clear(encoded)
	fixtureRootFile(t, approvalFixtureRoot+"/tls.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	private := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})
	fixtureRootFile(t, approvalFixtureRoot+"/tls.key", private)
	clear(private)
	fixtureRootFile(t, approvalConfigPath, []byte(syntheticApprovalConfig))
	fixtureRootFile(t, approvalFixtureRoot+"/valid", []byte("synthetic protected bytes"))
	fixtureRootFile(t, approvalFixtureRoot+"/wrong-mode", []byte("synthetic rejected bytes"))
	if os.Chmod(approvalFixtureRoot+"/wrong-mode", 0o644) != nil {
		t.Fatal("synthetic mode setup failed")
	}
	fixtureRootFile(t, approvalFixtureRoot+"/wrong-owner", []byte("synthetic rejected bytes"))
	if os.Chown(approvalFixtureRoot+"/wrong-owner", 65532, 65532) != nil {
		t.Fatal("synthetic owner setup failed")
	}
	fixtureRootFile(t, approvalFixtureRoot+"/wrong-group", []byte("synthetic rejected bytes"))
	if os.Chown(approvalFixtureRoot+"/wrong-group", 0, 0) != nil {
		t.Fatal("synthetic group setup failed")
	}
	fixtureRootFile(t, approvalFixtureRoot+"/oversized", make([]byte, maxApprovalConfigBytes+1))
	fixtureRootFile(t, approvalFixtureRoot+"/linked", []byte("synthetic rejected bytes"))
	if os.Link(approvalFixtureRoot+"/linked", approvalFixtureRoot+"/hardlink") != nil ||
		os.Symlink(approvalFixtureRoot+"/valid", approvalFixtureRoot+"/symlink") != nil ||
		unix.Mkfifo(approvalFixtureRoot+"/fifo", 0o640) != nil {
		t.Fatal("synthetic nonregular setup failed")
	}
	if os.Mkdir(approvalFixtureRoot+"/writable", 0o777) != nil ||
		os.Chmod(approvalFixtureRoot+"/writable", 0o777) != nil {
		t.Fatal("synthetic parent setup failed")
	}
	fixtureRootFile(t, approvalFixtureRoot+"/writable/child", []byte("synthetic rejected bytes"))
}

func fixtureRootFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if os.WriteFile(path, data, 0o640) != nil || os.Chmod(path, 0o640) != nil || os.Chown(path, 0, 65532) != nil {
		t.Fatal("synthetic protected file setup failed")
	}
}

func runApprovalFixture(t *testing.T) {
	t.Helper()
	identity := serviceIdentity{uid: 65532, gid: 65532}
	data, err := readApprovalFile(context.Background(), approvalFixtureRoot+"/valid", identity)
	if err != nil || string(data) != "synthetic protected bytes" {
		t.Fatal("protected Root file denied")
	}
	clear(data)
	for _, name := range []string{
		"wrong-mode", "wrong-owner", "wrong-group", "oversized", "linked", "hardlink",
		"symlink", "fifo", "writable/child",
	} {
		data, denied := readApprovalFile(context.Background(), approvalFixtureRoot+"/"+name, identity)
		if !errors.Is(denied, errUnavailable) || len(data) != 0 {
			t.Fatal("unsafe startup file admitted")
		}
	}
	encoded, err := os.ReadFile(approvalFixtureRoot + "/tls.crt")
	if err != nil {
		t.Fatal("synthetic TLS root unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(encoded) {
		t.Fatal("synthetic TLS root invalid")
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13,
			RootCAs:    roots, ServerName: "approval.example.org",
		}, DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", "127.0.0.1:18443")
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport, Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	// Successful cancellation must release the owner-store lock and remove only
	// the service-created socket, allowing a second clean protected startup.
	for range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- ServeApproval(ctx) }()
		stop := func() {
			cancel()
			select {
			case result := <-done:
				if result != nil {
					t.Fatal("protected service shutdown failed")
				}
			case <-time.After(15 * time.Second):
				t.Fatal("protected service shutdown exceeded bound")
			}
		}
		func() {
			defer stop()
			waitForApprovalFixture(t, client, done)
			for _, resource := range []string{"/", "/app.js", "/app.css"} {
				response, requestErr := client.Get("https://approval.example.org" + resource)
				if requestErr != nil {
					t.Fatal("protected phone resource unavailable")
				}
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 65536))
				_ = response.Body.Close()
				if response.StatusCode != http.StatusOK || response.TLS == nil ||
					response.TLS.Version != tls.VersionTLS13 || response.ProtoMajor != 1 ||
					!strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
					t.Fatal("native phone TLS or output contract failed")
				}
			}
			for _, path := range []string{
				"/unknown", "//api/approve/begin", "/api/approve/../approve/begin",
				"/api/approve/begin?unexpected=1", "/api/enroll/begin",
			} {
				request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost,
					"https://approval.example.org"+path, strings.NewReader("{}"))
				if requestErr != nil {
					t.Fatal("synthetic denial request failed")
				}
				request.Header.Set("Origin", "https://untrusted.example.org")
				response, requestErr := client.Do(request)
				if requestErr != nil {
					t.Fatal("synthetic denial response unavailable")
				}
				_ = response.Body.Close()
				if response.StatusCode < 400 {
					t.Fatal("invalid phone request admitted or redirected")
				}
			}
			verifyApprovalFixtureConsole(t)
		}()
		if _, statErr := os.Lstat(approvalConsoleDirectory + "/setup.sock"); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatal("owned enrollment socket not retired")
		}
	}
	wrongSNI := transport.Clone()
	wrongSNI.TLSClientConfig.ServerName = "untrusted.example.org"
	defer wrongSNI.CloseIdleConnections()
	// SNI denial is exercised while a third real service instance is listening.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeApproval(ctx) }()
	waitForApprovalFixture(t, client, done)
	response, denied := (&http.Client{Transport: wrongSNI, Timeout: 2 * time.Second}).Get(
		"https://approval.example.org/",
	)
	if response != nil {
		_ = response.Body.Close()
	}
	cancel()
	select {
	case result := <-done:
		if result != nil {
			t.Fatal("SNI fixture shutdown failed")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("SNI fixture shutdown exceeded bound")
	}
	if denied == nil {
		t.Fatal("incorrect TLS SNI admitted")
	}
}

func waitForApprovalFixture(t *testing.T, client *http.Client, done <-chan error) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err := client.Get("https://approval.example.org/")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				return
			}
		}
		select {
		case <-done:
			t.Fatal("protected service failed before readiness")
		case <-deadline.C:
			t.Fatal("protected service readiness exceeded bound")
		case <-ticker.C:
		}
	}
}

func verifyApprovalFixtureConsole(t *testing.T) {
	t.Helper()
	connection, err := net.DialUnix(
		"unixpacket",
		nil,
		&net.UnixAddr{Net: "unixpacket", Name: approvalConsoleDirectory + "/setup.sock"},
	)
	if err != nil {
		t.Fatal("service-created console unavailable")
	}
	defer func() { _ = connection.Close() }()
	if connection.SetDeadline(time.Now().Add(time.Second)) != nil {
		t.Fatal("console bound unavailable")
	}
	raw, err := connection.SyscallConn()
	if err != nil {
		t.Fatal("console role unavailable")
	}
	var peer *unix.Ucred
	if raw.Control(func(fd uintptr) { peer, err = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }) != nil ||
		err != nil || peer == nil ||
		peer.Uid != 65532 ||
		peer.Gid != 65532 {
		t.Fatal("console creator role mismatch")
	}
	_, _ = connection.Write([]byte("{}"))
	buffer := make([]byte, 128)
	count, _ := connection.Read(buffer)
	if count != 0 {
		t.Fatal("non-Root console peer received private setup data")
	}
}
