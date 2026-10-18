package toolshared

import (
	"context"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/identity"
)

// CanonicalInboundActorID derives identity only from authenticated inbound
// context, never from model tool arguments.
func CanonicalInboundActorID(ctx context.Context) string {
	inbound := ToolInboundContext(ctx)
	channel := strings.ToLower(strings.TrimSpace(inbound.Channel))
	actorID := strings.TrimSpace(inbound.ActorID)
	if actorID == "" {
		actorID = strings.TrimSpace(inbound.SenderID)
	}
	if channel == "" || actorID == "" {
		return ""
	}
	if platform, id, ok := identity.ParseCanonicalID(
		actorID,
	); ok &&
		strings.EqualFold(strings.TrimSpace(platform), channel) {
		return identity.BuildCanonicalID(channel, id)
	}
	return identity.BuildCanonicalID(channel, actorID)
}
