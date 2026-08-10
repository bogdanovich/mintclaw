package channels

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

// runTTLJanitor periodically scans the typingStops, placeholders, and stream
// tombstone maps and evicts entries that have exceeded their TTL. This prevents
// memory accumulation when outbound paths fail to trigger preSend (e.g. LLM errors).
func (l *ChannelLifecycle) runTTLJanitor(ctx context.Context, stream *StreamCoordinator) {
	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			stream.expireInteractions(now)
			stream.expireStreams(now)
		}
	}
}

func (m *Manager) GetChannel(name string) (Channel, bool) {
	return m.lifecycle.channel(name)
}

func (m *Manager) GetStatus() map[string]any {
	return m.lifecycle.status()
}

func (m *Manager) GetEnabledChannels() []string {
	return m.lifecycle.enabledChannels()
}

// Reload updates the config reference without restarting channels.
// This is used when channel config hasn't changed but other parts of the config have.
func (m *Manager) Reload(ctx context.Context, cfg *config.Config) error {
	return m.lifecycle.reload(ctx, cfg, m, m.deliveryRuntime(), m.streamCoordinator())
}

func (l *ChannelLifecycle) reload(
	ctx context.Context,
	cfg *config.Config,
	host channelLifecycleHost,
	delivery *DeliveryRuntime,
	stream *StreamCoordinator,
) error {
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()

	l.mu.Lock()
	locked := true
	defer func() {
		if locked {
			l.mu.Unlock()
		}
	}()

	// Save old config so we can revert on error.
	oldConfig := l.config

	// Update config early: initChannel uses l.config via factory(l.config, host.lifecycleBus()).
	l.config = cfg

	desiredHashes := toChannelHashes(cfg)
	list := make(map[string]string, len(desiredHashes))
	for name, hash := range desiredHashes {
		list[name] = hash
	}
	if l.restartRequired == nil {
		l.restartRequired = make(map[string]string)
	}
	added, removed := compareChannels(l.channelHashes, list)
	inactiveChanged := make(map[string]Channel)
	changed, added, removed := splitChangedChannels(added, removed)
	for _, name := range changed {
		currentHash, ok := l.channelHashes[name]
		if !ok {
			added = append(added, name)
			continue
		}
		if _, ok := l.channels[name]; !ok {
			added = append(added, name)
			continue
		}
		if !delivery.hasActiveWorker(name) {
			logger.InfoCF("channels", "Recreating inactive changed channel", map[string]any{
				"channel": name,
			})
			inactiveChanged[name] = l.channels[name]
			added = append(added, name)
			continue
		}
		l.restartRequired[name] = list[name]
		list[name] = currentHash
		logger.WarnCF("channels", "Channel config changed; restart required", map[string]any{
			"channel": name,
		})
	}
	for name := range l.restartRequired {
		desiredHash, ok := desiredHashes[name]
		if !ok || desiredHash == l.channelHashes[name] {
			delete(l.restartRequired, name)
		}
	}

	deferFuncs := make([]func(), 0, len(removed)+len(added))
	for _, name := range removed {
		channel := l.channels[name]
		deferFuncs = append(deferFuncs, func() {
			l.unregisterChannelDuringTransition(host, delivery, stream, name)
			if channel == nil {
				return
			}
			logger.InfoCF("channels", "Stopping channel", map[string]any{
				"channel": name,
			})
			if err := channel.Stop(ctx); err != nil {
				logger.ErrorCF("channels", "Error stopping channel", map[string]any{
					"channel": name,
					"error":   err.Error(),
				})
			}
		})
	}
	cc, err := toChannelConfig(cfg, added)
	if err != nil {
		logger.ErrorC("channels", fmt.Sprintf("toChannelConfig error: %v", err))
		l.config = oldConfig
		return err
	}
	err = l.initChannels(host, cc)
	if err != nil {
		logger.ErrorC("channels", fmt.Sprintf("initChannels error: %v", err))
		l.config = oldConfig
		return err
	}
	for name, oldChannel := range inactiveChanged {
		if l.channels[name] == oldChannel {
			err := fmt.Errorf("replacement channel %s was not initialized", name)
			logger.ErrorCF("channels", "Failed to initialize replacement channel", map[string]any{
				"channel": name,
				"error":   err.Error(),
			})
			l.config = oldConfig
			return err
		}
		stream.retireToolFeedbackChannel(ctx, name)
		if err := oldChannel.Stop(ctx); err != nil {
			logger.ErrorCF("channels", "Error stopping inactive changed channel", map[string]any{
				"channel": name,
				"error":   err.Error(),
			})
		}
	}
	for _, name := range added {
		channel := l.channels[name]
		logger.InfoCF("channels", "Starting channel", map[string]any{
			"channel": name,
		})
		if err := channel.Start(ctx); err != nil {
			logger.ErrorCF("channels", "Failed to start channel", map[string]any{
				"channel": name,
				"error":   err.Error(),
			})
			host.publishChannelEvent(
				runtimeevents.KindChannelLifecycleStartFailed,
				name,
				runtimeevents.Scope{Channel: name},
				runtimeevents.SeverityError,
				ChannelLifecyclePayload{Type: l.channelType(name), Error: err.Error()},
			)
			continue
		}
		// Lazily create worker only after channel starts successfully
		channelType := name
		if l.config != nil {
			if bc := l.config.Channels.Get(name); bc != nil && bc.Type != "" {
				channelType = bc.Type
			}
		}
		l.installDeliveryOwnerLocked(ctx, delivery, name, channel, channelType)
		host.publishChannelEvent(
			runtimeevents.KindChannelLifecycleStarted,
			name,
			runtimeevents.Scope{Channel: name},
			runtimeevents.SeverityInfo,
			ChannelLifecyclePayload{Type: channelType},
		)
		deferFuncs = append(deferFuncs, func() {
			l.registerChannelDuringTransition(host, name, channel)
		})
	}

	// Commit hashes only on full success.
	l.channelHashes = list
	if cfg != nil {
		stream.configureToolFeedback(
			ToolFeedbackAnimatorConfig{
				AnimationInterval: cfg.Agents.Defaults.GetToolFeedbackAnimationInterval(),
				MinEditInterval:   cfg.Agents.Defaults.GetToolFeedbackEditMinInterval(),
			},
			cfg.Agents.Defaults.IsToolFeedbackSeparateMessagesEnabled(),
		)
	}
	l.mu.Unlock()
	locked = false
	func() {
		defer func() {
			if r := recover(); r != nil {
				logger.ErrorCF("channels", "channel registration action panic recovered",
					map[string]any{
						"panic": fmt.Sprintf("%v", r),
						"stack": string(debug.Stack()),
					})
			}
		}()
		for _, f := range deferFuncs {
			f()
		}
	}()
	return nil
}

