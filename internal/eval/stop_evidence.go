package eval

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/events"
)

type NativeStopEvidence struct {
	Version          int                      `json:"version"`
	Code             string                   `json:"code"`
	Workspace        string                   `json:"workspace"`
	Profile          ActiveToolProfile        `json:"profile"`
	PrimaryRequestID string                   `json:"primary_request_id"`
	StopRequestID    string                   `json:"stop_request_id"`
	AbortObservedAt  time.Time                `json:"abort_observed_at"`
	Guidance         SteeringGuidanceEvidence `json:"guidance"`
	Fixture          activeToolObservation    `json:"fixture"`
	Log              []string                 `json:"log"`
	LogSHA256        string                   `json:"log_sha256"`
	Metadata         string                   `json:"metadata"`
	MetadataSHA256   string                   `json:"metadata_sha256"`
	History          []string                 `json:"history"`
	HistorySHA256    string                   `json:"history_sha256"`
	Outbox           []SteeringOutboxEvidence `json:"outbox"`
	OutboxNames      []string                 `json:"outbox_names"`
}

type nativeStopAbortBinding struct {
	turnID  string
	callID  string
	endedAt time.Time
}

func decodeStopLog(raw string) (activeToolLogEvent, bool) {
	var event activeToolLogEvent
	if len(raw) > MaxManifestBytes {
		return event, false
	}
	_, err := decodeJSONValue(json.NewDecoder(strings.NewReader(raw)), 0, true)
	if err != nil || json.Unmarshal([]byte(raw), &event) != nil ||
		!validID(event.ID) || event.Time.IsZero() || event.Kind == "" {
		return event, false
	}
	return event, true
}

// This checks native abort, not the stop ACK's wording. The full oracle also
// binds actual inputs, committed history, reuse and the complete outbox census.
func inspectNativeStopAbort(
	proof *NativeStopEvidence, agent, key, sessionID string,
) (nativeStopAbortBinding, bool) {
	var binding nativeStopAbortBinding
	if proof == nil || len(proof.Log) == 0 || len(proof.Log) > 4096 ||
		proof.LogSHA256 != digest([]byte(strings.Join(proof.Log, "\n"))) ||
		len(strings.Join(proof.Log, "\n")) > 2*1024*1024 ||
		proof.Guidance.SHA256 != digest([]byte(proof.Guidance.Enqueue)) {
		return binding, false
	}
	fixture, held := proof.Fixture, proof.Guidance.Held
	if fixture.TrialID != sessionID || fixture.SessionID != sessionID || fixture.Slot != 1 ||
		fixture.State != "canceled" || fixture.Cancellation != "context_canceled" ||
		fixture.EnteredSequence < 1 || fixture.ReleaseSequence != 0 ||
		fixture.SettledSequence <= fixture.EnteredSequence || held.TrialID != fixture.TrialID ||
		held.SessionID != fixture.SessionID || held.Slot != fixture.Slot || held.State != "active" ||
		held.Cancellation != "" || held.EnteredSequence != fixture.EnteredSequence ||
		held.ReleaseSequence != 0 || held.SettledSequence != 0 {
		return binding, false
	}
	enqueue, ok := decodeStopLog(proof.Guidance.Enqueue)
	if !ok || enqueue.Kind != events.KindAgentInterruptReceived.String() || enqueue.AgentID != agent ||
		enqueue.SessionKey != key || !validID(enqueue.TurnID) || enqueue.InterruptKind != "steering" ||
		enqueue.Role != "user" || enqueue.QueueDepth < 1 || enqueue.MessageID != proof.PrimaryRequestID {
		return binding, false
	}
	var start, end, terminal activeToolLogEvent
	starts, ends, terminals, enqueues := 0, 0, 0, 0
	seen := map[string]bool{}
	for _, raw := range proof.Log {
		event, valid := decodeStopLog(raw)
		if !valid || seen[event.ID] {
			return binding, false
		}
		seen[event.ID] = true
		if event.ID == enqueue.ID {
			if raw != proof.Guidance.Enqueue {
				return binding, false
			}
			enqueues++
		}
		if event.TurnID != enqueue.TurnID {
			continue
		}
		if event.AgentID != agent || event.SessionKey != key || event.MessageID != proof.PrimaryRequestID {
			return binding, false
		}
		switch event.Kind {
		case events.KindAgentToolExecStart.String():
			starts++
			start = event
		case events.KindAgentToolExecEnd.String():
			ends++
			end = event
		case events.KindAgentTurnEnd.String():
			terminals++
			terminal = event
		case events.KindAgentSteeringInjected.String():
			return binding, false
		}
	}
	if starts != 1 || ends > 1 || terminals != 1 || enqueues != 1 ||
		start.Tool != activeToolNativeName || !boundedIdentity(start.ToolCallID, 128) ||
		terminal.Status != "aborted" || terminal.LLMCalls != 1 ||
		enqueue.Time.Before(start.Time) || terminal.Time.Before(enqueue.Time) {
		return binding, false
	}
	// Hard abort commits its terminal tool result before returning early, without
	// the normal exec_end event. The full oracle requires that canonical result.
	if ends == 1 && (end.Tool != activeToolNativeName || end.ToolCallID != start.ToolCallID || !end.IsError ||
		end.ToolCallGeneration != start.ToolCallGeneration || end.Time.Before(enqueue.Time) ||
		terminal.Time.Before(end.Time)) {
		return binding, false
	}
	return nativeStopAbortBinding{turnID: enqueue.TurnID, callID: start.ToolCallID, endedAt: terminal.Time}, true
}
