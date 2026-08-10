package channels

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"sort"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/health"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

// initChannel is a helper that looks up a factory by type name and creates the channel.
// typeName is the channel type used for factory lookup (e.g., "telegram").
// channelName is the config map key used as the channel's runtime name (e.g., "my_telegram").
func (l *ChannelLifecycle) initChannel(host channelLifecycleHost, typeName, channelName string) {
	f, ok := getFactory(typeName)
	if !ok {
		logger.WarnCF("channels", "Factory not registered", map[string]any{
			"channel": channelName,
			"type":    typeName,
		})
		return
	}
	logger.DebugCF("channels", "Attempting to initialize channel", map[string]any{
		"channel": channelName,
		"type":    typeName,
	})
	ch, err := f(channelName, typeName, l.config, host.lifecycleBus())
	if err != nil {
		logger.ErrorCF("channels", "Failed to initialize channel", map[string]any{
			"channel": channelName,
			"type":    typeName,
			"error":   err.Error(),
		})
	} else {
		// Inject MediaStore if channel supports it
		if l.mediaStore != nil {
			if setter, ok := ch.(mediaStoreSetter); ok {
				setter.SetMediaStore(l.mediaStore)
			}
		}
		// Inject PlaceholderRecorder if channel supports it
		if setter, ok := ch.(interface{ SetPlaceholderRecorder(r PlaceholderRecorder) }); ok {
			setter.SetPlaceholderRecorder(host.lifecyclePlaceholderRecorder())
		}
		// Inject owner reference so BaseChannel.HandleMessage can auto-trigger typing/reaction
		if setter, ok := ch.(interface{ SetOwner(ch Channel) }); ok {
			setter.SetOwner(ch)
		}
		l.channels[channelName] = ch
		host.publishChannelEvent(
			runtimeevents.KindChannelLifecycleInitialized,
			channelName,
			runtimeevents.Scope{Channel: channelName},
			runtimeevents.SeverityInfo,
			ChannelLifecyclePayload{Type: typeName},
		)
		logger.InfoCF("channels", "Channel enabled successfully", map[string]any{
			"channel": channelName,
			"type":    typeName,
		})
	}
}

func (l *ChannelLifecycle) getChannelConfigAndEnabled(channelName string) (*config.Channel, bool) {
	bc, ok := l.config.Channels[channelName]
	if !ok || bc == nil {
		return nil, false
	}
	if !bc.Enabled {
		return bc, false
	}

	// Use Type to determine the config struct for validation.
	// The map key (channelName) is the config key, which may differ from the type.
	channelType := bc.Type
	if channelType == "" {
		channelType = channelName
	}

	// Settings have already been decoded by InitChannelList, so we just need to
	// type-assert and check the relevant fields.
	decoded, err := bc.GetDecoded()
	if err != nil {
		return bc, false
	}
	//nolint:revive
	switch settings := decoded.(type) {
	case *config.WhatsAppSettings:
		if channelType == config.ChannelWhatsApp {
			return bc, settings.BridgeURL != ""
		}
		return bc, channelType == config.ChannelWhatsAppNative && settings.UseNative
	case *config.MatrixSettings:
		return bc, settings.Homeserver != "" && settings.UserID != "" && settings.AccessToken.String() != ""
	case *config.WeComSettings:
		return bc, settings.BotID != "" && settings.Secret.String() != ""
	case *config.MintClawClientSettings:
		return bc, settings.URL != ""
	case *config.DingTalkSettings:
		return bc, settings.ClientID != ""
	case *config.SlackSettings:
		return bc, settings.BotToken.String() != ""
	case *config.WeixinSettings:
		return bc, settings.Token.String() != ""
	case *config.MintClawSettings:
		return bc, settings.Token.String() != ""
	case *config.IRCSettings:
		return bc, settings.Server != ""
	case *config.LINESettings:
		return bc, settings.ChannelAccessToken.String() != ""
	case *config.OneBotSettings:
		return bc, settings.WSUrl != ""
	case *config.QQSettings:
		return bc, settings.AppSecret.String() != ""
	case *config.TelegramSettings:
		return bc, settings.Token.String() != ""
	case *config.FeishuSettings:
		return bc, settings.AppSecret.String() != ""
	case *config.MaixCamSettings:
		return bc, true
	case *config.TeamsWebhookSettings:
		return bc, true
	case *config.SlackWebhookSettings:
		return bc, true
	case *config.DiscordSettings:
		return bc, settings.Token.String() != ""
	case *config.VKSettings:
		return bc, settings.GroupID != 0 && settings.Token.String() != ""
	case *config.MQTTSettings:
		return bc, settings.Broker != "" && settings.AgentID != ""
	}

	return bc, bc.Enabled
}

