//go:build linux && amd64 && integration

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/diagnostictrace"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/providers/protocoltypes"
	"github.com/bogdanovich/mintclaw/pkg/testharness/llmscenario"
	"github.com/bogdanovich/mintclaw/pkg/tools"
)

// Retain explicit legacy API coverage without pretending its single-field
// scripted provider exercises the newly grouped production composition.
func useLegacyDocumentFormE2ESurface(t *testing.T, fixture *agentLoopTestFixture) {
	t.Helper()
	registered, exists := fixture.Agent.Tools.GetRegistered("document")
	tool, typed := registered.(*tools.DocumentTool)
	if !exists || !typed {
		t.Fatal("legacy document fixture omitted its document tool")
	}
	tools.WithDocumentFormIntake(nil)(tool)
}

func TestGatewayDocumentIntakeUsesProductionCompositionAndOrdinaryTransport(t *testing.T) {
	requireDocumentFormBackend(t)
	workspace := documentE2EWorkspace(t)
	home := filepath.Join(workspace, "instance")
	t.Setenv(config.EnvHome, home)
	store, ref, digest, sourcePath := documentE2ESource(t, "acroform-fields.pdf")
	values := []string{"09/18/2026", "MINTCLAW_PDFI4_GATEWAY_53f0"}
	provider := &documentIntakeGatewayE2EProvider{
		documentFormReviewE2EProvider: newDocumentFormReviewE2EProvider(ref, digest, sourcePath, values),
	}
	fixture := newAgentLoopTestFixtureWithWorkspace(t, workspace, provider, func(cfg *config.Config) {
		configureDocumentE2E(cfg, provider.GetDefaultModel(), false)
		cfg.Tools.Document.AuditModel = provider.GetDefaultModel()
		cfg.ModelList = []*config.ModelConfig{{
			ModelName: provider.GetDefaultModel(), Provider: "openai", Model: provider.GetDefaultModel(), Enabled: true,
		}}
	})
	fixture.Loop.SetMediaStore(store)
	t.Cleanup(func() { closeDocumentE2EFixtureAfterTraceDrain(t, fixture) })
	channel := &fakeMediaChannel{fakeChannel: fakeChannel{id: "document-grouped-gateway-e2e"}}
	stop := startDocumentE2EChannel(t, fixture, store, channel)
	defer stop()
	publishDocumentE2EInbound(t, fixture.Bus, ref, "Fill this test form; ask only for missing facts.")
	waitDocumentE2EChannel(t, channel, func() bool {
		for _, message := range channel.messagesSnapshot() {
			if message.Content == "Provide the missing date and notes together in ordinary text." {
				return true
			}
		}
		return false
	})
	publishDocumentE2EFollowup(t, fixture.Bus,
		"The date is "+values[0]+" and the notes are "+values[1], 1)
	waitDocumentFormReviewReady(t, channel)
	assertDocumentFormPromptCounts(t, channel, 0, 0)
	assertDocumentSourceDigest(t, sourcePath, digest)
	metadata, err := os.ReadFile(filepath.Join(
		home, "state", "document-form-jobs", "document_form_jobs", "form_jobs.v1.json",
	))
	if err != nil || !strings.Contains(string(metadata), `"state":"review_ready"`) {
		t.Fatal("ordinary paragraph did not reach native review_ready")
	}
	for _, value := range values {
		if strings.Contains(string(metadata), value) {
			t.Fatal("native public metadata retained protected values")
		}
	}
	assertDocumentIntakeGatewayTraces(t, workspace, digest, append([]string{sourcePath, "same-name.pdf"}, values...)...)
	if provider.intakeCalls.Load() == 0 {
		t.Fatal("ordinary transport never invoked private interpretation")
	}
	if provider.routingCalls.Load() != 1 {
		t.Fatal("ordinary form paragraph did not cross the private task-routing boundary exactly once")
	}
	for _, key := range fixture.Agent.Sessions.ListSessions() {
		for _, message := range fixture.Agent.Sessions.GetHistory(key) {
			encoded, err := json.Marshal(message)
			if err != nil {
				t.Fatal("cannot inspect complete ordinary history projection")
			}
			for _, value := range values {
				if strings.Contains(string(encoded), value) {
					t.Fatal("ordinary gateway history retained the admitted paragraph")
				}
			}
		}
	}
}

func assertDocumentIntakeGatewayTraces(t *testing.T, workspace, digest string, forbidden ...string) {
	t.Helper()
	directory := filepath.Join(workspace, "state", "diagnostics", "traces")
	var traces [][]byte
	waitDocumentE2E(t, func() bool {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return false
		}
		traces = nil
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			trace, err := os.ReadFile(filepath.Join(directory, entry.Name()))
			if err != nil || !json.Valid(trace) {
				return false
			}
			traces = append(traces, trace)
		}
		// Attachment and ordinary protected follow-up must both be inspected.
		return len(traces) >= 2
	})
	lifecycle := false
	for _, trace := range traces {
		var captured diagnostictrace.Trace
		if err := json.Unmarshal(trace, &captured); err != nil || diagnostictrace.Validate(captured) != nil ||
			captured.Outcome == nil || captured.Outcome.Status != "completed" || captured.Truncation.Incomplete ||
			captured.Truncation.DroppedRecords != 0 || len(captured.Truncation.Reasons) != 0 ||
			len(captured.Truncation.DroppedByKind) != 0 {
			t.Fatal("document intake requires valid, complete, untruncated diagnostic traces")
		}
		var decoded any
		if err := json.Unmarshal(trace, &decoded); err != nil {
			t.Fatal("invalid document intake diagnostic trace")
		}
		compact, err := json.Marshal(decoded)
		if err != nil {
			t.Fatal("cannot normalize document intake diagnostic trace")
		}
		text := string(compact)
		if strings.Contains(text, `"tool":"document"`) && strings.Contains(text, digest) &&
			strings.Contains(text, "selected_pages") {
			lifecycle = true
		}
		for _, value := range forbidden {
			if value != "" && strings.Contains(text, value) {
				t.Fatal("document intake diagnostic trace retained protected input")
			}
		}
	}
	if !lifecycle {
		t.Fatal("document intake traces omitted bounded source-read lifecycle evidence")
	}
}

