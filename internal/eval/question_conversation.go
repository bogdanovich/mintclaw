package eval

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

var errQuestionEvidence = errors.New("eval: ordinary question evidence unavailable")

type questionConversation struct {
	task, home, workspace, sessionID, agent string
	started, cancelAt                       time.Time
	filter                                  *strings.Replacer
	facts                                   map[string]string
	prompt                                  Event
	waiting                                 interactions.Record
	cancellation                            *QuestionCancellationEvidence
}

func (domain *questionConversation) initialTask() string { return domain.task }

func (domain *questionConversation) request(
	ctx context.Context, action Action, prompt Event,
) (protocol.MintClawMessage, bool, error) {
	if prompt.Kind != "question" {
		return protocol.MintClawMessage{}, false, nil
	}
	if action.Kind == "clarify" || action.Kind == "back" || action.Kind == "skip" || action.Kind == "not_applicable" {
		return protocol.MintClawMessage{}, false, errQuestionEvidence
	}
	if err := domain.observe(ctx, prompt); err != nil {
		return protocol.MintClawMessage{}, false, err
	}
	if action.Kind == "cancel" {
		domain.cancelAt = time.Now()
	}
	return protocol.MintClawMessage{}, false, nil
}

func (domain *questionConversation) observe(ctx context.Context, prompt Event) error {
	if prompt.Kind != "question" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		record, err := domain.record(prompt.InteractionID)
		if errors.Is(err, ErrPrivateEvidence) {
			return err
		}
		if err == nil {
			matched, matchErr := domain.matchesPrompt(record, prompt)
			if errors.Is(matchErr, ErrPrivateEvidence) {
				return matchErr
			}
			if matched {
				domain.prompt, domain.waiting = prompt, record
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errQuestionEvidence
		case <-ticker.C:
		}
	}
}

func (domain *questionConversation) matchesPrompt(record interactions.Record, prompt Event) (bool, error) {
	return domain.matchesPromptKind(record, prompt, interactions.KindQuestion, "request_user_input")
}

func (domain *questionConversation) matchesPromptKind(
	record interactions.Record, prompt Event, kind interactions.Kind, tool string,
) (bool, error) {
	if record.ProtectedAnswer != nil {
		return false, nil
	}
	return domain.matchesPromptIdentity(record, prompt, kind, tool)
}

// Protected consumers add their own namespace/target checks; ordinary questions
// and approvals continue to reject every protected binding above.
func (domain *questionConversation) matchesPromptIdentity(
	record interactions.Record, prompt Event, kind interactions.Kind, tool string,
) (bool, error) {
	route := record.Route
	now := time.Now().UnixMilli()
	if record.ID != prompt.InteractionID || record.ShortID != prompt.InteractionShortID ||
		record.Kind != kind || record.Status != interactions.StatusWaiting ||
		record.Origin.ToolName != tool ||
		record.CreatedAt < domain.started.UnixMilli() || record.CreatedAt > now || record.ExpiresAt <= now ||
		record.UpdatedAt > now || record.Origin.ExecutionContext == nil ||
		record.Origin.ExecutionContext.MessageID != prompt.RequestID ||
		prompt.AgentID != domain.agent || route.AgentID != domain.agent ||
		prompt.SessionKey == "" || route.SessionKey != prompt.SessionKey ||
		route.RouteSessionKey != "" && route.RouteSessionKey != prompt.SessionKey ||
		route.Channel != "mintclaw" || route.ChatID != "mintclaw:"+domain.sessionID ||
		route.ChatType != "direct" || route.SenderID != "mintclaw-user" ||
		route.AccountID != "" || route.TopicID != "" || route.SpaceID != "" || route.SpaceType != "" {
		return false, nil
	}
	matchedScope := false
	for _, scope := range prompt.TraceScopes {
		matchedScope = matchedScope || scope.Complete() && scope.Workspace == domain.workspace &&
			scope.TurnID == record.Origin.TurnID
	}
	if !matchedScope || !validID(record.PromptDeliveryID) {
		return false, nil
	}
	data, err := domain.read(
		domain.workspace,
		"state/outbox/"+record.PromptDeliveryID+".json",
		outbox.MaxInspectionBytes,
	)
	if err != nil {
		return false, err
	}
	defer clear(data)
	intent, err := outbox.InspectIntent(data, record.PromptDeliveryID)
	return err == nil && intent.Status == outbox.StatusDelivered && intent.OwnerWorkspace == domain.workspace &&
		intent.Identity.Channel == route.Channel && intent.Identity.ChatID == route.ChatID &&
		intent.Identity.SessionKey == route.SessionKey && intent.Message != nil &&
		intent.Message.AgentID == route.AgentID && intent.Message.SessionKey == route.SessionKey &&
		intent.Message.Metadata.InteractionID == record.ID && intent.Message.Metadata.InteractionShortID == record.ShortID &&
		intent.Message.Metadata.InteractionKind == string(kind) &&
		intent.Message.Metadata.InteractionControls == bus.OutboundInteractionControlsPrompt &&
		slices.Contains(intent.Message.TraceScopes, events.NewTraceScope(domain.workspace, record.Origin.TurnID)) &&
		slices.Contains(intent.PlatformMessageIDs, prompt.MessageID) && !intent.CreatedAt.Before(domain.started), nil
}

