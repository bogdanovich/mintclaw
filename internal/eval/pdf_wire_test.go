package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/events"
)

func pdfWireControl(t *testing.T) (*pdfConversation, Trial, protocol.MintClawMessage) {
	t.Helper()
	domain, settled := settledPDFControl(t)
	domain.wire = &pdfWireObservation{workspace: domain.prepared.workspace}
	trial := Trial{
		SessionID: domain.prepared.sessionID, AgentID: domain.prepared.agent, SessionKey: domain.owner.RouteSessionKey,
		Verdict: Inconclusive, ExecutionVerdict: Pass,
		PDF: &PDFTrialEvidence{Settlement: &PDFSettlementEvidence{
			PlatformMessageIDs: settled.delivery.PlatformMessageIDs, OutboxDeliveryID: settled.delivery.ID,
			ArtifactRef: settled.operation.ArtifactRef,
		}},
		Checks: gradeConversation(domain.prepared.item.Case.Checks, nil, Event{}),
	}
	message := protocol.MintClawMessage{
		Type: protocol.TypeMessageCreate, SessionID: trial.SessionID,
		Payload: map[string]any{
			protocol.PayloadKeyMessageID:  settled.delivery.PlatformMessageIDs[0],
			protocol.PayloadKeyDeliveryID: settled.delivery.ID,
			protocol.PayloadKeyAgentID:    trial.AgentID,
			protocol.PayloadKeySessionKey: trial.SessionKey,
			protocol.PayloadKeyContent:    "private-caption-not-retained",
			protocol.PayloadKeyTraceScopes: []events.TraceScope{
				{Workspace: domain.prepared.workspace, TurnID: "wire-control"},
			},
			"attachments": []any{
				map[string]any{
					"type":         "file",
					"content_type": "application/pdf",
					"filename":     "private-filename-not-retained.pdf",
					"url": "/mintclaw/media/" + url.PathEscape(
						strings.TrimPrefix(settled.operation.ArtifactRef, "media://"),
					),
				},
			},
		},
	}
	return domain, trial, message
}

