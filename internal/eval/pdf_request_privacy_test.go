package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	protocol "github.com/bogdanovich/mintclaw/pkg/channels/mintclaw"
	"github.com/bogdanovich/mintclaw/pkg/events"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
	"github.com/bogdanovich/mintclaw/pkg/outbox"
)

type pdfRequestMutationControl struct {
	*pdfConversation
	beforeInput func()
}

func (control *pdfRequestMutationControl) request(
	ctx context.Context, action Action, prompt Event,
) (protocol.MintClawMessage, bool, error) {
	if prompt.Kind != "" && control.beforeInput != nil {
		control.beforeInput()
		control.beforeInput = nil
	}
	return control.pdfConversation.request(ctx, action, prompt)
}

func TestPDFRequestAdmissionKeepsConfirmedPrivacyFailuresHard(t *testing.T) {
	for _, surface := range []string{"registry", "outbox", "unavailable"} {
		for _, action := range []Action{{Kind: "protected_answer", Fact: "contact-reference"}, {Kind: "clarify"}} {
			t.Run(surface+"/"+action.Kind, func(t *testing.T) {
				prepared := newPDFTrialFixture(t)
				t.Cleanup(func() { _ = prepared.release(true) })
				record, prompt, _ := nativePDFPromptFixture(t, prepared)
				item := prepared.item
				item.Case.Driver.Mode, item.Case.Driver.Persona = "scripted", ""
				item.Case.Driver.Steps = []Step{{WaitFor: Expectation{Kind: "question"}, Action: action}}
				item.Case.Rubric = nil
				control := &pdfRequestMutationControl{pdfConversation: &pdfConversation{prepared: prepared}}
				control.beforeInput = func() {
					if control.jobID == "" {
						t.Fatal("control mutated evidence before the first native prompt observation")
					}
					path := filepath.Join(prepared.workspace, "state", "outbox", record.PromptDeliveryID+".json")
					if surface == "unavailable" {
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
						return
					}
					canary := prepared.item.Case.Driver.Facts["contact-reference"]
					var data []byte
					if surface == "registry" {
						path = interactions.WorkspaceStorePath(prepared.workspace)
						original, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						snapshot, err := interactions.InspectSnapshot(original)
						if err != nil || len(snapshot.Records) != 1 {
							t.Fatal("control registry invalid", err)
						}
						snapshot.Records[0].PromptSummary = canary
						data, err = json.Marshal(snapshot)
						if err != nil {
							t.Fatal(err)
						}
						if _, err = interactions.InspectSnapshot(data); err != nil {
							t.Fatal("leak control is not a valid native snapshot", err)
						}
					} else {
						intent, err := prepared.observeOutbox(record.PromptDeliveryID)
						if err != nil {
							t.Fatal(err)
						}
						intent.Message.Content = canary
						data, err = json.Marshal(intent)
						if err != nil {
							t.Fatal(err)
						}
						if _, err = outbox.InspectIntent(data, record.PromptDeliveryID); err != nil {
							t.Fatal("leak control is not a valid native outbox", err)
						}
					}
					if err := os.WriteFile(path, data, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				transport := newFakeConversation()
				transport.onSend = func(request protocol.MintClawMessage, number int) {
					response := testResponse(request, number)
					response.Payload[protocol.PayloadKeyAgentID] = prepared.agent
					response.Payload[protocol.PayloadKeyTraceScopes] = []events.TraceScope{{
						Workspace: prepared.workspace, TurnID: request.ID,
					}}
					response.Payload[protocol.PayloadKeyControls] = "prompt"
					response.Payload[protocol.PayloadKeyInteraction] = "question"
					response.Payload[protocol.PayloadKeyInteractionID] = prompt.InteractionID
					response.Payload[protocol.PayloadKeyInteractionShortID] = prompt.InteractionShortID
					response.Payload[protocol.PayloadKeyMessageID] = prompt.MessageID
					transport.events <- response
				}
				trial := runDomainConversation(t.Context(), transport, item, prepared.sessionID,
					prepared.agent, prepared.workspace, conversationRoles{}, control)
				data, err := json.Marshal(trial)
				want, code := Fail, "private_native_evidence"
				if surface == "unavailable" {
					want, code = Inconclusive, "domain_input_not_admitted"
				}
				if err != nil || trial.Verdict != want || trial.ExecutionVerdict != want || trial.Code != code ||
					len(transport.sent) != 1 || trial.EvidenceEligible || transport.closed != 1 ||
					bytes.Contains(data, []byte(prepared.item.Case.Driver.Facts["contact-reference"])) {
					t.Fatal(
						"request admission softened a privacy failure, replayed input or leaked the canary",
						trial.Code,
					)
				}
			})
		}
	}
}
