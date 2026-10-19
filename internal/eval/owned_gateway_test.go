package eval

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/health"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/pid"
)

type ownedGatewayControl struct {
	Mode              string `json:"mode"`
	ParentPID         int    `json:"parent_pid"`
	LateValue         string `json:"late_value,omitempty"`
	CrossTrialValue   string `json:"cross_trial_value,omitempty"`
	UncorrelatedMedia bool   `json:"uncorrelated_media,omitempty"`
	LateSourceChange  bool   `json:"late_source_change,omitempty"`
}

// The test binary accepts the same literal gateway command as the product.
// This stub exercises real child ownership/HTTP/WebSocket/exit, not AgentLoop.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "gateway" {
		os.Exit(runOwnedGatewayTestChild())
	}
	os.Exit(m.Run())
}

func runOwnedGatewayTestChild() int {
	home := os.Getenv(config.EnvHome)
	data, err := os.ReadFile(filepath.Join(home, ".security", "eval-child.json"))
	var control ownedGatewayControl
	if err != nil || json.Unmarshal(data, &control) != nil || os.Getenv("HOME") != home ||
		os.Getenv("MINTCLAW_EVAL_PARENT_SECRET") != "" || os.Getenv("MINTCLAW_GATEWAY_HOST") != "" ||
		os.Getenv("MINTCLAW_LOG_FORMAT") != "json" {
		return 8
	}
	if control.Mode == "early_exit" || control.Mode == "metadata_early_exit" {
		return 9
	}
	switch control.Mode {
	case "structured_safe":
		logger.WarnCF("eval-fixture", "Synthetic log", map[string]any{"value": "public\nquoted\"value"})
	case "structured_encoded":
		_, _ = os.Stdout.WriteString("{\"value\":\"\\u0050RIVATE_LOG_CANARY\"}\n")
	case "structured_malformed":
		_, _ = os.Stderr.WriteString("{\"incomplete\":\n")
	case "structured_late_leak":
		_, _ = os.Stderr.WriteString("not-json\\opaque\n")
		_, _ = os.Stdout.WriteString("{\"value\":\"\\u0050RIVATE_LOG_CANARY\",\"value\":\"public\"}\n")
	}
	cfg, err := config.LoadConfigReadOnly(filepath.Join(home, "config.json"))
	if err != nil {
		return 8
	}
	if control.Mode == "metadata_startup_leak" {
		if writeOwnedTestMetadata(cfg, control.Mode) != nil {
			return 8
		}
		return 9
	}
	temporary := filepath.Join(home, ownedGatewayTempDirectory)
	if os.TempDir() != temporary || os.Getenv("TMPDIR") != temporary || os.Getenv("TMP") != temporary ||
		os.Getenv("TEMP") != temporary {
		return 8
	}
	file, err := os.CreateTemp("", "owned-temp-proof-")
	if err != nil {
		return 8
	}
	_, writeErr := file.WriteString("owned-gateway-private-temp-control")
	if file.Close() != nil || writeErr != nil {
		return 8
	}
	settings, err := gatewayclient.ChannelSettings(cfg)
	if err != nil {
		return 8
	}
	if cfg.Tools.IsToolEnabled("document") {
		if _, err = outbox.Open(cfg.WorkspacePath()); err != nil {
			return 8
		}
	}
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	listener, err := net.Listen("tcp", net.JoinHostPort(cfg.Gateway.Host, strconv.Itoa(cfg.Gateway.Port)))
	if err != nil {
		return 8
	}
	defer func() { _ = listener.Close() }()
	process, err := pid.WritePidFile(home, cfg.Gateway.Host, cfg.Gateway.Port)
	if err != nil {
		return 8
	}
	defer pid.RemovePidFile(home)
	if control.Mode == "foreign" {
		process.PID = control.ParentPID
		data, _ = json.Marshal(process)
		if os.WriteFile(filepath.Join(home, ".mintclaw.pid"), data, 0o600) != nil {
			return 8
		}
	}
	fingerprint, err := preparedRuntimeFingerprint(cfg)
	if err != nil {
		return 8
	}
	probe := health.RuntimeProbe{
		Version: 1, PID: process.PID, InstanceSHA256: admissionHash(process.Token),
		ConfigSHA256: fingerprint, HomeSHA256: admissionHash(home),
		ConfigPathSHA256: admissionHash(filepath.Join(home, "config.json")), LogLevel: "warn",
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
	}
	if slices.Contains([]string{"native_models", "native_models_retry", "native_models_sources"}, control.Mode) {
		probe.GitCommit, probe.BuildVersion = nativeModelTestCommit, "native-test"
	}
	var first atomic.Bool
	var mediaSent atomic.Bool
	var sessionMu sync.Mutex
	firstSession := ""
	browserURLs := make(map[string]string)
	browserLastURL := ""
	browserMessages := 0
	browserChoices := []string{}
	upgrader := websocket.Upgrader{}
	server := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/runtime" && r.Header.Get("Authorization") == "Bearer "+process.Token {
				if control.Mode == "no_probe" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				_ = json.NewEncoder(w).Encode(probe)
				return
			}
			if r.URL.Path != "/mintclaw/ws" || r.Header.Get("Authorization") != "Bearer "+settings.Token.String() {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			connection, upgradeErr := upgrader.Upgrade(w, r, nil)
			if upgradeErr != nil {
				return
			}
			defer func() { _ = connection.Close() }()
			for {
				var request protocol.MintClawMessage
				if connection.ReadJSON(&request) != nil {
					return
				}
				if request.Type == protocol.TypePing {
					_ = connection.WriteJSON(protocol.MintClawMessage{Type: protocol.TypePong, ID: request.ID})
					continue
				}
				if control.Mode == "report_error" && first.CompareAndSwap(false, true) {
					entries, _ := os.ReadDir(filepath.Join(home, "reports"))
					if len(entries) == 1 {
						_ = os.Mkdir(filepath.Join(home, "reports", entries[0].Name(), "summary.md"), 0o700)
					}
				}
				sessionMu.Lock()
				if firstSession == "" {
					firstSession = request.SessionID
				}
				crossTrial := firstSession != request.SessionID
				sessionMu.Unlock()
				if crossTrial && control.CrossTrialValue != "" {
					if connection.WriteJSON(protocol.MintClawMessage{
						Type:    protocol.TypePong,
						Payload: map[string]any{"auxiliary": control.CrossTrialValue},
					}) != nil {
						return
					}
				}
				if control.UncorrelatedMedia && mediaSent.CompareAndSwap(false, true) {
					if connection.WriteJSON(protocol.MintClawMessage{
						Type: protocol.TypeMessageCreate, SessionID: request.SessionID,
						Payload: map[string]any{
							protocol.PayloadKeyMessageID: "synthetic-media-message",
							protocol.PayloadKeyAgentID:   "main", protocol.PayloadKeySessionKey: "eval-session-key",
							protocol.PayloadKeyDeliveryID: "unsettled-delivery",
							"attachments": []any{
								map[string]any{
									"type":         "file",
									"content_type": "application/pdf",
									"url":          "/mintclaw/media/unbound-control",
								},
							},
						},
					}) != nil {
						return
					}
				}
				response := testResponse(request, 1)
				response.Payload[protocol.PayloadKeyTraceScopes] = []events.TraceScope{{
					Workspace: cfg.WorkspacePath(), TurnID: request.ID,
				}}
				if slices.Contains(
					[]string{"native_models", "native_models_retry", "native_models_sources"},
					control.Mode,
				) {
					binding := nativeModelTestBinding(cfg.WorkspacePath())
					binding.TraceScope.TurnID = request.ID
					binding.SessionKey = payloadString(response.Payload, protocol.PayloadKeySessionKey)
					trace, traceErr := makeNativeModelTestTrace(binding)
					if control.Mode == "native_models_retry" {
						trace, traceErr = makeNativeModelRetryTestTrace(binding)
					}
					if control.Mode == "native_models_sources" {
						trace, traceErr = makeNativeSystemSourceTestTrace(binding)
					}
					trace.TraceID = "trace-turn-" + request.ID
					store := diagnostictrace.Store{Root: diagnostictrace.ResolveStoreRoot("", cfg.WorkspacePath())}
					if traceErr != nil {
						return
					}
					if _, traceErr = store.Save(trace); traceErr != nil {
						return
					}
				}
				if strings.HasPrefix(control.Mode, "browser_") {
					sessionMu.Lock()
					previous := browserURLs[request.SessionID]
					sessionMu.Unlock()
					current, ok := ownedBrowserChildResponse(control.Mode, cfg, previous, request, &response)
					if !ok {
						return
					}
					sessionMu.Lock()
					browserURLs[request.SessionID], browserLastURL = current, current
					browserMessages++
					if choice := payloadString(request.Payload, protocol.PayloadKeyInteractionChoice); choice != "" {
						browserChoices = append(browserChoices, choice)
					}
					sessionMu.Unlock()
				}
				if connection.WriteJSON(response) != nil {
					return
				}
			}
		}),
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	if control.Mode == "log_overflow" {
		_, _ = os.Stdout.WriteString(strings.Repeat("bounded-log-control", ownedGatewayLogBytes/8))
	}
	if control.Mode == "stubborn" {
		for range interrupts {
		}
	}
	select {
	case <-interrupts:
	case <-served:
		return 8
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if server.Shutdown(ctx) != nil {
		return 8
	}
	if strings.HasPrefix(control.Mode, "browser_") {
		sessionMu.Lock()
		err := ownedBrowserChildStop(home, cfg, browserLastURL, browserMessages, browserChoices, browserURLs)
		sessionMu.Unlock()
		if err != nil {
			return 8
		}
	}
	if control.LateSourceChange {
		entries, readErr := os.ReadDir(filepath.Join(home, "reports"))
		if readErr != nil || len(entries) != 1 {
			return 8
		}
		files, readErr := os.ReadDir(filepath.Join(home, "reports", entries[0].Name()))
		pending := false
		for _, file := range files {
			if strings.HasPrefix(file.Name(), "trial-") {
				return 8
			}
			pending = pending || strings.HasPrefix(file.Name(), "pending-trial-")
		}
		if readErr != nil || !pending {
			return 8
		}
		sessionMu.Lock()
		source := filepath.Join(cfg.WorkspacePath(), ".eval-trials", firstSession, "source.pdf")
		sessionMu.Unlock()
		if os.WriteFile(source, []byte("%PDF-1.7\nlate-owned-source-change\n"), 0o600) != nil {
			return 8
		}
	}
	if control.LateValue != "" {
		data, err := json.Marshal(map[string]string{"value": control.LateValue})
		if err != nil {
			return 8
		}
		if os.WriteFile(filepath.Join(os.TempDir(), "late-runtime.json"),
			data, 0o600) != nil {
			return 8
		}
	}
	if strings.HasPrefix(control.Mode, "metadata_") {
		if writeOwnedTestMetadata(cfg, control.Mode) != nil {
			return 8
		}
	}
	if control.Mode == "bad_exit" {
		return 9
	}
	return 0
}

