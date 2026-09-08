package agent

import (
	"errors"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/media"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type mediaOwnerBinder interface {
	BindOwner(ref string, owner media.MediaOwner) error
}

func bindInboundMediaOwnerForTarget(
	resolver mediaResolver,
	target *inboundDispatchTarget,
	msg bus.InboundMessage,
) error {
	if !hasOpaqueMediaRefs(msg.Media) || resolver == nil {
		return nil
	}
	owner, err := inboundMediaOwnerForTarget(target, msg)
	if err != nil {
		return err
	}
	return bindMediaOwnerRefs(resolver, owner, msg.Media)
}

func bindNodeFileMediaOwner(
	resolver mediaResolver,
	ts *turnState,
	refs []string,
) error {
	if len(refs) == 0 || resolver == nil || ts == nil || ts.agent == nil ||
		ts.agent.Tools == nil || !ts.agent.Tools.HasRegistered("nodes_upload") {
		return nil
	}
	return bindTurnMediaOwner(resolver, ts, refs)
}

func bindTurnMediaOwner(
	resolver mediaResolver,
	ts *turnState,
	refs []string,
) error {
	if !hasOpaqueMediaRefs(refs) || resolver == nil || ts == nil || ts.agent == nil {
		return nil
	}
	owner, err := nodeFileMediaOwnerForTurn(ts)
	if err != nil {
		return err
	}
	return bindMediaOwnerRefs(resolver, owner, refs)
}

func bindMediaOwnerRefs(resolver mediaResolver, owner media.MediaOwner, refs []string) error {
	binder, ok := resolver.(mediaOwnerBinder)
	if !ok {
		return errors.New("media store does not support durable owner binding")
	}
	var bindErr error
	for _, ref := range refs {
		if !strings.HasPrefix(strings.TrimSpace(ref), "media://") {
			continue
		}
		if err := binder.BindOwner(ref, owner); err != nil {
			bindErr = errors.Join(bindErr, err)
		}
	}
	return bindErr
}

func hasOpaqueMediaRefs(refs []string) bool {
	for _, ref := range refs {
		if strings.HasPrefix(strings.TrimSpace(ref), "media://") {
			return true
		}
	}
	return false
}

func inboundMediaOwnerForTarget(
	target *inboundDispatchTarget,
	msg bus.InboundMessage,
) (media.MediaOwner, error) {
	if target == nil || target.Agent == nil {
		return media.MediaOwner{}, errors.New("inbound media owner is unavailable")
	}
	actorID := strings.TrimSpace(msg.Context.ActorID)
	if actorID == "" {
		actorID = strings.TrimSpace(msg.Context.SenderID)
	}
	if actorID == "" {
		actorID = target.Agent.ID
	}
	routeSession := strings.TrimSpace(target.Allocation.RouteScopeKey)
	if routeSession == "" {
		routeSession = strings.TrimSpace(target.SessionKey)
	}
	effectiveSession := strings.TrimSpace(target.SessionKey)
	if effectiveSession == "" {
		effectiveSession = routeSession
	}
	return media.NewMediaOwner(
		target.Agent.Workspace,
		target.Agent.ID,
		actorID,
		routeSession,
		effectiveSession,
		msg.Context.Channel,
		msg.Context.ChatID,
		originTopicID(&msg.Context),
	)
}

func nodeFileMediaOwnerForTurn(ts *turnState) (media.MediaOwner, error) {
	if ts == nil || ts.agent == nil {
		return media.MediaOwner{}, errors.New("turn media owner is unavailable")
	}
	actorID := ""
	topicID := ""
	if inbound := ts.opts.Dispatch.InboundContext; inbound != nil {
		actorID = strings.TrimSpace(inbound.ActorID)
		if actorID == "" {
			actorID = strings.TrimSpace(inbound.SenderID)
		}
		topicID = originTopicID(inbound)
	}
	if actorID == "" {
		actorID = strings.TrimSpace(ts.opts.Dispatch.SenderID())
	}
	if actorID == "" {
		actorID = ts.agent.ID
	}
	routeSession := strings.TrimSpace(ts.opts.Dispatch.RouteSessionKey)
	if routeSession == "" {
		routeSession = strings.TrimSpace(ts.opts.Dispatch.SessionKey)
	}
	effectiveSession := strings.TrimSpace(ts.opts.Dispatch.SessionKey)
	if effectiveSession == "" {
		effectiveSession = routeSession
	}
	return media.NewMediaOwner(
		ts.workspace,
		ts.agent.ID,
		actorID,
		routeSession,
		effectiveSession,
		ts.channel,
		ts.chatID,
		topicID,
	)
}

func projectNodeFileMediaAttachments(
	messages []providers.Message,
	ts *turnState,
	refs []string,
	resolver mediaResolver,
) []providers.Message {
	if len(messages) == 0 || len(refs) == 0 || resolver == nil || ts == nil ||
		ts.agent == nil || ts.agent.Tools == nil ||
		!ts.agent.Tools.HasRegistered("nodes_upload") {
		return messages
	}
	allowed := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if strings.HasPrefix(strings.TrimSpace(ref), "media://") {
			allowed[ref] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return messages
	}
	projected := append([]providers.Message(nil), messages...)
	for index := range projected {
		if projected[index].Role != "user" || len(projected[index].Media) == 0 {
			continue
		}
		for _, ref := range projected[index].Media {
			if _, ok := allowed[ref]; !ok || providerAttachmentHasRef(projected[index].Attachments, ref) {
				continue
			}
			attachments := buildProviderAttachments(resolver, []string{ref})
			if len(attachments) == 1 {
				projected[index].Attachments = append(projected[index].Attachments, attachments[0])
			}
		}
	}
	return projected
}

func providerAttachmentHasRef(attachments []providers.Attachment, ref string) bool {
	for _, attachment := range attachments {
		if attachment.Ref == ref {
			return true
		}
	}
	return false
}
