package eval

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/health"
	"github.com/bogdanovich/mintclaw/pkg/pid"
)

const admissionTestToken = "00112233445566778899aabbccddeeff"

type admissionFixture struct {
	home        string
	workspace   string
	output      string
	preset      Preset
	cfg         *config.Config
	probe       health.RuntimeProbe
	probes      atomic.Int32
	tasks       atomic.Int32
	requests    chan protocol.MintClawMessage
	mutateProbe func(*health.RuntimeProbe, int32)
	respond     func(protocol.MintClawMessage, *protocol.MintClawMessage)
}

func newAdmissionFixture(t *testing.T) *admissionFixture {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("prepared runtime admission currently supports Linux and macOS")
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := &admissionFixture{
		home: home, workspace: filepath.Join(home, "workspace"), output: filepath.Join(home, "reports"),
		requests: make(chan protocol.MintClawMessage, 32),
	}
	for _, directory := range []string{fixture.workspace, fixture.output} {
		if err = os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, filepath.Join(home, "config.json"))
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/runtime" {
			if r.Header.Get("Authorization") != "Bearer "+admissionTestToken || r.URL.RawQuery != "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			probe := fixture.probe
			number := fixture.probes.Add(1)
			if fixture.mutateProbe != nil {
				fixture.mutateProbe(&probe, number)
			}
			_ = json.NewEncoder(w).Encode(probe)
			return
		}
		if r.URL.Path != "/mintclaw/ws" || r.Header.Get("Authorization") != "Bearer test-channel-token" {
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
			fixture.tasks.Add(1)
			fixture.requests <- request
			response := testResponse(request, 1)
			response.Payload[protocol.PayloadKeyTraceScopes] = []events.TraceScope{{
				Workspace: fixture.workspace, TurnID: request.ID,
			}}
			if fixture.respond != nil {
				fixture.respond(request, &response)
			}
			if connection.WriteJSON(response) != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Gateway.Host, cfg.Gateway.Port, cfg.Gateway.LogLevel = endpoint.Hostname(), port, "warn"
	cfg.Agents.Defaults.Workspace, cfg.Agents.Defaults.ModelName = fixture.workspace, "eval-model"
	cfg.Agents.List[0].ToolPolicy = &config.AgentCapabilityPolicy{
		Default: config.AgentCapabilityDefaultDeny, Allow: []string{"request_user_input"},
	}
	cfg.Agents.List[0].MCPServerPolicy = &config.AgentCapabilityPolicy{Default: config.AgentCapabilityDefaultDeny}
	cfg.Tools.Cron.Enabled, cfg.Tools.Exec.Enabled, cfg.Tools.MCP.Enabled = false, false, false
	cfg.Tools.Browser.Enabled, cfg.Tools.Document.Enabled, cfg.Tools.Web.PreferNative = false, false, false
	cfg.Tools.RequestUserInput.Enabled = true
	cfg.Tools.Approval.Mode = config.ToolApprovalModeRequired
	cfg.Hooks.Enabled, cfg.Heartbeat.Enabled, cfg.Devices.Enabled = false, false, false
	cfg.ModelList = config.SecureModelList{{
		ModelName: "eval-model", Provider: "openai", Model: "synthetic-model", Enabled: true,
	}}
	settings, err := json.Marshal(map[string]any{"token": "test-channel-token", "read_timeout": 60})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Channels = config.ChannelsConfig{"mintclaw": {
		Type: config.ChannelMintClaw, Enabled: true, Settings: settings,
	}}
	fixture.cfg = cfg
	fixture.preset = Preset{
		Version: Version, ID: "test-preset", ConfigPath: filepath.Join(home, "config.json"),
		ExpectedAgent: "main", ExpectedWorkspace: fixture.workspace, IsolationRoot: home,
		Capabilities: []string{"conversation", "interactions"},
	}
	fixture.writeConfig(t)
	loaded, err := config.LoadConfigReadOnly(fixture.preset.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := preparedRuntimeFingerprint(loaded)
	if err != nil {
		t.Fatal(err)
	}
	fixture.probe = health.RuntimeProbe{
		Version: 1, PID: os.Getpid(), InstanceSHA256: admissionHash(admissionTestToken),
		ConfigSHA256: fingerprint, HomeSHA256: admissionHash(home),
		ConfigPathSHA256: admissionHash(fixture.preset.ConfigPath), LogLevel: "warn",
		BuildVersion: "test-only", GitCommit: "unknown", BuildTime: "unknown", GoVersion: runtime.Version(),
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
	}
	writeAdmissionJSON(t, filepath.Join(home, ".mintclaw.pid"), pid.PidFileData{
		PID: os.Getpid(), Token: admissionTestToken, Version: "test-only", Port: port, Host: endpoint.Hostname(),
	})
	return fixture
}

func (fixture *admissionFixture) writeConfig(t *testing.T) {
	t.Helper()
	writeAdmissionJSON(t, fixture.preset.ConfigPath, fixture.cfg)
}

func writeAdmissionJSON(t *testing.T, name string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedAdmissionChecksActiveIdentity(t *testing.T) {
	fixture := newAdmissionFixture(t)
	target, err := admitPreparedTarget(t.Context(), fixture.preset, fixture.home)
	if err != nil || target.probe != fixture.probe || fixture.probes.Load() != 1 || fixture.tasks.Load() != 0 {
		t.Fatal("prepared identity was not admitted without task execution", err)
	}
}

func TestPreparedAdmissionRefusesUnsafeProfileBeforeTask(t *testing.T) {
	for name, mutate := range map[string]func(*admissionFixture){
		"hooks":     func(f *admissionFixture) { f.cfg.Hooks.Enabled = true },
		"exec":      func(f *admissionFixture) { f.cfg.Tools.Exec.Enabled = true },
		"mcp":       func(f *admissionFixture) { f.cfg.Tools.MCP.Enabled = true },
		"browser":   func(f *admissionFixture) { f.cfg.Tools.Browser.Enabled = true },
		"document":  func(f *admissionFixture) { f.cfg.Tools.Document.Enabled = true },
		"trace":     func(f *admissionFixture) { f.cfg.Diagnostics.TraceCapture.Enabled = true },
		"reload":    func(f *admissionFixture) { f.cfg.Gateway.HotReload = true },
		"wide tool": func(f *admissionFixture) { f.cfg.Agents.List[0].ToolPolicy.Allow = []string{"*"} },
		"allow default": func(f *admissionFixture) {
			f.cfg.Agents.List[0].ToolPolicy = &config.AgentCapabilityPolicy{Default: config.AgentCapabilityDefaultAllow}
		},
		"cli provider":        func(f *admissionFixture) { f.cfg.ModelList[0].Provider = "codex-cli" },
		"missing interaction": func(f *admissionFixture) { f.cfg.Tools.RequestUserInput.Enabled = false },
		"wrong home":          func(f *admissionFixture) { f.preset.IsolationRoot = f.workspace },
		"wrong workspace":     func(f *admissionFixture) { f.preset.ExpectedWorkspace = f.output },
		"wrong agent":         func(f *admissionFixture) { f.preset.ExpectedAgent = "different" },
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newAdmissionFixture(t)
			mutate(fixture)
			fixture.writeConfig(t)
			_, err := admitPreparedTarget(t.Context(), fixture.preset, fixture.home)
			if !errors.Is(err, ErrUnsafeTarget) || fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 {
				t.Fatal("unsafe profile reached the running target", err)
			}
		})
	}
}

