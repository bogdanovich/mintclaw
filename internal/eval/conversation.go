package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/bogdanovich/mintclaw/internal/gatewayclient"
	"github.com/bogdanovich/mintclaw/pkg/bus"
	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

type conversationTransport interface {
	Send(context.Context, protocol.MintClawMessage) error
	Receive(context.Context) (protocol.MintClawMessage, error)
	Failure() error
	Close()
}

type conversation struct {
	transport conversationTransport
	trial     *Trial
	agent     string
	workspace string
	frames    int
	bytes     int
	inputs    int
	last      Event
	filter    *strings.Replacer
	domain    conversationDomain
	facts     map[string]string
}

// Domain adapters bind inputs to native evidence and add independent checks.
// They do not own transport, turn correlation, grading, or runtime authority.
type conversationDomain interface {
	initialTask() string
	request(context.Context, Action, Event) (protocol.MintClawMessage, bool, error)
	observe(context.Context, Event) error
	conclude(context.Context, *Trial)
}

// A control reply is not an LLM turn. Only native terminal evidence can
// establish its outcome without inventing a missing LLM trace scope.
type cancellationObserver interface {
	observeCancellation(context.Context, Event, Event) error
}

// Called after the initial send, before receiving its terminal response. The
// native target runs independently; a domain must observe a real active barrier
// before sending guidance. The flow-owned sender enforces bounds and records
// each actual send; domains cannot inject synthetic transcript events.
type inFlightConversationDomain interface {
	afterSend(context.Context, protocol.MintClawMessage, int, func(string) (Event, error)) error
}

type conversationRoles struct {
	user   *ModelRole
	judge  *ModelRole
	filter *strings.Replacer
}

// The CLI admission layer must prove target isolation before connecting or
// calling this conversation engine. This engine owns turns, not tool authority.
func runScriptedConversation(
	ctx context.Context,
	transport conversationTransport,
	item LoadedCase,
	sessionID, agent, workspace string,
) (trial Trial) {
	return runConversation(ctx, transport, item, sessionID, agent, workspace, conversationRoles{})
}

func runConversation(
	ctx context.Context,
	transport conversationTransport,
	item LoadedCase,
	sessionID, agent, workspace string,
	roles conversationRoles,
) (trial Trial) {
	return runDomainConversation(ctx, transport, item, sessionID, agent, workspace, roles, nil)
}

