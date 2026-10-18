package approval

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
)

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	read, write time.Time
}

func (writer *deadlineRecorder) SetReadDeadline(deadline time.Time) error {
	writer.read = deadline
	return nil
}

func (writer *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	writer.write = deadline
	return nil
}

func phoneRequestFor(path string, body []byte) *http.Request {
	request := httptest.NewRequest(http.MethodPost, testOrigin+path, bytes.NewReader(body))
	request.Header.Set("Origin", testOrigin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.URL.Scheme, request.URL.Host = "", ""
	request.TLS = &tls.ConnectionState{Version: tls.VersionTLS13, ServerName: "approval.example.org"}
	return request
}

func phoneCall(t *testing.T, handler http.Handler, path string, body any) phoneResponse {
	t.Helper()
	writer := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	handler.ServeHTTP(writer, phoneRequestFor(path, marshalFixture(t, body)))
	if writer.Code != http.StatusOK || writer.read.IsZero() || writer.write.IsZero() {
		t.Fatal("phone request failed or was unbounded", writer.Code)
	}
	var response phoneResponse
	if json.Unmarshal(writer.Body.Bytes(), &response) != nil {
		t.Fatal("invalid phone response")
	}
	return response
}

func bootstrap(t *testing.T, enrollment *Enrollment) string {
	t.Helper()
	link, err := enrollment.issue(context.Background())
	if err != nil {
		t.Fatal("operator bootstrap failed")
	}
	return strings.TrimPrefix(string(link), testOrigin+"/#enroll=")
}

func TestPhoneHTTPLifecycle(t *testing.T) {
	fixture := newFixture(t)
	enrollment, err := NewEnrollment(fixture.setup)
	if err != nil {
		t.Fatal("enrollment handler failed")
	}
	handler, err := NewPhoneHandler(fixture.controller)
	if err != nil {
		t.Fatal("phone handler failed")
	}
	secret := bootstrap(t, enrollment)
	begin := phoneCall(t, enrollment, "/api/enroll/begin", map[string]any{"token": secret})
	if strings.Contains(string(begin.Options), secret) {
		t.Fatal("bootstrap escaped into public options")
	}
	ceremony := Ceremony{ID: begin.ID, Options: begin.Options}
	response := phoneCall(t, enrollment, "/api/enroll/finish", map[string]any{
		"token": secret, "id": begin.ID, "credential": json.RawMessage(fixture.registration(t, ceremony, testOrigin,
			protocol.FlagUserPresent|protocol.FlagUserVerified)),
	})
	if response.Status != "enrolled" || response.GrantID != "" {
		t.Fatal("enrollment issued authority")
	}
	id, err := fixture.controller.Request(
		context.Background(),
		testPrincipal,
		[]string{"facebook"},
		credentialbroker.MaxGrantLifetime,
	)
	if err != nil {
		t.Fatal("request failed")
	}
	begin = phoneCall(t, handler, "/api/approve/begin", map[string]any{"id": id})
	if begin.Scope == nil || begin.Scope.Principal != testPrincipal || begin.Scope.DurationSeconds != 72000 ||
		len(begin.Scope.Accounts) != 1 || begin.Scope.Accounts[0] != "facebook" {
		t.Fatal("phone scope changed")
	}
	ceremony = Ceremony{ID: begin.ID, Options: begin.Options}
	response = phoneCall(t, handler, "/api/approve/finish", map[string]any{
		"id": begin.ID, "credential": json.RawMessage(fixture.assertion(t, ceremony, testOrigin,
			protocol.FlagUserPresent|protocol.FlagUserVerified, 1, false)),
	})
	if response.Status != "approved" || !intent.ValidReference(response.GrantID) || response.ExpiresAt == "" {
		t.Fatal("phone approval failed")
	}
	grantID := response.GrantID
	if fixture.controller.Check(
		context.Background(),
		grantID,
		checkedRequest(fixture.controller.policy.Revision()),
	) != nil {
		t.Fatal("approved grant unavailable")
	}
	begin = phoneCall(t, handler, "/api/revoke/begin", map[string]any{"id": grantID})
	if begin.Scope == nil || begin.Scope.GrantID != grantID {
		t.Fatal("revoke omitted grant scope")
	}
	ceremony = Ceremony{ID: begin.ID, Options: begin.Options}
	response = phoneCall(t, handler, "/api/revoke/finish", map[string]any{
		"id": begin.ID, "credential": json.RawMessage(fixture.assertion(t, ceremony, testOrigin,
			protocol.FlagUserPresent|protocol.FlagUserVerified, 2, false)),
	})
	if response.Status != "revoked" || fixture.controller.Check(context.Background(), grantID,
		checkedRequest(fixture.controller.policy.Revision())) == nil {
		t.Fatal("phone revocation did not stop access")
	}
}

func TestPhoneRejectsWrongBoundariesAndMalformedBodies(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	handler, err := NewPhoneHandler(fixture.controller)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := fixture.approval(t, time.Hour)
	valid := marshalFixture(t, map[string]string{"id": id})
	cases := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"plaintext", func(r *http.Request) { r.TLS = nil }},
		{"tls12", func(r *http.Request) { r.TLS.Version = tls.VersionTLS12 }},
		{"sni", func(r *http.Request) { r.TLS.ServerName = "attacker.example.org" }},
		{"host", func(r *http.Request) { r.Host = "attacker.example.org" }},
		{"origin", func(r *http.Request) { r.Header.Set("Origin", "https://attacker.example.org") }},
		{"missing origin", func(r *http.Request) { r.Header.Del("Origin") }},
		{"duplicate origin", func(r *http.Request) { r.Header.Add("Origin", testOrigin) }},
		{"cross site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }},
		{"same site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }},
		{"query", func(r *http.Request) { r.URL.RawQuery = "token=SYNTHETIC_PRIVATE_MARKER" }},
		{"empty query", func(r *http.Request) { r.URL.ForceQuery = true }},
		{"escaped path", func(r *http.Request) { r.URL.RawPath = "/api/%61pprove/begin" }},
		{"fragment", func(r *http.Request) { r.URL.Fragment = "SYNTHETIC_PRIVATE_MARKER" }},
		{"absolute URI", func(r *http.Request) { r.URL.Scheme = "https"; r.URL.Host = "approval.example.org" }},
		{"form", func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }},
		{"charset", func(r *http.Request) { r.Header.Set("Content-Type", "application/json;charset=utf-16") }},
		{"parameter", func(r *http.Request) { r.Header.Set("Content-Type", "application/json;extra=utf-8") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := phoneRequestFor("/api/approve/begin", valid)
			// Actual HTTP server requests have an origin-form URL, unlike NewRequest.
			r.URL.Scheme, r.URL.Host = "", ""
			tc.mutate(r)
			writer := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			handler.ServeHTTP(writer, r)
			if writer.Code < 400 || writer.Body.String() != `{"status":"unavailable"}` {
				t.Fatal("unsafe request accepted or echoed")
			}
		})
	}
	for _, body := range []string{
		`{"id":"` + id + `","id":"` + id + `"}`, `{"ID":"` + id + `"}`, `{"id":null}`,
		`{"id":"` + id + `","principal":"SYNTHETIC_PRIVATE_MARKER"}`, `{"id":"` + id + `"} {}`,
		`[]`, `{"id":{}}`, `{"id":"` + strings.Repeat("x", maxResponseBytes+1024) + `"}`,
	} {
		r := phoneRequestFor("/api/approve/begin", []byte(body))
		r.URL.Scheme, r.URL.Host = "", ""
		writer := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		handler.ServeHTTP(writer, r)
		if writer.Code != http.StatusBadRequest || writer.Body.String() != `{"status":"unavailable"}` {
			t.Fatal("invalid body accepted or echoed")
		}
	}
}

