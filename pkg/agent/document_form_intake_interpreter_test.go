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

func privateIntakeTestView() document.FormIntakeInterpretationView {
	return document.FormIntakeInterpretationView{Pass: document.FormIntakePass{
		Input: document.FormIntakeInput{
			Text:    "I am Casey Mint, born 1989-02-15.",
			Context: document.FormIntakeContext{RoleContext: "The user is the borrower."},
		},
		CandidateIDs: []string{"field_name", "field_birth"},
		Fields: []document.FormField{
			{ID: "field_name", Name: "Borrower name", Kind: document.FormFieldText},
			{ID: "field_birth", Name: "Borrower birth date", Kind: document.FormFieldText},
		},
	}}
}

func TestDocumentIntakeInterpreterUsesPrivateCallAndNeverPublicJSON(t *testing.T) {
	provider := &documentAuditTestProvider{response: &providers.LLMResponse{Content: `{"intent":"facts",` +
		`"proposals":[{"field_id":"field_name","literals":[{"text":"Casey Mint","occurrence":1}]},` +
		`{"field_id":"field_birth","literals":[{"text":"1989-02-15","occurrence":1}]}],` +
		`"non_data":[],"findings":[]}`}}
	auditor := &documentFormAuditor{models: map[string]documentFormAuditModel{
		"deliberative": {provider: provider, model: "configured-model"},
	}}
	view := privateIntakeTestView()
	result, err := auditor.InterpretFormIntake(t.Context(), "deliberative", view)
	if err != nil || len(result.Proposals) != 2 || result.Proposals[0].Literals[0].Text != "Casey Mint" {
		t.Fatal("private interpretation did not preserve exact proposed literals")
	}
	if provider.calls != 1 || !provider.sensitive || len(provider.tools) != 0 ||
		provider.options["native_search"] != false || provider.options["temperature"] != 0 ||
		!protocoltypes.PromptCachePlanOptionPresent(provider.options) || len(provider.messages) != 2 ||
		provider.messages[0].Role != "system" || provider.messages[1].Role != "user" ||
		!strings.Contains(provider.messages[1].Content, view.Pass.Input.Text) ||
		strings.Contains(provider.messages[0].Content, view.Pass.Input.Text) ||
		protocoltypes.IsSensitiveRequest(t.Context()) {
		t.Fatal("private controls or parent-context isolation changed")
	}
	for _, value := range []any{view, result} {
		encoded, encodeErr := json.Marshal(value)
		if encodeErr != nil || string(encoded) != "{}" {
			t.Fatal("private interpretation escaped ordinary JSON")
		}
	}
}

func TestDocumentIntakeInterpreterRejectsMalformedAndUnboundedResponses(t *testing.T) {
	valid := `{"intent":"facts","proposals":[{"field_id":"field_name",` +
		`"literals":[{"text":"Casey Mint","occurrence":1}]}]}`
	for _, body := range []string{
		"", "{}", " " + valid, valid + "\n", valid + "{}", "```json\n" + valid + "\n```",
		strings.Replace(valid, "field_name", "unknown", 1),
		strings.Replace(valid, `"occurrence":1`, `"occurrence":0`, 1),
		strings.Replace(valid, `"occurrence":1`, `"occurrence":65`, 1),
		strings.Replace(valid, `"Casey Mint"`, `" Casey Mint"`, 1),
		strings.Replace(valid, `"facts"`, `"approve"`, 1),
		strings.Replace(valid, `"intent":`, `"secret_echo":"private","intent":`, 1),
		`{"intent":"facts","proposals":[{"field_id":"field_name","literals":[]}]}`,
		`{"intent":"facts","findings":[{"field_id":"field_name","code":"arbitrary"}]}`,
		`{"intent":"facts","findings":[{"field_id":"unknown","code":"field_ambiguous"}]}`,
		`{"intent":"facts","findings":[{"field_id":"field_name","code":"field_ambiguous"},` +
			`{"field_id":"field_name","code":"field_conflicting"}]}`,
		strings.Replace(valid, "Casey Mint", string([]byte{0xff}), 1),
		strings.Repeat("x", maxDocumentIntakeWireBytes+1),
	} {
		result, err := decodeDocumentIntakeInterpretation(body, privateIntakeTestView().Pass.CandidateIDs)
		if !errors.Is(err, errDocumentIntakeInterpretationUnavailable) || len(result.Proposals) != 0 {
			t.Fatal("malformed private response returned an accepted-looking prefix")
		}
	}
}

