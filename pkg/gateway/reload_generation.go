package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/agent"
	"github.com/bogdanovich/mintclaw/pkg/audio/asr"
	"github.com/bogdanovich/mintclaw/pkg/audio/tts"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/devices"
	"github.com/bogdanovich/mintclaw/pkg/heartbeat"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/state"
)

var errGatewayReloadRestartRequired = errors.New("gateway restart required after incomplete config reload transition")

type gatewayReloadStage string

const (
	gatewayReloadAgentPrepared      gatewayReloadStage = "agent_prepared"
	gatewayReloadCronPrepared       gatewayReloadStage = "cron_prepared"
	gatewayReloadHeartbeatPrepared  gatewayReloadStage = "heartbeat_prepared"
	gatewayReloadMediaPrepared      gatewayReloadStage = "media_prepared"
	gatewayReloadBrowserPrepared    gatewayReloadStage = "browser_prepared"
	gatewayReloadVoicePrepared      gatewayReloadStage = "voice_prepared"
	gatewayReloadDevicePrepared     gatewayReloadStage = "device_prepared"
	gatewayReloadChannelsReconciled gatewayReloadStage = "channels_reconciled"
	gatewayReloadNodesReconciled    gatewayReloadStage = "nodes_reconciled"
)

type gatewayReloadHooks struct {
	afterStage func(gatewayReloadStage) error
}

func (hooks gatewayReloadHooks) checkpoint(stage gatewayReloadStage) error {
	if hooks.afterStage == nil {
		return nil
	}
	return hooks.afterStage(stage)
}

type gatewayReloadGeneration struct {
	services    *services
	transcriber asr.Transcriber
	cleanup     *gatewayStartupTransaction
	msgBus      *bus.MessageBus
}

func prepareReloadGeneration(
	ctx context.Context,
	cfg *config.Config,
	al *agent.AgentLoop,
	prepared *agent.PreparedConfigReload,
	persistent *services,
	msgBus *bus.MessageBus,
	hooks gatewayReloadHooks,
) (generation *gatewayReloadGeneration, prepareErr error) {
	next := &services{
		ChannelManager:   persistent.ChannelManager,
		OutboundRecovery: persistent.OutboundRecovery,
		NodeAdmission:    persistent.NodeAdmission,
		HealthServer:     persistent.HealthServer,
		manualReloadChan: persistent.manualReloadChan,
		authToken:        persistent.authToken,
	}
	cleanup := &gatewayStartupTransaction{}
	generation = &gatewayReloadGeneration{services: next, cleanup: cleanup, msgBus: msgBus}
	defer func() {
		if prepareErr != nil {
			prepareErr = errors.Join(prepareErr, cleanup.rollback(serviceShutdownTimeout))
			generation = nil
		}
	}()
	registerTool := al.RegisterTool
	if prepared != nil {
		registerTool = prepared.RegisterTool
	}
	execTimeout := time.Duration(cfg.Tools.Cron.ExecTimeoutMinutes) * time.Minute
	var err error
	next.CronService, err = setupCronToolWithRegistrar(
		al,
		msgBus,
		cfg.WorkspacePath(),
		cfg.Agents.Defaults.RestrictToWorkspace,
		execTimeout,
		cfg,
		registerTool,
	)
	if err != nil {
		return nil, fmt.Errorf("prepare cron service: %w", err)
	}
	cleanup.add("cron service", func(cleanupCtx context.Context) error {
		return next.CronService.StopAndDrain(cleanupCtx)
	})
	if err = next.CronService.Prepare(); err != nil {
		return nil, fmt.Errorf("prepare cron service store: %w", err)
	}
	if err = hooks.checkpoint(gatewayReloadCronPrepared); err != nil {
		return nil, err
	}

	next.HeartbeatService = heartbeat.NewHeartbeatService(
		cfg.WorkspacePath(),
		cfg.Heartbeat.Interval,
		cfg.Heartbeat.Enabled,
	)
	next.HeartbeatService.SetBus(msgBus)
	next.HeartbeatService.SetHandler(createHeartbeatHandler(al))
	cleanup.add("heartbeat service", func(cleanupCtx context.Context) error {
		return next.HeartbeatService.StopAndDrain(cleanupCtx)
	})
	if err = hooks.checkpoint(gatewayReloadHeartbeatPrepared); err != nil {
		return nil, err
	}

	mediaStore, err := newWorkspaceMediaStore(cfg)
	if err != nil {
		return nil, fmt.Errorf("prepare media store: %w", err)
	}
	next.MediaStore = mediaStore
	cleanup.add("media store", func(context.Context) error {
		mediaStore.Stop()
		return nil
	})
	if err = hooks.checkpoint(gatewayReloadMediaPrepared); err != nil {
		return nil, err
	}

	if err = setupBrowserRuntime(ctx, cfg, next); err != nil {
		return nil, fmt.Errorf("prepare browser runtime: %w", err)
	}
	cleanup.add("browser runtime", func(cleanupCtx context.Context) error {
		return closeBrowserRuntime(cleanupCtx, next)
	})
	if err = hooks.checkpoint(gatewayReloadBrowserPrepared); err != nil {
		return nil, err
	}

	generation.transcriber = asr.DetectTranscriber(cfg)
	if err = hooks.checkpoint(gatewayReloadVoicePrepared); err != nil {
		return nil, err
	}

	stateManager, err := state.NewManagerChecked(cfg.WorkspacePath())
	if err != nil {
		return nil, fmt.Errorf("prepare device state: %w", err)
	}
	next.DeviceService = devices.NewService(devices.Config{
		Enabled:    cfg.Devices.Enabled,
		MonitorUSB: cfg.Devices.MonitorUSB,
	}, stateManager)
	next.DeviceService.SetBus(msgBus)
	cleanup.add("device service", func(context.Context) error {
		next.DeviceService.Stop()
		return nil
	})
	if err = hooks.checkpoint(gatewayReloadDevicePrepared); err != nil {
		return nil, err
	}

	return generation, nil
}

