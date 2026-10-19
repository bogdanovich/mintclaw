package eval

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/browseraction"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

type browserInputObservation struct {
	record interactions.Record
	target browseraction.InputTargetObservation
}

func (domain *browserConversation) observeQuestion(ctx context.Context, prompt Event) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		record, err := domain.questions.record(prompt.InteractionID)
		if err == nil {
			if record.Origin.ToolName == "browser_input" || record.ProtectedAnswer != nil {
				var input *browserInputObservation
				input, err = domain.readInput(ctx, record, prompt)
				if err == nil {
					domain.input = input
					return nil
				}
			} else {
				var matched bool
				matched, err = domain.questions.matchesPrompt(record, prompt)
				if err == nil && matched {
					domain.input = nil
					domain.questions.prompt, domain.questions.waiting = prompt, record
					return nil
				}
			}
		}
		if errors.Is(err, ErrPrivateEvidence) {
			return err
		}
		select {
		case <-ctx.Done():
			return errBrowserEvidence
		case <-ticker.C:
		}
	}
}

func (domain *browserConversation) readInput(
	ctx context.Context, record interactions.Record, prompt Event,
) (*browserInputObservation, error) {
	if domain.item.Browser == nil || !slices.Contains([]string{"submit", "followup"}, domain.item.Browser.Scenario) ||
		prompt.Kind != "question" || record.ProtectedAnswer == nil ||
		record.ProtectedAnswer.Namespace != tools.BrowserProtectedAnswerNamespace ||
		record.Answer != nil || record.Outcome != "" || record.ApprovalConsumedAt != 0 ||
		len(record.Questions) != 1 || record.Questions[0].ID != "browser_value" ||
		len(record.Questions[0].Options) != 0 || record.Questions[0].MultiSelect ||
		record.Origin.ExecutionID == "" || record.Origin.ToolCallID == "" ||
		record.Origin.ExecutionContext == nil ||
		record.Origin.ExecutionContext.ClientSessionID != domain.questions.sessionID ||
		record.Origin.RuntimeActorID != "" && record.Origin.RuntimeActorID != "mintclaw:mintclaw-user" {
		return nil, errBrowserEvidence
	}
	inbound, route := record.Origin.ExecutionContext, record.Route
	if inbound.Channel != route.Channel || inbound.ChatID != route.ChatID || inbound.ChatType != route.ChatType ||
		inbound.SenderID != route.SenderID || inbound.Account != "" || inbound.TopicID != "" ||
		inbound.SpaceID != "" || inbound.SpaceType != "" {
		return nil, errBrowserEvidence
	}
	matched, err := domain.questions.matchesPromptIdentity(record, prompt, interactions.KindQuestion, "browser_input")
	if err != nil {
		return nil, err
	}
	if !matched {
		return nil, errBrowserEvidence
	}
	binding := nativeModelBinding{
		AgentID: prompt.AgentID, SessionKey: prompt.SessionKey,
		TraceScope: events.NewTraceScope(domain.questions.workspace, record.Origin.TurnID),
	}
	var selected browseraction.InputTargetObservation
	count := 0
	var replace func(string) string
	if domain.questions.filter != nil {
		replace = domain.questions.filter.Replace
	}
	reason := visitNativeTraceSnapshots(ctx, domain.target, []nativeModelBinding{binding},
		domain.questions.facts, domain.questions.filter, func(_ int, trace diagnostictrace.Trace, sha string) {
			count++
			if target, ok := projectBrowserInputTarget(trace, binding, record, sha, replace); ok {
				selected = target
			}
		})
	if reason == "raw_private_evidence" {
		return nil, ErrPrivateEvidence
	}
	if reason != "" || count != 1 || !selected.Valid() {
		return nil, errBrowserEvidence
	}
	data, err := domain.questions.read(
		domain.questions.workspace,
		"state/browser/browser.json",
		browser.DefaultFileStoreBytes,
	)
	if err != nil {
		return nil, err
	}
	defer clear(data)
	snapshot, err := browser.InspectStateSnapshot(data)
	if err != nil || !domain.inputSessionMatches(record, selected, snapshot, time.Now()) {
		return nil, errBrowserEvidence
	}
	return &browserInputObservation{record: record, target: selected}, nil
}

