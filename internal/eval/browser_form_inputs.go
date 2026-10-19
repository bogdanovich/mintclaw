package eval

import (
	"reflect"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/browseraction"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/health"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/routing"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

const browserInputFillScope = "native_browser_input_fill_v1"

// This ephemeral observation contains the original value-free binding, not the
// submitted value. It is never serialized; only the joined projection below is.
type browserInputAttempt struct {
	observation     browserInputObservation
	prompt          Event
	fact, requestID string
	attemptedAt     time.Time
}

type BrowserInputFillEvidence struct {
	Scope              string                               `json:"scope"`
	Code               string                               `json:"code"`
	InteractionID      string                               `json:"interaction_id"`
	PromptMessageID    string                               `json:"prompt_message_id"`
	PromptRequestID    string                               `json:"prompt_request_id"`
	PromptDeliveryID   string                               `json:"prompt_delivery_id"`
	AnswerRequestID    string                               `json:"answer_request_id"`
	Fact               string                               `json:"fact"`
	Source             diagnostictrace.Source               `json:"source"`
	TraceScope         events.TraceScope                    `json:"trace_scope"`
	TraceID            string                               `json:"trace_id"`
	TraceSHA256        string                               `json:"trace_sha256"`
	ToolCallID         string                               `json:"tool_call_id"`
	ToolCallGeneration string                               `json:"tool_call_generation"`
	Target             browseraction.InputTargetObservation `json:"target"`
	PreparedActionID   string                               `json:"prepared_action_id"`
	InvocationID       string                               `json:"invocation_id"`
	ActionSHA256       string                               `json:"action_sha256"`
	WaitingRevision    int64                                `json:"waiting_revision"`
	ObservedRevision   int64                                `json:"observed_revision"`
	InteractionStatus  interactions.Status                  `json:"interaction_status"`
	ReceivedAt         int64                                `json:"received_at"`
	ResolvedAt         int64                                `json:"resolved_at"`
	AcceptedAt         int64                                `json:"accepted_at"`
	CompletedAt        int64                                `json:"completed_at"`
}

// An accepted-input checkpoint is deliberately distinct from submitted-value,
// confirmation, approval, delivery and post-stop cleanup assessment.
type BrowserFormInputsEvidence struct {
	Scope          string                     `json:"scope"`
	Code           string                     `json:"code"`
	RegistrySHA256 string                     `json:"registry_sha256,omitempty"`
	StateSHA256    string                     `json:"state_sha256,omitempty"`
	Fills          []BrowserInputFillEvidence `json:"fills,omitempty"`
}

func (domain *browserConversation) readFormInputs() (*BrowserFormInputsEvidence, error) {
	proof := &BrowserFormInputsEvidence{Scope: "native_browser_form_inputs_v1", Code: "unavailable"}
	if len(domain.inputAttempts) == 0 || len(domain.inputAttempts) > 64 {
		return proof, errBrowserEvidence
	}
	records, sha, err := domain.questions.records()
	if err != nil {
		return proof, err
	}
	raw, err := domain.questions.read(
		domain.questions.workspace,
		"state/browser/browser.json",
		browser.DefaultFileStoreBytes,
	)
	if err != nil {
		return proof, err
	}
	defer clear(raw)
	state, err := browser.InspectStateSnapshot(raw)
	if err != nil {
		return proof, errBrowserEvidence
	}
	byID := make(map[string]interactions.Record, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	fills := make([]BrowserInputFillEvidence, 0, len(domain.inputAttempts))
	for _, attempt := range domain.inputAttempts {
		fill, err := projectBrowserInputFill(attempt, byID[attempt.observation.record.ID], state, time.Now())
		if err != nil {
			return proof, err
		}
		fills = append(fills, *fill)
	}
	proof.Code, proof.RegistrySHA256, proof.StateSHA256, proof.Fills = "native_inputs_observed", sha, digest(raw), fills
	return proof, nil
}

func (domain *browserConversation) inputAttempted(id string) bool {
	for _, attempt := range domain.inputAttempts {
		if attempt.observation.record.ID == id {
			return true
		}
	}
	return false
}

// A delivered question/answer attempt is not a fill. Join the native accepted
// receipt to the original ask's preparation and one successful invocation. The
// broker's keyed input digest is not replaced by an invented raw-value hash.
func projectBrowserInputFill(
	attempt browserInputAttempt, current interactions.Record, state browser.StateSnapshot, now time.Time,
) (*BrowserInputFillEvidence, error) {
	before, target := attempt.observation.record, attempt.observation.target
	original := attempt.observation
	answer := current.Answer
	if !target.Valid() || !validID(attempt.requestID) || !validID(attempt.fact) || attempt.attemptedAt.IsZero() ||
		!validID(original.traceID) || !validDigest(original.traceSHA256) || !original.traceScope.Complete() ||
		!boundedIdentity(original.generation, 256) || original.traceScope.TurnID != before.Origin.TurnID ||
		!nativeModelSourceKnown(health.RuntimeProbe{
			GitCommit: original.source.Commit, BuildVersion: original.source.MintClawVersion,
		}) ||
		before.ProtectedAnswer == nil || before.ProtectedAnswer.Namespace != tools.BrowserProtectedAnswerNamespace ||
		!target.MatchesToken(before.ProtectedAnswer.Token) || before.Origin.ToolName != "browser_input" ||
		target.ActorID != browser.OpaqueActorID("mintclaw:mintclaw-user") ||
		target.AgentID != browser.OpaqueAgentID(routing.NormalizeAgentID(before.Route.AgentID)) ||
		target.SessionKey != browser.OpaqueSessionKey(before.Route.SessionKey) ||
		target.ExecutionID != browser.OpaqueExecutionID(before.Origin.ExecutionID) ||
		target.RequestID != browser.OpaqueRequestID(before.Origin.ExecutionID, before.Origin.ToolCallID) ||
		before.ID != current.ID || before.ShortID != current.ShortID || before.Kind != interactions.KindQuestion ||
		current.Kind != before.Kind || before.Status != interactions.StatusWaiting ||
		(current.Status != interactions.StatusResuming && current.Status != interactions.StatusResolved) ||
		current.Outcome != interactions.OutcomeAnswered || current.FailureCode != "" || current.ApprovalConsumedAt != 0 ||
		current.Route != before.Route || !reflect.DeepEqual(current.Origin, before.Origin) ||
		!reflect.DeepEqual(current.Questions, before.Questions) ||
		!reflect.DeepEqual(current.ProtectedAnswer, before.ProtectedAnswer) ||
		current.PromptDeliveryID != before.PromptDeliveryID || current.CreatedAt != before.CreatedAt ||
		current.ExpiresAt != before.ExpiresAt || current.Revision <= before.Revision ||
		current.UpdatedAt > now.UnixMilli() || answer == nil || answer.Protected == nil ||
		answer.Protected.State != "prepared" || answer.MessageID != attempt.requestID ||
		answer.ResponseMessageID != "" && answer.ResponseMessageID != attempt.prompt.MessageID ||
		answer.Superseded || answer.Text != "" || len(answer.Values) != 0 || len(answer.Media) != 0 || answer.Choice != "" ||
		answer.ReceivedAt < attempt.attemptedAt.UnixMilli() || answer.ReceivedAt >= current.ExpiresAt ||
		answer.ReceivedAt > current.UpdatedAt ||
		current.Status == interactions.StatusResolved &&
			(current.ResolvedAt < answer.ReceivedAt || current.ResolvedAt > now.UnixMilli()) {
		return nil, errBrowserEvidence
	}
	owner := browser.Owner{
		ActorID:     target.ActorID,
		AgentID:     target.AgentID,
		SessionKey:  target.SessionKey,
		ExecutionID: target.ExecutionID,
	}
	var selected *browser.PreparedAction
	for _, action := range state.PreparedActions {
		// Compare the native public receipt identity; never decode its HMAC token
		// or turn a model-provided locator into a resource path or grant.
		if answer.Protected.Reference != "browser_fill."+action.ID {
			continue
		}
		if selected != nil || action.Owner != owner || action.RequestID != target.RequestID ||
			action.SessionID != target.SessionID || action.TabID != target.TabID || action.FrameID != target.FrameID ||
			action.SnapshotID != target.SnapshotID || action.SnapshotGeneration != target.SnapshotGeneration ||
			action.ContextCatalogID != target.ContextCatalogID || action.ContextGeneration != target.ContextGeneration ||
			action.ControllerGeneration != target.ControllerGeneration || action.CatalogRevision != target.CatalogRevision ||
			action.Action != (browser.Action{Kind: browser.ActionFill, Ref: target.Ref}) ||
			action.ElementRole != target.ElementRole || digest([]byte(action.ElementName)) != target.ElementNameSHA256 ||
			action.ElementPosition != target.ElementPosition || !validDigest(action.InputDigest) || action.InputBytes <= 0 ||
			action.Effect != browser.EffectLocalEdit || action.PolicyEffect != browser.EffectLocalEdit || action.DryRun ||
			action.CreatedAt < attempt.attemptedAt.UnixNano() || action.CreatedAt >= target.ExpiresAt {
			return nil, errBrowserEvidence
		}
		copy := action
		selected = &copy
	}
	if selected == nil {
		return nil, errBrowserEvidence
	}
	session, found := state.Sessions[target.SessionID]
	if !found || session.Owner != owner || session.DryRun || selected.Target != session.Target ||
		selected.Profile != session.Profile || selected.ProfileRevision != session.ProfileRevision ||
		selected.PolicyRevision != session.PolicyRevision {
		return nil, errBrowserEvidence
	}
	var used *browser.Invocation
	for _, invocation := range state.Invocations {
		if invocation.PreparedActionID != selected.ID {
			continue
		}
		if used != nil || invocation.Execution != nil || invocation.Owner != owner ||
			invocation.SessionID != target.SessionID || invocation.ActionHash != selected.ActionHash ||
			invocation.State != browser.InvocationSucceeded || invocation.SafeFailure != "" ||
			invocation.Effect != browser.EffectLocalEdit || invocation.AcceptedAt < selected.CreatedAt ||
			invocation.AcceptedAt/1_000_000 < answer.ReceivedAt || invocation.AcceptedAt >= selected.ExpiresAt ||
			invocation.CompletedAt < invocation.AcceptedAt || invocation.CompletedAt > now.UnixNano() {
			return nil, errBrowserEvidence
		}
		copy := invocation
		used = &copy
	}
	if used == nil {
		return nil, errBrowserEvidence
	}
	if current.Status == interactions.StatusResolved && current.ResolvedAt < used.CompletedAt/1_000_000 {
		return nil, errBrowserEvidence
	}
	return &BrowserInputFillEvidence{
		Scope:              browserInputFillScope,
		Code:               "native_fill_observed",
		InteractionID:      current.ID,
		PromptMessageID:    attempt.prompt.MessageID,
		PromptRequestID:    attempt.prompt.RequestID,
		PromptDeliveryID:   current.PromptDeliveryID,
		AnswerRequestID:    attempt.requestID,
		Fact:               attempt.fact,
		Source:             original.source,
		TraceScope:         original.traceScope,
		TraceID:            original.traceID,
		TraceSHA256:        original.traceSHA256,
		ToolCallID:         before.Origin.ToolCallID,
		ToolCallGeneration: original.generation,
		Target:             target,
		PreparedActionID:   selected.ID,
		InvocationID:       used.ID,
		ActionSHA256:       selected.ActionHash,
		WaitingRevision:    before.Revision,
		ObservedRevision:   current.Revision,
		ReceivedAt:         answer.ReceivedAt,
		InteractionStatus:  current.Status,
		ResolvedAt:         current.ResolvedAt,
		AcceptedAt:         used.AcceptedAt,
		CompletedAt:        used.CompletedAt,
	}, nil
}