func TestPreparedAdmissionRefusesMismatchedProbe(t *testing.T) {
	for name, mutate := range map[string]func(*health.RuntimeProbe){
		"config":   func(p *health.RuntimeProbe) { p.ConfigSHA256 = strings.Repeat("0", 64) },
		"home":     func(p *health.RuntimeProbe) { p.HomeSHA256 = strings.Repeat("0", 64) },
		"instance": func(p *health.RuntimeProbe) { p.InstanceSHA256 = strings.Repeat("0", 64) },
		"pid":      func(p *health.RuntimeProbe) { p.PID++ },
		"platform": func(p *health.RuntimeProbe) { p.GOOS = "other" },
		"debug":    func(p *health.RuntimeProbe) { p.LogLevel = "debug" },
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newAdmissionFixture(t)
			fixture.mutateProbe = func(p *health.RuntimeProbe, _ int32) { mutate(p) }
			_, err := admitPreparedTarget(t.Context(), fixture.preset, fixture.home)
			if !errors.Is(err, ErrUnsafeTarget) || fixture.tasks.Load() != 0 {
				t.Fatal("mismatched active identity admitted", err)
			}
		})
	}
}

func TestPreparedAdmissionRefusesNullModelBeforeNativeLoader(t *testing.T) {
	fixture := newAdmissionFixture(t)
	fixture.cfg.ModelList = config.SecureModelList{nil}
	fixture.writeConfig(t)
	_, err := admitPreparedTarget(t.Context(), fixture.preset, fixture.home)
	if !errors.Is(err, ErrUnsafeTarget) || fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 {
		t.Fatal("null model reached the native runtime or task admission", err)
	}
}

