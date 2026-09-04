package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"testing"
)

func setGatewayVersionState(t *testing.T, h *Handler, pid int, alive *bool) {
	t.Helper()
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("FindProcess(%d) error = %v", pid, err)
	}
	h.gateway.cmd = &exec.Cmd{Process: process}
	h.gateway.processAlive = func(*exec.Cmd) bool { return *alive }
}

func TestGetSystemVersionUsesMintClawBinaryInfo(t *testing.T) {
	h := NewHandler("")

	h.gateway.fallbackVersion = func() systemVersionResponse {
		return systemVersionResponse{Version: "fallback", GoVersion: "go-fallback"}
	}

	h.gateway.findBinary = func() string { return "mintclaw" }
	h.gateway.runVersionOutput = func(_ context.Context, _ string) (string, error) {
		return "mintclaw v1.2.3 (git: deadbeef)\nBuild: 2026-03-27T12:34:56Z\nGo: go1.25.8\n", nil
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/system/version", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var got systemVersionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got.Version != "v1.2.3" {
		t.Fatalf("version = %q, want %q", got.Version, "v1.2.3")
	}
	if got.GitCommit != "deadbeef" {
		t.Fatalf("git_commit = %q, want %q", got.GitCommit, "deadbeef")
	}
	if got.BuildTime != "2026-03-27T12:34:56Z" {
		t.Fatalf("build_time = %q, want %q", got.BuildTime, "2026-03-27T12:34:56Z")
	}
	if got.GoVersion != "go1.25.8" {
		t.Fatalf("go_version = %q, want %q", got.GoVersion, "go1.25.8")
	}
}

func TestGetSystemVersionFallsBackToLauncherInfoWhenCommandFails(t *testing.T) {
	h := NewHandler("")

	expected := systemVersionResponse{
		Version:   "v9.9.9",
		GitCommit: "cafebabe",
		BuildTime: "2026-03-27T10:43:34+0000",
		GoVersion: "go1.25.8",
	}
	h.gateway.fallbackVersion = func() systemVersionResponse { return expected }

	h.gateway.findBinary = func() string { return "mintclaw" }
	h.gateway.runVersionOutput = func(_ context.Context, _ string) (string, error) {
		return "", errors.New("binary unavailable")
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/system/version", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var got systemVersionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got.Version != expected.Version {
		t.Fatalf("version = %q, want %q", got.Version, expected.Version)
	}
	if got.GitCommit != expected.GitCommit {
		t.Fatalf("git_commit = %q, want %q", got.GitCommit, expected.GitCommit)
	}
	if got.BuildTime != expected.BuildTime {
		t.Fatalf("build_time = %q, want %q", got.BuildTime, expected.BuildTime)
	}
	if got.GoVersion != expected.GoVersion {
		t.Fatalf("go_version = %q, want %q", got.GoVersion, expected.GoVersion)
	}
}

func TestParseMintClawVersionOutput(t *testing.T) {
	raw := "mintclaw 18ec263 (git: 18ec2631)\nBuild: 2026-03-27T10:43:34+0000\nGo: go1.25.8\n"
	got, ok := parseMintClawVersionOutput(raw)
	if !ok {
		t.Fatal("parseMintClawVersionOutput() should parse valid output")
	}
	if got.Version != "18ec263" {
		t.Fatalf("version = %q, want %q", got.Version, "18ec263")
	}
	if got.GitCommit != "18ec2631" {
		t.Fatalf("git_commit = %q, want %q", got.GitCommit, "18ec2631")
	}
	if got.BuildTime != "2026-03-27T10:43:34+0000" {
		t.Fatalf("build_time = %q, want %q", got.BuildTime, "2026-03-27T10:43:34+0000")
	}
	if got.GoVersion != "go1.25.8" {
		t.Fatalf("go_version = %q, want %q", got.GoVersion, "go1.25.8")
	}
}

func TestParseMintClawVersionOutputIgnoresUsageLine(t *testing.T) {
	raw := "Usage: mintclaw version [flags]\n"
	got, ok := parseMintClawVersionOutput(raw)
	if ok {
		t.Fatalf("parseMintClawVersionOutput() parsed usage line unexpectedly: %#v", got)
	}
}

func TestParseMintClawVersionOutputAcceptsLetterOnlyHashVersion(t *testing.T) {
	raw := "mintclaw abcdefa (git: abcdefabcdefabcdefabcdefabcdefabcdefabcd)\n"
	got, ok := parseMintClawVersionOutput(raw)
	if !ok {
		t.Fatal("parseMintClawVersionOutput() should parse letter-only hash version")
	}
	if got.Version != "abcdefa" {
		t.Fatalf("version = %q, want %q", got.Version, "abcdefa")
	}
	if got.GitCommit != "abcdefabcdefabcdefabcdefabcdefabcdefabcd" {
		t.Fatalf("git_commit = %q, want %q", got.GitCommit, "abcdefabcdefabcdefabcdefabcdefabcdefabcd")
	}
}

