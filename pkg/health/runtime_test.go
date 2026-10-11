package health

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testRuntimeProbe() RuntimeProbe {
	return RuntimeProbe{
		Version: 1, PID: 42, InstanceSHA256: strings.Repeat("1", 64), ConfigSHA256: strings.Repeat("2", 64),
		HomeSHA256: strings.Repeat("3", 64), ConfigPathSHA256: strings.Repeat("4", 64),
		LogLevel: "info", BuildVersion: "dev", GOOS: "synthetic", GOARCH: "synthetic",
	}
}

func TestRuntimeProbeAuthenticationAndReadiness(t *testing.T) {
	for _, mode := range []string{"valid", "empty-server-token", "missing-token", "wrong-token", "not-ready", "missing-probe", "method", "callback-error", "invalid-projection"} {
		t.Run(mode, func(t *testing.T) {
			server := NewServer("private-service-token")
			server.SetReady(mode != "not-ready")
			calls := 0
			server.SetRuntimeProbe(func() (RuntimeProbe, error) {
				calls++
				if mode == "callback-error" {
					return RuntimeProbe{}, errors.New("private-error-canary")
				}
				result := testRuntimeProbe()
				if mode == "invalid-projection" {
					result.ConfigSHA256 = "private-invalid-canary"
				}
				return result, nil
			})
			if mode == "empty-server-token" {
				server.authToken = ""
			}
			if mode == "missing-probe" {
				server.SetRuntimeProbe(nil)
			}
			method, token, status := http.MethodGet, "private-service-token", http.StatusOK
			switch mode {
			case "empty-server-token", "missing-token", "wrong-token":
				status = http.StatusUnauthorized
			case "not-ready", "missing-probe", "callback-error", "invalid-projection":
				status = http.StatusServiceUnavailable
			case "method":
				method, status = http.MethodPost, http.StatusMethodNotAllowed
			}
			switch mode {
			case "missing-token":
				token = ""
			case "wrong-token":
				token = "wrong-private-canary"
			}
			request := httptest.NewRequest(method, "/runtime?token=private-service-token", nil)
			if token != "" {
				request.Header.Set("Authorization", "Bearer "+token)
			}
			mux := http.NewServeMux()
			server.RegisterOnMux(mux)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != status || response.Header().Get("Cache-Control") != "no-store" ||
				strings.Contains(response.Body.String(), "private-") {
				t.Fatal("unsafe runtime projection", response.Code)
			}
			if mode == "valid" {
				var result RuntimeProbe
				if json.Unmarshal(response.Body.Bytes(), &result) != nil || result != testRuntimeProbe() || calls != 1 {
					t.Fatal("valid observation was not returned")
				}
			} else if mode != "callback-error" && mode != "invalid-projection" && calls != 0 {
				t.Fatal("runtime callback ran without admission")
			}
		})
	}
}
