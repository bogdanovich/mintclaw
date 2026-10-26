package eval

import (
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
)

type FinalDeliveryWireFrame struct {
	SessionID string `json:"session_id"`
	Event     Event  `json:"event"`
}

type FinalDeliveryWireEvidence struct {
	Version  int                      `json:"version"`
	Code     string                   `json:"code"`
	Complete bool                     `json:"complete"`
	Bytes    int                      `json:"bytes"`
	Requests []string                 `json:"requests"`
	Frames   []FinalDeliveryWireFrame `json:"frames"`
}

// The SDK invokes this before conversation request filtering. Retain both
// owned request IDs until Close joins the reader, including late old finals.
type finalDeliveryWire struct {
	mu        sync.Mutex
	sessionID string
	requests  []string
	frames    []FinalDeliveryWireFrame
	count     int
	bytes     int
	code      string
}

func (wire *finalDeliveryWire) register(request protocol.MintClawMessage) error {
	wire.mu.Lock()
	defer wire.mu.Unlock()
	if wire.code != "" || len(wire.requests) >= 2 || !validID(request.ID) ||
		request.SessionID != wire.sessionID || slices.Contains(wire.requests, request.ID) {
		wire.code = "wire_request_limit"
		return errNativeEvidence
	}
	wire.requests = append(wire.requests, request.ID)
	return nil
}

func (wire *finalDeliveryWire) observe(message protocol.MintClawMessage) error {
	if message.Type != protocol.TypeMessageCreate && message.Type != protocol.TypeMessageUpdate {
		return nil
	}
	wire.mu.Lock()
	defer wire.mu.Unlock()
	slot := slices.Index(wire.requests, payloadString(message.Payload, protocol.PayloadKeyRequestID))
	if slot < 0 {
		if message.SessionID == wire.sessionID {
			event, err := responseEvent(message, 1)
			if err != nil || event.Kind == "final" {
				wire.code = "unexpected_final_request"
				return errNativeEvidence
			}
		}
		return nil
	}
	data, err := json.Marshal(message)
	wire.bytes += len(data)
	wire.count++
	if err != nil || wire.code != "" || wire.count > 256 || wire.bytes > 2*1024*1024 {
		wire.code = "wire_limit"
		return errNativeEvidence
	}
	event, err := responseEvent(message, slot+1)
	if err != nil || message.SessionID != wire.sessionID {
		wire.code = "wire_scope_invalid"
		return errNativeEvidence
	}
	if event.Kind == "auxiliary" {
		return nil
	}
	event.ID = fmt.Sprintf("wire-%06d", len(wire.frames)+1)
	wire.frames = append(wire.frames, FinalDeliveryWireFrame{SessionID: message.SessionID, Event: event})
	return nil
}

func (wire *finalDeliveryWire) snapshot(closed bool) *FinalDeliveryWireEvidence {
	wire.mu.Lock()
	defer wire.mu.Unlock()
	code := wire.code
	complete := closed && code == "" && len(wire.requests) == 2
	if complete {
		code = "observed"
	} else if code == "" {
		code = "wire_incomplete"
	}
	return &FinalDeliveryWireEvidence{
		Version: Version, Code: code, Complete: complete, Bytes: wire.bytes,
		Requests: append([]string{}, wire.requests...), Frames: append([]FinalDeliveryWireFrame{}, wire.frames...),
	}
}

func finalDeliveryWireCode(trial Trial, item Case, workspace string) string {
	if trial.FinalDelivery == nil || trial.FinalDelivery.Wire == nil {
		return "final_delivery_wire_unavailable"
	}
	wire := trial.FinalDelivery.Wire
	if wire.Version != Version || wire.Code != "observed" || !wire.Complete || wire.Bytes < 1 ||
		wire.Bytes > 2*1024*1024 || len(wire.Requests) != 2 || len(wire.Frames) < 2 || len(wire.Frames) > 256 {
		return "final_delivery_wire_unavailable"
	}
	data, err := json.Marshal(wire)
	if err != nil || len(data) > 3*1024*1024 {
		return "final_delivery_wire_limit"
	}
	inputs, finals := []Event{}, []Event{}
	for _, event := range trial.Events {
		if event.Kind == "input" {
			inputs = append(inputs, event)
		}
		if event.Kind == "final" {
			finals = append(finals, event)
		}
	}
	if len(inputs) != 2 || len(finals) != 2 {
		return "final_delivery_wire_binding"
	}
	observed := trial
	observed.Events = []Event{}
	matched := 0
	for index, input := range inputs {
		if input.RequestID != wire.Requests[index] {
			return "final_delivery_wire_binding"
		}
		observed.Events = append(observed.Events, input)
		for _, frame := range wire.Frames {
			if frame.SessionID != trial.SessionID {
				return "final_delivery_wire_scope"
			}
			if frame.Event.RequestID == input.RequestID {
				observed.Events = append(observed.Events, frame.Event)
				matched++
			}
		}
	}
	if matched != len(wire.Frames) || !validTrialConversation(observed, trial.AgentID, workspace) {
		return "final_delivery_wire_binding"
	}
	actual, code := finalDeliveryRevisions(observed, item, workspace)
	if code != "" {
		return code
	}
	for index, final := range actual {
		final.ID = finals[index].ID
		got, gotErr := json.Marshal(final)
		want, wantErr := json.Marshal(finals[index])
		if gotErr != nil || wantErr != nil || string(got) != string(want) {
			return "final_delivery_wire_projection"
		}
	}
	return ""
}
