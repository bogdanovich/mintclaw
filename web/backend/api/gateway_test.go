package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/auth"
	"github.com/bogdanovich/mintclaw/pkg/config"
	ppid "github.com/bogdanovich/mintclaw/pkg/pid"
	"github.com/bogdanovich/mintclaw/web/backend/utils"
)

func startLongRunningProcess(t *testing.T) *exec.Cmd {
	t.Helper()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell", "-NoProfile", "-Command", "Start-Sleep -Seconds 30")
	} else {
		cmd = exec.Command("sleep", "30")
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	return cmd
}

func startGatewayLikeProcess(t *testing.T) *exec.Cmd {
	t.Helper()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		t.Skip("gateway-like process commandline check is not deterministic on Windows tests")
	}
	cmd = exec.Command("sh", "-c", "sleep 30 # mintclaw gateway")

	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	return cmd
}

func writeTestPidFile(t *testing.T, data ppid.PidFileData) string {
	t.Helper()

	path := filepath.Join(globalConfigDir(), ".mintclaw.pid")
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("marshal pid file: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
	return path
}

func mockGatewayHealthResponse(statusCode, pid int) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Body: io.NopCloser(strings.NewReader(
			`{"status":"ok","uptime":"1s","pid":` + strconv.Itoa(pid) + `}`,
		)),
	}
}

func startIgnoringTermProcess(t *testing.T) *exec.Cmd {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("TERM handling differs on Windows")
	}

	cmd := exec.Command("sh", "-c", "trap '' TERM; sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	return cmd
}

func stopManagedGatewayProcess(t *testing.T, h *Handler, cmd *exec.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.gateway.mu.Lock()
		managerReleased := h.gateway.cmd != cmd
		h.gateway.mu.Unlock()
		if managerReleased {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("gateway manager did not release test process")
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func resetGatewayTestState(t *testing.T) {
	t.Helper()
	t.Setenv("MINTCLAW_HOME", t.TempDir())
}

func TestMintClawGatewayProtocol(t *testing.T) {
	resetGatewayTestState(t)
	h := NewHandler("")

	h.gateway.mu.Lock()
	h.gateway.mintclawToken = "ui-token"
	h.gateway.mu.Unlock()

	if got := h.mintclawGatewayProtocol(); got != tokenPrefix+"ui-token" {
		t.Fatalf("h.mintclawGatewayProtocol() = %q, want %q", got, tokenPrefix+"ui-token")
	}
}

func TestHandlersOwnIndependentGatewayProcessState(t *testing.T) {
	first := NewHandler(filepath.Join(t.TempDir(), "first.json"))
	second := NewHandler(filepath.Join(t.TempDir(), "second.json"))
	if first.gateway == second.gateway || first.versionInfoCache == second.versionInfoCache {
		t.Fatal("handlers share gateway process state")
	}

	first.gateway.mu.Lock()
	first.gateway.pidData = &ppid.PidFileData{PID: 101, Port: 19001}
	first.gateway.mintclawToken = "first-token"
	first.gateway.runtimeStatus = "running"
	first.gateway.logs.Append("first-handler-log")
	first.gateway.mu.Unlock()

	second.gateway.mu.Lock()
	defer second.gateway.mu.Unlock()
	_, secondLogTotal, _ := second.gateway.logs.LinesSince(0)
	if second.gateway.pidData != nil || second.gateway.mintclawToken != "" ||
		second.gateway.runtimeStatus != "stopped" || secondLogTotal != 0 {
		t.Fatalf("second handler inherited process state: %#v", second.gateway)
	}
}

type gatewayStartEnvSnapshot struct {
	GatewayHost    string `json:"gateway_host"`
	GatewayHostSet bool   `json:"gateway_host_set"`
	ConfigPath     string `json:"config_path"`
}

func TestGatewayStartHelperProcess(t *testing.T) {
	var envPath string
	for i, arg := range os.Args {
		if arg == "--" && i+2 < len(os.Args) && os.Args[i+1] == "gateway-env-helper" {
			envPath = os.Args[i+2]
			break
		}
	}
	if envPath == "" {
		t.Skip("helper process")
	}

	host, ok := os.LookupEnv(config.EnvGatewayHost)
	raw, err := json.Marshal(gatewayStartEnvSnapshot{
		GatewayHost:    host,
		GatewayHostSet: ok,
		ConfigPath:     os.Getenv(config.EnvConfig),
	})
	if err != nil {
		_, _ = io.WriteString(os.Stderr, err.Error())
		os.Exit(2)
	}
	if err := os.WriteFile(envPath, raw, 0o600); err != nil {
		_, _ = io.WriteString(os.Stderr, err.Error())
		os.Exit(2)
	}
	os.Exit(0)
}

func unsetGatewayStartEnvForTest(t *testing.T, key string) {
	t.Helper()

	prev, hadPrev := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("Unsetenv(%q) error = %v", key, err)
	}
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv(key, prev)
			return
		}
		_ = os.Unsetenv(key)
	})
}

