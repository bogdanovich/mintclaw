package eval

import "context"

type UserDecision struct {
	Action Action    `json:"action"`
	Code   string    `json:"code"`
	Call   *RoleCall `json:"call,omitempty"`
}

func nextModelAction(
	ctx context.Context, user *ModelRole, item Case, events []Event, pendingKind string, remaining int,
) UserDecision {
	result := UserDecision{Code: "user_unavailable"}
	if user == nil || user.identity.Role != "user" {
		return result
	}
	system, version := userSystemPrompt, UserPromptVersion
	parse := ParseUserAction
	if item.PDF != nil && item.PDF.Output == "required" {
		system, version, parse = pdfUserSystemPrompt, PDFUserPromptVersion, parsePDFUserAction
	}
	input, err := userInputWithPrompt(item, events, pendingKind, remaining, system)
	if err != nil {
		result.Code = "ineligible_user_evidence"
		return result
	}
	output, call, err := user.call(ctx, version, system, input, item.Driver.Facts)
	result.Call = &call
	if err != nil {
		result.Code = "user_call_failed"
		return result
	}
	handles := make([]string, 0, len(item.Driver.Facts))
	for handle := range item.Driver.Facts {
		handles = append(handles, handle)
	}
	action, err := parse([]byte(output), handles, pendingKind)
	if err != nil || privateJSONValue(action, item.Driver.Facts) {
		result.Code = "invalid_user_action"
		return result
	}
	call.ActionKind = action.Kind
	result.Action, result.Code = action, "user_action_received"
	return result
}
