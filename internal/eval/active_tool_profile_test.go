package eval

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func enableTestActiveTool(fixture *admissionFixture) {
	limits := targetBudgetTestLimits()
	fixture.preset.TargetCallBudget, fixture.preset.CaptureModelMetadata = &limits, true
	fixture.preset.Capabilities = append(
		slices.Clone(fixture.preset.Capabilities),
		boundedCallsCapability,
		activeToolCapability,
	)
	fixture.preset.ActiveTool = &ActiveToolProfile{
		Address: "127.0.0.1:23456", Namespace: uuid.NewString(), SteeringMode: "one-at-a-time",
	}
	fixture.cfg.Tools.MCP.Enabled = true
	fixture.cfg.Tools.MCP.Discovery.Enabled = false
	fixture.cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{activeToolServer: {
		Enabled: true, Type: "http", URL: fixture.preset.ActiveTool.url(),
	}}
	fixture.cfg.Agents.List[0].ToolPolicy.Allow = append(
		fixture.cfg.Agents.List[0].ToolPolicy.Allow,
		activeToolNativeName,
	)
	fixture.cfg.Agents.List[0].MCPServerPolicy.Allow = []string{activeToolServer}
	fixture.cfg.Agents.Defaults.SteeringMode = fixture.preset.ActiveTool.SteeringMode
	fixture.cfg.Gateway.LogLevel, fixture.cfg.Events.Logging = "info", activeToolLogging()
}

func TestActiveToolProfileNarrowAdmissionAndNoAttach(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*admissionFixture)
	}{
		{"positive", func(*admissionFixture) {}},
		{"other_server", func(f *admissionFixture) {
			f.cfg.Tools.MCP.Servers["other"] = config.MCPServerConfig{Enabled: true, Type: "http", URL: "http://127.0.0.1:1234"}
		}},
		{"external_endpoint", func(f *admissionFixture) {
			server := f.cfg.Tools.MCP.Servers[activeToolServer]
			server.URL = "https://example.invalid/mcp"
			f.cfg.Tools.MCP.Servers[activeToolServer] = server
		}},
		{"stdio_authority", func(f *admissionFixture) {
			server := f.cfg.Tools.MCP.Servers[activeToolServer]
			server.Command = "sh"
			f.cfg.Tools.MCP.Servers[activeToolServer] = server
		}},
		{"headers", func(f *admissionFixture) {
			server := f.cfg.Tools.MCP.Servers[activeToolServer]
			server.Headers = map[string]string{"Authorization": "Bearer untyped"}
			f.cfg.Tools.MCP.Servers[activeToolServer] = server
		}},
		{"broad_tool", func(f *admissionFixture) {
			f.cfg.Agents.List[0].ToolPolicy.Allow = append(f.cfg.Agents.List[0].ToolPolicy.Allow, "mcp_*")
		}},
		{"broad_server", func(f *admissionFixture) { f.cfg.Agents.List[0].MCPServerPolicy.Allow = []string{"*"} }},
		{"raw_payload", func(f *admissionFixture) { f.cfg.Events.Logging.IncludePayload = true }},
		{"debug", func(f *admissionFixture) { f.cfg.Gateway.LogLevel = "debug" }},
		{"silent", func(f *admissionFixture) { f.cfg.Gateway.LogLevel = "warn" }},
		{"event_glob", func(f *admissionFixture) { f.cfg.Events.Logging.Include = []string{"*"} }},
		{"wrong_mode", func(f *admissionFixture) { f.cfg.Agents.Defaults.SteeringMode = "all" }},
		{"exec", func(f *admissionFixture) { f.cfg.Tools.Exec.Enabled = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAdmissionFixture(t)
			enableTestActiveTool(fixture)
			test.edit(fixture)
			err := validateConversationProfile(fixture.cfg, fixture.preset, fixture.workspace)
			if (err == nil) != (test.name == "positive") {
				t.Fatal("profile policy outcome", err)
			}
			if test.name == "positive" {
				item := loadedTestCase(t, "general.active")
				if _, err := Run(t.Context(), fixture.preset, []LoadedCase{item}, RunOptions{
					Home: fixture.home, OutputParent: fixture.output,
				}); !errors.Is(err, ErrUnsafeTarget) || fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 {
					t.Fatal("active fixture attached before owned admission", err)
				}
			}
		})
	}
}