func TestDocumentIntakeInterpreterRefusesUnknownModelsToolsAndEchoingErrors(t *testing.T) {
	provider := &documentAuditTestProvider{err: errors.New("MINTCLAW_PRIVATE_PROVIDER_ERROR_29d7")}
	auditor := &documentFormAuditor{models: map[string]documentFormAuditModel{
		"allowed": {provider: provider, model: "configured-model"},
	}}
	if _, err := auditor.InterpretFormIntake(
		t.Context(),
		"arbitrary",
		privateIntakeTestView(),
	); !errors.Is(err, errDocumentIntakeInterpretationUnavailable) ||
		provider.calls != 0 {
		t.Fatal("unconfigured alias invoked a provider")
	}
	if _, err := auditor.InterpretFormIntake(
		t.Context(),
		"allowed",
		privateIntakeTestView(),
	); !errors.Is(err, errDocumentIntakeInterpretationUnavailable) ||
		strings.Contains(err.Error(), "29d7") {
		t.Fatal("provider error echoed private content")
	}
	provider.err = nil
	provider.response = &providers.LLMResponse{
		Content: `{"intent":"facts"}`, ToolCalls: []providers.ToolCall{{Name: "unexpected"}},
	}
	if _, err := auditor.InterpretFormIntake(
		t.Context(),
		"allowed",
		privateIntakeTestView(),
	); !errors.Is(
		err,
		errDocumentIntakeInterpretationUnavailable,
	) {
		t.Fatal("unexpected tool call accepted")
	}
}

func TestDocumentIntakeInterpreterRejectsRequestBoundsBeforeProvider(t *testing.T) {
	for _, change := range []func(*document.FormIntakeInterpretationView){
		func(view *document.FormIntakeInterpretationView) { view.Pass.Input.Consumed = true },
		func(view *document.FormIntakeInterpretationView) { view.Pass.Input.Text = "" },
		func(view *document.FormIntakeInterpretationView) {
			view.Pass.Input.Text = strings.Repeat("x", document.MaxFormIntakeInputBytes+1)
		},
		func(view *document.FormIntakeInterpretationView) {
			view.Pass.Fields = make([]document.FormField, document.MaxFormIntakeCandidates+1)
			view.Pass.CandidateIDs = make([]string, len(view.Pass.Fields))
		},
		func(view *document.FormIntakeInterpretationView) {
			view.Pass.Input.Accounted = make([]document.FormIntakeAccountedLiteral,
				document.MaxFormIntakeAccountedSpans+1)
		},
		func(view *document.FormIntakeInterpretationView) { view.Pass.Fields[0].ReadOnly = true },
		func(view *document.FormIntakeInterpretationView) { view.Pass.CandidateIDs[0] = "other" },
		func(view *document.FormIntakeInterpretationView) {
			view.FieldStates = []document.FormJobFieldState{{FieldID: "unknown"}}
		},
		func(view *document.FormIntakeInterpretationView) {
			view.Evidence = []document.FormIntakeTextEvidence{{Page: 1, Text: strings.Repeat("x", 4001)}}
		},
		func(view *document.FormIntakeInterpretationView) {
			view.Evidence = []document.FormIntakeTextEvidence{{Page: 1}, {Page: 1}}
		},
	} {
		view := privateIntakeTestView()
		change(&view)
		provider := &documentAuditTestProvider{}
		auditor := &documentFormAuditor{models: map[string]documentFormAuditModel{
			"allowed": {provider: provider, model: "configured-model"},
		}}
		if _, err := auditor.InterpretFormIntake(
			t.Context(),
			"allowed",
			view,
		); !errors.Is(err, errDocumentIntakeInterpretationUnavailable) ||
			provider.calls != 0 {
			t.Fatal("invalid request reached the private provider")
		}
	}
}