func runDomainConversation(
	ctx context.Context,
	transport conversationTransport,
	item LoadedCase,
	sessionID, agent, workspace string,
	roles conversationRoles,
	domain conversationDomain,
) (trial Trial) {
	trial = Trial{
		Version: Version, ID: uuid.NewString(), CaseID: item.Case.ID, CaseSHA256: item.Digest,
		SessionID: sessionID, AgentID: agent, Verdict: Inconclusive, Code: "not_completed",
		StartedAt: time.Now().UTC(), Events: []Event{}, Checks: []CheckResult{}, RemoteState: "unknown",
		ExecutionVerdict: Inconclusive, ExecutionCode: "not_completed",
		TargetBudgetRequired: requiresTargetBudget(item.Case),
		ActiveToolRequired:   requiresActiveTool(item.Case),
	}
	defer func() {
		transport.Close()
		trial.TransportClosed = true
		if errors.Is(transport.Failure(), ErrPrivateEvidence) {
			trial.Verdict, trial.Code = Fail, "private_wire_evidence"
			trial.ExecutionVerdict, trial.ExecutionCode = Fail, trial.Code
			trial.EvidenceEligible = false
		} else if errors.Is(transport.Failure(), gatewayclient.ErrFrameInspection) {
			if trial.Verdict == Pass {
				trial.Code = "wire_inspection_unavailable"
			}
			if trial.ExecutionVerdict == Pass {
				trial.ExecutionCode = "wire_inspection_unavailable"
			}
			trial.Verdict = combineVerdicts(trial.Verdict, Inconclusive)
			trial.ExecutionVerdict = combineVerdicts(trial.ExecutionVerdict, Inconclusive)
			trial.EvidenceEligible = false
		}
		if failure := transport.Failure(); errors.Is(failure, gatewayclient.ErrQueueLimit) ||
			errors.Is(failure, websocket.ErrReadLimit) {
			if trial.Verdict == Pass {
				trial.Code = "transport_limit"
			}
			trial.Verdict = combineVerdicts(trial.Verdict, Inconclusive)
			trial.ExecutionVerdict = combineVerdicts(trial.ExecutionVerdict, Inconclusive)
			trial.ExecutionCode = "transport_limit"
			trial.EvidenceEligible = false
		}
		trial.DurationMS = time.Since(trial.StartedAt).Milliseconds()
	}()
	if item.Case.Validate() != nil || !validDigest(item.Digest) || !validID(agent) ||
		!validID(sessionID) || workspace == "" {
		trial.Verdict, trial.Code = Error, "invalid_admission"
		trial.ExecutionVerdict, trial.ExecutionCode = trial.Verdict, trial.Code
		return trial
	}
	if item.Case.Driver.Mode == "llm_user" && roles.user == nil {
		trial.Verdict, trial.Code = Skipped, "driver_unavailable"
		trial.ExecutionVerdict, trial.ExecutionCode = trial.Verdict, trial.Code
		return trial
	}
	caseTimeout, _ := time.ParseDuration(item.Case.Limits.CaseTimeout)
	caseCtx, cancel := context.WithTimeout(ctx, caseTimeout)
	defer cancel()
	flow := conversation{
		transport: transport, trial: &trial, agent: agent, workspace: workspace, filter: roles.filter, domain: domain,
		facts: item.Case.Driver.Facts,
	}
	task := item.Case.Task
	if domain != nil {
		task = domain.initialTask()
	}
	verdict, code := flow.turn(caseCtx, item.Case.Limits, Action{Kind: "message", Text: task}, Event{}, 1)
	if verdict == Pass && item.Case.Driver.Mode == "scripted" {
		for index, step := range item.Case.Driver.Steps {
			if !step.WaitFor.matches(flow.last.Kind, flow.last.Content) {
				verdict, code = Fail, "unexpected_turn_outcome"
				break
			}
			if step.Action.Kind == "stop" {
				verdict, code = Inconclusive, "driver_stopped"
				break
			}
			verdict, code = flow.turn(caseCtx, item.Case.Limits, step.Action, flow.last, index+2)
			if verdict != Pass {
				break
			}
		}
	}
	if verdict == Pass && item.Case.Driver.Mode == "llm_user" {
		verdict, code = flow.adaptiveFollowups(caseCtx, item.Case, roles.user)
	}
	trial.Checks = gradeConversation(item.Case.Checks, trial.Events, flow.last)
	trial.Verdict, trial.Code = verdict, code
	trial.ExecutionVerdict, trial.ExecutionCode = verdict, code
	if domain != nil {
		domain.conclude(caseCtx, &trial)
	}
	if errors.Is(transport.Failure(), ErrPrivateEvidence) {
		trial.Verdict, trial.Code = Fail, "private_wire_evidence"
		trial.ExecutionVerdict, trial.ExecutionCode = Fail, trial.Code
		trial.EvidenceEligible = false
	}
	for _, check := range trial.Checks {
		combined := combineVerdicts(trial.Verdict, check.Verdict)
		if combined != trial.Verdict {
			switch combined {
			case Fail:
				trial.Code = "hard_check_failed"
			case Inconclusive:
				trial.Code = "required_check_unknown"
			}
		}
		trial.Verdict = combined
	}
	if _, err := visibleConversation(
		trial.Events,
		item.Case.Driver.Facts,
	); item.Case.Browser == nil && verdict == Pass && err == nil &&
		len(item.Case.Driver.Facts) == 0 &&
		len(item.Case.Fixtures) == 0 {
		trial.EvidenceEligible = true
	}
	if item.Case.Steering != nil && trial.Verdict != Pass {
		trial.EvidenceEligible = false
	}
	if len(item.Case.Rubric) > 0 && item.Case.Steering == nil {
		judgement := Judgement{
			Version: Version, Verdict: Inconclusive, Code: "ineligible_judge_evidence",
			Findings: JudgeFindings{Version: Version, Criteria: []JudgeFinding{}},
		}
		if trial.EvidenceEligible {
			judgement = judgeConversation(caseCtx, roles.judge, item.Case, trial.Events)
		}
		if credentialJSONValue(judgement, flow.filter) {
			judgement.Findings = JudgeFindings{Version: Version, Criteria: []JudgeFinding{}}
			judgement.Verdict, judgement.Code = Fail, "private_judge_output"
		}
		trial.Judge = &judgement
		if !roleBoundaryEligible(judgement.Call) {
			trial.EvidenceEligible = false
		}
		if judgement.Code == "private_judge_output" {
			trial.EvidenceEligible = false
			trial.ExecutionVerdict, trial.ExecutionCode = Fail, judgement.Code
		}
		if requiredRubric(item.Case.Rubric) || judgement.Code == "private_judge_output" ||
			!roleBoundaryEligible(judgement.Call) {
			combined := combineVerdicts(trial.Verdict, judgement.Verdict)
			if combined != trial.Verdict {
				trial.Code = judgement.Code
			}
			trial.Verdict = combined
		}
	}
	return trial
}

