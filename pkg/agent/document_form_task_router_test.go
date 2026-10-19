package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/providers/protocoltypes"
)

func taskRoutingPrivateTestView() document.FormTaskRouteSnapshot {
	return document.FormTaskRouteSnapshot{
		State: document.FormTaskPending, Passes: 1,
		Text: "Help with this different task instead.", MediaRefs: []string{"media://PRIVATE_ATTACHMENT"},
		Context: document.FormIntakeContext{
			RoleContext: "The user is the borrower", Question: "Provide the details known so far",
		},
	}
}

func TestDocumentFormTaskQuestionFreeReplyUsesPrivateReplyContract(t *testing.T) {
	view := taskRoutingPrivateTestView()
	view.State = document.FormTaskQuestion
	view.Answer = &document.FormTaskRouteAnswer{State: "committed", Passes: 1, Text: "Продолжим прежнюю форму."}
	wire := `{"action":"keep_form","evidence":{"text":"` + view.Answer.Text + `","occurrence":1}}`
	provider := &documentAuditTestProvider{response: &providers.LLMResponse{Content: wire}}
	auditor := &documentFormAuditor{models: map[string]documentFormAuditModel{
		"configured": {provider: provider, model: "private-model"},
	}}
	result, err := auditor.InterpretFormTaskRoute(t.Context(), "configured", view)
	if err != nil || result.Action != document.FormTaskKeep || !provider.sensitive || len(provider.tools) != 0 ||
		provider.messages[0].Content != documentFormTaskReplyPrompt ||
		!strings.Contains(provider.messages[1].Content, view.Answer.Text) ||
		strings.Contains(provider.messages[1].Content, "PRIVATE_ATTACHMENT") {
		t.Fatal("free reply bypassed the private scope contract")
	}
	if _, err = decodeDocumentFormTaskRoute(wire, view.Answer.Text); err == nil {
		t.Fatal("keep-form reply was accepted outside a native question")
	}
}

func TestDocumentFormTaskRouteUsesOnlyBoundedPrivateCurrentTaskContext(t *testing.T) {
	view := taskRoutingPrivateTestView()
	wire := `{"action":"switch_task","evidence":{"text":"` + view.Text + `","occurrence":1}}`
	provider := &documentAuditTestProvider{response: &providers.LLMResponse{Content: wire}}
	auditor := &documentFormAuditor{models: map[string]documentFormAuditModel{
		"configured": {provider: provider, model: "private-model"},
	}}
	result, err := auditor.InterpretFormTaskRoute(t.Context(), "configured", view)
	if err != nil || result.Action != document.FormTaskSwitch || result.Evidence.Text != view.Text {
		t.Fatal("whole-message routing evidence was not preserved")
	}
	if provider.calls != 1 || !provider.sensitive || len(provider.tools) != 0 || len(provider.messages) != 2 ||
		provider.options["native_search"] != false || provider.options["temperature"] != 0 ||
		!protocoltypes.PromptCachePlanOptionPresent(provider.options) ||
		strings.Contains(provider.messages[1].Content, "PRIVATE_ATTACHMENT") ||
		!strings.Contains(provider.messages[1].Content, `"attachment_count":1`) ||
		!strings.Contains(provider.messages[1].Content, view.Text) {
		t.Fatal("routing bypassed private tool-free context isolation or disclosed attachment authority")
	}
	encoded, err := json.Marshal(result)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("private routing interpretation escaped ordinary JSON")
	}
}

func TestDocumentFormTaskRouteRejectsFabricatedPartialUnknownOrTrailingEvidence(t *testing.T) {
	view := taskRoutingPrivateTestView()
	valid := `{"action":"switch_task","evidence":{"text":"` + view.Text + `","occurrence":1}}`
	for _, content := range []string{
		"", "{}", "```json\n" + valid + "\n```", valid + "{}",
		strings.Replace(valid, view.Text, "different task", 1),
		strings.Replace(valid, `"occurrence":1`, `"occurrence":2`, 1),
		strings.Replace(valid, `"switch_task"`, `"approve"`, 1),
		strings.Replace(valid, `"switch_task"`, `"continue_form"`, 1),
		strings.Replace(valid, `"evidence":`, `"secret":"PRIVATE","evidence":`, 1),
		`{"action":"switch_task"}`, strings.Repeat("x", maxDocumentIntakeWireBytes+1),
	} {
		result, err := decodeDocumentFormTaskRoute(content, view.Text)
		if !errors.Is(err, document.ErrFormAuditUnavailable) || result.Action != "" {
			t.Fatal("rejected private response returned routing authority")
		}
	}
	for _, content := range []string{valid, " \n" + valid + "\n", `{"action":"continue_form"}`, `{"action":"ask"}`} {
		if _, err := decodeDocumentFormTaskRoute(content, view.Text); err != nil {
			t.Fatal("valid single JSON routing response was rejected")
		}
	}
}

func TestDocumentFormTaskRouteInvalidContextModelAndProviderErrorsStayClosed(t *testing.T) {
	view := taskRoutingPrivateTestView()
	provider := &documentAuditTestProvider{err: errors.New("PRIVATE_ROUTE_PROVIDER_ERROR")}
	auditor := &documentFormAuditor{models: map[string]documentFormAuditModel{
		"configured": {provider: provider, model: "private-model"},
	}}
	for _, alias := range []string{"missing", "configured"} {
		if _, err := auditor.InterpretFormTaskRoute(
			t.Context(),
			alias,
			view,
		); !errors.Is(err, document.ErrFormAuditUnavailable) ||
			strings.Contains(err.Error(), "PRIVATE_ROUTE") {
			t.Fatal("routing error disclosed private provider data")
		}
	}
	for _, mutate := range []func(*document.FormTaskRouteSnapshot){
		func(v *document.FormTaskRouteSnapshot) { v.Passes = 0 },
		func(v *document.FormTaskRouteSnapshot) { v.Passes = document.MaxFormTaskRoutingPasses + 1 },
		func(v *document.FormTaskRouteSnapshot) { v.State = document.FormTaskGranted },
		func(v *document.FormTaskRouteSnapshot) { v.Text, v.MediaRefs = "", nil },
		func(v *document.FormTaskRouteSnapshot) {
			v.Text = strings.Repeat("x", document.MaxFormIntakeInputBytes+1)
		},
		func(v *document.FormTaskRouteSnapshot) { v.Context.Question = strings.Repeat("x", 8*1024+1) },
	} {
		changed := view
		mutate(&changed)
		if _, err := encodeDocumentFormTaskRoute(changed); !errors.Is(err, document.ErrFormAuditUnavailable) {
			t.Fatal("unreserved or unbounded routing context reached the private model")
		}
	}
}
