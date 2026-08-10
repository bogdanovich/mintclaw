package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type gatewayCleanupFunc func(context.Context) error

type gatewayCleanupStep struct {
	name string
	run  gatewayCleanupFunc
}

type gatewayStartupStage string

const (
	gatewayStartupCronStarted              gatewayStartupStage = "cron_started"
	gatewayStartupHeartbeatStarted         gatewayStartupStage = "heartbeat_started"
	gatewayStartupMediaStarted             gatewayStartupStage = "media_started"
	gatewayStartupOutboundOutboxOpened     gatewayStartupStage = "outbound_outbox_opened"
	gatewayStartupOutboundOutboxRecovered  gatewayStartupStage = "outbound_outbox_recovered"
	gatewayStartupChannelsCreated          gatewayStartupStage = "channels_created"
	gatewayStartupNodeAdmissionReady       gatewayStartupStage = "node_admission_ready"
	gatewayStartupNodeToolsReady           gatewayStartupStage = "node_tools_ready"
	gatewayStartupBrowserToolsReady        gatewayStartupStage = "browser_tools_ready"
	gatewayStartupBrowserRuntimeReady      gatewayStartupStage = "browser_runtime_ready"
	gatewayStartupChannelsStarted          gatewayStartupStage = "channels_started"
	gatewayStartupOutboundRecoveryStarted  gatewayStartupStage = "outbound_recovery_started"
	gatewayStartupVoiceRuntimeStarted      gatewayStartupStage = "voice_runtime_started"
	gatewayStartupDeviceRuntimeInitialized gatewayStartupStage = "device_runtime_initialized"
)

type gatewayStartupHooks struct {
	afterStage func(gatewayStartupStage, *services) error
	onRegister func(string)
	onCleanup  func(string)
}

// gatewayStartupTransaction owns resources until startup commits. Rollback is
// deliberately gateway-specific so lifecycle policy stays close to its owner.
type gatewayStartupTransaction struct {
	steps      []gatewayCleanupStep
	committed  bool
	onRegister func(string)
	onCleanup  func(string)
}

func (tx *gatewayStartupTransaction) add(name string, cleanup gatewayCleanupFunc) {
	if tx == nil || cleanup == nil {
		return
	}
	tx.steps = append(tx.steps, gatewayCleanupStep{name: name, run: cleanup})
	if tx.onRegister != nil {
		tx.onRegister(name)
	}
}

func (tx *gatewayStartupTransaction) ownProvider(provider providers.LLMProvider) {
	tx.add("provider", func(context.Context) error {
		if stateful, ok := provider.(providers.StatefulProvider); ok {
			stateful.Close()
		}
		return nil
	})
}

func (tx *gatewayStartupTransaction) ownMessageBus(msgBus *bus.MessageBus) {
	tx.add("message bus", func(context.Context) error {
		msgBus.Close()
		return nil
	})
}

func (tx *gatewayStartupTransaction) ownAgent(agentLoop *agent.AgentLoop) {
	tx.add("agent", func(context.Context) error {
		agentLoop.Close()
		return nil
	})
}

func (tx *gatewayStartupTransaction) ownAgentRun(
	agentLoop *agent.AgentLoop,
	cancel context.CancelFunc,
	done <-chan struct{},
) {
	tx.add("agent loop", func(cleanupCtx context.Context) error {
		cancel()
		agentLoop.Stop()
		select {
		case <-done:
			return nil
		case <-cleanupCtx.Done():
			return cleanupCtx.Err()
		}
	})
}

func (tx *gatewayStartupTransaction) commit() {
	if tx == nil {
		return
	}
	tx.committed = true
	tx.steps = nil
}

func (tx *gatewayStartupTransaction) rollback(timeout time.Duration) error {
	if tx == nil || tx.committed || len(tx.steps) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cleanupErrors := make([]error, 0)
	for index := len(tx.steps) - 1; index >= 0; index-- {
		step := tx.steps[index]
		if err := step.run(ctx); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("cleanup %s: %w", step.name, err))
		}
		if tx.onCleanup != nil {
			tx.onCleanup(step.name)
		}
	}
	tx.steps = nil

	return errors.Join(cleanupErrors...)
}
