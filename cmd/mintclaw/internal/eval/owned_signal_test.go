package eval

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	manualeval "github.com/bogdanovich/mintclaw/internal/eval"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/gateway"
	"github.com/bogdanovich/mintclaw/pkg/pid"
)

// The parent executes the actual eval command; its owned child executes the
// native gateway. The only stub is a local, tools-disabled model endpoint.
func TestMain(m *testing.M) {
	if len(os.Args) >= 2 && os.Args[1] == "owned-eval-signal-parent" {
		command := NewEvalCommand()
		command.SetArgs(os.Args[2:])
		if command.Execute() != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "gateway" {
		if gateway.Run(false, config.GetHome(), filepath.Join(config.GetHome(), "config.json"), false) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestOwnedEvalCLIInterruptStopsAndReapsNativeGateway(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("owned runtime admission supports Linux and macOS")
	}
	for _, interrupt := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(interrupt.String(), func(t *testing.T) {
			home, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Chmod(home, 0o700); err != nil {
				t.Fatal(err)
			}
			workspace, reports := filepath.Join(home, "workspace"), filepath.Join(home, "reports")
			for _, directory := range []string{workspace, reports} {
				if err = os.Mkdir(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				select {
				case entered <- struct{}{}:
				default:
				}
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			t.Cleanup(func() {
				close(release)
				model.Close()
			})
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			if err = listener.Close(); err != nil {
				t.Fatal(err)
			}
			cfg := config.DefaultConfig()
			cfg.Gateway.Host, cfg.Gateway.Port, cfg.Gateway.LogLevel = "127.0.0.1", port, "warn"
			cfg.Agents.Defaults.Workspace, cfg.Agents.Defaults.ModelName = workspace, "eval-model"
			cfg.Agents.Defaults.ContextManager = "none"
			cfg.Agents.List[0].ToolPolicy = &config.AgentCapabilityPolicy{Default: config.AgentCapabilityDefaultDeny}
			cfg.Agents.List[0].MCPServerPolicy = &config.AgentCapabilityPolicy{
				Default: config.AgentCapabilityDefaultDeny,
			}
			cfg.ModelList = config.SecureModelList{{
				ModelName: "eval-model", Provider: "openai", Model: "synthetic-model", Enabled: true,
				APIBase: model.URL, APIKeys: config.SimpleSecureStrings("synthetic-signal-test-key"),
			}}
			cfg.Tools.Cron.Enabled, cfg.Tools.Exec.Enabled, cfg.Tools.MCP.Enabled = false, false, false
			cfg.Tools.Browser.Enabled, cfg.Tools.Document.Enabled, cfg.Tools.Web.PreferNative = false, false, false
			cfg.Tools.Approval.Mode = config.ToolApprovalModeRequired
			cfg.Hooks.Enabled, cfg.Heartbeat.Enabled, cfg.Devices.Enabled = false, false, false
			settings, err := json.Marshal(map[string]any{"token": "synthetic-signal-channel", "read_timeout": 60})
			if err != nil {
				t.Fatal(err)
			}
			cfg.Channels = config.ChannelsConfig{"mintclaw": {
				Type: config.ChannelMintClaw, Enabled: true, AllowFrom: []string{"mintclaw-user"}, Settings: settings,
			}}
			configPath := filepath.Join(home, "config.json")
			if _, err = config.NewRepository(configPath).Save(cfg); err != nil {
				t.Fatal("native profile preparation failed", err)
			}
			preset := manualeval.Preset{
				Version: 1, ID: "signal-control", ConfigPath: configPath,
				ExpectedAgent: "main", ExpectedWorkspace: workspace, IsolationRoot: home,
				Capabilities: []string{"conversation"},
			}
			data, err := json.Marshal(preset)
			presetPath := filepath.Join(reports, "preset.json")
			if err != nil || os.WriteFile(presetPath, data, 0o600) != nil {
				t.Fatal("preset preparation failed", err)
			}
			corpus, err := filepath.Abs("../../../../evals")
			if err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			log, err := os.OpenFile(filepath.Join(reports, "parent.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = log.Close() })
			parent := exec.Command(
				executable,
				"owned-eval-signal-parent",
				"run",
				"--root",
				corpus,
				"--case",
				"general.context.followup",
				"--preset",
				presetPath,
				"--out",
				reports,
				"--start-target",
				"--json",
			)
			parent.Env = []string{
				"HOME=" + home, config.EnvHome + "=" + home, config.EnvConfig + "=" + configPath,
				"PATH=" + os.Getenv("PATH"), "LANG=C",
			}
			parent.Stdout, parent.Stderr = log, log
			if err = parent.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- parent.Wait() }()
			finished := false
			childPID := 0
			t.Cleanup(func() {
				if !finished {
					_ = parent.Process.Kill()
					<-done
				}
				if childPID > 1 && pid.ProcessRunning(childPID) {
					process, findErr := os.FindProcess(childPID)
					if findErr == nil {
						_ = process.Signal(os.Interrupt)
					}
				}
			})
			select {
			case <-entered:
			case waitErr := <-done:
				finished = true
				t.Fatal("eval parent exited before native provider admission", waitErr)
			case <-time.After(40 * time.Second):
				t.Fatal("native provider was not entered")
			}
			data, err = os.ReadFile(filepath.Join(home, ".mintclaw.pid"))
			var process pid.PidFileData
			if err != nil || json.Unmarshal(data, &process) != nil || process.PID <= 1 ||
				process.PID == parent.Process.Pid {
				t.Fatal("owned native child identity unavailable", err)
			}
			childPID = process.PID
			if err = parent.Process.Signal(interrupt); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				finished = true
			case <-time.After(45 * time.Second):
				t.Fatal("signaled eval parent did not finish bounded cleanup")
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || pid.ProcessRunning(childPID) {
				t.Fatal("signal killed the parent or left the native gateway running", err)
			}
			entries, err := os.ReadDir(reports)
			if err != nil {
				t.Fatal(err)
			}
			var report manualeval.RunReport
			for _, entry := range entries {
				if entry.IsDir() {
					data, err = os.ReadFile(filepath.Join(reports, entry.Name(), "run.json"))
					if err != nil || json.Unmarshal(data, &report) != nil {
						t.Fatal("interrupted run report unavailable", err)
					}
				}
			}
			if !report.Complete || report.Verdict == manualeval.Pass || report.TargetLifecycle == nil ||
				!report.TargetLifecycle.ExitObserved || report.TargetLifecycle.PID != childPID || report.TargetLifecycle.Forced {
				t.Fatal("native interruption receipt did not establish owned child reap")
			}
		})
	}
}