func TestPhoneRouteSeparationCapacityAndDeadlines(t *testing.T) {
	fixture := newFixture(t)
	enrollment, _ := NewEnrollment(fixture.setup)
	handler, _ := NewPhoneHandler(fixture.controller)
	secret := bootstrap(t, enrollment)
	for _, path := range []string{"/api/enroll/begin", "/api/enroll/finish", "/api/enroll/issue", "/api/get_secret"} {
		writer := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		request := phoneRequestFor(path, []byte(`{"id":"`+secret+`"}`))
		request.URL.Scheme, request.URL.Host = "", ""
		handler.ServeHTTP(writer, request)
		if writer.Code != http.StatusNotFound {
			t.Fatal("ordinary handler exposed enrollment or retrieval")
		}
	}
	for _, target := range []struct {
		handler  http.Handler
		boundary phoneBoundary
		path     string
		body     []byte
	}{
		{handler, handler.boundary, "/api/approve/begin", []byte(`{"id":"` + secret + `"}`)},
		{enrollment, enrollment.boundary, "/api/enroll/begin", []byte(`{"token":"` + secret + `"}`)},
	} {
		for range cap(target.boundary.slots) {
			target.boundary.slots <- struct{}{}
		}
		writer := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		request := phoneRequestFor(target.path, target.body)
		request.URL.Scheme, request.URL.Host = "", ""
		target.handler.ServeHTTP(writer, request)
		if writer.Code != http.StatusServiceUnavailable {
			t.Fatal("capacity limit was bypassed")
		}
		for range cap(target.boundary.slots) {
			<-target.boundary.slots
		}
		unsupported := httptest.NewRecorder()
		target.handler.ServeHTTP(unsupported, request)
		if unsupported.Code != http.StatusServiceUnavailable || len(target.boundary.slots) != 0 {
			t.Fatal("unsupported deadlines admitted or leaked a slot")
		}
	}
	if len(fixture.controller.owner.Credentials) != 0 {
		t.Fatal("HTTP issued enrollment authority")
	}
}

