package mintclaw

import (
	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func init() {
	channels.RegisterFactory(
		config.ChannelMintClaw,
		func(channelName, channelType string, cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
			bc := cfg.Channels[channelName]
			decoded, err := bc.GetDecoded()
			if err != nil {
				return nil, err
			}
			c, ok := decoded.(*config.MintClawSettings)
			if !ok {
				return nil, channels.ErrSendFailed
			}
			return NewMintClawChannel(bc, c, b)
		},
	)
	channels.RegisterFactory(
		config.ChannelMintClawClient,
		func(channelName, channelType string, cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
			bc := cfg.Channels[channelName]
			decoded, err := bc.GetDecoded()
			if err != nil {
				return nil, err
			}
			c, ok := decoded.(*config.MintClawClientSettings)
			if !ok {
				return nil, channels.ErrSendFailed
			}
			return NewMintClawClientChannel(bc, c, b)
		},
	)
}