func (m *Manager) RegisterChannel(name string, channel Channel) {
	m.lifecycle.registerChannel(m, name, channel)
}

func (l *ChannelLifecycle) registerChannel(
	publisher channelLifecycleEventPublisher,
	name string,
	channel Channel,
) {
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	l.registerChannelDuringTransition(publisher, name, channel)
}

func (l *ChannelLifecycle) registerChannelDuringTransition(
	publisher channelLifecycleEventPublisher,
	name string,
	channel Channel,
) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.channels[name] = channel
	l.shutdownComplete = false
	if l.mux != nil {
		l.registerChannelHTTPHandler(publisher, name, channel)
	}
}

func (m *Manager) UnregisterChannel(name string) {
	m.lifecycle.unregisterChannel(m, m.deliveryRuntime(), m.streamCoordinator(), name)
}

func (l *ChannelLifecycle) unregisterChannel(
	publisher channelLifecycleEventPublisher,
	delivery *DeliveryRuntime,
	stream *StreamCoordinator,
	name string,
) {
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	l.unregisterChannelDuringTransition(publisher, delivery, stream, name)
}

func (l *ChannelLifecycle) unregisterChannelDuringTransition(
	publisher channelLifecycleEventPublisher,
	delivery *DeliveryRuntime,
	stream *StreamCoordinator,
	name string,
) {
	l.mu.Lock()
	ch := l.channels[name]
	if ch != nil && l.mux != nil {
		l.unregisterChannelHTTPHandler(publisher, name, ch)
	}
	owner := delivery.owner(name)
	if owner == nil {
		delete(l.channels, name)
	}
	l.mu.Unlock()

	if owner != nil {
		owner.CloseDeliveryAndWait()
	}
	stream.retireToolFeedbackChannel(context.Background(), name)

	l.mu.Lock()
	delivery.removeIfMatches(name, owner)
	if ch != nil && l.channels[name] == ch {
		delete(l.channels, name)
	}
	l.mu.Unlock()
}