// initChannels initializes all enabled channels based on the configuration.
// It iterates config entries and uses bc.Type to look up the appropriate factory.
func (l *ChannelLifecycle) initChannels(host channelLifecycleHost, channels *config.ChannelsConfig) error {
	logger.InfoC("channels", "Initializing channel manager")

	for name, bc := range *channels {
		if !bc.Enabled {
			continue
		}
		_, ready := l.getChannelConfigAndEnabled(name)
		if !ready {
			continue
		}
		typeName := bc.Type
		if typeName == "" {
			typeName = name
		}
		l.initChannel(host, typeName, name)
	}

	logger.InfoCF("channels", "Channel initialization completed", map[string]any{
		"enabled_channels": len(l.channels),
	})

	return nil
}

// SetupHTTPServer creates a shared HTTP server with the given listen address.
// It registers health endpoints from the health server and discovers channels
// that implement WebhookHandler and/or HealthChecker to register their handlers.
func (m *Manager) SetupHTTPServer(addr string, healthServer *health.Server) {
	m.SetupHTTPServerListeners(nil, addr, healthServer)
}

// SetupHTTPServerListeners creates a shared HTTP server on pre-opened listeners.
// When listeners is empty it falls back to Addr-based ListenAndServe behavior.
func (m *Manager) SetupHTTPServerListeners(listeners []net.Listener, addr string, healthServer *health.Server) {
	m.lifecycle.setupHTTPServer(m, listeners, addr, healthServer)
}

// RegisterHTTPHandler adds a non-channel route to the shared gateway server.
// It must be called after SetupHTTPServerListeners and rejects route collisions.
func (m *Manager) RegisterHTTPHandler(pattern string, handler http.Handler) error {
	return m.lifecycle.registerHTTPHandler(pattern, handler)
}

// ReplaceHTTPHandler atomically replaces an existing non-channel route.
func (m *Manager) ReplaceHTTPHandler(pattern string, handler http.Handler) error {
	return m.lifecycle.replaceHTTPHandler(pattern, handler)
}

// UnregisterHTTPHandler removes a non-channel route from the shared gateway server.
func (m *Manager) UnregisterHTTPHandler(pattern string) {
	m.lifecycle.unregisterHTTPHandler(pattern)
}

func (m *Manager) StartAll(ctx context.Context) error {
	return m.lifecycle.startAll(ctx, m, m.deliveryRuntime(), m.streamCoordinator())
}

func (l *ChannelLifecycle) startAll(
	ctx context.Context,
	publisher channelLifecycleEventPublisher,
	delivery *DeliveryRuntime,
	stream *StreamCoordinator,
) error {
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()

	l.mu.Lock()
	defer l.mu.Unlock()
	l.shutdownComplete = false

	if len(l.channels) == 0 {
		logger.WarnC("channels", "No channels enabled")
	}

	logger.InfoC("channels", "Starting all channels")

	dispatchCtx, dispatcherStarted := delivery.ensureDispatcher(ctx)
	failedStarts := make([]error, 0, len(l.channels))
	failedNames := make([]string, 0, len(l.channels))

	for name, channel := range l.channels {
		if delivery.hasActiveWorker(name) {
			continue
		}
		logger.InfoCF("channels", "Starting channel", map[string]any{
			"channel": name,
		})
		if err := channel.Start(ctx); err != nil {
			logger.ErrorCF("channels", "Failed to start channel", map[string]any{
				"channel": name,
				"error":   err.Error(),
			})
			publisher.publishChannelEvent(
				runtimeevents.KindChannelLifecycleStartFailed,
				name,
				runtimeevents.Scope{Channel: name},
				runtimeevents.SeverityError,
				ChannelLifecyclePayload{Type: l.channelType(name), Error: err.Error()},
			)
			failedStarts = append(failedStarts, fmt.Errorf("channel %s: %w", name, err))
			failedNames = append(failedNames, name)
			continue
		}
		// Lazily create worker only after channel starts successfully
		channelType := name
		if l.config != nil {
			if bc := l.config.Channels.Get(name); bc != nil && bc.Type != "" {
				channelType = bc.Type
			}
		}
		l.installDeliveryOwnerLocked(dispatchCtx, delivery, name, channel, channelType)
		publisher.publishChannelEvent(
			runtimeevents.KindChannelLifecycleStarted,
			name,
			runtimeevents.Scope{Channel: name},
			runtimeevents.SeverityInfo,
			ChannelLifecyclePayload{Type: channelType},
		)
	}

	if len(l.channels) > 0 && delivery.workerCount() == 0 {
		delivery.stopDispatcher()

		sort.Strings(failedNames)
		if len(failedStarts) == 0 {
			return fmt.Errorf("failed to start any enabled channels")
		}

		logger.ErrorCF("channels", "All enabled channels failed to start", map[string]any{
			"failed":          len(failedNames),
			"total":           len(l.channels),
			"failed_channels": failedNames,
		})

		return fmt.Errorf("failed to start any enabled channels: %w", errors.Join(failedStarts...))
	}

	if len(failedNames) > 0 {
		sort.Strings(failedNames)
		logger.WarnCF("channels", "Some channels failed to start", map[string]any{
			"failed":          len(failedNames),
			"started":         delivery.workerCount(),
			"total":           len(l.channels),
			"failed_channels": failedNames,
		})
	}

	// Start the dispatcher that reads from the bus and routes to workers
	if dispatcherStarted {
		go delivery.dispatchOutbound(dispatchCtx)
		go delivery.dispatchOutboundMedia(dispatchCtx)

		// Start the TTL janitor that cleans up stale typing/placeholder entries.
		go l.runTTLJanitor(dispatchCtx, stream)
	}

	// Capture the HTTP runtime while lifecycle state is locked. Shutdown may
	// clear the owner fields as soon as this transition completes.
	httpServer := l.httpServer
	httpListeners := append([]net.Listener(nil), l.httpListeners...)
	startHTTPServer := httpServer != nil && !l.httpServing
	if startHTTPServer {
		l.httpServing = true
		if len(httpListeners) > 0 {
			for _, listener := range httpListeners {
				ln := listener
				go func() {
					defer func() {
						if r := recover(); r != nil {
							logger.ErrorCF("channels", "HTTP server goroutine panic recovered",
								map[string]any{
									"addr":  ln.Addr().String(),
									"panic": fmt.Sprintf("%v", r),
									"stack": string(debug.Stack()),
								})
						}
					}()
					logger.InfoCF("channels", "Shared HTTP server listening", map[string]any{
						"addr": ln.Addr().String(),
					})
					if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
						logger.FatalCF("channels", "Shared HTTP server error", map[string]any{
							"addr":  ln.Addr().String(),
							"error": err.Error(),
						})
					}
				}()
			}
		} else {
			go func() {
				defer func() {
					if r := recover(); r != nil {
						logger.ErrorCF("channels", "HTTP server goroutine panic recovered",
							map[string]any{
								"addr":  httpServer.Addr,
								"panic": fmt.Sprintf("%v", r),
								"stack": string(debug.Stack()),
							})
					}
				}()
				logger.InfoCF("channels", "Shared HTTP server listening", map[string]any{
					"addr": httpServer.Addr,
				})
				if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					logger.FatalCF("channels", "Shared HTTP server error", map[string]any{
						"error": err.Error(),
					})
				}
			}()
		}
	}

	logger.InfoCF("channels", "Channel startup completed", map[string]any{
		"started": delivery.workerCount(),
		"failed":  len(failedNames),
		"total":   len(l.channels),
	})
	return nil
}

