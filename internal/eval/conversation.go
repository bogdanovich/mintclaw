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
	last      Event
}

// The CLI admission layer must prove target isolation before connecting or
// calling this conversation engine. This engine owns turns, not tool authority.
func runScriptedConversation(
	ctx context.Context,
	transport conversationTransport,
	item LoadedCase,
	sessionID, agent, workspace string,
) (trial Trial) {
	trial = Trial{
		Version: Version, ID: uuid.NewString(), CaseID: item.Case.ID, CaseSHA256: item.Digest,
		SessionID: sessionID, AgentID: agent, Verdict: Inconclusive, Code: "not_completed",
		StartedAt: time.Now().UTC(), Events: []Event{}, Checks: []CheckResult{}, RemoteState: "unknown",
	}
	defer func() {
		transport.Close()
		trial.TransportClosed = true
		if failure := transport.Failure(); errors.Is(failure, gatewayclient.ErrQueueLimit) ||
			errors.Is(failure, websocket.ErrReadLimit) {
			if trial.Verdict == Pass {
				trial.Code = "transport_limit"
			}
			trial.Verdict = combineVerdicts(trial.Verdict, Inconclusive)
		}
		trial.DurationMS = time.Since(trial.StartedAt).Milliseconds()
	}()
	if item.Case.Validate() != nil || !validDigest(item.Digest) || !validID(agent) ||
		!validID(sessionID) || workspace == "" {
		trial.Verdict, trial.Code = Error, "invalid_admission"
		return trial
	}
	if item.Case.Driver.Mode != "scripted" {
		trial.Verdict, trial.Code = Skipped, "driver_unavailable"
		return trial
	}
	caseTimeout, _ := time.ParseDuration(item.Case.Limits.CaseTimeout)
	caseCtx, cancel := context.WithTimeout(ctx, caseTimeout)
	defer cancel()
	flow := conversation{transport: transport, trial: &trial, agent: agent, workspace: workspace}
	verdict, code := flow.turn(caseCtx, item.Case.Limits, Action{Kind: "message", Text: item.Case.Task}, Event{}, 1)
	if verdict == Pass {
		for index, step := range item.Case.Driver.Steps {
			if flow.last.Kind != step.WaitFor.Kind ||
				step.WaitFor.Contains != "" && !strings.Contains(flow.last.Content, step.WaitFor.Contains) {
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
	trial.Checks = gradeConversation(item.Case.Checks, trial.Events, flow.last)
	trial.Verdict, trial.Code = verdict, code
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
	if len(item.Case.Rubric) > 0 && trial.Verdict == Pass {
		trial.Verdict, trial.Code = Inconclusive, "judge_required"
	}
	return trial
}

func (flow *conversation) turn(
	ctx context.Context,
	limits Limits,
	action Action,
	prompt Event,
	number int,
) (Verdict, string) {
	turnTimeout, _ := time.ParseDuration(limits.TurnTimeout)
	turnCtx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()
	request, code := flow.request(turnCtx, action, prompt)
	if code != "" {
		return Inconclusive, code
	}
	if err := flow.transport.Send(turnCtx, request); err != nil {
		return Inconclusive, transportCode(err)
	}
	if !flow.append(Event{Turn: number, Kind: "input", RequestID: request.ID, Content: action.Text}) {
		return Inconclusive, "event_limit"
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
		if event.Kind == "auxiliary" {
			continue
		}
		if !flow.append(event) {
			return Inconclusive, "event_limit"
		}
		if event.AgentID != "" && event.AgentID != flow.agent ||
			flow.trial.SessionKey != "" && event.SessionKey != "" && event.SessionKey != flow.trial.SessionKey {
			return Fail, "route_changed"
		}
		if event.Kind == "stream" {
			continue
		}
		if event.AgentID == "" || event.SessionKey == "" || len(event.TraceScopes) == 0 {
			return Inconclusive, "missing_route_identity"
		}
		for _, scope := range event.TraceScopes {
			if !scope.Complete() || scope.Workspace != flow.workspace {
				return Fail, "unexpected_workspace"
			}
		}
		flow.trial.SessionKey = event.SessionKey
		flow.last = flow.trial.Events[len(flow.trial.Events)-1]
		return Pass, "script_completed"
	}
}

func (flow *conversation) request(ctx context.Context, action Action, prompt Event) (protocol.MintClawMessage, string) {
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
	default:
		return "disconnected_remote_unknown"
	}
}
