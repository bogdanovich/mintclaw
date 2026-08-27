package health

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"
)

type Server struct {
	mu         sync.RWMutex
	ready      bool
	startTime  time.Time
	reloadFunc func() error
	authToken  string // optional bearer token for protected endpoints
}

type StatusResponse struct {
	Status string `json:"status"`
	Uptime string `json:"uptime"`
	PID    int    `json:"pid,omitempty"`
}

func NewServer(token string) *Server {
	return &Server{
		ready:     false,
		startTime: time.Now(),
		authToken: token,
	}
}

func (s *Server) SetReady(ready bool) {
	s.mu.Lock()
	s.ready = ready
	s.mu.Unlock()
}

// SetReloadFunc sets the callback function for config reload.
func (s *Server) SetReloadFunc(fn func() error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadFunc = fn
}

func (s *Server) reloadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed, use POST"})
		return
	}

	// Token check
	s.mu.RLock()
	requiredToken := s.authToken
	s.mu.RUnlock()

	if requiredToken != "" {
		given := extractBearerToken(r.Header.Get("Authorization"))
		if given == "" || subtle.ConstantTimeCompare([]byte(given), []byte(requiredToken)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
	}

	s.mu.Lock()
	reloadFunc := s.reloadFunc
	s.mu.Unlock()

	if reloadFunc == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "reload not configured"})
		return
	}

	if err := reloadFunc(); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "reload triggered"})
}

func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	uptime := time.Since(s.startTime)
	resp := StatusResponse{
		Status: "ok",
		Uptime: uptime.String(),
		PID:    os.Getpid(),
	}

	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) readyHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	s.mu.RLock()
	ready := s.ready
	s.mu.RUnlock()

	if !ready {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(StatusResponse{
			Status: "not ready",
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	uptime := time.Since(s.startTime)
	_ = json.NewEncoder(w).Encode(StatusResponse{
		Status: "ready",
		Uptime: uptime.String(),
	})
}

// HandlerMux is the shared HTTP registration boundary used by the gateway.
type HandlerMux interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

// RegisterOnMux registers /health, /ready and /reload handlers onto the given mux.
func (s *Server) RegisterOnMux(mux HandlerMux) {
	mux.HandleFunc("/health", s.healthHandler)
	mux.HandleFunc("/ready", s.readyHandler)
	mux.HandleFunc("/reload", s.reloadHandler)
}

// extractBearerToken returns the token from an "Authorization: Bearer <t>" header,
// or the empty string if the header is missing or malformed.
func extractBearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) < len(prefix) {
		return ""
	}
	if header[:len(prefix)] != prefix {
		return ""
	}
	return header[len(prefix):]
}