func TestActiveToolPresetRejectsIncompleteOrWiderAdmissionBeforeWork(t *testing.T) {
	for _, mode := range []string{
		"missing_capability", "missing_profile", "missing_budget", "missing_metadata", "external_listener",
		"unknown_mode", "model_user", "browser", "pdf",
	} {
		t.Run(mode, func(t *testing.T) {
			fixture := newAdmissionFixture(t)
			enableTestActiveTool(fixture)
			switch mode {
			case "missing_capability":
				fixture.preset.Capabilities = slices.DeleteFunc(fixture.preset.Capabilities,
					func(value string) bool { return value == activeToolCapability })
			case "missing_profile":
				fixture.preset.ActiveTool = nil
			case "missing_budget":
				fixture.preset.TargetCallBudget = nil
			case "missing_metadata":
				fixture.preset.CaptureModelMetadata = false
			case "external_listener":
				fixture.preset.ActiveTool.Address = "0.0.0.0:12345"
			case "unknown_mode":
				fixture.preset.ActiveTool.SteeringMode = "adaptive"
			case "model_user":
				fixture.preset.UserModel = "eval-model"
			case "browser", "pdf":
				fixture.preset.Capabilities = append(fixture.preset.Capabilities, mode)
			}
			if fixture.preset.Validate() == nil {
				t.Fatal("incomplete or wider active tool preset accepted")
			}
			item := loadedTestCase(t, "general.active")
			if _, err := Run(t.Context(), fixture.preset, []LoadedCase{item}, RunOptions{
				Home: fixture.home, OutputParent: fixture.output, StartTarget: true,
			}); !errors.Is(err, ErrUnsafeTarget) || fixture.probes.Load() != 0 || fixture.tasks.Load() != 0 {
				t.Fatal("rejected preset reached native target work", err)
			}
		})
	}
}

func TestPrepareActiveToolProfileIsFreshAndDoesNotAdoptListener(t *testing.T) {
	fixture, parent := preparationFixture(t)
	enableTestActiveTool(fixture)
	if _, err := config.NewRepository(fixture.preset.ConfigPath).Save(fixture.cfg); err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareProfile(t.Context(), fixture.preset, fixture.home, parent)
	if err != nil || prepared.RuntimeRun {
		t.Fatal("offline preparation", err)
	}
	preset, err := LoadPreset(prepared.PresetPath)
	if err != nil || preset.ActiveTool == nil || preset.ActiveTool.Namespace == fixture.preset.ActiveTool.Namespace ||
		preset.ActiveTool.Address == fixture.preset.ActiveTool.Address {
		t.Fatal("fixture identity was reused", err)
	}
	t.Setenv(config.EnvHome, prepared.Home)
	t.Setenv(config.EnvConfig, prepared.ConfigPath)
	profile, err := readPreparedProfile(preset, prepared.Home)
	if err != nil || validateConversationProfile(profile.config, preset, prepared.Workspace) != nil ||
		preset.ActiveTool.Address == net.JoinHostPort(
			profile.config.Gateway.Host,
			strconv.Itoa(profile.config.Gateway.Port),
		) {
		t.Fatal("prepared fixture binding", err)
	}
	listener, err := net.Listen("tcp4", preset.ActiveTool.Address)
	if err != nil {
		t.Fatal(err)
	}
	if host, err := bindOwnedActiveTool(preset, prepared.Home); err == nil || host != nil {
		_ = listener.Close()
		t.Fatal("adopted occupied fixture address")
	}
	_ = listener.Close()
	host, err := bindOwnedActiveTool(preset, prepared.Home)
	if host != nil {
		t.Cleanup(func() { _ = closeActiveTool(host) })
	}
	if err != nil || !activeToolMatches(preset, host) {
		t.Fatal("own listener unavailable", err)
	}
	if err := closeActiveTool(host); err != nil || activeToolMatches(preset, host) {
		t.Fatal("closed listener remained admitted", err)
	}
	if _, err := os.Stat(filepath.Join(prepared.Home, ".mintclaw.pid")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preparation started a gateway")
	}
}
