package mintclaw

import (
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func init() {
	channels.RegisterTypedFactory(config.ChannelMintClaw, NewMintClawChannel)
	channels.RegisterTypedFactory(config.ChannelMintClawClient, NewMintClawClientChannel)
}
