package eval

import (
	"net"
	"slices"
	"strconv"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/events"
)

const (
	activeToolCapability = "active_tool"
	activeToolServer     = "corefixture"
	activeToolNativeName = "mcp_corefixture_eval_barrier"
)

// ActiveToolProfile pins one owned, read-only local fixture, not generic MCP.
type ActiveToolProfile struct {
	Address          string `json:"address"`
	Namespace        string `json:"namespace"`
	SteeringMode     string `json:"steering_mode"`
	MaxParallelTurns int    `json:"max_parallel_turns,omitempty"`
}

func (profile ActiveToolProfile) validate() error {
	host, port, err := net.SplitHostPort(profile.Address)
	number, numberErr := strconv.Atoi(port)
	id, idErr := uuid.Parse(profile.Namespace)
	if err != nil || numberErr != nil || host != "127.0.0.1" || number < 1024 || number > 65535 ||
		strconv.Itoa(number) != port || profile.Address != net.JoinHostPort(host, port) ||
		idErr != nil || id.Version() != 4 || id.Variant() != uuid.RFC4122 || id.String() != profile.Namespace ||
		profile.SteeringMode != "one-at-a-time" && profile.SteeringMode != "all" ||
		profile.MaxParallelTurns < 0 || profile.MaxParallelTurns > 2 {
		return ErrInvalidPreset
	}
	return nil
}

func (profile ActiveToolProfile) url() string {
	return "http://" + profile.Address + "/eval/" + profile.Namespace + "/mcp"
}

func activeToolLogging() config.EventLoggingConfig {
	return config.EventLoggingConfig{
		Enabled: true, MinSeverity: "info", Include: []string{
			events.KindAgentToolExecStart.String(), events.KindAgentToolExecEnd.String(),
			events.KindAgentInterruptReceived.String(), events.KindAgentSteeringInjected.String(),
			events.KindAgentTurnEnd.String(),
		},
	}
}

func admittedEvalLogLevel(preset Preset, level string) bool {
	if preset.ActiveTool != nil || hasApprovalTool(preset) || hasSkillSelection(preset) || hasCompaction(preset) {
		return level == "info"
	}
	return slices.Contains([]string{"warn", "error", "fatal"}, level)
}

func validateActiveToolPolicy(cfg *config.Config, preset Preset) error {
	profile := preset.ActiveTool
	if profile == nil {
		return nil
	}
	if cfg == nil || len(cfg.Agents.List) != 1 || cfg.Agents.List[0].ToolPolicy == nil ||
		!slices.Contains(cfg.Agents.List[0].ToolPolicy.Allow, activeToolNativeName) ||
		len(cfg.Agents.List[0].ToolPolicy.Deny) != 0 {
		return ErrUnsafeTarget
	}
	logging, want := cfg.Events.Logging, activeToolLogging()
	if profile.MaxParallelTurns != 0 && (cfg.Agents.List[0].MaxParallelTurns != profile.MaxParallelTurns ||
		cfg.Agents.Defaults.MaxParallelTurns != profile.MaxParallelTurns) {
		return ErrUnsafeTarget
	}
	server := cfg.Tools.MCP.Servers[activeToolServer]
	if profile.validate() != nil || !cfg.Tools.MCP.Enabled || cfg.Tools.MCP.Discovery.Enabled ||
		len(cfg.Tools.MCP.Servers) != 1 || !server.Enabled || server.Type != "http" || server.URL != profile.url() ||
		server.Command != "" || len(server.Args) != 0 || len(server.Env) != 0 || server.EnvFile != "" ||
		len(server.Headers) != 0 || server.Deferred != nil || len(server.VisibleTools) != 0 ||
		server.ExclusiveLockFile != "" || cfg.Agents.Defaults.SteeringMode != profile.SteeringMode ||
		!logging.Enabled || logging.IncludePayload || logging.MinSeverity != want.MinSeverity ||
		!slices.Equal(logging.Include, want.Include) || len(logging.Exclude) != 0 ||
		!admittedEvalLogLevel(preset, config.EffectiveGatewayLogLevel(cfg)) {
		return ErrUnsafeTarget
	}
	return nil
}

// Preparation only allocates a new identity/port. Run must bind and retain it
// before launching the child; it never adopts whatever is listening there.
func prepareActiveToolProfile(cfg *config.Config, preset *Preset) error {
	if preset.ActiveTool == nil {
		return nil
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return ErrPreparation
	}
	defer func() { _ = listener.Close() }()
	address := listener.Addr().String()
	if listener.Addr().(*net.TCPAddr).Port == cfg.Gateway.Port {
		second, listenErr := net.Listen("tcp4", "127.0.0.1:0")
		if listenErr != nil {
			return ErrPreparation
		}
		defer func() { _ = second.Close() }()
		address = second.Addr().String()
	}
	copyProfile := *preset.ActiveTool
	copyProfile.Address, copyProfile.Namespace = address, uuid.NewString()
	preset.ActiveTool = &copyProfile
	server := cfg.Tools.MCP.Servers[activeToolServer]
	server.URL = copyProfile.url()
	cfg.Tools.MCP.Servers[activeToolServer] = server
	return nil
}

func bindOwnedActiveTool(preset Preset, home string) (*activeToolFixture, error) {
	if preset.ActiveTool == nil {
		return nil, nil
	}
	profile, err := readPreparedProfile(preset, home)
	if err != nil || validateConversationProfile(profile.config, preset, profile.workspace) != nil ||
		validateFreshOwnedProfile(profile) != nil {
		return nil, ErrUnsafeTarget
	}
	fixture, err := newActiveToolFixture(preset.ActiveTool.Address, preset.ActiveTool.Namespace)
	if err != nil {
		return nil, ErrUnsafeTarget
	}
	return fixture, nil
}

func activeToolMatches(preset Preset, fixture *activeToolFixture) bool {
	if preset.ActiveTool == nil || fixture == nil {
		return preset.ActiveTool == nil && fixture == nil
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return !fixture.closed && !fixture.failed && fixture.url == preset.ActiveTool.url()
}

func closeActiveTool(fixture *activeToolFixture) error {
	if fixture == nil {
		return nil
	}
	return fixture.shutdown()
}
