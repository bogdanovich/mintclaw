package approval

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

// This opt-in fixture uses only a memory owner store and a new virtual passkey.
// It is not a physical Android, real-vault, deployment or browser-login claim.
func TestPhoneBrowserFixture(t *testing.T) {
	if os.Getenv("MINTCLAW_SYNTHETIC_PHONE_BROWSER_FIXTURE") != "1" {
		t.Skip("synthetic browser fixture only")
	}
	for _, pagePath := range []string{"/", "/credentials/"} {
		for _, discovery := range []bool{false, true} {
			name := pagePath + "fixed"
			if discovery {
				name = pagePath + "discovery"
			}
			t.Run(name, func(t *testing.T) { testPhoneBrowserFixture(t, pagePath, discovery, false) })
		}
	}
	t.Run("idle-approval", func(t *testing.T) { testPhoneBrowserFixture(t, "/credentials/", true, true) })
}

func testPhoneBrowserFixture(t *testing.T, pagePath string, discovery, idle bool) {
	t.Helper()
	fixture := newFixture(t)
	var elapsed atomic.Int64
	fixture.controller.now = func() time.Time { return time.Now().Add(time.Duration(elapsed.Load())) }
	if discovery {
		rules := []credentialbroker.AccountRule{{
			Alias: "facebook", VaultID: "private-vault", DiscoverLogins: true,
			Principals: []credentialbroker.Principal{testPrincipal},
		}}
		if fixture.controller.ReplacePolicy(t.Context(), rules) != nil {
			t.Fatal("discovery policy fixture unavailable")
		}
	}
	fixture.controller.pagePath = pagePath
	enrollment, err := NewEnrollment(fixture.setup)
	if err != nil {
		t.Fatal("enrollment unavailable")
	}
	handler, err := NewPhoneHandler(fixture.controller)
	if err != nil {
		t.Fatal("phone handler unavailable")
	}
	invite, err := enrollment.issue(context.Background())
	if err != nil {
		t.Fatal("synthetic setup unavailable")
	}
	defer clear(invite)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_fixture/idle":
			elapsed.Store(int64(PendingLifetime + time.Minute))
			writePhoneResponse(writer, phoneResponse{Status: "pending"})
		case "/_fixture/request":
			id, requestErr := fixture.controller.Request(
				request.Context(),
				testPrincipal,
				[]string{"facebook"},
				credentialbroker.MaxGrantLifetime,
			)
			if requestErr != nil {
				writePhoneError(writer, http.StatusForbidden)
				return
			}
			writePhoneResponse(writer, phoneResponse{Status: "pending", ID: id})
		case "/_fixture/check":
			var body struct {
				ID string `json:"id"`
			}
			if json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1024)).Decode(&body) != nil {
				writePhoneError(writer, http.StatusBadRequest)
				return
			}
			if fixture.controller.Check(
				request.Context(),
				body.ID,
				checkedRequest(fixture.controller.policy.Revision()),
			) != nil {
				writePhoneResponse(writer, phoneResponse{Status: "denied"})
				return
			}
			writePhoneResponse(writer, phoneResponse{Status: "active"})
		case pagePath + "api/enroll/begin", pagePath + "api/enroll/finish":
			enrollment.ServeHTTP(writer, request)
		default:
			handler.ServeHTTP(writer, request)
		}
	}))
	certificate, ca := phoneFixtureCertificate(t)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}}
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.Config.ReadHeaderTimeout = 2 * time.Second
	server.Config.ReadTimeout = 12 * time.Second
	server.Config.WriteTimeout = 12 * time.Second
	server.Config.MaxHeaderBytes = 8192
	server.StartTLS()
	defer server.Close()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("fixture path unavailable")
	}
	config := map[string]string{
		"backend":            server.URL,
		"ca":                 string(ca),
		"invite":             string(invite),
		"page_path":          pagePath,
		"artifact_directory": os.Getenv("MINTCLAW_PHONE_BROWSER_ARTIFACT_DIR"),
	}
	if discovery {
		config["discovery"] = "true"
	}
	if idle {
		config["idle"] = "true"
	}
	input := marshalFixture(t, config)
	defer clear(input)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", filepath.Join(filepath.Dir(source), "phone-browser.test.cjs"))
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	if path := os.Getenv("PLAYWRIGHT_BROWSERS_PATH"); path != "" {
		command.Env = append(command.Env, "PLAYWRIGHT_BROWSERS_PATH="+path)
	}
	command.Stdin = bytes.NewReader(input)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if command.Run() != nil {
		for _, stage := range []string{"fixture-input", "browser-launch", "enrollment-page", "enrollment-passkey", "verification-passkey", "approval-page", "approval-passkey", "revocation-passkey", "replay-denial", "browser-errors", "external-navigation", "route-coverage"} {
			if output.String() == "phone browser qualification: failed at "+stage+"\n" {
				t.Fatalf("synthetic browser/WebAuthn qualification failed at %s", stage)
			}
		}
		t.Fatal("synthetic browser/WebAuthn qualification failed")
	}
	if output.String() != "phone browser qualification: OK\n" || strings.Contains(output.String(), string(invite)) {
		t.Fatal("unexpected browser fixture output")
	}
	if fixture.store.commits != 6 {
		t.Fatal("browser enrollment, verification, approval and revocation were not all persisted")
	}
}

func phoneFixtureCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal("fixture TLS key unavailable")
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "synthetic phone fixture"},
		DNSNames: []string{
			"approval.example.org",
		},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal("fixture TLS certificate unavailable")
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal("fixture TLS certificate invalid")
	}
	return tls.Certificate{
			Certificate: [][]byte{der},
			PrivateKey:  key,
			Leaf:        certificate,
		}, pem.EncodeToMemory(
			&pem.Block{Type: "CERTIFICATE", Bytes: der},
		)
}