func TestEnrollmentBootstrapLifecycle(t *testing.T) {
	fixture := newFixture(t)
	enrollment, _ := NewEnrollment(fixture.setup)
	oldSecret := bootstrap(t, enrollment)
	old, err := enrollment.begin(context.Background(), oldSecret)
	if err != nil {
		t.Fatal(err)
	}
	secret := bootstrap(t, enrollment)
	if _, err = enrollment.begin(context.Background(), oldSecret); !errors.Is(err, ErrDenied) {
		t.Fatal("old operator bootstrap survived")
	}
	if _, exists := fixture.controller.ceremonies[old.ID]; exists {
		t.Fatal("old enrollment ceremony survived")
	}
	begin, err := enrollment.begin(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := enrollment.begin(context.Background(), secret)
	if err != nil || repeat.ID != begin.ID || !bytes.Equal(begin.Options, repeat.Options) {
		t.Fatal("reopen replaced the enrollment challenge")
	}
	if enrollment.finish(context.Background(), strings.Repeat("0", 64), begin.ID,
		fixture.registration(t, begin, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified)) == nil {
		t.Fatal("locator became an enrollment bearer")
	}
	if enrollment.finish(context.Background(), secret, begin.ID, []byte(`{"invalid":true}`)) == nil {
		t.Fatal("malformed registration accepted")
	}
	if _, err = enrollment.begin(context.Background(), secret); err == nil {
		t.Fatal("failed registration did not consume invite")
	}
	secret = bootstrap(t, enrollment)
	begin, err = enrollment.begin(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.finish(context.Background(), secret, begin.ID,
		fixture.registration(t, begin, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified)) != nil {
		t.Fatal("fresh enrollment failed")
	}
	if _, err = enrollment.issue(context.Background()); err == nil {
		t.Fatal("operator setup replaced enrolled owner")
	}
}

func TestEnrollmentOriginalDeadlineAndSafeZero(t *testing.T) {
	fixture := newFixture(t)
	enrollment, _ := NewEnrollment(fixture.setup)
	secret := bootstrap(t, enrollment)
	deadline := time.Now().Add(500 * time.Millisecond)
	enrollment.issued = deadline.Add(-PendingLifetime)
	begin, err := enrollment.begin(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	fixture.store.onCommit = func() { time.Sleep(time.Until(deadline) + 10*time.Millisecond) }
	if enrollment.finish(context.Background(), secret, begin.ID,
		fixture.registration(t, begin, testOrigin, protocol.FlagUserPresent|protocol.FlagUserVerified)) == nil {
		t.Fatal("late begin renewed bootstrap lifetime during commit")
	}
	if fixture.store.commits != 1 {
		t.Fatal("fixture did not exercise an in-flight durable write")
	}
	assertEnrollmentInactive(t, fixture.controller)
	restarted, pendingSetup, reloadErr := newController(testConfig(), fixture.store)
	if reloadErr != nil || pendingSetup == nil {
		t.Fatal("rejected enrollment lost protected recovery")
	}
	defer func() { _ = restarted.Close() }()
	assertEnrollmentInactive(t, restarted)
	for _, empty := range []*Enrollment{nil, {}, {setup: &OwnerSetup{}}} {
		if _, err = empty.issue(context.Background()); err == nil {
			t.Fatal("zero enrollment issued")
		}
		if _, err = empty.begin(context.Background(), secret); err == nil {
			t.Fatal("zero enrollment began")
		}
		if empty.finish(context.Background(), secret, begin.ID, nil) == nil {
			t.Fatal("zero enrollment finished")
		}
	}
}

func TestCanceledEnrollmentAllowsFreshPrivateSetup(t *testing.T) {
	fixture := newFixture(t)
	enrollment, _ := NewEnrollment(fixture.setup)
	secret := bootstrap(t, enrollment)
	begin, err := enrollment.begin(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if enrollment.finish(ctx, secret, begin.ID, nil) == nil {
		t.Fatal("canceled enrollment completed")
	}
	if _, exists := fixture.controller.ceremonies[begin.ID]; exists {
		t.Fatal("canceled enrollment retained an orphan ceremony")
	}
	secret = bootstrap(t, enrollment)
	if _, err = enrollment.begin(context.Background(), secret); err != nil {
		t.Fatal("fresh private setup was blocked by canceled enrollment")
	}
}

func TestReopenPreservesApprovalDeadlineAndPublicCopies(t *testing.T) {
	fixture := newFixture(t)
	fixture.enroll(t)
	id, begin := fixture.approval(t, time.Hour)
	now := fixture.controller.now()
	fixture.controller.now = func() time.Time { return now.Add(PendingLifetime - time.Second) }
	begin.Options[0] = '!'
	begin.Scope.Accounts[0] = "ebay"
	repeat, err := fixture.controller.BeginApproval(context.Background(), id)
	if err != nil || repeat.ID != begin.ID || repeat.Options[0] == '!' || repeat.Scope.Accounts[0] != "facebook" {
		t.Fatal("reopen lost immutable original ceremony")
	}
	fixture.controller.now = func() time.Time { return now.Add(RequestLifetime) }
	if _, err = fixture.controller.BeginApproval(context.Background(), id); err == nil {
		t.Fatal("reopen renewed approval request")
	}
}
