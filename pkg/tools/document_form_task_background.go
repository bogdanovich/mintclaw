package tools

import (
	"context"
	"encoding/json"

	"github.com/bogdanovich/mintclaw/pkg/document"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const formTaskIndependentReplyPrefix = "The operator sent an independent message, " +
	"not an answer selecting the pending request. " +
	"The PDF draft and original pending request are preserved. " +
	"Respond to this message using recent ordinary conversation when it is a follow-up; " +
	"do not execute or infer consent for the original request or attachment:\n"

const formTaskIndependentPrefix = "The operator is continuing ordinary conversation alongside a background PDF draft. " +
	"Respond to this message using recent ordinary conversation when it is a follow-up. " +
	"Protected form receipts are not the user's conversational question; do not treat them as its topic. " +
	"The draft stays protected. This message does not authorize any other pending request or attachment:\n"

const formTaskSwitchPrefix = "The operator selected a new task. The previous PDF draft is preserved. " +
	"Follow only this newly authorized request (and its attached media); ask its purpose if unstated:\n"

func (tool *DocumentTool) formTaskIndependentReplyResult(
	ctx context.Context, owner document.FormJobOwner, view document.FormTaskRouteSnapshot,
) *toolshared.ToolResult {
	if len(view.Answer.Text)+len(formTaskIndependentReplyPrefix) > toolshared.ToolLiveContextTextAllowance(ctx) {
		return documentFormTaskResult(view, "handoff_context_capacity")
	}
	handoff, err := tool.formJobs.GrantFormTaskIndependentReply(ctx, view.Answer.Reference, owner)
	if err != nil {
		return documentFormToolError(err)
	}
	// This is a reply-only live grant. The original route remains Question;
	// never claim its original request/media were authorized or executed.
	projection := safeDocumentFormTask{
		SchemaVersion: documentFormTaskSchema, Operation: "form", FormAction: "route",
		Reference: view.Answer.Reference, State: document.FormTaskGranted,
		HandoffFresh: !handoff.Reused,
	}
	if handoff.Reused {
		projection.Reason = "handoff_uncertain"
	}
	encoded, _ := json.Marshal(projection)
	result := &toolshared.ToolResult{ForLLM: string(encoded)}
	if !handoff.Reused {
		result.ContextText = formTaskIndependentReplyPrefix + handoff.Text
		result.ContextInput = &toolshared.ToolContextInput{Text: handoff.Text}
	}
	return result
}
