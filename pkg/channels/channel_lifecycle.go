package channels

import (
	"net"
	"net/http"
	"sync"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/media"
)

// ChannelLifecycle owns channel registry, configuration, and shared HTTP state.
// Manager composes it with delivery and stream owners.
type ChannelLifecycle struct {
	mu              sync.RWMutex
	channels        map[string]Channel
	config          *config.Config
	mediaStore      media.MediaStore
	mux             *dynamicServeMux
	httpServer      *http.Server
	httpListeners   []net.Listener
	channelHashes   map[string]string
	restartRequired map[string]string
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