func newGatewayStartTestHandler(t *testing.T) *Handler {
	t.Helper()
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	if err := saveTestConfig(configPath, cfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	h.SetServerOptions(18800, false, false, nil)
	return h
}

func startGatewayAndCaptureEnv(t *testing.T, h *Handler) gatewayStartEnvSnapshot {
	t.Helper()

	unsetGatewayStartEnvForTest(t, config.EnvGatewayHost)

	envPath := filepath.Join(t.TempDir(), "gateway-child-env.json")
	h.gateway.execCommand = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command(
			os.Args[0],
			"-test.run=TestGatewayStartHelperProcess",
			"--",
			"gateway-env-helper",
			envPath,
		)
	}

	pid, err := h.startGatewayLocked("starting", 0)
	if err != nil {
		t.Fatalf("startGatewayLocked() error = %v", err)
	}
	if pid <= 0 {
		t.Fatalf("startGatewayLocked() pid = %d, want > 0", pid)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err := os.ReadFile(envPath)
		if err == nil {
			var snapshot gatewayStartEnvSnapshot
			err = json.Unmarshal(raw, &snapshot)
			if err != nil {
				t.Fatalf("Unmarshal(child env) error = %v", err)
			}
			return snapshot
		}
		if !os.IsNotExist(err) {
			t.Fatalf("ReadFile(%q) error = %v", envPath, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for gateway child env snapshot %q", envPath)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStartGatewayLocked_ForwardsLauncherHostOverrideToGatewayEnv(t *testing.T) {
	h := newGatewayStartTestHandler(t)
	h.SetServerBindHost("127.0.0.1,::1", true)

	snapshot := startGatewayAndCaptureEnv(t, h)
	if !snapshot.GatewayHostSet {
		t.Fatal("gateway host env was not set")
	}
	if snapshot.GatewayHost != "127.0.0.1,::1" {
		t.Fatalf("gateway host env = %q, want %q", snapshot.GatewayHost, "127.0.0.1,::1")
	}
	if snapshot.ConfigPath != h.configPath {
		t.Fatalf("config env = %q, want %q", snapshot.ConfigPath, h.configPath)
	}
}

func TestStartGatewayLocked_ForwardsLauncherHostFromEnvironmentToGatewayEnv(t *testing.T) {
	h := newGatewayStartTestHandler(t)
	h.SetServerBindHost("::", true)

	snapshot := startGatewayAndCaptureEnv(t, h)
	if !snapshot.GatewayHostSet {
		t.Fatal("gateway host env was not set")
	}
	if snapshot.GatewayHost != "::" {
		t.Fatalf("gateway host env = %q, want %q", snapshot.GatewayHost, "::")
	}
}

func TestStartGatewayLocked_ForwardsWildcardHostForPublicLauncher(t *testing.T) {
	h := newGatewayStartTestHandler(t)
	h.SetServerOptions(18800, true, true, nil)

	snapshot := startGatewayAndCaptureEnv(t, h)
	if !snapshot.GatewayHostSet {
		t.Fatal("gateway host env was not set")
	}
	if snapshot.GatewayHost != "*" {
		t.Fatalf("gateway host env = %q, want %q", snapshot.GatewayHost, "*")
	}
}

func TestStartGatewayLocked_UsesReloadedConfigForBootSignature(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep command differs on Windows")
	}

	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	delete(cfg.Channels, "mintclaw")
	if err := saveTestConfig(configPath, cfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	h.SetServerOptions(18800, false, false, nil)
	h.gateway.execCommand = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("sleep", "30")
	}

	originalSignature := computeConfigSignature(cfg)
	pid, err := h.startGatewayLocked("starting", 0)
	if err != nil {
		t.Fatalf("startGatewayLocked() error = %v", err)
	}
	if pid <= 0 {
		t.Fatalf("startGatewayLocked() pid = %d, want > 0", pid)
	}

	h.gateway.mu.Lock()
	cmd := h.gateway.cmd
	bootSignature := h.gateway.bootConfigSignature
	h.gateway.mu.Unlock()
	t.Cleanup(func() {
		stopManagedGatewayProcess(t, h, cmd)
	})

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	expectedSignature := computeConfigSignature(updatedCfg)
	if expectedSignature == originalSignature {
		t.Fatal("expected EnsureMintClawChannel() to change the config signature during gateway start")
	}
	if bootSignature != expectedSignature {
		t.Fatalf("bootConfigSignature = %q, want %q", bootSignature, expectedSignature)
	}
}

func TestGatewayStartReady_NoDefaultModel(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if ready {
		t.Fatalf("gatewayStartReady() ready = true, want false")
	}
	if reason != "no default model configured" {
		t.Fatalf("gatewayStartReady() reason = %q, want %q", reason, "no default model configured")
	}
}

func TestGatewayStartReady_RejectsASROnlyDefaultModel(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "elevenlabs-asr",
		Provider:  "elevenlabs",
		Model:     "scribe_v1",
		APIKeys:   config.SimpleSecureStrings("sk_elevenlabs_test"),
		Enabled:   true,
	}}
	cfg.Agents.Defaults.ModelName = "elevenlabs-asr"

	err = saveTestConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if ready {
		t.Fatal("gatewayStartReady() ready = true, want false")
	}
	if reason != `default model "elevenlabs-asr" is not usable for chat` {
		t.Fatalf(
			"gatewayStartReady() reason = %q, want %q",
			reason,
			`default model "elevenlabs-asr" is not usable for chat`,
		)
	}
}

