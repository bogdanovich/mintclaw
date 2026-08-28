package deltachat

import (
	"github.com/bogdanovich/mintclaw/pkg/channels"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

func init() {
	channels.RegisterTypedFactory(config.ChannelDeltaChat, NewDeltaChatChannel)
}