func requiredRubric(rubric []Criterion) bool {
	for _, criterion := range rubric {
		if criterion.Required {
			return true
		}
	}
	return false
}

func (flow *conversation) adaptiveFollowups(ctx context.Context, item Case, user *ModelRole) (Verdict, string) {
	for number := 2; number <= item.Limits.MaxUserTurns; number++ {
		if len(flow.trial.DriverCalls) >= item.Limits.MaxDriverCalls {
			return Inconclusive, "driver_call_limit"
		}
		decision := nextModelAction(ctx, user, item, flow.trial.Events, flow.last.Kind,
			item.Limits.MaxUserTurns-number+1)
		if decision.Call != nil {
			flow.trial.DriverCalls = append(flow.trial.DriverCalls, *decision.Call)
		}
		if decision.Code != "user_action_received" {
			if decision.Call != nil &&
				(decision.Call.Code == "private_input" || decision.Call.Code == "private_output") {
				return Fail, "private_user_action"
			}
			return Error, decision.Code
		}
		if credentialJSONValue(decision.Action, flow.filter) {
			return Fail, "private_user_action"
		}
		if decision.Action.Kind == "stop" {
			return Inconclusive, "driver_stopped"
		}
		verdict, code := flow.turn(ctx, item.Limits, decision.Action, flow.last, number)
		if verdict != Pass {
			return verdict, code
		}
		if decision.Action.Kind == "finish" || decision.Action.Kind == "allow_once" {
			if flow.last.Kind != "final" {
				return Inconclusive, "unfinished_interaction"
			}
			return Pass, "adaptive_completed"
		}
		if decision.Action.Kind == "cancel" && flow.last.Kind == "canceled" {
			return Pass, "adaptive_canceled"
		}
	}
	return Inconclusive, "user_turn_limit"
}

