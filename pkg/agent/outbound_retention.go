package agent

import (
	"context"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/config"
)

// This immutable marker exists only around the ordinary native final publish.
// Tools, interim messages, async results and interaction-bound publications do
// not inherit it. The exact durable acknowledgement seals its source later.
type nativeFinalPublication struct {
	sourceID, workspace, channel, chatID, sessionKey string
}

type nativeFinalPublicationKey struct{}

func (al *AgentLoop) withNativeFinalPublication(
	ctx context.Context, msg bus.InboundMessage, target *continuationTarget,
) context.Context {
	transaction := outboundTransactionFromContext(ctx)
	if transaction == nil || msg.SpoolID == "" || transaction.sourceID != msg.SpoolID ||
		transaction.bindDelivery != nil || transaction.validate != nil || target == nil ||
		msg.Context.Channel != target.Channel || msg.Context.ChatID != target.ChatID ||
		len(msg.Media) != 0 || !msg.Context.Interaction.IsZero() || !msg.Context.DocumentIntake.IsZero() {
		return ctx
	}
	cfg := al.GetConfig()
	if cfg == nil {
		return ctx
	}
	channel := cfg.Channels.Get(msg.Context.Channel)
	// Qualify Telegram first. Eval-native channels (including aliases), other
	// adapters and unknown configurations retain full payloads until their
	// owners have an explicit evidence-completion contract.
	if channel == nil || !channel.Enabled || channel.Type != config.ChannelTelegram {
		return ctx
	}
	return context.WithValue(ctx, nativeFinalPublicationKey{}, nativeFinalPublication{
		sourceID: msg.SpoolID, workspace: target.Workspace, channel: target.Channel,
		chatID: target.ChatID, sessionKey: target.SessionKey,
	})
}

func nativeFinalPublicationMatches(ctx context.Context, workspace string, msg bus.OutboundMessage) bool {
	if ctx == nil {
		return false
	}
	owner, ok := ctx.Value(nativeFinalPublicationKey{}).(nativeFinalPublication)
	transaction := outboundTransactionFromContext(ctx)
	return ok && transaction != nil && owner.sourceID == transaction.sourceID &&
		owner.workspace == workspace && owner.channel == msg.Channel && owner.chatID == msg.ChatID &&
		owner.sessionKey == msg.SessionKey
}