func (domain *questionConversation) observeCancellation(_ context.Context, prompt, reply Event) error {
	return domain.observeCancellationKind(prompt, reply, interactions.KindQuestion)
}

func (domain *questionConversation) observeCancellationKind(prompt, reply Event, kind interactions.Kind) error {
	if domain.cancelAt.IsZero() || domain.cancellation != nil || prompt.MessageID != domain.prompt.MessageID ||
		prompt.InteractionID != domain.waiting.ID || prompt.InteractionShortID != domain.waiting.ShortID ||
		!boundedIdentity(reply.MessageID, 128) ||
		reply.AgentID != domain.agent || reply.SessionKey != domain.waiting.Route.SessionKey ||
		reply.Controls != bus.OutboundInteractionControlsRemove || reply.InteractionKind != string(kind) ||
		domain.waiting.Kind != kind {
		return errQuestionEvidence
	}
	current, err := domain.cancelledRecord(kind)
	if err != nil {
		return err
	}
	before := domain.waiting
	domain.cancellation = &QuestionCancellationEvidence{
		Version: Version, InteractionID: current.ID, ShortID: current.ShortID,
		PromptMessageID: prompt.MessageID, PromptRequestID: prompt.RequestID,
		PromptDeliveryID: current.PromptDeliveryID, CancelRequestID: reply.RequestID,
		ReplyMessageID: reply.MessageID, AgentID: current.Route.AgentID,
		SessionKey: current.Route.SessionKey, Workspace: domain.workspace,
		OriginTurnID: current.Origin.TurnID, WaitingRevision: before.Revision,
		TerminalRevision: current.Revision, ResolvedAt: current.ResolvedAt,
	}
	return nil
}

func (domain *questionConversation) cancelledRecord(kind interactions.Kind) (interactions.Record, error) {
	current, err := domain.record(domain.waiting.ID)
	if err != nil {
		return interactions.Record{}, err
	}
	before := domain.waiting
	if current.ShortID != before.ShortID || current.CreatedAt != before.CreatedAt ||
		current.PromptDeliveryID != before.PromptDeliveryID || current.Route != before.Route ||
		!reflect.DeepEqual(current.Origin, before.Origin) || !reflect.DeepEqual(current.Questions, before.Questions) ||
		current.Kind != kind || before.Kind != kind || current.ProtectedAnswer != nil || current.Answer != nil ||
		current.ApprovalAction != before.ApprovalAction || current.ApprovalConsumedAt != 0 ||
		current.Status != interactions.StatusCancelled || current.Outcome != "" ||
		current.FailureCode != "session_control_stop" || current.Revision <= before.Revision ||
		current.ResolvedAt < domain.cancelAt.UnixMilli() || current.ResolvedAt > time.Now().UnixMilli() {
		return interactions.Record{}, errQuestionEvidence
	}
	return current, nil
}

func (domain *questionConversation) conclude(_ context.Context, trial *Trial) {
	trial.Cancellation = domain.cancellation
}

func (domain *questionConversation) record(id string) (interactions.Record, error) {
	if !validID(id) {
		return interactions.Record{}, errQuestionEvidence
	}
	records, _, err := domain.records()
	if err != nil {
		return interactions.Record{}, err
	}
	for _, record := range records {
		if record.ID == id {
			return record, nil
		}
	}
	return interactions.Record{}, errQuestionEvidence
}

func (domain *questionConversation) records() ([]interactions.Record, string, error) {
	data, err := domain.read(domain.workspace, "state/interaction_registry.json", interactions.DefaultMaxBytes)
	if err != nil {
		return nil, "", err
	}
	defer clear(data)
	snapshot, err := interactions.InspectSnapshot(data)
	if err != nil {
		return nil, "", errQuestionEvidence
	}
	return snapshot.Records, digest(data), nil
}

func (domain *questionConversation) read(path, name string, maximum int64) ([]byte, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, errQuestionEvidence
	}
	defer func() { _ = root.Close() }()
	data, err := readNativeEvidenceFile(root, name, maximum)
	if err == nil {
		err = inspectNativeJSON(data, domain.facts, domain.filter)
	}
	if err != nil {
		clear(data)
		return nil, err
	}
	return data, nil
}
