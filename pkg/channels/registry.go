package channels

import (
	"fmt"
	"sync"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

// ChannelFactory constructs the configured channel instance named by its
// channel_list map key. Each channel subpackage registers its factories in init.
type ChannelFactory func(channelName string, cfg *config.Config, bus *bus.MessageBus) (Channel, error)

var (
	factoriesMu sync.RWMutex
	factories   = map[string]ChannelFactory{}
)

// RegisterFactory registers a named channel factory. Called from subpackage init() functions.
func RegisterFactory(name string, f ChannelFactory) {
	factoriesMu.Lock()
	defer factoriesMu.Unlock()
	factories[name] = f
}

// RegisterTypedFactory registers a factory whose settings were decoded by the
// config boundary. Adapters with additional construction inputs use RegisterFactory.
//
// Usage:
//
//	func init() {
//	    channels.RegisterTypedFactory(config.ChannelTelegram, NewTelegramChannel)
//	}
func RegisterTypedFactory[S any, C Channel](
	channelType string,
	ctor func(bc *config.Channel, settings *S, bus *bus.MessageBus) (C, error),
) {
	RegisterFactory(channelType, func(channelName string, cfg *config.Config, b *bus.MessageBus) (Channel, error) {
		bc := cfg.Channels[channelName]
		if bc == nil {
			return nil, fmt.Errorf("channel %q: config not found", channelName)
		}
		decoded, err := bc.GetDecoded()
		if err != nil {
			return nil, fmt.Errorf("channel %q: failed to decode settings: %w", channelName, err)
		}
		settings, ok := decoded.(*S)
		if !ok {
			return nil, fmt.Errorf("channel %q: expected %T settings, got %T", channelName, (*S)(nil), decoded)
		}
		channel, err := ctor(bc, settings, b)
		if err != nil {
			return nil, err
		}
		return channel, nil
	})
}

// getFactory looks up a channel factory by name.
func getFactory(name string) (ChannelFactory, bool) {
	factoriesMu.RLock()
	defer factoriesMu.RUnlock()
	f, ok := factories[name]
	return f, ok
}

// GetRegisteredFactoryNames returns a slice of all registered channel factory names.
func GetRegisteredFactoryNames() []string {
	factoriesMu.RLock()
	defer factoriesMu.RUnlock()
	names := make([]string, 0, len(factories))
	for name := range factories {
		names = append(names, name)
	}
	return names
}