func (flow *conversation) turn(
	ctx context.Context,
	limits Limits,
	action Action,
	prompt Event,
	number int,
) (Verdict, string) {
	if flow.inputs >= limits.MaxUserTurns {
		return Inconclusive, "user_input_limit"
	}
	turnTimeout, _ := time.ParseDuration(limits.TurnTimeout)
	turnCtx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()
	request, code := flow.request(turnCtx, action, prompt)
	if code != "" {
		if code == "private_native_evidence" {
			return Fail, code
		}
		return Inconclusive, code
	}
	if credentialJSONValue(request, flow.filter) || action.Kind != "protected_answer" &&
		(privateJSONValue(request, flow.facts) ||
			strings.Contains(payloadString(request.Payload, protocol.PayloadKeyContent), generatePrivateFact)) {
		return Fail, "private_input"
	}
	if err := flow.transport.Send(turnCtx, request); err != nil {
		return Inconclusive, transportCode(err)
	}
	flow.inputs++
	content := payloadString(request.Payload, protocol.PayloadKeyContent)
	if action.Kind == "protected_answer" {
		content = "[protected answer supplied using an admitted fact handle]"
	}
	if !flow.append(Event{
		Turn: number, Kind: "input", RequestID: request.ID,
		Content: content,
	}) {
		return Inconclusive, "event_limit"
	}
	if domain, ok := flow.domain.(inFlightConversationDomain); ok {
		additional := 0
		var sendErr error
		refuse := func(err error) (Event, error) {
			sendErr = err
			return Event{}, err
		}
		send := func(text string) (Event, error) {
			if sendErr != nil {
				return Event{}, sendErr
			}
			if additional >= 2 || flow.inputs >= limits.MaxUserTurns || len(flow.trial.Events) >= 256 ||
				!boundedText(text, MaxInputBytes) {
				return refuse(ErrRunOptions)
			}
			guidance := protocol.MintClawMessage{
				Type: protocol.TypeMessageSend, ID: uuid.NewString(), SessionID: flow.trial.SessionID,
				Timestamp: time.Now().UnixMilli(), Payload: map[string]any{protocol.PayloadKeyContent: text},
			}
			if credentialJSONValue(guidance, flow.filter) || privateJSONValue(guidance, flow.facts) {
				return refuse(ErrPrivateEvidence)
			}
			additional++
			flow.inputs++
			if err := flow.transport.Send(turnCtx, guidance); err != nil {
				return refuse(err)
			}
			flow.append(Event{Turn: number, Kind: "input", RequestID: guidance.ID, Content: text})
			return flow.trial.Events[len(flow.trial.Events)-1], nil
		}
		err := domain.afterSend(turnCtx, request, number, send)
		if sendErr != nil {
			err = sendErr
		}
		if err != nil {
			if errors.Is(err, ErrPrivateEvidence) {
				return Fail, "private_in_flight_input"
			}
			return Inconclusive, "in_flight_evidence_unavailable"
		}
	}
	for {
		message, err := flow.transport.Receive(turnCtx)
		if err != nil {
			return Inconclusive, transportCode(err)
		}
		flow.frames++
		encoded, encodeErr := json.Marshal(message)
		flow.bytes += len(encoded)
		if encodeErr != nil || flow.frames > 256 || flow.bytes > 2*1024*1024 {
			return Inconclusive, "event_limit"
		}
		if credentialJSONValue(message, flow.filter) || privateJSONValue(message, flow.facts) {
			return Fail, "private_wire_evidence"
		}
		if message.SessionID != "" && message.SessionID != flow.trial.SessionID {
			continue
		}
		requestID := payloadString(message.Payload, protocol.PayloadKeyRequestID)
		if message.Type == protocol.TypeError && (requestID == "" || requestID == request.ID) {
			return Inconclusive, "protocol_rejected"
		}
		if message.SessionID == "" {
			continue
		}
		if requestID != request.ID ||
			message.Type != protocol.TypeMessageCreate && message.Type != protocol.TypeMessageUpdate {
			continue
		}
		event, eventErr := responseEvent(message, number)
		if eventErr != nil {
			return Inconclusive, "invalid_response"
		}
		for _, scope := range event.TraceScopes {
			if !scope.Complete() || scope.Workspace != flow.workspace {
				event.Usage = nil
				flow.append(event)
				return Fail, "unexpected_workspace"
			}
		}
		if event.Kind == "auxiliary" {
			continue
		}
		if event.AgentID != "" && event.AgentID != flow.agent ||
			flow.trial.SessionKey != "" && event.SessionKey != "" && event.SessionKey != flow.trial.SessionKey {
			event.Usage = nil
			flow.append(event)
			return Fail, "route_changed"
		}
		if action.Kind == "cancel" && event.Kind == "final" {
			if observer, ok := flow.domain.(cancellationObserver); ok {
				if err := observer.observeCancellation(turnCtx, prompt, event); err != nil {
					flow.append(event)
					if errors.Is(err, ErrPrivateEvidence) {
						return Fail, "private_native_evidence"
					}
					return Inconclusive, "cancellation_evidence_unavailable"
				}
				event.Kind = "canceled"
				event.Usage = nil
			}
		}
		if !flow.append(event) {
			return Inconclusive, "event_limit"
		}
		if event.Kind == "stream" {
			continue
		}
		if event.AgentID == "" || event.SessionKey == "" || len(event.TraceScopes) == 0 && event.Kind != "canceled" {
			return Inconclusive, "missing_route_identity"
		}
		flow.trial.SessionKey = event.SessionKey
		flow.last = flow.trial.Events[len(flow.trial.Events)-1]
		if flow.domain != nil {
			if err := flow.domain.observe(turnCtx, flow.last); err != nil {
				if errors.Is(err, ErrPrivateEvidence) {
					return Fail, "private_native_evidence"
				}
				return Inconclusive, "domain_evidence_unavailable"
			}
		}
		return Pass, "script_completed"
	}
}