func (generation *gatewayReloadGeneration) commit(
	persistent *services,
	al *agent.AgentLoop,
) error {
	next := generation.services
	persistent.CronService = next.CronService
	persistent.HeartbeatService = next.HeartbeatService
	persistent.MediaStore = next.MediaStore
	persistent.DeviceService = next.DeviceService
	persistent.VoiceAgentCancel = nil
	persistent.VoiceAgentDone = nil

	next.browserMu.Lock()
	persistent.browserMu.Lock()
	persistent.Browser = next.Browser
	next.Browser = nil
	persistent.browserMu.Unlock()
	next.browserMu.Unlock()

	persistent.ChannelManager.SetMediaStore(persistent.MediaStore)
	al.SetChannelManager(persistent.ChannelManager)
	al.SetMediaStore(persistent.MediaStore)
	al.SetTranscriber(generation.transcriber)
	if starter, ok := persistent.MediaStore.(interface{ Start() }); ok {
		starter.Start()
	}
	if err := persistent.HeartbeatService.Start(); err != nil {
		return fmt.Errorf("activate heartbeat service: %w", err)
	}
	if err := persistent.DeviceService.Start(context.Background()); err != nil {
		logger.WarnCF("device", "Failed to activate device service", map[string]any{"error": err.Error()})
	}
	if generation.transcriber != nil {
		persistent.VoiceAgentCancel, persistent.VoiceAgentDone = startVoiceAgent(
			generation.msgBus,
			generation.transcriber,
		)
	}
	persistent.CronService.Activate()
	logChannelVoiceCapabilities(
		persistent.ChannelManager,
		generation.transcriber != nil,
		tts.DetectTTS(al.GetConfig()) != nil,
	)
	generation.cleanup.commit()
	return nil
}

func (generation *gatewayReloadGeneration) rollback() error {
	if generation == nil {
		return nil
	}
	return generation.cleanup.rollback(serviceShutdownTimeout)
}

func reloadRollbackResult(cause error, rollbackErrors []error) error {
	if len(rollbackErrors) == 0 {
		return cause
	}
	return errors.Join(append([]error{cause, errGatewayReloadRestartRequired}, rollbackErrors...)...)
}