func TestPDFWireBindsActualMessagesWithoutGrantingWholeDeliveryQualification(t *testing.T) {
	for _, mode := range []string{
		"observed", "same-id-retry", "no-media", "no-receipt", "not-native-id", "incomplete", "missing-route",
		"extra-delivery", "conflict", "agent", "session", "route", "delivery", "workspace", "url", "type", "mime", "parts",
	} {
		t.Run(mode, func(t *testing.T) {
			domain, trial, message := pdfWireControl(t)
			part := message.Payload["attachments"].([]any)[0].(map[string]any)
			complete, expected := true, "wire_media_observed"
			switch mode {
			case "no-media", "no-receipt", "not-native-id", "incomplete", "missing-route":
				expected = "wire_media_unavailable"
			case "extra-delivery":
				expected = "multiple_wire_deliveries"
			case "conflict":
				expected = "wire_media_conflict"
			case "agent", "session", "route", "delivery", "workspace", "url", "type", "mime", "parts":
				expected = "wire_media_binding_mismatch"
			}
			switch mode {
			case "agent":
				message.Payload[protocol.PayloadKeyAgentID] = "foreign-agent"
			case "session":
				message.SessionID = "foreign-session"
			case "route":
				message.Payload[protocol.PayloadKeySessionKey] = "foreign-route"
			case "delivery":
				message.Payload[protocol.PayloadKeyDeliveryID] = "foreign-delivery"
			case "workspace":
				message.Payload[protocol.PayloadKeyTraceScopes] = []events.TraceScope{
					{Workspace: "/foreign", TurnID: "turn"},
				}
			case "url", "type", "mime":
				field := mode
				value := "https://foreign.invalid/not-followed"
				switch mode {
				case "type":
					value = "image"
				case "mime":
					field, value = "content_type", "image/png"
				}
				part[field] = value
			case "parts":
				message.Payload["attachments"] = []any{part, part}
			case "missing-route":
				delete(message.Payload, protocol.PayloadKeyAgentID)
			case "no-receipt":
				trial.PDF.Settlement = nil
			case "not-native-id":
				message.Payload[protocol.PayloadKeyMessageID] = "unsettled-platform-id"
			case "incomplete":
				complete = false
			}
			if mode != "no-media" {
				if err := domain.wire.observe(message); err != nil {
					t.Fatal("control observation failed", err)
				}
			}
			if mode == "same-id-retry" || mode == "extra-delivery" || mode == "conflict" {
				switch mode {
				case "extra-delivery":
					message.Payload[protocol.PayloadKeyMessageID] = "second-logical-message"
				case "conflict":
					part["url"] = "/mintclaw/media/conflicting-object"
				}
				if err := domain.wire.observe(message); err != nil {
					t.Fatal("second observation failed", err)
				}
			}
			domain.concludeWire(&trial, complete)
			if trial.PDF.Wire == nil || trial.PDF.Wire.Code != expected || trial.EvidenceEligible ||
				trial.Verdict == Pass || len(domain.wire.messages) != 0 {
				t.Fatal("wire witness was wrong, retained candidates, or granted whole qualification", trial.PDF.Wire)
			}
			if strings.Contains(expected, "mismatch") || expected == "multiple_wire_deliveries" ||
				expected == "wire_media_conflict" {
				if trial.Verdict != Fail || trial.ExecutionVerdict != Fail {
					t.Fatal("known wire violation was not a hard failure")
				}
			}
			if expected == "wire_media_observed" && (trial.PDF.Wire.MatchingMessages != 1 ||
				trial.PDF.Wire.MessageID != trial.PDF.Settlement.PlatformMessageIDs[0]) {
				t.Fatal("observed logical delivery was not bound to the native platform ID")
			}
			for _, check := range trial.Checks {
				if check.Verdict == Pass {
					t.Fatal("wire metadata alone passed an independent required check")
				}
			}
			data, err := json.Marshal(trial.PDF.Wire)
			if err != nil || strings.Contains(string(data), "private-caption") ||
				strings.Contains(
					string(data),
					"private-filename",
				) || strings.Contains(string(data), "/mintclaw/media/") {
				t.Fatal("wire observation published untrusted content")
			}
		})
	}
}

func TestPDFWireBoundsCannotHideAnObservedDuplicate(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		domain, trial, message := pdfWireControl(t)
		for index := range maxPDFWireMessages + 1 {
			if !duplicate || index > 1 {
				message.SessionID = "foreign-session"
			}
			if index > 0 || !duplicate {
				message.Payload[protocol.PayloadKeyMessageID] = fmt.Sprintf("bounded-wire-%d", index)
			}
			err := domain.wire.observe(message)
			if (index == maxPDFWireMessages) != errors.Is(err, errPDFWire) {
				t.Fatal("wire observation was not bounded", err)
			}
		}
		domain.concludeWire(&trial, false)
		want := "wire_media_limit"
		if duplicate {
			want = "multiple_wire_deliveries"
		}
		if trial.PDF.Wire.Code != want || trial.PDF.Wire.Complete ||
			trial.PDF.Wire.ObservedMessages != maxPDFWireMessages ||
			duplicate && trial.Verdict != Fail {
			t.Fatal("limit erased a known failure or became complete coverage", trial.PDF.Wire)
		}
	}
}

