package agent

import (
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

const adjacentMediaFollowupWindow = 2 * time.Minute

var attachmentOnlyPlaceholders = map[string]struct{}{
	"[media only]": {},
	"[image]":      {},
	"[photo]":      {},
	"[audio]":      {},
	"[video]":      {},
	"[file]":       {},
}

type (
	InboundRelationKind    = bus.InboundRelationKind
	InboundMessageRelation = bus.InboundMessageRelation
)

const (
	InboundRelationStandalone            = bus.InboundRelationStandalone
	InboundRelationReplyToMessage        = bus.InboundRelationReplyToMessage
	InboundRelationAdjacentFollowupMedia = bus.InboundRelationAdjacentFollowupMedia
)

func classifyPromptCurrentMessageRelation(
	content string,
	media []string,
	replyToMessageID string,
	allowAdjacentMediaFollowup bool,
	history []providers.Message,
	now time.Time,
) InboundMessageRelation {
	content = strings.TrimSpace(content)
	_, placeholderOnly := attachmentOnlyPlaceholders[content]
	mediaOnly := len(media) > 0 && (content == "" || placeholderOnly)
	if !mediaOnly {
		return InboundMessageRelation{Kind: InboundRelationStandalone, MediaOnly: false}
	}
	if strings.TrimSpace(replyToMessageID) != "" {
		return InboundMessageRelation{Kind: InboundRelationReplyToMessage, MediaOnly: true}
	}
	if allowAdjacentMediaFollowup && recentUserFollowupCandidate(history, now, adjacentMediaFollowupWindow) {
		return InboundMessageRelation{Kind: InboundRelationAdjacentFollowupMedia, MediaOnly: true}
	}
	return InboundMessageRelation{Kind: InboundRelationStandalone, MediaOnly: true}
}

func recentUserFollowupCandidate(history []providers.Message, now time.Time, window time.Duration) bool {
	if len(history) == 0 || window <= 0 {
		return false
	}
	if now.IsZero() {
		return false
	}

	lastUserIdx := -1
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx < 0 {
		return false
	}

	lastUser := history[lastUserIdx]
	for i := lastUserIdx + 1; i < len(history); i++ {
		if history[i].Role == "assistant" {
			return false
		}
	}

	if lastUser.CreatedAt == nil || lastUser.CreatedAt.IsZero() {
		return false
	}
	age := now.Sub(*lastUser.CreatedAt)
	if age < 0 || age > window {
		return false
	}

	return true
}
