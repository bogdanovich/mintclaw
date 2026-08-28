package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/netbind"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
	"github.com/bogdanovich/mintclaw/pkg/state"
)

type trackedStartupProvider struct {
	startupBlockedProvider
	closed atomic.Bool
}

func (provider *trackedStartupProvider) Close() {
	provider.closed.Store(true)
}

type failingStopStartupChannel struct {
	channels.BaseChannel
	stopErr error
}

func (channel *failingStopStartupChannel) Start(context.Context) error {
	return nil
}

func (channel *failingStopStartupChannel) Stop(context.Context) error {
	return channel.stopErr
}

func (channel *failingStopStartupChannel) DeliverText(
	context.Context,
	[]bus.OutboundMessage,
) channels.DeliveryResult[bus.OutboundMessage] {
	return channels.SuccessfulDelivery[bus.OutboundMessage](nil)
}

func TestGatewayServiceCompositionRejectsSplitStateOwnership(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	msgBus := bus.NewMessageBus()
	t.Cleanup(msgBus.Close)
	runtimeState := state.NewManager(cfg.WorkspacePath())
	loop := agent.NewAgentLoop(
		cfg,
		msgBus,
		&startupBlockedProvider{reason: "not used"},
		agent.WithStateManager(runtimeState),
	)
	t.Cleanup(loop.Close)

	_, err := setupAndStartServicesWithHooks(
		context.Background(),
		cfg,
		loop,
		msgBus,
		state.NewManager(t.TempDir()),
		"test-token",
		netbind.OpenResult{},
		gatewayStartupHooks{},
	)
	if err == nil || !strings.Contains(err.Error(), "must share one state manager") {
		t.Fatalf("setupAndStartServicesWithHooks() error = %v, want split-owner rejection", err)
	}
}

func TestServiceShutdownReportsChannelAndNodeDrainFailures(t *testing.T) {
	channelErr := errors.New("channel stop failed")
	nodeErr := errors.New("node drain failed")
	messageBus := bus.NewMessageBus()
	t.Cleanup(messageBus.Close)
	manager, err := channels.NewManager(config.DefaultConfig(), messageBus, media.NewFileMediaStore())
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	manager.RegisterChannel("failing", &failingStopStartupChannel{stopErr: channelErr})
	services := &services{
		ChannelManager: manager,
		NodeAdmission: &nodeAdmissionRuntime{
			handler: &closeErrorNodeAdmissionHandler{err: nodeErr},
		},
	}

	err = stopAndCleanupServices(services, time.Second, false)
	if !errors.Is(err, channelErr) || !errors.Is(err, nodeErr) {
		t.Fatalf("stopAndCleanupServices() error = %v, want channel and node failures", err)
	}
}

func TestGatewayStartupTransactionRollsBackInReverseOrderAndJoinsErrors(t *testing.T) {
	t.Parallel()

	firstErr := errors.New("first cleanup failed")
	lastErr := errors.New("last cleanup failed")
	var cleaned []string
	tx := &gatewayStartupTransaction{}
	tx.add("first", func(context.Context) error {
		cleaned = append(cleaned, "first")
		return firstErr
	})
	tx.add("middle", func(context.Context) error {
		cleaned = append(cleaned, "middle")
		return nil
	})
	tx.add("last", func(context.Context) error {
		cleaned = append(cleaned, "last")
		return lastErr
	})

	err := tx.rollback(time.Second)
	if !reflect.DeepEqual(cleaned, []string{"last", "middle", "first"}) {
		t.Fatalf("cleanup order = %v, want reverse registration order", cleaned)
	}
	if !errors.Is(err, firstErr) || !errors.Is(err, lastErr) {
		t.Fatalf("rollback error = %v, want both cleanup errors", err)
	}
	if secondErr := tx.rollback(time.Second); secondErr != nil {
		t.Fatalf("second rollback error = %v, want nil", secondErr)
	}
}

func TestGatewayStartupTransactionCommitTransfersOwnership(t *testing.T) {
	t.Parallel()

	cleaned := false
	tx := &gatewayStartupTransaction{}
	tx.add("resource", func(context.Context) error {
		cleaned = true
		return nil
	})
	tx.commit()

	if err := tx.rollback(time.Second); err != nil {
		t.Fatalf("rollback after commit error = %v", err)
	}
	if cleaned {
		t.Fatal("rollback cleaned a committed resource")
	}
}

func TestGatewayProcessStartupRollbackClosesEveryOwner(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.Defaults.ContextManagerConfig = nil

	provider := &trackedStartupProvider{
		startupBlockedProvider: startupBlockedProvider{reason: "not used"},
	}
	msgBus := bus.NewMessageBus()
	al := agent.NewAgentLoop(cfg, msgBus, provider)
	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = al.Run(runCtx)
	}()

	tx := &gatewayStartupTransaction{}
	tx.ownProvider(provider)
	tx.ownMessageBus(msgBus)
	tx.ownAgent(al)
	tx.ownAgentRun(al, cancelRun, runDone)
	if err := tx.rollback(time.Second); err != nil {
		t.Fatalf("process startup rollback error = %v", err)
	}

	if !provider.closed.Load() {
		t.Fatal("stateful provider remains open after startup rollback")
	}
	if err := msgBus.PublishVoiceControl(context.Background(), bus.VoiceControl{}); !errors.Is(err, bus.ErrBusClosed) {
		t.Fatalf("message bus publish error = %v, want %v", err, bus.ErrBusClosed)
	}
	select {
	case <-runDone:
	default:
		t.Fatal("agent loop remains running after startup rollback")
	}
}

