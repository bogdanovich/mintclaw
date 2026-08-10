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
	gatewayReloadCronStarted        gatewayReloadStage = "cron_started"
	gatewayReloadHeartbeatStarted   gatewayReloadStage = "heartbeat_started"
	gatewayReloadMediaStarted       gatewayReloadStage = "media_started"
	gatewayReloadBrowserStarted     gatewayReloadStage = "browser_started"
	gatewayReloadVoiceStarted       gatewayReloadStage = "voice_started"
	gatewayReloadDeviceInitialized  gatewayReloadStage = "device_initialized"
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
	generation = &gatewayReloadGeneration{services: next, cleanup: cleanup}
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
	cleanup.add("cron service", func(context.Context) error {
		next.CronService.Stop()
		return nil
	})
	if err = next.CronService.Start(); err != nil {
		return nil, fmt.Errorf("start cron service: %w", err)
	}
	if err = hooks.checkpoint(gatewayReloadCronStarted); err != nil {
		return nil, err
	}

	next.HeartbeatService = heartbeat.NewHeartbeatService(
		cfg.WorkspacePath(),
		cfg.Heartbeat.Interval,
		cfg.Heartbeat.Enabled,
	)
	next.HeartbeatService.SetBus(msgBus)
	next.HeartbeatService.SetHandler(createHeartbeatHandler(al))
	cleanup.add("heartbeat service", func(context.Context) error {
		next.HeartbeatService.Stop()
		return nil
	})
	if err = next.HeartbeatService.Start(); err != nil {
		return nil, fmt.Errorf("start heartbeat service: %w", err)
	}
	if err = hooks.checkpoint(gatewayReloadHeartbeatStarted); err != nil {
		return nil, err
	}

	mediaStore, err := newWorkspaceMediaStore(cfg)
	if err != nil {
		return nil, fmt.Errorf("prepare media store: %w", err)
	}
	mediaStore.Start()
	next.MediaStore = mediaStore
	cleanup.add("media store", func(context.Context) error {
		mediaStore.Stop()
		return nil
	})
	if err = hooks.checkpoint(gatewayReloadMediaStarted); err != nil {
		return nil, err
	}

	if err = setupBrowserRuntime(ctx, cfg, next); err != nil {
		return nil, fmt.Errorf("prepare browser runtime: %w", err)
	}
	cleanup.add("browser runtime", func(cleanupCtx context.Context) error {
		return closeBrowserRuntime(cleanupCtx, next)
	})
	if err = hooks.checkpoint(gatewayReloadBrowserStarted); err != nil {
		return nil, err
	}

	generation.transcriber = asr.DetectTranscriber(cfg)
	if generation.transcriber != nil {
		voiceCtx, cancelVoice := context.WithCancel(context.Background())
		next.VoiceAgentCancel = cancelVoice
		asr.NewAgent(msgBus, generation.transcriber).Start(voiceCtx)
		cleanup.add("voice runtime", func(context.Context) error {
			cancelVoice()
			return nil
		})
	}
	if err = hooks.checkpoint(gatewayReloadVoiceStarted); err != nil {
		return nil, err
	}

	next.DeviceService = devices.NewService(devices.Config{
		Enabled:    cfg.Devices.Enabled,
		MonitorUSB: cfg.Devices.MonitorUSB,
	}, state.NewManager(cfg.WorkspacePath()))
	next.DeviceService.SetBus(msgBus)
	cleanup.add("device service", func(context.Context) error {
		next.DeviceService.Stop()
		return nil
	})
	if err = next.DeviceService.Start(context.Background()); err != nil {
		logger.WarnCF("device", "Failed to prepare device service", map[string]any{"error": err.Error()})
	}
	if err = hooks.checkpoint(gatewayReloadDeviceInitialized); err != nil {
		return nil, err
	}

	return generation, nil
}

func (generation *gatewayReloadGeneration) commit(
	persistent *services,
	al *agent.AgentLoop,
) {
	next := generation.services
	persistent.CronService = next.CronService
	persistent.HeartbeatService = next.HeartbeatService
	persistent.MediaStore = next.MediaStore
	persistent.DeviceService = next.DeviceService
	persistent.VoiceAgentCancel = next.VoiceAgentCancel

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
	logChannelVoiceCapabilities(
		persistent.ChannelManager,
		generation.transcriber != nil,
		tts.DetectTTS(al.GetConfig()) != nil,
	)
	generation.cleanup.commit()
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