func TestPreparedFingerprintOnlyMirrorsNativeStartup(t *testing.T) {
	fixture := newAdmissionFixture(t)
	before, err := config.PublicFingerprint(fixture.cfg)
	if err != nil {
		t.Fatal(err)
	}
	expected := *fixture.cfg
	copyModel := *fixture.cfg.ModelList[0]
	copyModel.Workspace = fixture.workspace
	expected.ModelList = config.SecureModelList{&copyModel}
	expected.Agents.Defaults.ModelName = copyModel.Model
	want, err := config.PublicFingerprint(&expected)
	got, gotErr := preparedRuntimeFingerprint(fixture.cfg)
	after, afterErr := config.PublicFingerprint(fixture.cfg)
	if err != nil || gotErr != nil || afterErr != nil || got != want || before != after {
		t.Fatal("fingerprint masks differences or mutates prepared config")
	}
	expected.Tools.Exec.Enabled = true
	changed, err := config.PublicFingerprint(&expected)
	if err != nil || changed == got {
		t.Fatal("a policy difference was erased")
	}
}

func TestConversationTemplateUsesNativeConfigAndAdmissionPolicy(t *testing.T) {
	fixture := newAdmissionFixture(t)
	data, err := os.ReadFile("../../evals/profiles/conversation.template.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	if err = config.DecodeCurrentConfig(data, &cfg); err != nil {
		t.Fatal("public template is not a current native config", err)
	}
	cfg.Agents.Defaults.Workspace = fixture.workspace
	cfg.ModelList[0].Model = "synthetic-model"
	cfg.Channels = fixture.cfg.Channels
	if err = validateConversationProfile(&cfg, fixture.preset, fixture.workspace); err != nil {
		t.Fatal("documented template is refused by admission", err)
	}
	security := []byte("channel_list:\n  mintclaw:\n    settings:\n      token: file://eval-channel-token\n")
	if err = os.WriteFile(filepath.Join(fixture.home, ".security.yml"), security, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(
		filepath.Join(fixture.home, "eval-channel-token"),
		[]byte("test-channel-token"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	fixture.cfg = &cfg
	fixture.writeConfig(t)
	loaded, err := config.LoadConfigReadOnly(fixture.preset.ConfigPath)
	if err != nil || validateConversationProfile(loaded, fixture.preset, fixture.workspace) != nil {
		t.Fatal("documented native security reference is not usable", err)
	}
}