func TestLooksLikeGatewayCommandLine(t *testing.T) {
	cases := []struct {
		name    string
		cmdline string
		want    bool
	}{
		{
			name:    "default mintclaw gateway",
			cmdline: "/usr/local/bin/mintclaw gateway -E",
			want:    true,
		},
		{
			name:    "renamed binary with gateway subcommand",
			cmdline: "/opt/bin/custom-claw gateway -E -d",
			want:    true,
		},
		{
			name:    "standalone gateway binary path",
			cmdline: "/opt/bin/gateway -E",
			want:    true,
		},
		{
			name:    "non gateway process",
			cmdline: "/bin/sleep 30",
			want:    false,
		},
		{
			name:    "gateway substring only",
			cmdline: "/opt/bin/gatewayd --serve",
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := looksLikeGatewayCommandLine(tc.cmdline)
			if got != tc.want {
				t.Fatalf("looksLikeGatewayCommandLine(%q) = %v, want %v", tc.cmdline, got, tc.want)
			}
		})
	}
}

func TestValidateGatewayPidDataAcceptsHealthWhenMatcherInconclusive(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	const testPID = 34567
	pidData := &ppid.PidFileData{
		PID:  testPID,
		Host: "127.0.0.1",
		Port: 18790,
	}

	h.gateway.processMatcher = func(int) (bool, bool) { return false, false }
	h.gateway.healthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, testPID), nil
	}

	ok, decisive, reason := h.validateGatewayPidData(pidData, nil)
	if !ok {
		t.Fatalf("validateGatewayPidData() ok = false, want true (reason=%q)", reason)
	}
	if !decisive {
		t.Fatalf("validateGatewayPidData() decisive = false, want true")
	}
}

func TestValidateGatewayPidDataRejectsHealthPidMismatchWhenMatcherInconclusive(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	pidData := &ppid.PidFileData{
		PID:  34567,
		Host: "127.0.0.1",
		Port: 18790,
	}

	h.gateway.processMatcher = func(int) (bool, bool) { return false, false }
	h.gateway.healthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, 99999), nil
	}

	ok, decisive, reason := h.validateGatewayPidData(pidData, nil)
	if ok {
		t.Fatalf("validateGatewayPidData() ok = true, want false")
	}
	if !decisive {
		t.Fatalf("validateGatewayPidData() decisive = false, want true")
	}
	if !strings.Contains(reason, "health pid mismatch") {
		t.Fatalf("validateGatewayPidData() reason = %q, want contains %q", reason, "health pid mismatch")
	}
}

func TestGatewayStartReady_RejectsUnknownDefaultModel(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "missing-model"
	err := saveTestConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	ready, reason, err := h.gatewayStartReady()
	if err == nil || !strings.Contains(err.Error(), `references unknown or disabled model_name "missing-model"`) {
		t.Fatalf("gatewayStartReady() = (%v, %q, %v), want config validation error", ready, reason, err)
	}
}

func TestGatewayStartReady_ValidDefaultModel(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = cfg.ModelList[0].ModelName
	cfg.ModelList[0].Enabled = true
	cfg.ModelList[0].SetAPIKey("test-key")
	err := saveTestConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if !ready {
		t.Fatalf("gatewayStartReady() ready = false, want true (reason=%q)", reason)
	}
}

func TestGatewayStartReady_DefaultModelWithoutCredential(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = cfg.ModelList[0].ModelName
	cfg.ModelList[0].Enabled = true
	cfg.ModelList[0].SetAPIKey("")
	cfg.ModelList[0].AuthMethod = ""
	err := saveTestConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if ready {
		t.Fatalf("gatewayStartReady() ready = true, want false")
	}
	if !strings.Contains(reason, "no credentials configured") {
		t.Fatalf("gatewayStartReady() reason = %q, want contains %q", reason, "no credentials configured")
	}
}

func TestGatewayCommandArgsIncludesDebugFlagWhenEnabled(t *testing.T) {
	h := NewHandler(filepath.Join(t.TempDir(), "config.json"))
	h.SetDebug(true)

	args := h.gatewayCommandArgs()
	want := []string{"gateway", "-E", "-d"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("gatewayCommandArgs() = %v, want %v", args, want)
	}
}

func TestGatewayStartReady_LocalModelWithoutAPIKey(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetModelProbeHooks(t)

	probeOpenAICompatibleModelFunc = func(apiBase, modelID, apiKey string) bool {
		return false
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "local-vllm", Provider: "vllm", Model: "custom-model",
		APIBase: "http://localhost:8000/v1", Enabled: true,
	}}
	cfg.Agents.Defaults.ModelName = "local-vllm"
	err = saveTestConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if ready {
		t.Fatalf("gatewayStartReady() ready = true, want false without a running local service")
	}
	if !strings.Contains(reason, "not reachable") {
		t.Fatalf("gatewayStartReady() reason = %q, want contains %q", reason, "not reachable")
	}
}

func TestGatewayStartReady_LocalModelWithRunningService(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetModelProbeHooks(t)

	probeOpenAICompatibleModelFunc = func(apiBase, modelID, apiKey string) bool {
		return apiBase == "http://127.0.0.1:8000/v1" && modelID == "custom-model" && apiKey == ""
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "local-vllm", Provider: "vllm", Model: "custom-model",
		APIBase: "http://127.0.0.1:8000/v1", Enabled: true,
	}}
	cfg.Agents.Defaults.ModelName = "local-vllm"
	err = saveTestConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if !ready {
		t.Fatalf("gatewayStartReady() ready = false, want true with a running local service (reason=%q)", reason)
	}
}

