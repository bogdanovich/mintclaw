package channels

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
	runtimeevents "github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/health"
	"github.com/bogdanovich/mintclaw/pkg/logger"
	"github.com/bogdanovich/mintclaw/pkg/media"
)

// ChannelLifecycle owns channel registry, configuration, and shared HTTP state.
// Manager composes it with delivery and stream owners.
type ChannelLifecycle struct {
	mu               sync.RWMutex
	transitionMu     sync.Mutex
	channels         map[string]Channel
	config           *config.Config
	mediaStore       media.MediaStore
	mux              *dynamicServeMux
	httpServer       *http.Server
	httpListeners    []net.Listener
	httpServing      bool
	channelHashes    map[string]string
	restartRequired  map[string]string
	shutdownRunning  bool
	shutdownComplete bool
}

func newChannelLifecycle(cfg *config.Config, store media.MediaStore) *ChannelLifecycle {
	return &ChannelLifecycle{
		channels:        make(map[string]Channel),
		config:          cfg,
		mediaStore:      store,
		channelHashes:   make(map[string]string),
		restartRequired: make(map[string]string),
	}
}

type channelLifecycleEventPublisher interface {
	publishChannelEvent(
		kind runtimeevents.Kind,
		channelName string,
		scope runtimeevents.Scope,
		severity runtimeevents.Severity,
		payload any,
	)
}

type channelLifecycleHost interface {
	channelLifecycleEventPublisher
	lifecycleBus() *bus.MessageBus
	lifecyclePlaceholderRecorder() PlaceholderRecorder
}

func (l *ChannelLifecycle) channel(name string) (Channel, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	channel, ok := l.channels[name]
	return channel, ok
}

func (l *ChannelLifecycle) storeChannel(name string, channel Channel) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.channels[name] = channel
	l.shutdownComplete = false
}

func (l *ChannelLifecycle) channelHash(name string) string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.channelHashes[name]
}

func (l *ChannelLifecycle) shutdownInProgress() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.shutdownRunning
}

func (l *ChannelLifecycle) splitOnMarker() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.config != nil && l.config.Agents.Defaults.SplitOnMarker
}

func (l *ChannelLifecycle) responseFooterEnabled() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.config != nil && l.config.Agents.Defaults.IsResponseFooterEnabled()
}

func (l *ChannelLifecycle) setMediaStore(store media.MediaStore) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.mediaStore = store
	for _, ch := range l.channels {
		if setter, ok := ch.(mediaStoreSetter); ok {
			setter.SetMediaStore(store)
		}
	}
}

func (l *ChannelLifecycle) setInitialHashes(hashes map[string]string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.channelHashes = hashes
}

func (l *ChannelLifecycle) status() map[string]any {
	l.mu.RLock()
	defer l.mu.RUnlock()

	status := make(map[string]any, len(l.channels))
	for name, channel := range l.channels {
		channelStatus := map[string]any{
			"enabled": true,
			"running": channel.IsRunning(),
		}
		if _, ok := l.restartRequired[name]; ok {
			channelStatus["restart_required"] = true
			channelStatus["restart_reason"] = "channel config changed"
		}
		status[name] = channelStatus
	}
	return status
}

func (l *ChannelLifecycle) enabledChannels() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	names := make([]string, 0, len(l.channels))
	for name := range l.channels {
		names = append(names, name)
	}
	return names
}

func (l *ChannelLifecycle) setupHTTPServer(
	publisher channelLifecycleEventPublisher,
	listeners []net.Listener,
	addr string,
	healthServer *health.Server,
) {
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()

	l.mu.Lock()
	defer l.mu.Unlock()
	l.shutdownComplete = false

	l.mux = newDynamicServeMux()
	if healthServer != nil {
		healthServer.RegisterOnMux(l.mux)
	}
	l.registerHTTPHandlersLocked(publisher)
	l.httpServer = &http.Server{
		Addr:         addr,
		Handler:      l.mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	l.httpListeners = append([]net.Listener(nil), listeners...)
	l.httpServing = false
}

func (l *ChannelLifecycle) registerHTTPHandler(pattern string, handler http.Handler) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.mux == nil {
		return errors.New("shared HTTP server is not configured")
	}
	if pattern == "" || handler == nil {
		return errors.New("HTTP handler pattern and implementation are required")
	}
	if err := l.mux.TryHandle(pattern, handler); err != nil {
		return fmt.Errorf("register HTTP handler %q: %w", pattern, err)
	}
	return nil
}

func (l *ChannelLifecycle) replaceHTTPHandler(pattern string, handler http.Handler) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.mux == nil {
		return errors.New("shared HTTP server is not configured")
	}
	if pattern == "" || handler == nil {
		return errors.New("HTTP handler pattern and implementation are required")
	}
	if err := l.mux.Replace(pattern, handler); err != nil {
		return fmt.Errorf("replace HTTP handler %q: %w", pattern, err)
	}
	return nil
}

func (l *ChannelLifecycle) unregisterHTTPHandler(pattern string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.mux != nil {
		l.mux.Unhandle(pattern)
	}
}

func (l *ChannelLifecycle) registerHTTPHandlersLocked(publisher channelLifecycleEventPublisher) {
	for name, ch := range l.channels {
		l.registerChannelHTTPHandler(publisher, name, ch)
	}
}

func (l *ChannelLifecycle) registerChannelHTTPHandler(
	publisher channelLifecycleEventPublisher,
	name string,
	ch Channel,
) {
	if wh, ok := ch.(WebhookHandler); ok {
		l.mux.Handle(wh.WebhookPath(), wh)
		publisher.publishChannelEvent(
			runtimeevents.KindChannelWebhookRegistered,
			name,
			runtimeevents.Scope{Channel: name},
			runtimeevents.SeverityInfo,
			ChannelLifecyclePayload{Type: l.channelType(name)},
		)
		logger.InfoCF("channels", "Webhook handler registered", map[string]any{
			"channel": name,
			"path":    wh.WebhookPath(),
		})
	}
	if hc, ok := ch.(HealthChecker); ok {
		l.mux.HandleFunc(hc.HealthPath(), hc.HealthHandler)
		logger.InfoCF("channels", "Health endpoint registered", map[string]any{
			"channel": name,
			"path":    hc.HealthPath(),
		})
	}
}

func (l *ChannelLifecycle) unregisterChannelHTTPHandler(
	publisher channelLifecycleEventPublisher,
	name string,
	ch Channel,
) {
	if wh, ok := ch.(WebhookHandler); ok {
		l.mux.Unhandle(wh.WebhookPath())
		publisher.publishChannelEvent(
			runtimeevents.KindChannelWebhookUnregistered,
			name,
			runtimeevents.Scope{Channel: name},
			runtimeevents.SeverityInfo,
			ChannelLifecyclePayload{Type: l.channelType(name)},
		)
		logger.InfoCF("channels", "Webhook handler unregistered", map[string]any{
			"channel": name,
			"path":    wh.WebhookPath(),
		})
	}
	if hc, ok := ch.(HealthChecker); ok {
		l.mux.Unhandle(hc.HealthPath())
		logger.InfoCF("channels", "Health endpoint unregistered", map[string]any{
			"channel": name,
			"path":    hc.HealthPath(),
		})
	}
}

func (l *ChannelLifecycle) channelType(name string) string {
	if l.config != nil {
		if channelConfig := l.config.Channels.Get(name); channelConfig != nil && channelConfig.Type != "" {
			return channelConfig.Type
		}
	}
	return name
}