func writeOwnedTestMetadata(cfg *config.Config, mode string) error {
	traceRoot := filepath.Join(cfg.WorkspacePath(), "state", "diagnostics", "traces")
	data := []byte(`{"metadata":"public"}`)
	if mode == "metadata_leak" || mode == "metadata_startup_leak" {
		data = []byte(`{"metadata":"\u0073ynthetic-preparation-api-key-not-a-live-credential"}`)
	}
	if !cfg.Diagnostics.TraceCapture.Enabled || os.MkdirAll(traceRoot, 0o700) != nil {
		return ErrOwnedGateway
	}
	return os.WriteFile(filepath.Join(traceRoot, "post-stop.json"), data, 0o600)
}

func newOwnedGatewayFixture(t *testing.T, mode string) *admissionFixture {
	t.Helper()
	fixture := newAdmissionFixture(t)
	if err := os.Remove(filepath.Join(fixture.home, ".mintclaw.pid")); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture.cfg.Gateway.Port = listener.Addr().(*net.TCPAddr).Port
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.writeConfig(t)
	if err = os.Mkdir(filepath.Join(fixture.home, ".security"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeAdmissionJSON(t, filepath.Join(fixture.home, ".security", "eval-child.json"), ownedGatewayControl{
		Mode: mode, ParentPID: os.Getpid(),
	})
	t.Setenv("MINTCLAW_EVAL_PARENT_SECRET", "not-for-the-child")
	return fixture
}

func TestOwnedGatewayRunStopsBeforePublishingCompleteReport(t *testing.T) {
	fixture := newOwnedGatewayFixture(t, "normal")
	result, err := Run(t.Context(), fixture.preset, []LoadedCase{
		loadedTestCase(t, "general.owned.first"),
		loadedTestCase(t, "general.owned.second"),
	}, RunOptions{
		Home: fixture.home, OutputParent: fixture.output, StartTarget: true, SuiteTimeout: 15 * time.Second, Repeat: 2,
	})
	if err != nil || !result.Complete || result.Verdict != Pass || len(result.Trials) != 4 ||
		result.TargetLifecycle == nil || !result.TargetLifecycle.Admitted || result.TargetLifecycle.Forced ||
		result.TargetLifecycle.Code != "native_exit_zero" || pid.ProcessRunning(result.TargetLifecycle.PID) {
		t.Fatal("owned gateway was not admitted, reaped and reported", err)
	}
	if result.Cleanup != "owned_gateway_stopped; domain_resources_unknown" {
		t.Fatal("child exit was promoted into domain cleanup proof")
	}
	assertPrivateReport(t, result)
	for _, summary := range result.Trials {
		data, readErr := os.ReadFile(filepath.Join(result.ReportDir, summary.File))
		var trial Trial
		if readErr != nil || json.Unmarshal(data, &trial) != nil || trial.RemoteState != "unknown" {
			t.Fatal("child exit rewrote remote task state")
		}
	}
}

func TestOwnedGatewayAcceptsFreshProfileSavedByNativeRepository(t *testing.T) {
	fixture := newOwnedGatewayFixture(t, "normal")
	if _, err := config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg); err != nil {
		t.Fatal("native profile preparation failed", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.home, config.SecurityConfigFile+".lock")); err != nil {
		t.Fatal("native configuration lock metadata missing", err)
	}
	result, err := Run(t.Context(), fixture.preset, []LoadedCase{loadedTestCase(t, "general.owned.native-profile")},
		RunOptions{Home: fixture.home, OutputParent: fixture.output, StartTarget: true, SuiteTimeout: 15 * time.Second})
	if err != nil || !result.Complete || result.Verdict != Pass || result.TargetLifecycle == nil ||
		result.TargetLifecycle.Code != "native_exit_zero" {
		t.Fatal("fresh native-prepared profile was refused", err)
	}
}

func TestOwnedGatewayRefusesInvalidNativeConfigurationLockMetadata(t *testing.T) {
	for _, mode := range []string{"contents", "permissions", "directory", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newOwnedGatewayFixture(t, "normal")
			lock := filepath.Join(fixture.home, config.SecurityConfigFile+".lock")
			var err error
			switch mode {
			case "directory":
				err = os.Mkdir(lock, 0o700)
			case "symlink":
				err = os.Symlink(fixture.preset.ConfigPath, lock)
			case "contents":
				err = os.WriteFile(lock, []byte("not-native-lock-metadata"), 0o600)
			case "permissions":
				err = os.WriteFile(lock, nil, 0o600)
				if err == nil {
					err = os.Chmod(lock, 0o666)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			child, _, err := startOwnedGateway(t.Context(), fixture.preset, fixture.home)
			registerOwnedGatewayCleanup(t, child)
			if !errors.Is(err, ErrOwnedGateway) || child != nil {
				t.Fatal("invalid native configuration lock metadata was admitted")
			}
		})
	}
}

func TestOwnedGatewayRunPreservesNonzeroExitAndReportFailure(t *testing.T) {
	for _, mode := range []string{"bad_exit", "report_error"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newOwnedGatewayFixture(t, mode)
			result, err := Run(
				t.Context(),
				fixture.preset,
				[]LoadedCase{loadedTestCase(t, "general.owned.error")},
				RunOptions{
					Home:         fixture.home,
					OutputParent: fixture.output,
					StartTarget:  true,
					SuiteTimeout: 15 * time.Second,
				},
			)
			if mode == "bad_exit" {
				if !errors.Is(err, ErrNotPassed) || !result.Complete || result.Verdict != Inconclusive ||
					result.TargetLifecycle == nil || result.TargetLifecycle.Code != "abnormal_exit" {
					t.Fatal("nonzero target exit became a pass", err)
				}
			} else if !errors.Is(err, ErrReport) || result.Complete {
				t.Fatal("report error was hidden", err)
			}
			data, readErr := os.ReadFile(filepath.Join(fixture.home, ".mintclaw.pid"))
			if readErr == nil {
				var process pid.PidFileData
				if json.Unmarshal(data, &process) != nil || pid.ProcessRunning(process.PID) {
					t.Fatal("failed run left its gateway running")
				}
			}
		})
	}
}