func TestGatewayStartReady_RemoteVLLMWithAPIKeyDoesNotProbe(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetModelProbeHooks(t)

	probeOpenAICompatibleModelFunc = func(apiBase, modelID, apiKey string) bool {
		t.Fatalf("unexpected OpenAI-compatible probe for %q (%q)", apiBase, modelID)
		return false
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "remote-vllm", Provider: "vllm", Model: "custom-model",
		APIBase: "https://models.example.com/v1", Enabled: true,
	}}
	cfg.ModelList[0o0].SetAPIKey("remote-key")
	cfg.Agents.Defaults.ModelName = "remote-vllm"
	err = saveTestConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if !ready {
		t.Fatalf("gatewayStartReady() ready = false, want true for remote vllm with api key (reason=%q)", reason)
	}
}

func TestGatewayStartReady_LocalOllamaUsesDefaultProbeBase(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetModelProbeHooks(t)

	probeOllamaModelFunc = func(apiBase, modelID string) bool {
		return apiBase == "http://localhost:11434/v1" && modelID == "llama3"
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "local-ollama", Provider: "ollama", Model: "llama3",
		Enabled: true,
	}}
	cfg.Agents.Defaults.ModelName = "local-ollama"
	err = saveTestConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if !ready {
		t.Fatalf("gatewayStartReady() ready = false, want true with default Ollama probe base (reason=%q)", reason)
	}
}

func TestGatewayStartReady_OAuthModelRequiresStoredCredential(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "openai-oauth", Provider: "openai", Model: "gpt-5.4",
		AuthMethod: "oauth", Enabled: true,
	}}
	cfg.Agents.Defaults.ModelName = "openai-oauth"
	err = saveTestConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if ready {
		t.Fatalf("gatewayStartReady() ready = true, want false without stored credential")
	}
	if !strings.Contains(reason, "no credentials configured") {
		t.Fatalf("gatewayStartReady() reason = %q, want contains %q", reason, "no credentials configured")
	}

	err = auth.SetCredential(oauthProviderOpenAI, &auth.AuthCredential{
		AccessToken: "openai-token",
		Provider:    oauthProviderOpenAI,
		AuthMethod:  "oauth",
	})
	if err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	ready, reason, err = h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if !ready {
		t.Fatalf("gatewayStartReady() ready = false, want true with stored credential (reason=%q)", reason)
	}
}

func TestGatewayStatusIncludesStartConditionWhenNotReady(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	allowed, ok := body["gateway_start_allowed"].(bool)
	if !ok {
		t.Fatalf("gateway_start_allowed missing or not bool: %#v", body["gateway_start_allowed"])
	}
	if allowed {
		t.Fatalf("gateway_start_allowed = true, want false")
	}
	if _, ok := body["gateway_start_reason"].(string); !ok {
		t.Fatalf("gateway_start_reason missing or not string: %#v", body["gateway_start_reason"])
	}
}