func TestPDFWireConflictingUnboundIDCannotHideAnotherDelivery(t *testing.T) {
	for _, mode := range []string{"artifact", "route"} {
		for _, order := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
			t.Run(fmt.Sprintf("%s/%v", mode, order), func(t *testing.T) {
				domain, trial, message := pdfWireControl(t)
				part := message.Payload["attachments"].([]any)[0].(map[string]any)
				path := part["url"].(string)
				for _, variant := range order {
					message.Payload[protocol.PayloadKeyMessageID] = trial.PDF.Settlement.PlatformMessageIDs[0]
					message.SessionID, part["url"] = trial.SessionID, path
					if variant != 0 {
						message.Payload[protocol.PayloadKeyMessageID] = "conflicting-unbound-id"
					}
					if variant == 1 {
						if mode == "artifact" {
							part["url"] = "/mintclaw/media/another-artifact"
						} else {
							message.SessionID = "foreign-session"
						}
					}
					if err := domain.wire.observe(message); err != nil {
						t.Fatal("bounded variant observation failed", err)
					}
				}
				domain.concludeWire(&trial, true)
				if trial.PDF.Wire.Code != "multiple_wire_deliveries" || trial.PDF.Wire.MatchingMessages != 2 ||
					trial.PDF.Wire.ObservedMessages != 2 || trial.Verdict != Fail || trial.ExecutionVerdict != Fail ||
					trial.EvidenceEligible {
					t.Fatal("conflicting variant concealed an observed delivery", trial.PDF.Wire)
				}
			})
		}
	}
}

func TestPDFWireVariantBoundsPreserveKnownDuplicateAndDeduplicateRetries(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		domain, trial, message := pdfWireControl(t)
		if err := domain.wire.observe(message); err != nil {
			t.Fatal(err)
		}
		part := message.Payload["attachments"].([]any)[0].(map[string]any)
		path := part["url"].(string)
		message.Payload[protocol.PayloadKeyMessageID] = "bounded-conflicting-id"
		for index := range maxPDFWireVariants + 1 {
			part["url"] = fmt.Sprintf("/mintclaw/media/another-artifact-%d", index)
			if duplicate && index == 0 {
				part["url"] = path
			}
			err := domain.wire.observe(message)
			if (index == maxPDFWireVariants) != errors.Is(err, errPDFWire) {
				t.Fatal("conflicting variants were not bounded", err)
			}
			if index < maxPDFWireVariants {
				if err := domain.wire.observe(message); err != nil {
					t.Fatal("identical retry consumed the variant budget", err)
				}
			}
		}
		if len(domain.wire.messages["bounded-conflicting-id"]) != maxPDFWireVariants {
			t.Fatal("variant retention exceeded its bound")
		}
		domain.concludeWire(&trial, false)
		want := "wire_media_limit"
		if duplicate {
			want = "multiple_wire_deliveries"
		}
		if trial.PDF.Wire.Code != want || trial.PDF.Wire.Complete || trial.PDF.Wire.ObservedMessages != 2 ||
			duplicate && trial.Verdict != Fail {
			t.Fatal("variant limit erased a known duplicate or granted complete coverage", trial.PDF.Wire)
		}
	}
}

func TestPDFWireMalformedMetadataDoesNotBecomeAConfirmedLeakOrDelivery(t *testing.T) {
	for _, mode := range []string{"array", "identity", "type", "mime", "url", "count", "message-type"} {
		t.Run(mode, func(t *testing.T) {
			domain, trial, message := pdfWireControl(t)
			part := message.Payload["attachments"].([]any)[0].(map[string]any)
			switch mode {
			case "array":
				message.Payload["attachments"] = "not-an-array"
			case "identity":
				message.Payload[protocol.PayloadKeyMessageID] = 123
			case "type":
				part["type"] = true
			case "mime":
				part["content_type"] = ";invalid"
			case "url":
				part["url"] = strings.Repeat("x", 513)
			case "count":
				message.Payload["attachments"] = make([]any, 17)
			case "message-type":
				message.Type = protocol.TypePong
			}
			if err := domain.wire.observe(message); !errors.Is(err, errPDFWire) {
				t.Fatal("malformed metadata was admitted", err)
			}
			domain.concludeWire(&trial, false)
			if trial.PDF.Wire.Code != "wire_media_unavailable" || trial.PDF.Wire.Complete ||
				trial.Verdict != Inconclusive {
				t.Fatal("malformed metadata became a confirmed leak or passing delivery", trial.PDF.Wire)
			}
		})
	}
}
