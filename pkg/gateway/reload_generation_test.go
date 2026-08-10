package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/netbind"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type gatewayReloadHarness struct {
	config   *config.Config
	provider providers.LLMProvider
	bus      *bus.MessageBus
	loop     *agent.AgentLoop
	services *services
}

func newGatewayReloadHarness(t *testing.T) *gatewayReloadHarness {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.Defaults.ContextManagerConfig = nil
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port = port

	msgBus := bus.NewMessageBus()
	provider := providers.LLMProvider(&startupBlockedProvider{reason: "not used"})
	loop := agent.NewAgentLoop(cfg, msgBus, provider)
	runningServices, err := setupAndStartServices(
		context.Background(),
		cfg,
		loop,
		msgBus,
		"reload-test-token",
		netbind.OpenResult{
			Listeners: []net.Listener{listener},
			BindHosts: []string{"127.0.0.1"},
			Port:      strconv.Itoa(port),
			ProbeHost: "127.0.0.1",
		},
	)
	if err != nil {
		_ = listener.Close()
		loop.Close()
		msgBus.Close()
		t.Fatalf("setupAndStartServices() error = %v", err)
	}
	harness := &gatewayReloadHarness{
		config:   cfg,
		provider: provider,
		bus:      msgBus,
		loop:     loop,
		services: runningServices,
	}
	t.Cleanup(func() {
		shutdownGateway(harness.services, harness.loop, harness.provider, harness.bus, true)
		_ = listener.Close()
	})
	return harness
}

func TestConfigReloadRollsBackEveryPostQuiesceStage(t *testing.T) {
	useRestartRuntimeGOOS(t, "linux")
	stages := []gatewayReloadStage{
		gatewayReloadAgentPrepared,
		gatewayReloadCronStarted,
		gatewayReloadHeartbeatStarted,
		gatewayReloadMediaStarted,
		gatewayReloadBrowserStarted,
		gatewayReloadVoiceStarted,
		gatewayReloadDeviceInitialized,
		gatewayReloadChannelsReconciled,
		gatewayReloadNodesReconciled,
	}

	for _, stage := range stages {
		stage := stage
		t.Run(string(stage), func(t *testing.T) {
			harness := newGatewayReloadHarness(t)
			originalProvider := harness.provider
			originalRegistry := harness.loop.GetRegistry()
			oldChannelManager := harness.services.ChannelManager
			oldNodeAdmission := harness.services.NodeAdmission
			oldOutboundRecovery := harness.services.OutboundRecovery
			oldHealthServer := harness.services.HealthServer
			next := *harness.config
			next.Heartbeat.Interval++
			next.Gateway.SafeRestart = config.GatewaySafeRestartConfig{
				Enabled:        true,
				ServiceManager: "systemd-user",
				Service:        "mintclaw-main.service",
			}
			injectedErr := fmt.Errorf("injected reload failure after %s", stage)

			err := handleConfigReloadWithHooks(
				context.Background(),
				harness.loop,
				&next,
				&harness.provider,
				harness.services,
				harness.bus,
				true,
				false,
				serviceShutdownTimeout,
				gatewayReloadHooks{
					afterStage: func(current gatewayReloadStage) error {
						if current == stage {
							return injectedErr
						}
						return nil
					},
				},
			)
			if !errors.Is(err, injectedErr) {
				t.Fatalf("handleConfigReloadWithHooks() error = %v, want injected error", err)
			}
			if harness.loop.GetConfig() != harness.config {
				t.Fatal("failed reload published the new agent config")
			}
			if harness.loop.GetRegistry() != originalRegistry {
				t.Fatal("failed reload published the prepared agent registry")
			}
			if harness.provider != originalProvider {
				t.Fatal("failed reload published the prepared provider")
			}
			if _, ok := originalRegistry.GetDefaultAgent().Tools.Get("gateway_restart"); ok {
				t.Fatal("failed reload leaked a new runtime tool into the old registry")
			}
			if harness.services.HeartbeatService == nil || !harness.services.HeartbeatService.IsRunning() {
				t.Fatal("failed reload did not restore a serviceable heartbeat generation")
			}
			if harness.services.ChannelManager != oldChannelManager {
				t.Fatal("failed reload replaced the persistent channel manager")
			}
			if harness.services.NodeAdmission != oldNodeAdmission {
				t.Fatal("failed reload replaced the persistent node admission runtime")
			}
			if harness.services.OutboundRecovery != oldOutboundRecovery {
				t.Fatal("failed reload replaced durable outbox recovery")
			}
			if harness.services.HealthServer != oldHealthServer {
				t.Fatal("failed reload replaced the persistent health listener")
			}
			if harness.services.CronService == nil || harness.services.MediaStore == nil ||
				harness.services.DeviceService == nil {
				t.Fatal("failed reload did not restore the complete service generation")
			}
		})
	}
}

func TestConfigReloadCommitsPreparedGeneration(t *testing.T) {
	useRestartRuntimeGOOS(t, "linux")
	harness := newGatewayReloadHarness(t)
	oldProvider := harness.provider
	oldHeartbeat := harness.services.HeartbeatService
	next := *harness.config
	next.Heartbeat.Interval++
	next.Gateway.SafeRestart = config.GatewaySafeRestartConfig{
		Enabled:        true,
		ServiceManager: "systemd-user",
		Service:        "mintclaw-main.service",
	}

	if err := handleConfigReload(
		context.Background(),
		harness.loop,
		&next,
		&harness.provider,
		harness.services,
		harness.bus,
		true,
		false,
		serviceShutdownTimeout,
	); err != nil {
		t.Fatalf("handleConfigReload() error = %v", err)
	}
	if harness.loop.GetConfig() != &next {
		t.Fatal("successful reload did not publish the prepared config")
	}
	if harness.provider == oldProvider {
		t.Fatal("successful reload did not publish the prepared provider")
	}
	if harness.services.HeartbeatService == oldHeartbeat || !harness.services.HeartbeatService.IsRunning() {
		t.Fatal("successful reload did not commit the prepared heartbeat generation")
	}
	if _, ok := harness.loop.GetRegistry().GetDefaultAgent().Tools.Get("gateway_restart"); !ok {
		t.Fatal("successful reload did not publish the enabled runtime tool")
	}
}

func TestReloadRollbackResultRequiresRestartOnlyWhenRollbackFails(t *testing.T) {
	t.Parallel()

	cause := errors.New("reload failed")
	if result := reloadRollbackResult(cause, nil); !errors.Is(result, cause) ||
		errors.Is(result, errGatewayReloadRestartRequired) {
		t.Fatalf("clean rollback result = %v, want only original cause", result)
	}

	rollbackErr := errors.New("restore failed")
	result := reloadRollbackResult(cause, []error{rollbackErr})
	if !errors.Is(result, cause) || !errors.Is(result, rollbackErr) {
		t.Fatalf("rollback result = %v, want both underlying errors", result)
	}
	if !errors.Is(result, errGatewayReloadRestartRequired) {
		t.Fatalf("rollback result = %v, want restart-required marker", result)
	}
}