func TestGatewayStatusKeepsRunningWhenHealthProbeFailsAfterRunning(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	h.gateway.mu.Lock()
	h.gateway.cmd = cmd
	h.gateway.bootDefaultModel = "existing-model"
	// Simulate a process that has already reached the running state.
	h.gateway.setRuntimeStatusLocked("running")
	h.gateway.mu.Unlock()

	h.gateway.healthGet = func(string, time.Duration) (*http.Response, error) {
		return nil, errors.New("probe failed")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
}

func TestGatewayStatusKeepsPidDataWhileTrackedProcessAliveWhenPidFileUnavailable(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	h.gateway.mu.Lock()
	h.gateway.cmd = cmd
	h.gateway.pidData = &ppid.PidFileData{
		PID:   cmd.Process.Pid,
		Token: "existing-token",
	}
	h.gateway.setRuntimeStatusLocked("running")
	h.gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	h.gateway.mu.Lock()
	defer h.gateway.mu.Unlock()
	if h.gateway.pidData == nil {
		t.Fatal("gateway.pidData was cleared while runtime status remained running")
	}
}

func TestGatewayStatusDowngradesRunningWhenTrackedProcessExitedAndPidFileMissing(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()

	h.gateway.mu.Lock()
	h.gateway.cmd = cmd
	h.gateway.pidData = &ppid.PidFileData{
		PID:   cmd.Process.Pid,
		Token: "stale-token",
	}
	h.gateway.setRuntimeStatusLocked("running")
	h.gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := body["gateway_status"]; got != "stopped" {
		t.Fatalf("gateway_status = %#v, want %q", got, "stopped")
	}

	h.gateway.mu.Lock()
	defer h.gateway.mu.Unlock()
	if h.gateway.pidData != nil {
		t.Fatal("gateway.pidData should be cleared when tracked process has exited")
	}
}

func TestGatewayStatusIgnoresAndRemovesPidFileForNonGatewayProcess(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	pidPath := writeTestPidFile(t, ppid.PidFileData{
		PID:   cmd.Process.Pid,
		Token: "stale-token",
		Host:  "127.0.0.1",
		Port:  18790,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := body["gateway_status"]; got != "stopped" {
		t.Fatalf("gateway_status = %#v, want %q", got, "stopped")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatal("stale pid file should be removed for non-gateway process")
	}
}

func TestGatewayStopRefusesNonGatewayAttachedProcess(t *testing.T) {
	resetGatewayTestState(t)
	if runtime.GOOS == "windows" {
		t.Skip("commandline-based process type check is best-effort on Windows")
	}

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	h.gateway.mu.Lock()
	h.gateway.cmd = cmd
	h.gateway.owned = false
	h.gateway.setRuntimeStatusLocked("running")
	h.gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/gateway/stop", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !h.gateway.processAlive(cmd) {
		t.Fatal("non-gateway process should not be terminated by /api/gateway/stop")
	}
}

func TestGatewayStatusReportsRunningFromPidProbe(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	h.gateway.processMatcher = func(int) (bool, bool) { return true, true }
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startGatewayLikeProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	h.gateway.mu.Lock()
	h.gateway.setRuntimeStatusLocked("stopped")
	h.gateway.mu.Unlock()

	h.gateway.healthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, cmd.Process.Pid), nil
	}

	writeTestPidFile(t, ppid.PidFileData{
		PID:   cmd.Process.Pid,
		Token: "test-token",
		Host:  "127.0.0.1",
		Port:  18790,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["gateway_restart_required"]; got != false {
		t.Fatalf("gateway_restart_required = %#v, want false", got)
	}
}

func TestGatewayStatusRequiresRestartAfterDefaultModelChange(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = cfg.ModelList[0].ModelName
	cfg.ModelList[0].Enabled = true
	cfg.ModelList[0].SetAPIKey("test-key")
	cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
		ModelName: "second-model", Provider: "openai", Model: "gpt-4.1", Enabled: true,
	})
	cfg.ModelList[len(cfg.ModelList)-1].SetAPIKey("second-key")
	if err := saveTestConfig(configPath, cfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	h.gateway.processMatcher = func(int) (bool, bool) { return true, true }
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startGatewayLikeProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	writeTestPidFile(t, ppid.PidFileData{
		PID:   cmd.Process.Pid,
		Token: "test-token",
		Host:  "127.0.0.1",
		Port:  18790,
	})

	bootSignature := computeConfigSignature(cfg)
	h.gateway.mu.Lock()
	h.gateway.cmd = cmd
	h.gateway.bootDefaultModel = cfg.ModelList[0].ModelName
	h.gateway.bootConfigSignature = bootSignature
	h.gateway.setRuntimeStatusLocked("running")
	h.gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	updatedCfg.Agents.Defaults.ModelName = "second-model"
	if err := saveTestConfig(configPath, updatedCfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h.gateway.healthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, os.Getpid()), nil
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["boot_default_model"]; got != cfg.ModelList[0].ModelName {
		t.Fatalf("boot_default_model = %#v, want %q", got, cfg.ModelList[0].ModelName)
	}
	if got := body["config_default_model"]; got != "second-model" {
		t.Fatalf("config_default_model = %#v, want %q", got, "second-model")
	}
	if got := body["gateway_restart_required"]; got != true {
		t.Fatalf("gateway_restart_required = %#v, want true", got)
	}
}

func TestGatewayStatusRequiresRestartAfterToolChange(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = cfg.ModelList[0].ModelName
	cfg.ModelList[0].Enabled = true
	cfg.ModelList[0].SetAPIKey("test-key")
	cfg.Tools.WriteFile.Enabled = true
	if err := saveTestConfig(configPath, cfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess() error = %v", err)
	}

	bootSignature := computeConfigSignature(cfg)
	h.gateway.mu.Lock()
	h.gateway.cmd = &exec.Cmd{Process: process}
	h.gateway.bootDefaultModel = cfg.ModelList[0].ModelName
	h.gateway.bootConfigSignature = bootSignature
	h.gateway.setRuntimeStatusLocked("running")
	h.gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	updatedCfg.Tools.WriteFile.Enabled = false
	if err := saveTestConfig(configPath, updatedCfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h.gateway.healthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, os.Getpid()), nil
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["gateway_restart_required"]; got != true {
		t.Fatalf("gateway_restart_required = %#v, want true", got)
	}
}

func TestGatewayStatusRequiresRestartAfterChannelChange(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = cfg.ModelList[0].ModelName
	cfg.ModelList[0].Enabled = true
	cfg.ModelList[0].SetAPIKey("test-key")
	if err := saveTestConfig(configPath, cfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess() error = %v", err)
	}

	bootSignature := computeConfigSignature(cfg)
	h.gateway.mu.Lock()
	h.gateway.cmd = &exec.Cmd{Process: process}
	h.gateway.bootDefaultModel = cfg.ModelList[0].ModelName
	h.gateway.bootConfigSignature = bootSignature
	h.gateway.setRuntimeStatusLocked("running")
	h.gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	telegram := updatedCfg.Channels.Get("telegram")
	if telegram == nil {
		t.Fatalf("expected default telegram channel config")
	}
	telegram.Enabled = !telegram.Enabled
	if err := saveTestConfig(configPath, updatedCfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h.gateway.healthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, os.Getpid()), nil
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["gateway_restart_required"]; got != true {
		t.Fatalf("gateway_restart_required = %#v, want true", got)
	}
}

func TestGatewayStatusRequiresRestartAfterDefaultModelStreamingChange(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = cfg.ModelList[0].ModelName
	cfg.ModelList[0].Enabled = true
	cfg.ModelList[0].SetAPIKey("test-key")
	cfg.ModelList[0].Streaming = config.ModelStreamingConfig{Enabled: false}
	if err := saveTestConfig(configPath, cfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess() error = %v", err)
	}

	bootSignature := computeConfigSignature(cfg)
	h.gateway.mu.Lock()
	h.gateway.cmd = &exec.Cmd{Process: process}
	h.gateway.bootDefaultModel = cfg.ModelList[0].ModelName
	h.gateway.bootConfigSignature = bootSignature
	h.gateway.setRuntimeStatusLocked("running")
	h.gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	updatedCfg.ModelList[0].Streaming = config.ModelStreamingConfig{Enabled: true}
	if err := saveTestConfig(configPath, updatedCfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h.gateway.healthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, os.Getpid()), nil
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["gateway_restart_required"]; got != true {
		t.Fatalf("gateway_restart_required = %#v, want true", got)
	}
}

func TestConfigSignatureIncludesModelStreamingForExactDefaultModelName(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList[0].Enabled = true
	cfg.ModelList[0].ModelName = "friendly-alias"
	cfg.ModelList[0].Model = "openai/gpt-4o-ref"
	cfg.Agents.Defaults.ModelName = "friendly-alias"
	cfg.ModelList[0].Streaming = config.ModelStreamingConfig{Enabled: false}

	before := computeConfigSignature(cfg)

	cfg.ModelList[0].Streaming = config.ModelStreamingConfig{Enabled: true}
	after := computeConfigSignature(cfg)

	if before == after {
		t.Fatal("config signature should change when streaming changes for the exact default model_name")
	}
}

func TestModelConfigsMatchingNameRequiresExactModelName(t *testing.T) {
	models := []*config.ModelConfig{{
		ModelName: "friendly-alias",
		Provider:  "openai",
		Model:     "gpt-4o",
		Enabled:   true,
	}}

	if matches := modelConfigsMatchingName(models, "friendly-alias"); len(matches) != 1 || matches[0].index != 0 {
		t.Fatalf("exact model_name matches = %#v, want index 0", matches)
	}
	for _, ref := range []string{"gpt-4o", "openai/gpt-4o"} {
		if matches := modelConfigsMatchingName(models, ref); len(matches) != 0 {
			t.Fatalf("modelConfigsMatchingName(%q) = %#v, want no match", ref, matches)
		}
	}
}

func TestConfigSignatureIncludesModelStreamingForLoadBalancedAliasEntries(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "lb-alias", Provider: "openai", Model: "gpt-4o-primary",
			Enabled:   true,
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
		{
			ModelName: "lb-alias", Provider: "openai", Model: "gpt-4o-secondary",
			Enabled:   true,
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
	}
	cfg.Agents.Defaults.ModelName = "lb-alias"

	before := computeConfigSignature(cfg)

	cfg.ModelList[1].Streaming = config.ModelStreamingConfig{Enabled: true}
	after := computeConfigSignature(cfg)

	if before == after {
		t.Fatal("config signature should change when streaming changes for a load-balanced alias entry")
	}
}

func TestConfigSignatureIncludesLoadBalancedDuplicateEntryIndex(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "lb-alias",
			Provider:  "openai",
			Model:     "gpt-4o",
			Enabled:   true,
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
		{
			ModelName: "lb-alias",
			Provider:  "openai",
			Model:     "gpt-4o",
			Enabled:   true,
			Streaming: config.ModelStreamingConfig{Enabled: true},
		},
	}
	cfg.Agents.Defaults.ModelName = "lb-alias"

	before := computeConfigSignature(cfg)

	cfg.ModelList[0].Streaming.Enabled = true
	cfg.ModelList[1].Streaming.Enabled = false
	after := computeConfigSignature(cfg)

	if before == after {
		t.Fatal("config signature should change when duplicate load-balanced entries swap streaming state")
	}
}