func TestServiceStartupRollbackReturnsChannelStopFailure(t *testing.T) {
	const channelType = "gateway-startup-stop-failure-test"

	stopErr := errors.New("channel stop failed")
	channels.RegisterFactory(
		channelType,
		func(string, *config.Config, *bus.MessageBus) (channels.Channel, error) {
			return &failingStopStartupChannel{stopErr: stopErr}, nil
		},
	)

	workspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.ContextManager = "none"
	cfg.Agents.Defaults.ContextManagerConfig = nil
	cfg.Channels["rollback-test"] = &config.Channel{
		Enabled:  true,
		Type:     channelType,
		Settings: config.RawNode(`{"enabled":true}`),
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	cfg.Gateway.Port = port

	msgBus := bus.NewMessageBus()
	t.Cleanup(msgBus.Close)
	runtimeState := state.NewManager(cfg.WorkspacePath())
	al := agent.NewAgentLoop(
		cfg,
		msgBus,
		&startupBlockedProvider{reason: "not used"},
		agent.WithStateManager(runtimeState),
	)
	t.Cleanup(al.Close)
	injectedErr := errors.New("startup failed after channels started")

	_, setupErr := setupAndStartServicesWithHooks(
		context.Background(),
		cfg,
		al,
		msgBus,
		runtimeState,
		"test-token",
		netbind.OpenResult{
			Listeners: []net.Listener{listener},
			BindHosts: []string{"127.0.0.1"},
			Port:      strconv.Itoa(port),
			ProbeHost: "127.0.0.1",
		},
		gatewayStartupHooks{
			afterStage: func(stage gatewayStartupStage, _ *services) error {
				if stage == gatewayStartupChannelsStarted {
					return injectedErr
				}
				return nil
			},
		},
	)
	if !errors.Is(setupErr, injectedErr) || !errors.Is(setupErr, stopErr) {
		t.Fatalf("setup error = %v, want startup and channel cleanup errors", setupErr)
	}
}

func TestSetupAndStartServicesRollsBackEveryCompletedStage(t *testing.T) {
	stages := []gatewayStartupStage{
		gatewayStartupCronStarted,
		gatewayStartupHeartbeatStarted,
		gatewayStartupMediaStarted,
		gatewayStartupOutboundOutboxOpened,
		gatewayStartupOutboundOutboxRecovered,
		gatewayStartupChannelsCreated,
		gatewayStartupNodeAdmissionReady,
		gatewayStartupNodeToolsReady,
		gatewayStartupBrowserToolsReady,
		gatewayStartupBrowserRuntimeReady,
		gatewayStartupChannelsStarted,
		gatewayStartupOutboundRecoveryStarted,
		gatewayStartupVoiceRuntimeStarted,
		gatewayStartupDeviceRuntimeInitialized,
	}

	for _, stage := range stages {
		stage := stage
		t.Run(string(stage), func(t *testing.T) {
			workspace := t.TempDir()
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.Workspace = workspace
			cfg.Agents.Defaults.ContextManager = "none"
			cfg.Agents.Defaults.ContextManagerConfig = nil

			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("Listen() error = %v", err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			port := listener.Addr().(*net.TCPAddr).Port
			cfg.Gateway.Port = port

			msgBus := bus.NewMessageBus()
			t.Cleanup(msgBus.Close)
			runtimeState := state.NewManager(cfg.WorkspacePath())
			al := agent.NewAgentLoop(
				cfg,
				msgBus,
				&startupBlockedProvider{reason: "not used"},
				agent.WithStateManager(runtimeState),
			)
			t.Cleanup(al.Close)

			injectedErr := fmt.Errorf("injected failure after %s", stage)
			var registered []string
			var cleaned []string
			var captured *services
			_, setupErr := setupAndStartServicesWithHooks(
				context.Background(),
				cfg,
				al,
				msgBus,
				runtimeState,
				"test-token",
				netbind.OpenResult{
					Listeners: []net.Listener{listener},
					BindHosts: []string{"127.0.0.1"},
					Port:      strconv.Itoa(port),
					ProbeHost: "127.0.0.1",
				},
				gatewayStartupHooks{
					afterStage: func(current gatewayStartupStage, services *services) error {
						if current != stage {
							return nil
						}
						captured = services
						return injectedErr
					},
					onRegister: func(name string) {
						registered = append(registered, name)
					},
					onCleanup: func(name string) {
						cleaned = append(cleaned, name)
					},
				},
			)
			if !errors.Is(setupErr, injectedErr) {
				t.Fatalf("setup error = %v, want injected error", setupErr)
			}
			if captured == nil {
				t.Fatal("startup checkpoint did not expose the in-progress generation")
			}

			wantCleaned := make([]string, len(registered))
			for index := range registered {
				wantCleaned[len(registered)-1-index] = registered[index]
			}
			if !reflect.DeepEqual(cleaned, wantCleaned) {
				t.Fatalf("cleanup order = %v, want %v", cleaned, wantCleaned)
			}
			if captured.HeartbeatService != nil && captured.HeartbeatService.IsRunning() {
				t.Fatal("heartbeat remains running after startup rollback")
			}

			if stageAtOrAfter(stage, gatewayStartupOutboundOutboxOpened, stages) {
				reopened, reopenErr := outbox.OpenCoordinator(workspace)
				if reopenErr != nil {
					t.Fatalf("outbox remains owned after startup rollback: %v", reopenErr)
				}
				if closeErr := reopened.Close(); closeErr != nil {
					t.Fatalf("Close(reopened outbox) error = %v", closeErr)
				}
			}
		})
	}
}

func stageAtOrAfter(stage, threshold gatewayStartupStage, stages []gatewayStartupStage) bool {
	stageIndex := -1
	thresholdIndex := -1
	for index, candidate := range stages {
		if candidate == stage {
			stageIndex = index
		}
		if candidate == threshold {
			thresholdIndex = index
		}
	}
	return stageIndex >= thresholdIndex
}