func TestOwnedGatewayRefusesExistingStateWithoutStartingChild(t *testing.T) {
	for _, entry := range []string{".mintclaw.pid", "state", "workspace/sessions", "workspace/state", "workspace/memory"} {
		t.Run(entry, func(t *testing.T) {
			fixture := newOwnedGatewayFixture(t, "normal")
			path := filepath.Join(fixture.home, entry)
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			child, _, err := startOwnedGateway(t.Context(), fixture.preset, fixture.home)
			if !errors.Is(err, ErrOwnedGateway) || child != nil {
				t.Fatal("retained native state was launched or adopted")
			}
			if _, err := os.Stat(filepath.Join(fixture.home, ".eval-gateway-owner")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("refused target acquired process ownership")
			}
		})
	}
}

func TestOwnedGatewayRejectsForeignPIDAndReapsStartupFailure(t *testing.T) {
	for _, mode := range []string{"foreign", "early_exit"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newOwnedGatewayFixture(t, mode)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			child, _, err := startOwnedGateway(ctx, fixture.preset, fixture.home)
			registerOwnedGatewayCleanup(t, child)
			if !errors.Is(err, ErrOwnedGateway) || child == nil || child.admitted {
				t.Fatal("foreign/failed child was admitted", err)
			}
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer stopCancel()
			receipt := child.stopAndWait(stopCtx)
			if !receipt.ExitObserved || pid.ProcessRunning(receipt.PID) || !pid.ProcessRunning(os.Getpid()) {
				t.Fatal("startup cleanup did not reap exactly its own child")
			}
		})
	}
}

