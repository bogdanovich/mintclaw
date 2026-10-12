package approval

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
)

func TestApprovalOnCanonicalHTTPSPortVerifiesExactOrigin(t *testing.T) {
	fixture := newFixture(t)
	config := testConfig()
	config.Origin = testOrigin + ":18443"
	controller, setup, err := newController(config, fixture.store)
	if err != nil || setup == nil {
		t.Fatal("canonical HTTPS port rejected")
	}
	t.Cleanup(func() { _ = controller.Close() })
	fixture.controller, fixture.setup = controller, setup
	ceremony, err := setup.Begin(context.Background())
	if err != nil || setup.Finish(context.Background(), ceremony.ID,
		fixture.registration(t, ceremony, config.Origin, protocol.FlagUserPresent|protocol.FlagUserVerified)) != nil {
		t.Fatal("exact-port passkey enrollment failed")
	}
	_, ceremony = fixture.approval(t, time.Hour)
	if _, err = controller.FinishApproval(
		context.Background(),
		ceremony.ID,
		fixture.assertion(
			t,
			ceremony,
			testOrigin,
			protocol.FlagUserPresent|protocol.FlagUserVerified,
			1,
			false,
		),
	); err == nil {
		t.Fatal("assertion from another port approved access")
	}
	_, ceremony = fixture.approval(t, time.Hour)
	if _, err = controller.FinishApproval(
		context.Background(),
		ceremony.ID,
		fixture.assertion(
			t,
			ceremony,
			config.Origin,
			protocol.FlagUserPresent|protocol.FlagUserVerified,
			1,
			false,
		),
	); err != nil {
		t.Fatal("assertion from exact port rejected")
	}
}

func TestPhoneLoopbackProxyAdmissionIsExplicitAndOriginBound(t *testing.T) {
	fixture := newFixture(t)
	fixture.controller.pagePath = "/credentials/"
	fixture.controller.tlsProxy = true
	handler, err := NewPhoneHandler(fixture.controller)
	if err != nil {
		t.Fatal("runtime phone handler unavailable")
	}
	for _, test := range []struct {
		name, address, proto, host, origin string
		allowed                            bool
	}{
		{"local proxy", "127.0.0.1:12345", "https", "approval.example.org", "", true},
		{"IPv6 proxy", "[::1]:12345", "https", "approval.example.org", "", true},
		{"untrusted peer", "192.0.2.10:12345", "https", "approval.example.org", "", false},
		{"plaintext", "127.0.0.1:12345", "http", "approval.example.org", "", false},
		{"missing proto", "127.0.0.1:12345", "", "approval.example.org", "", false},
		{"wrong host", "127.0.0.1:12345", "https", "other.example.org", "", false},
		{"wrong origin", "127.0.0.1:12345", "https", "approval.example.org", "https://other.example.org", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/credentials/", nil)
			request.Host, request.RemoteAddr = test.host, test.address
			request.Header.Set("X-Forwarded-Proto", test.proto)
			request.Header.Set("Origin", test.origin)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if (response.Code == http.StatusOK) != test.allowed {
				t.Fatal("phone proxy admission differs from configured transport/origin")
			}
		})
	}
	handler.boundary.tlsProxy = false
	request := httptest.NewRequest(http.MethodGet, "/credentials/", nil)
	request.Host, request.RemoteAddr = "approval.example.org", "127.0.0.1:12345"
	request.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatal("forwarded header enabled an unconfigured proxy")
	}
	enrollment, err := NewEnrollment(fixture.setup)
	if err != nil {
		t.Fatal("private enrollment unavailable")
	}
	invite, err := enrollment.Issue(context.Background())
	defer clear(invite)
	if err != nil || !strings.HasPrefix(string(invite), testOrigin+"/credentials/#enroll=") {
		t.Fatal("private enrollment link escaped the mounted page")
	}
}