func (flow *conversation) request(ctx context.Context, action Action, prompt Event) (protocol.MintClawMessage, string) {
	if flow.domain != nil {
		request, handled, err := flow.domain.request(ctx, action, prompt)
		if err != nil {
			if errors.Is(err, ErrPrivateEvidence) {
				return protocol.MintClawMessage{}, "private_native_evidence"
			}
			return protocol.MintClawMessage{}, "domain_input_not_admitted"
		}
		if handled {
			return request, ""
		}
	}
	payload := map[string]any{protocol.PayloadKeyContent: action.Text}
	switch action.Kind {
	case "message", "finish":
		if prompt.Kind == "approval" {
			return protocol.MintClawMessage{}, "approval_not_authorized"
		}
	case "clarify", "back", "skip", "not_applicable", "cancel":
		if prompt.Kind != "question" && prompt.Kind != "approval" ||
			prompt.Kind == "approval" && action.Kind != "cancel" ||
			!boundedIdentity(prompt.InteractionShortID, 64) || !boundedIdentity(prompt.MessageID, 128) {
			return protocol.MintClawMessage{}, "invalid_interaction"
		}
		// Delivery precedes the durable waiting transition. This is one bounded
		// grace period, not a retry of an operation with uncertain admission.
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return protocol.MintClawMessage{}, transportCode(ctx.Err())
		}
		labels := map[string]string{
			"clarify": bus.InboundInteractionClarifyLabel, "back": bus.InboundInteractionBackLabel,
			"skip": bus.InboundInteractionSkipLabel, "not_applicable": bus.InboundInteractionNotApplicableLabel,
			"cancel": bus.InboundInteractionCancelLabel,
		}
		payload[protocol.PayloadKeyContent] = labels[action.Kind]
		payload[protocol.PayloadKeyInteractionChoice] = action.Kind
		payload[protocol.PayloadKeyInteractionShortID] = prompt.InteractionShortID
		payload[protocol.PayloadKeyInteractionPrompt] = prompt.MessageID
	default:
		return protocol.MintClawMessage{}, "action_unavailable"
	}
	return protocol.MintClawMessage{
		Type: protocol.TypeMessageSend, ID: uuid.NewString(), SessionID: flow.trial.SessionID,
		Timestamp: time.Now().UnixMilli(), Payload: payload,
	}, ""
}

func (flow *conversation) append(event Event) bool {
	if len(flow.trial.Events) >= 256 {
		return false
	}
	event.ID = fmt.Sprintf("event-%06d", len(flow.trial.Events)+1)
	flow.trial.Events = append(flow.trial.Events, event)
	return true
}