type documentIntakeGatewayE2EProvider struct {
	*documentFormReviewE2EProvider
	intakeCalls  atomic.Int32
	routingCalls atomic.Int32
	readEvidence bool
}

func (provider *documentIntakeGatewayE2EProvider) Chat(
	ctx context.Context, messages []providers.Message, definitions []providers.ToolDefinition,
	model string, options map[string]any,
) (*providers.LLMResponse, error) {
	joined := documentProviderMessagesText(messages)
	if protocoltypes.IsSensitiveRequest(ctx) && len(messages) == 2 &&
		messages[0].Content == documentFormTaskRoutingPrompt {
		var input struct {
			Message     string `json:"current_message"`
			Attachments int    `json:"attachment_count"`
		}
		if len(definitions) != 0 || json.Unmarshal([]byte(messages[1].Content), &input) != nil ||
			input.Message != "The date is "+provider.privateValues[0]+" and the notes are "+provider.privateValues[1] ||
			input.Attachments != 0 {
			return nil, errors.New("private routing received an unexpected paragraph or ordinary tools")
		}
		provider.routingCalls.Add(1)
		return llmscenario.TextResponse(`{"action":"continue_form"}`), nil
	}
	if provider.initialCalls == 2 && !provider.readEvidence {
		provider.readEvidence = true
		return llmscenario.ToolCallResponse(
			"",
			llmscenario.ToolCall("read-grouped-form-evidence", "document", map[string]any{
				"action": "extract", "source": provider.ref, "pages": []any{1}, "max_characters": 4000,
			}),
		), nil
	}
	if protocoltypes.IsSensitiveRequest(ctx) && strings.Contains(joined, "mintclaw.document_intake_interpretation.v1") {
		if len(definitions) != 0 {
			return nil, errors.New("private intake received ordinary tools")
		}
		var input struct {
			Fields []documentIntakeFieldWire `json:"fields"`
		}
		if len(messages) != 2 || json.Unmarshal([]byte(messages[1].Content), &input) != nil {
			return nil, errors.New("private intake received an invalid bounded view")
		}
		proposals := make([]documentIntakeProposalWire, 0, 2)
		for _, field := range input.Fields {
			value := ""
			switch field.Name {
			case "start_date":
				value = provider.privateValues[0]
			case "notes":
				value = provider.privateValues[1]
			}
			if value != "" {
				proposals = append(proposals, documentIntakeProposalWire{
					FieldID: field.FieldID, Literals: []documentIntakeLiteralWire{{Text: value, Occurrence: 1}},
				})
			}
		}
		payload, err := json.Marshal(struct {
			Intent    document.FormIntakeDialogIntent `json:"intent"`
			Proposals []documentIntakeProposalWire    `json:"proposals"`
			NonData   []documentIntakeLiteralWire     `json:"non_data"`
		}{document.FormIntakeIntentFacts, proposals, []documentIntakeLiteralWire{
			{Text: "The date is", Occurrence: 1}, {Text: "and the notes are", Occurrence: 1},
		}})
		if err != nil {
			return nil, err
		}
		provider.intakeCalls.Add(1)
		return llmscenario.TextResponse(string(payload)), nil
	}
	if strings.Contains(joined, "<runtime_protected_answer_continuation>") {
		for index := len(messages) - 1; index >= 0; index-- {
			content := messages[index].Content
			if !strings.Contains(content, "<runtime_protected_answer_continuation>") {
				continue
			}
			start, end := strings.IndexByte(content, '{'), strings.LastIndexByte(content, '}')
			var arguments map[string]any
			if start < 0 || end <= start || json.Unmarshal([]byte(content[start:end+1]), &arguments) != nil {
				return nil, errors.New("opaque intake continuation was unavailable")
			}
			return llmscenario.ToolCallResponse(
				"",
				llmscenario.ToolCall("continue-grouped-intake", "document", arguments),
			), nil
		}
	}
	if len(definitions) == 0 && strings.Contains(joined, `"form_action":"collect"`) &&
		strings.Contains(joined, "<runtime_response_only_followup>") && !strings.Contains(joined, `"ready":true`) {
		return llmscenario.TextResponse("Provide the missing date and notes together in ordinary text."), nil
	}
	if provider.initialCalls >= 4 {
		if jobID, fieldID, ready := documentFormProgressFromMessages(messages); jobID != "" && !ready && fieldID != "" {
			return llmscenario.ToolCallResponse(
				"",
				llmscenario.ToolCall("collect-grouped-intake", "document", map[string]any{
					"action":              "form",
					"form_action":         "collect",
					"job_id":              jobID,
					"candidate_field_ids": []any{fieldID},
					"question":            "Provide the missing date and notes together.",
					"form_summary":        "This is the supplied test form.",
					"collection_plan":     "Provide its missing date and notes.",
				}),
			), nil
		}
	}
	return provider.documentFormReviewE2EProvider.Chat(ctx, messages, definitions, model, options)
}
