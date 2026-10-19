package eval

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/interactions"
)

const (
	browserFormAssessmentScope  = "owned_post_stop_browser_form_v1"
	browserFormApprovalScope    = "fixture_form_submit_allow_once_v1"
	browserFormConfirmationText = "I confirm the reviewed fixture form. Submit this form once."
)

type BrowserFormConfirmationEvidence struct {
	Scope                string `json:"scope"`
	InteractionID        string `json:"interaction_id"`
	PromptMessageID      string `json:"prompt_message_id"`
	PromptRequestID      string `json:"prompt_request_id"`
	PromptDeliveryID     string `json:"prompt_delivery_id"`
	RequestID            string `json:"request_id"`
	InputSetSHA256       string `json:"input_set_sha256"`
	PageStateSHA256      string `json:"page_state_sha256"`
	SessionID            string `json:"session_id"`
	TabID                string `json:"tab_id"`
	ControllerGeneration uint64 `json:"controller_generation"`
	WaitingRevision      int64  `json:"waiting_revision"`
	ObservedRevision     int64  `json:"observed_revision"`
	RequestedAt          int64  `json:"requested_at"`
	ReceivedAt           int64  `json:"received_at"`
	ResolvedAt           int64  `json:"resolved_at"`
}

type BrowserFormEvidence struct {
	Version                int                              `json:"version"`
	Scope                  string                           `json:"scope"`
	Code                   string                           `json:"code"`
	Confirmation           *BrowserFormConfirmationEvidence `json:"confirmation,omitempty"`
	Approval               *BrowserApprovalEvidence         `json:"approval,omitempty"`
	SubmissionActionID     string                           `json:"submission_action_id,omitempty"`
	SubmissionInvocationID string                           `json:"submission_invocation_id,omitempty"`
	ContextRetained        bool                             `json:"context_retained"`
	Corrections            int                              `json:"corrections"`
	ColorSelections        int                              `json:"color_selections"`
	Submissions            int                              `json:"submissions"`
	Native                 *BrowserTaskEvidence             `json:"native,omitempty"`
}

type browserFormReview struct {
	prompt                    Event
	record                    interactions.Record
	session                   browser.Session
	inputSetSHA256, requestID string
	requestedAt               time.Time
}

func isBrowserForm(item Case) bool {
	return item.Browser != nil && slices.Contains([]string{"submit", "followup"}, item.Browser.Scenario)
}

func fixtureSubmitAction(action browser.PreparedAction) bool {
	return action.Action == (browser.Action{Kind: browser.ActionClick, Ref: action.Action.Ref}) &&
		action.Action.Ref != "" && action.ElementRole == "button" && action.ElementName == "Submit form" &&
		action.PolicyEffect == browser.EffectExternalCommit && action.Effect == browser.EffectExternalCommit &&
		action.InputBytes == 0 && action.ArtifactBytes == 0
}

func fixtureColorAction(action browser.PreparedAction) bool {
	return action.Action == (browser.Action{Kind: browser.ActionSelect, Ref: action.Action.Ref}) &&
		action.Action.Ref != "" && action.ElementRole == "combobox" && action.ElementName == "Color" &&
		action.PolicyEffect == browser.EffectLocalEdit && action.Effect == browser.EffectLocalEdit &&
		action.InputBytes > 0 && validDigest(action.InputDigest) && action.ArtifactBytes == 0
}

// Identity excludes mutable registry/delivery revisions. Later resolution of an
// already successful fill must not silently change what the user confirmed.
func browserFormInputSet(inputs *BrowserFormInputsEvidence) string {
	if inputs == nil || len(inputs.Fills) == 0 || len(inputs.Fills) > 64 {
		return ""
	}
	type identity struct {
		Interaction, Answer, Fact, Preparation, Invocation, Action, Trace string
		Accepted, Completed                                               int64
	}
	values := make([]identity, 0, len(inputs.Fills))
	for _, fill := range inputs.Fills {
		values = append(values, identity{
			fill.InteractionID,
			fill.AnswerRequestID,
			fill.Fact,
			fill.PreparedActionID,
			fill.InvocationID,
			fill.ActionSHA256,
			fill.TraceSHA256,
			fill.AcceptedAt,
			fill.CompletedAt,
		})
	}
	encoded, _ := json.Marshal(values)
	return digest(encoded)
}

// Private handles belong to semantic fields, not to question prose or spelling.
// Followup requires a real earlier name and a later final name in one native
// session/tab/controller; an ordinary chat session alone is insufficient.
func browserFormInputContext(item Case, inputs *BrowserFormInputsEvidence) (string, int, bool) {
	if !isBrowserForm(item) || inputs == nil || inputs.Code != "native_inputs_observed" ||
		inputs.Scope != "native_browser_form_inputs_v1" || len(inputs.Fills) < 2 || len(inputs.Fills) > 64 {
		return "", 0, false
	}
	fields := map[string]BrowserField{}
	for _, field := range item.Browser.Fields {
		fields[field.Name] = field
	}
	if fields["name"].Fact == "" || fields["note"].Fact == "" {
		return "", 0, false
	}
	first := inputs.Fills[0].Target
	last := map[string]string{}
	seen := map[string]bool{}
	corrections := 0
	var prior int64
	for _, fill := range inputs.Fills {
		target := fill.Target
		if fill.Scope != browserInputFillScope || fill.Code != "native_fill_observed" || !target.Valid() ||
			target.SessionID != first.SessionID || target.TabID != first.TabID || target.FrameID != "" ||
			target.ControllerGeneration != first.ControllerGeneration || target.ActorID != first.ActorID ||
			target.AgentID != first.AgentID || target.SessionKey != first.SessionKey || target.ExecutionID != first.ExecutionID ||
			target.ElementRole != "textbox" || fill.AcceptedAt < prior || seen[fill.PreparedActionID] {
			return "", 0, false
		}
		seen[fill.PreparedActionID] = true
		prior = fill.AcceptedAt
		var name string
		switch target.ElementNameSHA256 {
		case digest([]byte("Name")):
			name = "name"
		case digest([]byte("Note")):
			name = "note"
		default:
			return "", 0, false
		}
		if !slices.Contains(fields[name].inputFacts(), fill.Fact) {
			return "", 0, false
		}
		if name == "name" && last[name] != "" && last[name] != fill.Fact {
			corrections++
		}
		last[name] = fill.Fact
	}
	if last["name"] != fields["name"].Fact || last["note"] != fields["note"].Fact ||
		item.Browser.Scenario == "followup" && corrections < 1 {
		return "", 0, false
	}
	return first.SessionID, corrections, true
}
