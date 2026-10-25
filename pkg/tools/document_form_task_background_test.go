package tools

import (
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
)

func TestDocumentFormBackgroundConversationDoesNotReopenAnUnrelatedScopeQuestion(t *testing.T) {
	tool, ctx, owner, guard, model := taskWorkflowFixture(t, document.FormTaskAsk)
	pending, err := tool.AdmitProtectedFormTask(ctx, guard, "unclear-purpose", "PRIVATE_PENDING_PURPOSE", nil)
	if err != nil {
		t.Fatal(err)
	}
	question := executeTaskRoute(tool, ctx, pending)
	if question.IsError || question.Control.Suspension == nil {
		t.Fatal("genuine uncertainty did not retain a protected question")
	}
	if strings.Contains(question.Control.Suspension.Questions[0].Question, "attachment") {
		t.Fatal("text-only uncertainty claimed there was an attachment")
	}
	model.route = func(view document.FormTaskRouteSnapshot) (document.FormTaskRouteInterpretation, error) {
		return document.FormTaskRouteInterpretation{
			Action:   document.FormTaskIndependent,
			Evidence: document.FormIntakeLiteral{Text: view.Text, Occurrence: 1},
		}, nil
	}
	for _, entry := range []struct{ identity, text string }{
		{"ping", "Are you there?"}, {"math", "What is two plus two?"}, {"follow-up", "Why?"},
	} {
		ref, admitErr := tool.AdmitProtectedFormTask(ctx, guard, entry.identity, entry.text, nil)
		if admitErr != nil {
			t.Fatal(admitErr)
		}
		result := executeTaskRoute(tool, ctx, ref)
		if result.IsError || result.Control.Suspension != nil || !strings.Contains(result.ContextText, entry.text) ||
			strings.Contains(result.ContextText, "PRIVATE_PENDING_PURPOSE") || result.ContextInput == nil {
			t.Fatal("ordinary conversation was modal, lost its exact request or released another request")
		}
		follow, followErr := tool.ToolResultFollowup(result)
		if followErr != nil || follow != nil {
			t.Fatal("fresh side conversation remained in protected-only mode")
		}
	}
	model.route = func(document.FormTaskRouteSnapshot) (document.FormTaskRouteInterpretation, error) {
		return document.FormTaskRouteInterpretation{Action: document.FormTaskContinue}, nil
	}
	ref, err := tool.AdmitProtectedFormTask(ctx, guard, "ordinary-return", "Continue the previous form.", nil)
	if err != nil {
		t.Fatal(err)
	}
	returned := executeTaskRoute(tool, ctx, ref)
	if returned.IsError || returned.ContextText != "" || returned.Control.Suspension != nil ||
		strings.Contains(returned.ForLLM, pending) {
		t.Fatal("return to the form automatically reopened an unrelated pending scope question")
	}
	view, err := tool.formJobs.ReadFormTaskRoute(ctx, pending, owner)
	if err != nil || view.State != document.FormTaskQuestion || view.Text != "PRIVATE_PENDING_PURPOSE" {
		t.Fatal("ordinary return erased or authorized the old unresolved request")
	}
	progress, err := tool.formJobs.FindFormIntake(ctx, owner)
	if err != nil || progress == nil || progress.JobID != guard.JobID || progress.ModeID != guard.ModeID {
		t.Fatal("ordinary return selected another draft")
	}
}