func responseEvent(message protocol.MintClawMessage, turn int) (Event, error) {
	for _, key := range []string{protocol.PayloadKeyFinal, protocol.PayloadKeyPlaceholder} {
		if raw, present := message.Payload[key]; present {
			if _, ok := raw.(bool); !ok {
				return Event{}, ErrInvalidJSON
			}
		}
	}
	for _, key := range []string{
		protocol.PayloadKeyContent, protocol.PayloadKeyRequestID, protocol.PayloadKeyMessageID,
		protocol.PayloadKeyAgentID, protocol.PayloadKeySessionKey, protocol.PayloadKeyModelName,
		protocol.PayloadKeyInteractionID, protocol.PayloadKeyInteractionShortID,
		protocol.PayloadKeyKind, protocol.PayloadKeyControls, protocol.PayloadKeyInteraction, protocol.PayloadKeyOutbound,
	} {
		if raw, present := message.Payload[key]; present {
			value, ok := raw.(string)
			if !ok || len(value) > MaxInputBytes || strings.ContainsRune(value, 0) {
				return Event{}, ErrInvalidJSON
			}
		}
	}
	content := payloadString(message.Payload, protocol.PayloadKeyContent)
	if len(content) > MaxInputBytes || content != "" && !boundedText(content, MaxInputBytes) {
		return Event{}, ErrInvalidJSON
	}
	event := Event{
		Turn: turn, Kind: "stream", RequestID: payloadString(message.Payload, protocol.PayloadKeyRequestID),
		MessageID: payloadString(message.Payload, protocol.PayloadKeyMessageID), Content: content,
		AgentID:            payloadString(message.Payload, protocol.PayloadKeyAgentID),
		SessionKey:         payloadString(message.Payload, protocol.PayloadKeySessionKey),
		Model:              payloadString(message.Payload, protocol.PayloadKeyModelName),
		InteractionID:      payloadString(message.Payload, protocol.PayloadKeyInteractionID),
		InteractionShortID: payloadString(message.Payload, protocol.PayloadKeyInteractionShortID),
		Controls:           payloadString(message.Payload, protocol.PayloadKeyControls),
		InteractionKind:    payloadString(message.Payload, protocol.PayloadKeyInteraction),
	}
	if raw, present := message.Payload[protocol.PayloadKeyTraceScopes]; present {
		data, err := json.Marshal(raw)
		if err != nil || json.Unmarshal(data, &event.TraceScopes) != nil || len(event.TraceScopes) > 16 {
			return Event{}, ErrInvalidJSON
		}
	}
	kind := payloadString(message.Payload, protocol.PayloadKeyKind)
	placeholder, _ := message.Payload[protocol.PayloadKeyPlaceholder].(bool)
	if placeholder || kind == protocol.MessageKindThought || kind == protocol.MessageKindToolCalls {
		event.Kind = "auxiliary"
	} else if payloadString(message.Payload, protocol.PayloadKeyControls) == "prompt" {
		switch payloadString(message.Payload, protocol.PayloadKeyInteraction) {
		case "question", "approval":
			event.Kind = payloadString(message.Payload, protocol.PayloadKeyInteraction)
		}
	} else if final, _ := message.Payload[protocol.PayloadKeyFinal].(bool); final ||
		kind == protocol.MessageKindFinalReply || payloadString(message.Payload, protocol.PayloadKeyOutbound) == "final" {
		event.Kind = "final"
	}
	if event.Kind == "final" {
		event.Usage = returnedTokenUsage(message.Payload[protocol.PayloadKeyUsage])
	}
	return event, nil
}

func payloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func boundedIdentity(value string, maximum int) bool {
	return boundedText(value, maximum) && !strings.ContainsAny(value, " \t\r\n")
}

func transportCode(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout_remote_unknown"
	case errors.Is(err, context.Canceled):
		return "canceled_remote_unknown"
	case errors.Is(err, gatewayclient.ErrQueueLimit), errors.Is(err, websocket.ErrReadLimit):
		return "transport_limit"
	case errors.Is(err, gatewayclient.ErrFrameInspection):
		return "wire_inspection_unavailable"
	default:
		return "disconnected_remote_unknown"
	}
}
