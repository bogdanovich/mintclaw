package bus

import (
	"strconv"
	"strings"
)

// NormalizeInboundMessage normalizes the canonical inbound context.
func NormalizeInboundMessage(msg InboundMessage) InboundMessage {
	msg.Context = normalizeInboundContext(msg.Context)
	return msg
}

func NormalizeObservedMessage(msg ObservedMessage) ObservedMessage {
	msg.Context = normalizeInboundContext(msg.Context)
	msg.Reason = strings.TrimSpace(msg.Reason)
	return msg
}

func (ctx InboundContext) isZero() bool {
	return ctx.Channel == "" &&
		ctx.Account == "" &&
		ctx.ChatID == "" &&
		ctx.ChatType == "" &&
		ctx.TopicID == "" &&
		ctx.SpaceID == "" &&
		ctx.SpaceType == "" &&
		ctx.SenderID == "" &&
		ctx.ActorID == "" &&
		ctx.MessageID == "" &&
		ctx.OriginID == "" &&
		ctx.OriginType == "" &&
		ctx.SourceRef == "" &&
		ctx.ClientSessionID == "" &&
		!ctx.Mentioned &&
		ctx.ReplyToMessageID == "" &&
		ctx.ReplyToSenderID == "" &&
		len(ctx.ReplyHandles) == 0 &&
		len(ctx.Raw) == 0
}

func NormalizeInboundContext(ctx InboundContext) InboundContext {
	return normalizeInboundContext(ctx)
}

func normalizeInboundContext(ctx InboundContext) InboundContext {
	ctx.Channel = strings.TrimSpace(ctx.Channel)
	ctx.Account = strings.TrimSpace(ctx.Account)
	ctx.ChatID = strings.TrimSpace(ctx.ChatID)
	ctx.ChatType = normalizeKind(ctx.ChatType)
	ctx.TopicID = strings.TrimSpace(ctx.TopicID)
	ctx.SpaceID = strings.TrimSpace(ctx.SpaceID)
	ctx.SpaceType = normalizeKind(ctx.SpaceType)
	ctx.SenderID = strings.TrimSpace(ctx.SenderID)
	ctx.ActorID = strings.TrimSpace(ctx.ActorID)
	if ctx.ActorID == "" {
		ctx.ActorID = ctx.SenderID
	}
	ctx.MessageID = strings.TrimSpace(ctx.MessageID)
	ctx.OriginID = strings.TrimSpace(ctx.OriginID)
	ctx.OriginType = normalizeKind(ctx.OriginType)
	ctx.SourceRef = strings.TrimSpace(ctx.SourceRef)
	if ctx.SourceRef == "" {
		ctx.SourceRef = defaultSourceRef(ctx)
	}
	ctx.ClientSessionID = strings.TrimSpace(ctx.ClientSessionID)
	ctx.ReplyToMessageID = strings.TrimSpace(ctx.ReplyToMessageID)
	ctx.ReplyToSenderID = strings.TrimSpace(ctx.ReplyToSenderID)
	if !ctx.ReceivedAt.IsZero() {
		ctx.ReceivedAt = ctx.ReceivedAt.UTC()
	}
	ctx.Relation.Kind = InboundRelationKind(normalizeKind(string(ctx.Relation.Kind)))
	ctx.MediaGroup.ID = strings.TrimSpace(ctx.MediaGroup.ID)
	ctx.MediaGroup.MessageIDs = append([]string(nil), ctx.MediaGroup.MessageIDs...)
	for index := range ctx.MediaGroup.MessageIDs {
		ctx.MediaGroup.MessageIDs[index] = strings.TrimSpace(ctx.MediaGroup.MessageIDs[index])
	}
	ctx.ReplyHandles = cloneStringMap(ctx.ReplyHandles)
	ctx.Raw = cloneStringMap(ctx.Raw)
	ctx.Interaction = normalizeInboundInteractionProjection(ctx.Interaction)
	migrateLegacyMintClawClientSessionID(&ctx)
	migrateLegacyInboundInteractionProjection(&ctx)
	return ctx
}

func migrateLegacyMintClawClientSessionID(ctx *InboundContext) {
	if ctx == nil || !strings.EqualFold(ctx.Channel, "mintclaw") || len(ctx.Raw) == 0 {
		return
	}
	if ctx.ClientSessionID == "" {
		ctx.ClientSessionID = strings.TrimSpace(ctx.Raw[legacyInboundClientSessionIDKey])
	}
	delete(ctx.Raw, legacyInboundClientSessionIDKey)
	if len(ctx.Raw) == 0 {
		ctx.Raw = nil
	}
}

func normalizeInboundInteractionProjection(
	projection InboundInteractionProjection,
) InboundInteractionProjection {
	projection.Choice = InboundInteractionChoice(normalizeKind(string(projection.Choice)))
	projection.Response = strings.TrimSpace(projection.Response)
	projection.ResponseCandidate = strings.TrimSpace(projection.ResponseCandidate)
	projection.ShortID = strings.TrimSpace(projection.ShortID)
	projection.ResponseMessageID = strings.TrimSpace(projection.ResponseMessageID)
	if projection.OptionIndex != nil {
		optionIndex := *projection.OptionIndex
		projection.OptionIndex = &optionIndex
	}
	return projection
}

func migrateLegacyInboundInteractionProjection(ctx *InboundContext) {
	if ctx == nil || len(ctx.Raw) == 0 {
		return
	}
	if ctx.Interaction.IsZero() {
		ctx.Interaction = InboundInteractionProjection{
			Choice: InboundInteractionChoice(
				normalizeKind(ctx.Raw[legacyInboundInteractionChoiceKey]),
			),
			Response: strings.TrimSpace(
				ctx.Raw[legacyInboundInteractionResponseKey],
			),
			ResponseCandidate: strings.TrimSpace(
				ctx.Raw[legacyInboundInteractionResponseCandidateKey],
			),
			ShortID: strings.TrimSpace(
				ctx.Raw[legacyInboundInteractionShortIDKey],
			),
			Unresolved: strings.TrimSpace(ctx.Raw[legacyInboundInteractionResponseErrorKey]) != "",
			ResponseMessageID: strings.TrimSpace(
				ctx.Raw[legacyInboundInteractionResponseMessageIDKey],
			),
		}
		if optionIndex, err := strconv.Atoi(strings.TrimSpace(
			ctx.Raw[legacyInboundInteractionOptionIndexKey],
		)); err == nil && optionIndex >= 0 {
			ctx.Interaction.OptionIndex = &optionIndex
		}
	}
	delete(ctx.Raw, legacyInboundInteractionChoiceKey)
	delete(ctx.Raw, legacyInboundInteractionResponseKey)
	delete(ctx.Raw, legacyInboundInteractionResponseCandidateKey)
	delete(ctx.Raw, legacyInboundInteractionShortIDKey)
	delete(ctx.Raw, legacyInboundInteractionResponseErrorKey)
	delete(ctx.Raw, legacyInboundInteractionOptionIndexKey)
	delete(ctx.Raw, legacyInboundInteractionResponseMessageIDKey)
	if len(ctx.Raw) == 0 {
		ctx.Raw = nil
	}
}

func defaultSourceRef(ctx InboundContext) string {
	channel := strings.TrimSpace(ctx.Channel)
	chatID := strings.TrimSpace(ctx.ChatID)
	messageID := strings.TrimSpace(ctx.MessageID)
	if channel == "" || chatID == "" || messageID == "" {
		return ""
	}
	return channel + ":" + chatID + ":" + messageID
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}

	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func normalizeKind(kind string) string {
	return strings.ToLower(strings.TrimSpace(kind))
}