func TestGatewayStatusRequiresRestartAfterWebSearchConfigChange(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = cfg.ModelList[0].ModelName
	cfg.ModelList[0].Enabled = true
	cfg.ModelList[0].SetAPIKey("test-key")
	cfg.Tools.Web.Enabled = true
	cfg.Tools.Web.Provider = "sogou"
	if err := saveTestConfig(configPath, cfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess() error = %v", err)
	}

	bootSignature := computeConfigSignature(cfg)
	h.gateway.mu.Lock()
	h.gateway.cmd = &exec.Cmd{Process: process}
	h.gateway.bootDefaultModel = cfg.ModelList[0].ModelName
	h.gateway.bootConfigSignature = bootSignature
	h.gateway.setRuntimeStatusLocked("running")
	h.gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	updatedCfg.Tools.Web.Provider = "duckduckgo"
	if err := saveTestConfig(configPath, updatedCfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h.gateway.healthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, os.Getpid()), nil
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["gateway_restart_required"]; got != true {
		t.Fatalf("gateway_restart_required = %#v, want true", got)
	}
}

func TestGatewayStatusNoRestartRequiredForNonSensitiveChanges(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = cfg.ModelList[0].ModelName
	cfg.ModelList[0].Enabled = true
	cfg.ModelList[0].SetAPIKey("test-key")
	cfg.Agents.Defaults.MaxTokens = 1000
	if err := saveTestConfig(configPath, cfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess() error = %v", err)
	}

	bootSignature := computeConfigSignature(cfg)
	h.gateway.mu.Lock()
	h.gateway.cmd = &exec.Cmd{Process: process}
	h.gateway.bootDefaultModel = cfg.ModelList[0].ModelName
	h.gateway.bootConfigSignature = bootSignature
	h.gateway.setRuntimeStatusLocked("running")
	h.gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	updatedCfg.Agents.Defaults.MaxTokens = 2000
	if err := saveTestConfig(configPath, updatedCfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h.gateway.healthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, os.Getpid()), nil
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["gateway_restart_required"]; got != false {
		t.Fatalf("gateway_restart_required = %#v, want false", got)
	}
}