func (m *Manager) StopAll(ctx context.Context) error {
	return m.lifecycle.stopAll(ctx, m, m.deliveryRuntime(), m.streamCoordinator())
}

func (l *ChannelLifecycle) stopAll(
	ctx context.Context,
	publisher channelLifecycleEventPublisher,
	delivery *DeliveryRuntime,
	stream *StreamCoordinator,
) error {
	type channelStopTarget struct {
		name        string
		channel     Channel
		channelType string
	}

	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()

	l.mu.Lock()
	if l.shutdownComplete {
		l.mu.Unlock()
		return nil
	}
	l.shutdownRunning = true
	defer func() {
		l.mu.Lock()
		l.shutdownRunning = false
		l.shutdownComplete = true
		l.mu.Unlock()
	}()
	httpServer := l.httpServer
	l.httpServer = nil
	l.httpListeners = nil
	l.httpServing = false

	delivery.stopDispatcher()

	deliveries := delivery.snapshot()

	channels := make([]channelStopTarget, 0, len(l.channels))
	for name, channel := range l.channels {
		channels = append(channels, channelStopTarget{
			name:        name,
			channel:     channel,
			channelType: l.channelType(name),
		})
	}
	l.mu.Unlock()

	logger.InfoC("channels", "Stopping all channels")
	stopErrors := make([]error, 0)

	// Shutdown shared HTTP server first
	if httpServer != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.ErrorCF("channels", "Shared HTTP server shutdown error", map[string]any{
				"error": err.Error(),
			})
			stopErrors = append(stopErrors, fmt.Errorf("shutdown shared HTTP server: %w", err))
		}
	}

	// Close delivery queues and wait for accepted work to drain.
	for _, owner := range deliveries {
		owner.CloseDeliveryAndWait()
	}
	stream.stopToolFeedback()

	// Stop all channels
	for _, target := range channels {
		logger.InfoCF("channels", "Stopping channel", map[string]any{
			"channel": target.name,
		})
		if err := target.channel.Stop(ctx); err != nil {
			logger.ErrorCF("channels", "Error stopping channel", map[string]any{
				"channel": target.name,
				"error":   err.Error(),
			})
			stopErrors = append(stopErrors, fmt.Errorf("stop channel %s: %w", target.name, err))
			continue
		}
		publisher.publishChannelEvent(
			runtimeevents.KindChannelLifecycleStopped,
			target.name,
			runtimeevents.Scope{Channel: target.name},
			runtimeevents.SeverityInfo,
			ChannelLifecyclePayload{Type: target.channelType},
		)
	}

	logger.InfoC("channels", "All channels stopped")
	return errors.Join(stopErrors...)
}