func (domain *browserConversation) inputSessionMatches(
	record interactions.Record, target browseraction.InputTargetObservation, state browser.StateSnapshot, now time.Time,
) bool {
	owner := browser.Owner{
		ActorID:     browser.OpaqueActorID("mintclaw:mintclaw-user"),
		AgentID:     browser.OpaqueAgentID(routing.NormalizeAgentID(record.Route.AgentID)),
		SessionKey:  browser.OpaqueSessionKey(record.Route.SessionKey),
		ExecutionID: browser.OpaqueExecutionID(record.Origin.ExecutionID),
	}
	if !target.Valid() || target.ActorID != owner.ActorID || target.AgentID != owner.AgentID ||
		target.SessionKey != owner.SessionKey || target.ExecutionID != owner.ExecutionID ||
		target.RequestID != browser.OpaqueRequestID(record.Origin.ExecutionID, record.Origin.ToolCallID) ||
		target.IssuedAt < domain.questions.started.UnixNano() || target.IssuedAt > now.UnixNano() ||
		target.IssuedAt/1_000_000 > record.CreatedAt || target.ExpiresAt <= now.UnixNano() ||
		record.ExpiresAt > target.ExpiresAt/1_000_000 ||
		target.FrameID != "" || target.ContextCatalogID != "" || target.ContextGeneration != 0 ||
		target.URLSHA256 != digest([]byte(domain.fixture.origin+domain.fixture.path)) ||
		target.OriginSHA256 != digest([]byte(domain.fixture.origin)) {
		return false
	}
	name, profile := browserEvalProfile(domain.target.config)
	policy, err := domain.target.config.Tools.Browser.PolicyRevision()
	session, found := state.Sessions[target.SessionID]
	return err == nil && found && session.Owner == owner && session.State == browser.SessionReady &&
		session.EffectiveController() == browser.ControllerAgent && session.ControllerGeneration == target.ControllerGeneration &&
		!session.DryRun && session.Target == config.BrowserDefaultTarget && session.Profile == name &&
		session.ProfileRevision == profile.Revision && session.PolicyRevision == policy &&
		session.CreatedAt >= domain.questions.started.UnixNano() && session.UpdatedAt <= now.UnixNano() &&
		session.ExpiresAt >= target.ExpiresAt && session.ExpiresAt > now.UnixNano() &&
		session.TabID == target.TabID && session.FrameID == "" && session.SnapshotID == target.SnapshotID &&
		session.SnapshotGeneration == target.SnapshotGeneration && session.SnapshotOrigin == domain.fixture.origin &&
		session.PageStateHash == target.PageStateSHA256
}

// Values leave the private fact map only at this final, freshly checked write
// boundary. Neither role prompts, reports, ordinary chat nor token decoding can
// choose a field. Native ingress independently authenticates and claims it.
func (domain *browserConversation) protectedInputRequest(
	ctx context.Context, prompt Event, handle string,
) (protocol.MintClawMessage, error) {
	if prompt.Kind != "question" {
		return protocol.MintClawMessage{}, errBrowserEvidence
	}
	if err := domain.observeQuestion(ctx, prompt); err != nil {
		return protocol.MintClawMessage{}, err
	}
	input := domain.input
	if input == nil || domain.item.Validate() != nil || domain.sentInputs[input.record.ID] ||
		len(domain.sentInputs) >= 64 {
		return protocol.MintClawMessage{}, errBrowserEvidence
	}
	admitted := false
	for _, field := range domain.item.Browser.Fields {
		label := ""
		switch field.Name {
		case "name":
			label = "Name"
		case "note":
			label = "Note"
		}
		if label != "" && input.target.ElementRole == "textbox" &&
			input.target.ElementNameSHA256 == digest([]byte(label)) && slices.Contains(field.inputFacts(), handle) {
			admitted = true
		}
	}
	value, found := domain.item.Driver.Facts[handle]
	if !admitted || !found || value == "" || value == generatePrivateFact {
		return protocol.MintClawMessage{}, errBrowserEvidence
	}
	if domain.sentInputs == nil {
		domain.sentInputs = map[string]bool{}
	}
	// Fail closed on send uncertainty; this is not a replay/claim manager.
	domain.sentInputs[input.record.ID] = true
	return protocol.MintClawMessage{
		Type: protocol.TypeMessageSend, ID: uuid.NewString(), SessionID: domain.questions.sessionID,
		Timestamp: time.Now().UnixMilli(), Payload: map[string]any{
			protocol.PayloadKeyContent: "/answer " + input.record.ShortID + " " + value,
		},
	}, nil
}
