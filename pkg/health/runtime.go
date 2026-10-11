package health

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"unicode/utf8"
)

// RuntimeProbe is a value-free identity observation. It neither certifies a
// sandbox nor grants tool authority. Fingerprints do not expose configuration.
type RuntimeProbe struct {
	Version          int    `json:"version"`
	PID              int    `json:"pid"`
	InstanceSHA256   string `json:"instance_sha256"`
	ConfigSHA256     string `json:"config_sha256"`
	HomeSHA256       string `json:"home_sha256"`
	ConfigPathSHA256 string `json:"config_path_sha256"`
	LogLevel         string `json:"log_level"`
	BuildVersion     string `json:"build_version"`
	GitCommit        string `json:"git_commit"`
	BuildTime        string `json:"build_time"`
	GoVersion        string `json:"go_version"`
	GOOS             string `json:"goos"`
	GOARCH           string `json:"goarch"`
}

func (s *Server) SetRuntimeProbe(probe func() (RuntimeProbe, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtimeProbe = probe
}

func (s *Server) runtimeHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	probe, token, ready := s.runtimeProbe, s.authToken, s.ready
	s.mu.RUnlock()
	given := extractBearerToken(r.Header.Get("Authorization"))
	if token == "" || given == "" || subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if !ready || probe == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	result, err := probe()
	if err != nil || !result.valid() {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

func (probe RuntimeProbe) valid() bool {
	if probe.Version != 1 || probe.PID <= 0 ||
		!slices.Contains([]string{"debug", "info", "warn", "error", "fatal"}, probe.LogLevel) {
		return false
	}
	for _, hash := range []string{probe.InstanceSHA256, probe.ConfigSHA256, probe.HomeSHA256, probe.ConfigPathSHA256} {
		if len(hash) != 64 {
			return false
		}
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != 32 {
			return false
		}
	}
	for _, value := range []string{
		probe.BuildVersion, probe.GitCommit, probe.BuildTime, probe.GoVersion, probe.GOOS, probe.GOARCH,
	} {
		if len(value) > 512 || !utf8.ValidString(value) {
			return false
		}
	}
	return true
}
