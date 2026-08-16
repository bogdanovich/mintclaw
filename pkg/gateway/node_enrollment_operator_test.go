package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

func TestNodeEnrollmentOperatorIssuesAuthenticatedOffer(t *testing.T) {
	manager := nodes.NewEnrollmentOfferManager(nodes.EnrollmentOfferConfig{})
	handler := newNodeEnrollmentOperatorHandler("operator-token", manager)
	request := httptest.NewRequest(http.MethodPost, nodeEnrollmentOperatorPath, strings.NewReader(
		`{"endpoint":"wss://gateway.example/nodes/v1/ws","ttl_seconds":60}`,
	))
	request.Header.Set("Authorization", "Bearer operator-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
	var result nodes.EnrollmentOfferResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	decoded, err := nodes.DecodeEnrollmentOfferURI(result.URI)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != result.Offer || result.Offer.ExpiresAt-result.Offer.IssuedAt != 60 {
		t.Fatalf("offer response = %#v", result)
	}
}

func TestNodeEnrollmentOperatorFailsClosed(t *testing.T) {
	manager := nodes.NewEnrollmentOfferManager(nodes.EnrollmentOfferConfig{MaxOffers: 1})
	handler := newNodeEnrollmentOperatorHandler("operator-token", manager)
	validBody := `{"endpoint":"wss://gateway.example/nodes/v1/ws","ttl_seconds":60}`
	tests := []struct {
		name   string
		method string
		token  string
		body   string
		status int
	}{
		{name: "missing token", method: http.MethodPost, body: validBody, status: http.StatusUnauthorized},
		{
			name:   "wrong token",
			method: http.MethodPost,
			token:  "wrong",
			body:   validBody,
			status: http.StatusUnauthorized,
		},
		{name: "method", method: http.MethodGet, token: "operator-token", status: http.StatusMethodNotAllowed},
		{
			name:   "unknown field",
			method: http.MethodPost,
			token:  "operator-token",
			body:   `{"extra":true}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "oversized",
			method: http.MethodPost,
			token:  "operator-token",
			body:   strings.Repeat("x", nodeEnrollmentOperatorBodyLimit+1),
			status: http.StatusBadRequest,
		},
		{
			name:   "long lifetime",
			method: http.MethodPost,
			token:  "operator-token",
			body:   `{"endpoint":"wss://gateway.example/nodes/v1/ws","ttl_seconds":301}`,
			status: http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, nodeEnrollmentOperatorPath, strings.NewReader(test.body))
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.status, response.Body.String())
			}
		})
	}
}

func TestNodeEnrollmentOperatorReportsCapacity(t *testing.T) {
	manager := nodes.NewEnrollmentOfferManager(nodes.EnrollmentOfferConfig{MaxOffers: 1})
	handler := newNodeEnrollmentOperatorHandler("operator-token", manager)
	body := []byte(`{"endpoint":"wss://gateway.example/nodes/v1/ws","ttl_seconds":60}`)
	for attempt, want := range []int{http.StatusCreated, http.StatusTooManyRequests} {
		request := httptest.NewRequest(http.MethodPost, nodeEnrollmentOperatorPath, bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer operator-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt, response.Code, want)
		}
	}
}

func TestNodeEnrollmentOperatorReportsInvalidatedGeneration(t *testing.T) {
	manager := nodes.NewEnrollmentOfferManager(nodes.EnrollmentOfferConfig{})
	manager.Invalidate()
	handler := newNodeEnrollmentOperatorHandler("operator-token", manager)
	request := httptest.NewRequest(
		http.MethodPost,
		nodeEnrollmentOperatorPath,
		strings.NewReader(`{"endpoint":"wss://gateway.example/nodes/v1/ws","ttl_seconds":60}`),
	)
	request.Header.Set("Authorization", "Bearer operator-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), "OFFER_UNAVAILABLE") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestNodeEnrollmentOperatorUsesDefaultLifetime(t *testing.T) {
	now := time.Unix(1000, 0)
	manager := nodes.NewEnrollmentOfferManager(nodes.EnrollmentOfferConfig{Now: func() time.Time { return now }})
	handler := newNodeEnrollmentOperatorHandler("operator-token", manager)
	request := httptest.NewRequest(
		http.MethodPost,
		nodeEnrollmentOperatorPath,
		strings.NewReader(`{"endpoint":"wss://gateway.example/nodes/v1/ws"}`),
	)
	request.Header.Set("Authorization", "Bearer operator-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var result nodes.EnrollmentOfferResponse
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &result) != nil {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if result.Offer.ExpiresAt-result.Offer.IssuedAt != int64(nodes.DefaultEnrollmentOfferTTL/time.Second) {
		t.Fatalf("offer lifetime = %d", result.Offer.ExpiresAt-result.Offer.IssuedAt)
	}
}