func TestOwnedGatewayForcedExitIsNotNativeTeardown(t *testing.T) {
	fixture := newOwnedGatewayFixture(t, "stubborn")
	child, _, err := startOwnedGateway(t.Context(), fixture.preset, fixture.home)
	registerOwnedGatewayCleanup(t, child)
	if err != nil || child == nil {
		t.Fatal("stub child could not start", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	receipt := child.stopAndWait(ctx)
	if !receipt.Forced || receipt.Code != "forced_exit" || !receipt.ExitObserved || pid.ProcessRunning(receipt.PID) {
		t.Fatal("forced exit was not reaped or became native teardown")
	}
	if _, err := os.Stat(filepath.Join(fixture.home, ".eval-gateway-owner")); err != nil {
		t.Fatal("uncertain cleanup erased the ownership marker")
	}
}

func registerOwnedGatewayCleanup(t *testing.T, child *ownedGateway) {
	t.Helper()
	if child != nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			child.stopAndWait(ctx)
		})
	}
}

func TestOwnedGatewayInterruptedAdmissionIsReapedWithoutReplay(t *testing.T) {
	fixture := newOwnedGatewayFixture(t, "no_probe")
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	child, _, err := startOwnedGateway(ctx, fixture.preset, fixture.home)
	registerOwnedGatewayCleanup(t, child)
	if !errors.Is(err, ErrOwnedGateway) || child == nil || child.admitted {
		t.Fatal("interrupted startup was admitted or not retained for cleanup", err)
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cleanupCancel()
	receipt := child.stopAndWait(cleanupCtx)
	if !receipt.ExitObserved || pid.ProcessRunning(receipt.PID) {
		t.Fatal("interrupted admission left a child running")
	}
}

func TestOwnedGatewayRunComparesRetainedCleanupUncertainty(t *testing.T) {
	fixture := newOwnedGatewayFixture(t, "bad_exit")
	result, err := Run(t.Context(), fixture.preset, []LoadedCase{loadedTestCase(t, "general.owned.compare")},
		RunOptions{Home: fixture.home, OutputParent: fixture.output, StartTarget: true, SuiteTimeout: 15 * time.Second})
	if !errors.Is(err, ErrNotPassed) || result.Verdict != Inconclusive {
		t.Fatal("uncertain cleanup run unavailable", err)
	}
	comparison, err := Compare(result.ReportDir, result.ReportDir)
	if err != nil || !comparison.CompatibleCases || len(comparison.Cases) != 1 ||
		comparison.Cases[0].Current.Coverage[Pass] != 1 ||
		!slices.Contains(comparison.EnvironmentUnknowns, "baseline_target_cleanup") ||
		!slices.Contains(comparison.EnvironmentUnknowns, "current_target_cleanup") {
		t.Fatal("retained trial verdict and run cleanup uncertainty could not be compared", err)
	}
	data, err := os.ReadFile(filepath.Join(result.ReportDir, "run.json"))
	var report RunReport
	if err != nil || json.Unmarshal(data, &report) != nil {
		t.Fatal(err)
	}
	report.TargetLifecycle.ExitObserved = false
	writeAdmissionJSON(t, filepath.Join(result.ReportDir, "run.json"), report)
	if _, err = Compare(result.ReportDir, result.ReportDir); !errors.Is(err, ErrCompare) {
		t.Fatal("malformed lifecycle receipt was admitted")
	}
}

func TestOwnedGatewayLogIsBoundedPrivateAndTruncationVisible(t *testing.T) {
	fixture := newOwnedGatewayFixture(t, "log_overflow")
	result, err := Run(t.Context(), fixture.preset, []LoadedCase{loadedTestCase(t, "general.owned.logs")},
		RunOptions{Home: fixture.home, OutputParent: fixture.output, StartTarget: true, SuiteTimeout: 15 * time.Second})
	if err != nil || result.TargetLifecycle == nil || !result.TargetLifecycle.LogTruncated {
		t.Fatal("bounded log truncation was hidden", err)
	}
	logs, err := filepath.Glob(filepath.Join(fixture.home, "eval-gateway-*.jsonl"))
	if err != nil || len(logs) != 1 {
		t.Fatal("owned log unavailable")
	}
	info, err := os.Stat(logs[0])
	if err != nil || info.Mode().Perm() != 0o600 || info.Size() != ownedGatewayLogBytes {
		t.Fatal("owned log escaped its byte/permission bounds")
	}
}

func TestOwnedGatewayStructuredLogsAreScannedBeforeAnyProjection(t *testing.T) {
	for _, mode := range []string{"safe", "encoded", "malformed", "late_leak"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newOwnedGatewayFixture(t, "structured_"+mode)
			child, target, err := startOwnedGateway(t.Context(), fixture.preset, fixture.home)
			registerOwnedGatewayCleanup(t, child)
			if err != nil || child == nil {
				t.Fatal("structured capture child was not admitted", err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if receipt := child.stopAndWait(ctx); receipt.Code != "native_exit_zero" || receipt.LogTruncated {
				t.Fatal("log capture did not settle normally", receipt.Code)
			}
			logs, err := filepath.Glob(filepath.Join(fixture.home, "eval-gateway-*.jsonl"))
			if err != nil || len(logs) != 1 {
				t.Fatal("native capture did not declare its strict JSON-lines format", err)
			}
			data, err := os.ReadFile(logs[0])
			if err != nil {
				t.Fatal(err)
			}
			// This legacy process fixture has inline synthetic credentials. Inspect
			// the unmodified capture, not an unrelated whole-profile privacy claim.
			logHome := t.TempDir()
			if os.WriteFile(filepath.Join(logHome, "capture.jsonl"), data, 0o600) != nil {
				t.Fatal("raw capture observation unavailable")
			}
			observation := observePrivateSurfaces(t.Context(), logHome,
				map[string]string{"name": "PRIVATE_LOG_CANARY"}, target.config.SensitiveDataReplacer(), nil)
			want := "leak_observed"
			switch mode {
			case "safe":
				want = "observed"
			case "malformed":
				want = "unavailable"
			}
			if !observation.valid() || observation.Code != want {
				t.Fatal("raw log projection lost safe records, encoded leaks, or uncertainty", mode, observation)
			}
		})
	}
}