func TestGatewayStatusNoRestartRequiredWhenNotRunning(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = cfg.ModelList[0].ModelName
	cfg.ModelList[0].Enabled = true
	cfg.ModelList[0].SetAPIKey("test-key")
	cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
		ModelName: "different-model", Provider: "openai", Model: "gpt-4.1", Enabled: true,
	})
	if err := saveTestConfig(configPath, cfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	h.gateway.mu.Lock()
	h.gateway.cmd = nil
	h.gateway.bootDefaultModel = ""
	h.gateway.bootConfigSignature = ""
	h.gateway.setRuntimeStatusLocked("stopped")
	h.gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	updatedCfg.Agents.Defaults.ModelName = "different-model"
	if err := saveTestConfig(configPath, updatedCfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h.gateway.healthGet = func(string, time.Duration) (*http.Response, error) {
		return nil, errors.New("no gateway running")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "stopped" {
		t.Fatalf("gateway_status = %#v, want %q", got, "stopped")
	}
	if got := body["gateway_restart_required"]; got != false {
		t.Fatalf("gateway_restart_required = %#v, want false", got)
	}
}

func TestGatewayStatusReturnsErrorAfterStartupWindowExpires(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	h.gateway.mu.Lock()
	h.gateway.cmd = cmd
	h.gateway.bootDefaultModel = "existing-model"
	h.gateway.setRuntimeStatusLocked("starting")
	h.gateway.startupDeadline = time.Now().Add(-time.Second)
	h.gateway.mu.Unlock()

	h.gateway.healthGet = func(string, time.Duration) (*http.Response, error) {
		return nil, errors.New("probe failed")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "error" {
		t.Fatalf("gateway_status = %#v, want %q", got, "error")
	}
}

func TestGatewayStatusReturnsRestartingDuringRestartGap(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	// Mock health check to return error, so it won't override our "restarting" status
	h.gateway.healthGet = func(url string, timeout time.Duration) (*http.Response, error) {
		return nil, errors.New("mock health check error")
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	h.gateway.mu.Lock()
	h.gateway.setRuntimeStatusLocked("restarting")
	h.gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "restarting" {
		t.Fatalf("gateway_status = %#v, want %q", got, "restarting")
	}
}

func TestGatewayRestartKeepsRunningProcessWhenPreconditionsFail(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = cfg.ModelList[0].ModelName
	cfg.ModelList[0].Enabled = true
	cfg.ModelList[0].SetAPIKey("")
	cfg.ModelList[0].AuthMethod = ""
	if err := saveTestConfig(configPath, cfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		h.gateway.mu.Lock()
		if h.gateway.cmd == cmd {
			h.gateway.cmd = nil
			h.gateway.bootDefaultModel = ""
		}
		h.gateway.mu.Unlock()

		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	h.gateway.mu.Lock()
	h.gateway.cmd = cmd
	h.gateway.bootDefaultModel = "existing-model"
	h.gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/gateway/restart", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	h.gateway.mu.Lock()
	stillRunning := h.gateway.cmd == cmd && h.gateway.processAlive(cmd)
	h.gateway.mu.Unlock()

	if !stillRunning {
		t.Fatalf("gateway process was stopped when restart preconditions failed")
	}
}

func TestGatewayRestartKeepsOldProcessWhenItDoesNotExitInTime(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = cfg.ModelList[0].ModelName
	cfg.ModelList[0].Enabled = true
	cfg.ModelList[0].SetAPIKey("test-key")
	if err := saveTestConfig(configPath, cfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startIgnoringTermProcess(t)
	t.Cleanup(func() {
		h.gateway.mu.Lock()
		if h.gateway.cmd == cmd {
			h.gateway.cmd = nil
			h.gateway.bootDefaultModel = ""
		}
		h.gateway.mu.Unlock()

		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	h.gateway.restartGracePeriod = 150 * time.Millisecond
	h.gateway.restartKillWindow = 150 * time.Millisecond
	h.gateway.restartPollInterval = 10 * time.Millisecond

	h.gateway.mu.Lock()
	h.gateway.cmd = cmd
	h.gateway.bootDefaultModel = "existing-model"
	h.gateway.setRuntimeStatusLocked("running")
	h.gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/gateway/restart", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	h.gateway.mu.Lock()
	stillRunning := h.gateway.cmd == cmd && h.gateway.processAlive(cmd)
	status := h.gateway.runtimeStatus
	h.gateway.mu.Unlock()

	if !stillRunning {
		t.Fatalf("gateway process was replaced before the old process exited")
	}
	if status != "running" {
		t.Fatalf("runtimeStatus = %q, want %q", status, "running")
	}
}

func TestGatewayRestartReturnsErrorStatusWhenReplacementFailsToStart(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = cfg.ModelList[0].ModelName
	cfg.ModelList[0].Enabled = true
	cfg.ModelList[0].SetAPIKey("test-key")
	if err := saveTestConfig(configPath, cfg); err != nil {
		t.Fatalf("saveTestConfig() error = %v", err)
	}

	invalidBinaryPath := filepath.Join(t.TempDir(), "fake-mintclaw")
	if err := os.WriteFile(invalidBinaryPath, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("MINTCLAW_BINARY", invalidBinaryPath)

	h := NewHandler(configPath)
	// Mock health check to return error, so it won't override our "error" status
	h.gateway.healthGet = func(url string, timeout time.Duration) (*http.Response, error) {
		return nil, errors.New("mock health check error")
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/gateway/restart", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("restart status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	statusRec := httptest.NewRecorder()
	statusReq := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(statusRec, statusReq)

	if statusRec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", statusRec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(statusRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "error" {
		t.Fatalf("gateway_status = %#v, want %q", got, "error")
	}
}

func TestGatewayStatusExcludesLogsFields(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if _, ok := body["logs"]; ok {
		t.Fatalf("logs unexpectedly present in status response: %#v", body["logs"])
	}
	if _, ok := body["log_total"]; ok {
		t.Fatalf("log_total unexpectedly present in status response: %#v", body["log_total"])
	}
	if _, ok := body["log_run_id"]; ok {
		t.Fatalf("log_run_id unexpectedly present in status response: %#v", body["log_run_id"])
	}
}

func TestGatewayLogsReturnsIncrementalHistory(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	h.gateway.logs.Clear()
	h.gateway.logs.Append("first line")
	h.gateway.logs.Append("second line")
	runID := h.gateway.logs.RunID()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/gateway/logs?log_offset=1&log_run_id="+strconv.Itoa(runID),
		nil,
	)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("logs status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal logs response: %v", err)
	}

	logs, ok := body["logs"].([]any)
	if !ok {
		t.Fatalf("logs missing or not array: %#v", body["logs"])
	}
	if len(logs) != 1 || logs[0] != "second line" {
		t.Fatalf("logs = %#v, want [\"second line\"]", logs)
	}
	if got := body["log_total"]; got != float64(2) {
		t.Fatalf("log_total = %#v, want 2", got)
	}
	if got := body["log_run_id"]; got != float64(runID) {
		t.Fatalf("log_run_id = %#v, want %d", got, runID)
	}
}

func TestGatewayClearLogsResetsBufferedHistory(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	h.gateway.logs.Clear()
	h.gateway.logs.Append("first line")
	h.gateway.logs.Append("second line")
	previousRunID := h.gateway.logs.RunID()

	clearRec := httptest.NewRecorder()
	clearReq := httptest.NewRequest(http.MethodPost, "/api/gateway/logs/clear", nil)
	mux.ServeHTTP(clearRec, clearReq)

	if clearRec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want %d", clearRec.Code, http.StatusOK)
	}

	var clearBody map[string]any
	if err := json.Unmarshal(clearRec.Body.Bytes(), &clearBody); err != nil {
		t.Fatalf("unmarshal clear response: %v", err)
	}

	if got := clearBody["status"]; got != "cleared" {
		t.Fatalf("clear status body = %#v, want %q", got, "cleared")
	}

	clearRunID, ok := clearBody["log_run_id"].(float64)
	if !ok {
		t.Fatalf("log_run_id missing or not number: %#v", clearBody["log_run_id"])
	}
	if int(clearRunID) <= previousRunID {
		t.Fatalf("log_run_id = %d, want > %d", int(clearRunID), previousRunID)
	}

	logsRec := httptest.NewRecorder()
	logsReq := httptest.NewRequest(
		http.MethodGet,
		"/api/gateway/logs?log_offset=0&log_run_id="+strconv.Itoa(previousRunID),
		nil,
	)
	mux.ServeHTTP(logsRec, logsReq)

	if logsRec.Code != http.StatusOK {
		t.Fatalf("logs code = %d, want %d", logsRec.Code, http.StatusOK)
	}

	var logsBody map[string]any
	if err := json.Unmarshal(logsRec.Body.Bytes(), &logsBody); err != nil {
		t.Fatalf("unmarshal logs response: %v", err)
	}

	logs, ok := logsBody["logs"].([]any)
	if !ok {
		t.Fatalf("logs missing or not array: %#v", logsBody["logs"])
	}
	if len(logs) != 0 {
		t.Fatalf("logs len = %d, want 0", len(logs))
	}
	if got := logsBody["log_total"]; got != float64(0) {
		t.Fatalf("log_total = %#v, want 0", got)
	}
	if got := logsBody["log_run_id"]; got != clearBody["log_run_id"] {
		t.Fatalf("log_run_id = %#v, want %#v", got, clearBody["log_run_id"])
	}
}

func TestFindMintClawBinary_EnvOverride(t *testing.T) {
	// Create a temporary file to act as the mock binary
	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "mintclaw-mock")
	if err := os.WriteFile(mockBinary, []byte("mock"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("MINTCLAW_BINARY", mockBinary)

	got := utils.FindMintClawBinary()
	if got != mockBinary {
		t.Errorf("FindMintClawBinary() = %q, want %q", got, mockBinary)
	}
}

func TestFindMintClawBinary_EnvOverride_InvalidPath(t *testing.T) {
	// When MINTCLAW_BINARY points to a non-existent path, fall through to next strategy
	t.Setenv("MINTCLAW_BINARY", "/nonexistent/mintclaw-binary")

	got := utils.FindMintClawBinary()
	// Should not return the invalid path; falls back to "mintclaw" or another found path
	if got == "/nonexistent/mintclaw-binary" {
		t.Errorf("FindMintClawBinary() returned invalid env path %q, expected fallback", got)
	}
}
