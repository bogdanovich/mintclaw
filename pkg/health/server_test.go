package health

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer() *Server {
	return NewServer("test")
}

func TestHealthHandler_ReturnsOK(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	s.healthHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("health status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp StatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("status = %q, want %q", resp.Status, "ok")
	}
	if resp.Uptime == "" {
		t.Error("uptime should not be empty")
	}
}

func TestReadyHandler_NotReady(t *testing.T) {
	s := newTestServer()
	// s.ready defaults to false
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()

	s.readyHandler(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("ready status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}

	var resp StatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != "not ready" {
		t.Errorf("status = %q, want %q", resp.Status, "not ready")
	}
}

func TestReadyHandler_Ready(t *testing.T) {
	s := newTestServer()
	s.SetReady(true)

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()

	s.readyHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("ready status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp StatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != "ready" {
		t.Errorf("status = %q, want %q", resp.Status, "ready")
	}
}

func TestReloadHandler_MethodNotAllowed(t *testing.T) {
	s := newTestServer()

	req := httptest.NewRequest(http.MethodGet, "/reload", nil)
	w := httptest.NewRecorder()

	s.reloadHandler(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("reload GET status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestReloadHandler_NoReloadFunc(t *testing.T) {
	s := newTestServer()

	req := httptest.NewRequest(http.MethodPost, "/reload", nil)
	req.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()

	s.reloadHandler(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("reload without func = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestReloadHandler_Success(t *testing.T) {
	s := newTestServer()
	called := false
	s.SetReloadFunc(func() error {
		called = true
		return nil
	})

	req := httptest.NewRequest(http.MethodPost, "/reload", nil)
	req.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()

	s.reloadHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("reload status = %d, want %d", w.Code, http.StatusOK)
	}
	if !called {
		t.Error("reload function was not called")
	}
}

func TestReloadHandler_Error(t *testing.T) {
	s := newTestServer()
	s.SetReloadFunc(func() error {
		return errors.New("config parse error")
	})

	req := httptest.NewRequest(http.MethodPost, "/reload", nil)
	req.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()

	s.reloadHandler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("reload error status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestSetReady_Toggle(t *testing.T) {
	s := newTestServer()

	s.SetReady(true)
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	s.readyHandler(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("after SetReady(true): status = %d, want %d", w.Code, http.StatusOK)
	}

	s.SetReady(false)
	w = httptest.NewRecorder()
	s.readyHandler(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("after SetReady(false): status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestRegisterOnMux(t *testing.T) {
	s := newTestServer()
	s.SetReady(true)

	mux := http.NewServeMux()
	s.RegisterOnMux(mux)

	// Test /health on custom mux
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("/health on custom mux = %d, want %d", w.Code, http.StatusOK)
	}

	// Test /ready on custom mux
	req = httptest.NewRequest(http.MethodGet, "/ready", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("/ready on custom mux = %d, want %d", w.Code, http.StatusOK)
	}

	// The shared mux owns the reload route as well.
	req = httptest.NewRequest(http.MethodGet, "/reload", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("/reload on custom mux = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}