func TestResolveSystemVersionInfoFallsBackRuntimeGoVersion(t *testing.T) {
	h := NewHandler("")

	h.gateway.fallbackVersion = func() systemVersionResponse {
		return systemVersionResponse{Version: "dev", GoVersion: ""}
	}

	h.gateway.findBinary = func() string { return "mintclaw" }
	h.gateway.runVersionOutput = func(_ context.Context, _ string) (string, error) {
		return "mintclaw v1.0.0\n", nil
	}

	got := h.resolveSystemVersionInfo(context.Background())
	if got.GoVersion != runtime.Version() {
		t.Fatalf("go_version = %q, want runtime version %q", got.GoVersion, runtime.Version())
	}
}

func TestResolveSystemVersionInfoCachesWhileGatewayAlive(t *testing.T) {
	h := NewHandler("")

	h.gateway.fallbackVersion = func() systemVersionResponse {
		return systemVersionResponse{Version: "dev", GoVersion: "go-fallback"}
	}
	h.gateway.findBinary = func() string { return "mintclaw" }

	pid := 4321
	alive := true
	setGatewayVersionState(t, h, pid, &alive)

	runCount := 0
	h.gateway.runVersionOutput = func(_ context.Context, _ string) (string, error) {
		runCount++
		return fmt.Sprintf("mintclaw v1.2.%d\n", runCount), nil
	}

	first := h.resolveSystemVersionInfo(context.Background())
	second := h.resolveSystemVersionInfo(context.Background())

	if first.Version != "v1.2.1" {
		t.Fatalf("first version = %q, want %q", first.Version, "v1.2.1")
	}
	if second.Version != "v1.2.1" {
		t.Fatalf("second version = %q, want cached %q", second.Version, "v1.2.1")
	}
	if runCount != 1 {
		t.Fatalf("run count = %d, want %d", runCount, 1)
	}
}

func TestResolveSystemVersionInfoInvalidatesCacheWhenGatewayStops(t *testing.T) {
	h := NewHandler("")

	h.gateway.fallbackVersion = func() systemVersionResponse {
		return systemVersionResponse{Version: "dev", GoVersion: "go-fallback"}
	}
	h.gateway.findBinary = func() string { return "mintclaw" }

	alive := true
	pid := 9876
	setGatewayVersionState(t, h, pid, &alive)

	runCount := 0
	h.gateway.runVersionOutput = func(_ context.Context, _ string) (string, error) {
		runCount++
		return fmt.Sprintf("mintclaw v2.0.%d\n", runCount), nil
	}

	first := h.resolveSystemVersionInfo(context.Background())
	second := h.resolveSystemVersionInfo(context.Background())

	if first.Version != "v2.0.1" || second.Version != "v2.0.1" {
		t.Fatalf("expected cached version v2.0.1, got first=%q second=%q", first.Version, second.Version)
	}
	if runCount != 1 {
		t.Fatalf("run count after cache hit = %d, want %d", runCount, 1)
	}

	alive = false
	third := h.resolveSystemVersionInfo(context.Background())
	if third.Version != "v2.0.2" {
		t.Fatalf("third version = %q, want refreshed %q", third.Version, "v2.0.2")
	}
	if runCount != 2 {
		t.Fatalf("run count after invalidation = %d, want %d", runCount, 2)
	}
}

func TestResolveSystemVersionInfoSkipsCommandWhenContextCanceled(t *testing.T) {
	h := NewHandler("")

	h.gateway.fallbackVersion = func() systemVersionResponse {
		return systemVersionResponse{Version: "v3.0.0", GoVersion: "go-fallback"}
	}
	h.gateway.findBinary = func() string { return "mintclaw" }

	runCount := 0
	h.gateway.runVersionOutput = func(_ context.Context, _ string) (string, error) {
		runCount++
		return "mintclaw v9.9.9\n", nil
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	got := h.resolveSystemVersionInfo(canceledCtx)

	if runCount != 0 {
		t.Fatalf("run count = %d, want %d", runCount, 0)
	}
	if got.Version != "v3.0.0" {
		t.Fatalf("version = %q, want fallback %q", got.Version, "v3.0.0")
	}
}

func TestResolveGatewayBinaryForVersionInfoPrefersGatewayCommandPath(t *testing.T) {
	h := NewHandler("")

	h.gateway.mu.Lock()
	originalCmd := h.gateway.cmd
	h.gateway.cmd = &exec.Cmd{Path: "/tmp/mintclaw-from-gateway"}
	h.gateway.mu.Unlock()
	t.Cleanup(func() {
		h.gateway.mu.Lock()
		h.gateway.cmd = originalCmd
		h.gateway.mu.Unlock()
	})

	got := h.resolveGatewayBinaryForVersionInfo()
	if got != "/tmp/mintclaw-from-gateway" {
		t.Fatalf("exec path = %q, want %q", got, "/tmp/mintclaw-from-gateway")
	}
}
