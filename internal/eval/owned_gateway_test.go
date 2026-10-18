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
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/health"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/pid"
)

type ownedGatewayControl struct {
	Mode      string `json:"mode"`
	ParentPID int    `json:"parent_pid"`
	LateValue string `json:"late_value,omitempty"`
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
		os.Getenv("MINTCLAW_EVAL_PARENT_SECRET") != "" || os.Getenv("MINTCLAW_GATEWAY_HOST") != "" {
		return 8
	}
	if control.Mode == "early_exit" {
		return 9
	}
	cfg, err := config.LoadConfigReadOnly(filepath.Join(home, "config.json"))
	if err != nil {
		return 8
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
	var first atomic.Bool
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
				response := testResponse(request, 1)
				response.Payload[protocol.PayloadKeyTraceScopes] = []events.TraceScope{{
					Workspace: cfg.WorkspacePath(), TurnID: request.ID,
				}}
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
	if control.Mode == "bad_exit" {
		return 9
	}
	return 0
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
	logs, err := filepath.Glob(filepath.Join(fixture.home, "eval-gateway-*.log"))
	if err != nil || len(logs) != 1 {
		t.Fatal("owned log unavailable")
	}
	info, err := os.Stat(logs[0])
	if err != nil || info.Mode().Perm() != 0o600 || info.Size() != ownedGatewayLogBytes {
		t.Fatal("owned log escaped its byte/permission bounds")
	}
}
