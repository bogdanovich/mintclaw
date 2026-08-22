package session

import (
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/routing"
)

// Allocation contains the structured scope and current session key selected
// for a routed turn.
type Allocation struct {
	Scope         SessionScope
	RouteScopeKey string
	SessionKey    string
}

// AllocationInput contains the routing result and peer context needed to
// derive the session keys for a turn.
type AllocationInput struct {
	AgentID       string
	Context       bus.InboundContext
	SessionPolicy routing.SessionPolicy
}

// AllocateRouteSession maps a route decision onto a structured scope and the
// current opaque session-key format.
func AllocateRouteSession(input AllocationInput) Allocation {
	scope := buildSessionScope(input)
	routeScopeKey := BuildSessionKey(scope)
	scope.RouteScopeKey = routeScopeKey
	return Allocation{
		Scope:         scope,
		RouteScopeKey: routeScopeKey,
		SessionKey:    routeScopeKey,
	}
}

func buildSessionScope(input AllocationInput) SessionScope {
	inbound := input.Context
	includeTopicInChatDimension := shouldPreserveTelegramForumIsolation(input)
	scope := SessionScope{
		Version:         ScopeVersionV2,
		AgentID:         routing.NormalizeAgentID(input.AgentID),
		Channel:         strings.ToLower(strings.TrimSpace(inbound.Channel)),
		Account:         routing.NormalizeAccountID(inbound.Account),
		ClientSessionID: mintClawClientSessionID(inbound),
	}
	if scope.Channel == "" {
		scope.Channel = "unknown"
	}

	dimensions := make([]string, 0, len(input.SessionPolicy.Dimensions))
	values := make(map[string]string, len(input.SessionPolicy.Dimensions))

	for _, dimension := range input.SessionPolicy.Dimensions {
		switch dimension {
		case "space":
			if spaceID := strings.TrimSpace(inbound.SpaceID); spaceID != "" {
				spaceType := strings.ToLower(strings.TrimSpace(inbound.SpaceType))
				if spaceType == "" {
					spaceType = "space"
				}
				dimensions = append(dimensions, "space")
				values["space"] = fmt.Sprintf("%s:%s", spaceType, strings.ToLower(spaceID))
			}
		case "chat":
			chatID := strings.TrimSpace(inbound.ChatID)
			if chatID == "" {
				continue
			}
			if includeTopicInChatDimension {
				if topicID := strings.TrimSpace(inbound.TopicID); topicID != "" {
					chatID = chatID + "/" + topicID
				}
			}
			chatType := strings.ToLower(strings.TrimSpace(inbound.ChatType))
			if chatType == "" {
				chatType = "direct"
			}
			dimensions = append(dimensions, "chat")
			values["chat"] = fmt.Sprintf("%s:%s", chatType, strings.ToLower(chatID))
		case "topic":
			if topicID := strings.TrimSpace(inbound.TopicID); topicID != "" {
				dimensions = append(dimensions, "topic")
				values["topic"] = "topic:" + strings.ToLower(topicID)
			}
		case "sender":
			senderID := CanonicalSessionIdentityID(
				inbound.Channel,
				inbound.SenderID,
				input.SessionPolicy.IdentityLinks,
			)
			if senderID == "" {
				continue
			}
			dimensions = append(dimensions, "sender")
			values["sender"] = senderID
		}
	}

	if len(dimensions) > 0 {
		scope.Dimensions = dimensions
		scope.Values = values
	}

	return scope
}

func mintClawClientSessionID(inbound bus.InboundContext) string {
	if !strings.EqualFold(strings.TrimSpace(inbound.Channel), "mintclaw") {
		return ""
	}
	const chatPrefix = "mintclaw:"
	chatID := strings.TrimSpace(inbound.ChatID)
	if !strings.HasPrefix(chatID, chatPrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(chatID, chatPrefix))
}

func shouldPreserveTelegramForumIsolation(input AllocationInput) bool {
	inbound := input.Context
	if !strings.EqualFold(strings.TrimSpace(inbound.Channel), "telegram") {
		return false
	}
	if strings.TrimSpace(inbound.TopicID) == "" {
		return false
	}
	for _, dimension := range input.SessionPolicy.Dimensions {
		if strings.EqualFold(strings.TrimSpace(dimension), "topic") {
			return false
		}
	}
	return true
}