func TestDocumentIntakeInterpreterKeepsCorrectionContextPrivateAndBounded(t *testing.T) {
	view := privateIntakeTestView()
	view.FieldStates = []document.FormJobFieldState{{FieldID: "field_name", EventID: "event_current"}}
	view.Pass.Input.Accounted = []document.FormIntakeAccountedLiteral{{
		Literal: document.FormIntakeLiteral{Text: "1989-02-15", Occurrence: 1}, FieldID: "field_birth",
	}}
	view.Evidence = []document.FormIntakeTextEvidence{{Page: 1, Text: "Borrower and emergency contact"}}
	payload, err := encodeDocumentIntakeView(view)
	if err != nil || !strings.Contains(string(payload), `"current_event_id":"event_current"`) ||
		!strings.Contains(string(payload), `"accounted":[{"literal":{"text":"1989-02-15","occurrence":1}`) ||
		!strings.Contains(string(payload), `"field_id":"field_birth"`) ||
		!strings.Contains(string(payload), `"page":1`) {
		t.Fatal("bounded private correction and attribution context was lost")
	}
	body := `{"intent":"facts","proposals":[{"field_id":"field_name",` +
		`"literals":[{"text":"Casey Mint","occurrence":1}],"supersedes_event_id":"event_current",` +
		`"replace_existing":true,"correction_evidence":[{"text":"correct my name","occurrence":1}]}]}`
	result, err := decodeDocumentIntakeInterpretation(body, view.Pass.CandidateIDs)
	if err != nil || len(result.Proposals) != 1 || !result.Proposals[0].ReplaceExisting ||
		result.Proposals[0].SupersedesEventID != "event_current" ||
		result.Proposals[0].CorrectionEvidence[0].Text != "correct my name" {
		t.Fatal("correction proposal lost its native-validation evidence")
	}
	// The adapter deliberately proposes, rather than accepting, literals. This
	// response contains evidence absent from the utterance; native application
	// must reject it, and interpretation alone has no store or mutation authority.
	for _, intent := range []string{"cancel", "finish", "draft"} {
		result, err = decodeDocumentIntakeInterpretation(`{"intent":"`+intent+`"}`, view.Pass.CandidateIDs)
		if err != nil || string(result.Intent) != intent || len(result.Proposals) != 0 {
			t.Fatal("advisory dialog intent acquired assignment authority")
		}
	}
}

func TestDocumentIntakeInterpreterRejectsAggregateResponseBounds(t *testing.T) {
	refs := make([]documentIntakeLiteralWire, document.MaxFormIntakeAccountedSpans+1)
	for index := range refs {
		refs[index] = documentIntakeLiteralWire{Text: "x", Occurrence: 1}
	}
	for _, response := range []documentIntakeResponseWire{
		{Intent: document.FormIntakeIntentFacts, NonData: refs},
		{Intent: document.FormIntakeIntentFacts, NonData: []documentIntakeLiteralWire{
			{Text: strings.Repeat("x", document.MaxFormIntakeInputBytes), Occurrence: 1},
			{Text: "x", Occurrence: 1},
		}},
		{Intent: document.FormIntakeIntentFacts, Proposals: []documentIntakeProposalWire{
			{FieldID: "field_name", Literals: refs[:1]}, {FieldID: "field_name", Literals: refs[:1]},
		}},
		{Intent: document.FormIntakeIntentFacts, Proposals: []documentIntakeProposalWire{
			{FieldID: "field_name", Literals: refs[:1]},
		}, Findings: []document.FormFieldMappingBlocker{{FieldID: "field_name", Code: "field_ambiguous"}}},
	} {
		body, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		result, err := decodeDocumentIntakeInterpretation(string(body), privateIntakeTestView().Pass.CandidateIDs)
		if !errors.Is(err, errDocumentIntakeInterpretationUnavailable) || len(result.Proposals) != 0 {
			t.Fatal("aggregate response violation returned a valid-looking prefix")
		}
	}
}
