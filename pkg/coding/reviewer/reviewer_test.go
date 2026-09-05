package reviewer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/coding/review"
	"github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

type fakeProvider struct {
	responses []*providers.LLMResponse
	calls     []providerCall
	err       error
}

type providerCall struct {
	messages []providers.Message
	tools    []providers.ToolDefinition
	model    string
	options  map[string]any
}

func (provider *fakeProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	provider.calls = append(provider.calls, providerCall{
		messages: append([]providers.Message(nil), messages...),
		tools:    append([]providers.ToolDefinition(nil), tools...),
		model:    model,
		options:  options,
	})
	if provider.err != nil {
		return nil, provider.err
	}
	if len(provider.responses) == 0 {
		return nil, nil
	}
	response := provider.responses[0]
	provider.responses = provider.responses[1:]
	return response, nil
}

func (*fakeProvider) GetDefaultModel() string { return "test-model" }

func (*fakeProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{CallerMediatedTools: true}
}

type autonomousFakeProvider struct{ fakeProvider }

func (*autonomousFakeProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{}
}

type fakeToolset struct {
	definitions []providers.ToolDefinition
	results     map[string]ToolResult
	executed    []string
}

func (toolset *fakeToolset) Definitions() []providers.ToolDefinition {
	return append([]providers.ToolDefinition(nil), toolset.definitions...)
}

func (toolset *fakeToolset) Execute(
	_ context.Context,
	name string,
	_ map[string]any,
) ToolResult {
	toolset.executed = append(toolset.executed, name)
	return toolset.results[name]
}

func TestReviewUsesOnlyReadToolsAndNormalizesRenameLocation(t *testing.T) {
	provider := &fakeProvider{responses: []*providers.LLMResponse{
		{
			ToolCalls: []providers.ToolCall{
				{ID: "call-1", Name: "read_file", Arguments: map[string]any{"path": "new.go"}},
			},
		},
		{
			Content: `{"summary":"One defect found.","findings":[{"severity":"major","title":"Wrong result","explanation":"The new branch returns the wrong value.","confidence":0.93,"location_state":"current","path":"old.go","start_line":8,"end_line":8}]}`,
		},
	}}
	toolset := &fakeToolset{
		definitions: append(readToolDefinitions(), providers.ToolDefinition{
			Type: "function",
			Function: providers.ToolFunctionDefinition{
				Name: "exec", Description: "must not be exposed", Parameters: map[string]any{"type": "object"},
			},
		}),
		results: map[string]ToolResult{"read_file": {Content: "package example"}},
	}
	now := time.Date(2026, time.September, 4, 22, 0, 0, 0, time.FixedZone("PDT", -7*60*60))
	executor, err := New(provider, "native-model", toolset, Limits{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	frozen := currentDiff()
	result, err := executor.Review(
		t.Context(),
		"e5768a80-a5be-4bda-b21c-0da34b02502c",
		review.Target{Kind: review.TargetCurrent, Instructions: "Focus on arithmetic behavior."},
		frozen,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.calls) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(provider.calls))
	}
	if prompt := provider.calls[0].messages[1].Content; !strings.Contains(prompt, "Focus on arithmetic behavior.") ||
		!strings.Contains(prompt, "return 0") || !strings.Contains(prompt, "untrusted-frozen-repository-evidence") {
		t.Fatalf("review prompt omitted bounded target evidence: %q", prompt)
	}
	for _, call := range provider.calls {
		if call.model != "native-model" || call.options["max_tokens"] != defaultMaxTokens {
			t.Fatalf("provider selection = %q / %#v", call.model, call.options)
		}
		if got := definitionNames(call.tools); strings.Join(got, ",") != "list_dir,read_file,search_files" {
			t.Fatalf("review tool definitions = %v", got)
		}
	}
	if len(toolset.executed) != 1 || toolset.executed[0] != "read_file" {
		t.Fatalf("executed tools = %v", toolset.executed)
	}
	toolMessage := provider.calls[1].messages[len(provider.calls[1].messages)-1]
	if toolMessage.Role != "tool" || toolMessage.ToolCallID != "call-1" ||
		toolMessage.ToolResultStatus != providers.ToolResultStatusSuccess {
		t.Fatalf("tool result message = %#v", toolMessage)
	}
	if len(result.Findings) != 1 || result.Findings[0].Path != "new.go" ||
		result.Findings[0].LocationState != review.LocationCurrent {
		t.Fatalf("normalized findings = %#v", result.Findings)
	}
	if !result.CompletedAt.Equal(now.UTC()) || result.EvidenceGeneration != frozen.EvidenceGeneration {
		t.Fatalf("trusted result identity = %#v", result)
	}
	if err := result.ValidateAgainstFrozenDiff(frozen); err != nil {
		t.Fatalf("published result does not validate: %v", err)
	}
}

func TestNewRejectsProviderWithoutCallerMediatedTools(t *testing.T) {
	_, err := New(
		&autonomousFakeProvider{},
		"native-model",
		&fakeToolset{definitions: readToolDefinitions()},
		Limits{},
		time.Now,
	)
	if err == nil || !strings.Contains(err.Error(), "caller-mediated tool execution") {
		t.Fatalf("New() error = %v", err)
	}
}

func TestReviewRejectsProviderMutationToolCall(t *testing.T) {
	provider := &fakeProvider{responses: []*providers.LLMResponse{{
		ToolCalls: []providers.ToolCall{{ID: "call-1", Name: "write_file"}},
	}}}
	toolset := &fakeToolset{definitions: readToolDefinitions()}
	executor, err := New(provider, "native-model", toolset, Limits{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Review(
		t.Context(),
		"e5768a80-a5be-4bda-b21c-0da34b02502c",
		review.Target{Kind: review.TargetCurrent},
		currentDiff(),
	)
	if err == nil || !strings.Contains(err.Error(), `forbidden tool "write_file"`) {
		t.Fatalf("Review() error = %v", err)
	}
	if len(toolset.executed) != 0 {
		t.Fatalf("forbidden tool executed: %v", toolset.executed)
	}
}

func TestReviewRejectsToolCallBatchBeforeCopyOrExecution(t *testing.T) {
	calls := make([]providers.ToolCall, defaultToolCalls+1)
	for index := range calls {
		calls[index] = providers.ToolCall{ID: "call", Name: "read_file"}
	}
	provider := &fakeProvider{responses: []*providers.LLMResponse{{ToolCalls: calls}}}
	toolset := &fakeToolset{definitions: readToolDefinitions()}
	executor, err := New(provider, "native-model", toolset, Limits{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Review(
		t.Context(),
		"e5768a80-a5be-4bda-b21c-0da34b02502c",
		review.Target{Kind: review.TargetCurrent},
		currentDiff(),
	)
	if err == nil || !strings.Contains(err.Error(), "exceeded 24 read-only tool calls") {
		t.Fatalf("Review() error = %v", err)
	}
	if len(toolset.executed) != 0 {
		t.Fatalf("oversized tool batch executed: %v", toolset.executed)
	}
}

func TestCommitReviewUsesOnlyFrozenEvidence(t *testing.T) {
	provider := &fakeProvider{responses: []*providers.LLMResponse{{
		Content: `{"summary":"No findings.","findings":[]}`,
	}}}
	executor, err := New(
		provider,
		"native-model",
		&fakeToolset{definitions: readToolDefinitions()},
		Limits{},
		time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	frozen := currentDiff()
	frozen.Target = workspace.DiffTarget{Kind: workspace.DiffTargetCommit, Ref: "main~1"}
	frozen.ResolvedRevision = "0123456789abcdef"
	frozen.EvidenceGeneration = ""
	result, err := executor.Review(
		t.Context(),
		"e5768a80-a5be-4bda-b21c-0da34b02502c",
		review.Target{Kind: review.TargetCommit, Ref: "main~1"},
		frozen,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.calls) != 1 || len(provider.calls[0].tools) != 0 ||
		result.ResolvedRevision != frozen.ResolvedRevision {
		t.Fatalf("commit review call/result = %#v / %#v", provider.calls, result)
	}
}

func TestCommitReviewRejectsProviderToolCall(t *testing.T) {
	provider := &fakeProvider{responses: []*providers.LLMResponse{{
		ToolCalls: []providers.ToolCall{{ID: "call-1", Name: "read_file"}},
	}}}
	executor, err := New(
		provider,
		"native-model",
		&fakeToolset{definitions: readToolDefinitions()},
		Limits{},
		time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	frozen := currentDiff()
	frozen.Target = workspace.DiffTarget{Kind: workspace.DiffTargetCommit, Ref: "main~1"}
	frozen.ResolvedRevision = "0123456789abcdef"
	frozen.EvidenceGeneration = ""
	_, err = executor.Review(
		t.Context(),
		"e5768a80-a5be-4bda-b21c-0da34b02502c",
		review.Target{Kind: review.TargetCommit, Ref: "main~1"},
		frozen,
	)
	if err == nil || !strings.Contains(err.Error(), "unavailable for this review target") {
		t.Fatalf("Review() error = %v", err)
	}
}

func TestReviewRejectsMalformedFrozenEvidenceBeforeCallingProvider(t *testing.T) {
	provider := &fakeProvider{}
	executor, err := New(
		provider,
		"native-model",
		&fakeToolset{definitions: readToolDefinitions()},
		Limits{},
		time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	frozen := currentDiff()
	frozen.EvidenceGeneration = ""
	_, err = executor.Review(
		t.Context(),
		"e5768a80-a5be-4bda-b21c-0da34b02502c",
		review.Target{Kind: review.TargetCurrent},
		frozen,
	)
	if err == nil || !strings.Contains(err.Error(), "invalid frozen evidence") {
		t.Fatalf("Review() error = %v", err)
	}
	if len(provider.calls) != 0 {
		t.Fatalf("provider called for malformed evidence: %#v", provider.calls)
	}
}

func TestReviewDowngradesUnprovenLocationsAndPropagatesIncompleteEvidence(t *testing.T) {
	provider := &fakeProvider{responses: []*providers.LLMResponse{{Content: `{
		"summary":"Review completed.",
		"findings":[
			{"severity":"minor","title":"Outside diff","explanation":"This line was not added by the change.","confidence":0.7,"location_state":"current","path":"new.go","start_line":40,"end_line":40},
			{"severity":"minor","title":"Unsafe path","explanation":"The location cannot be trusted.","confidence":0.6,"location_state":"stale","path":"../secret","start_line":4,"end_line":4}
		]
	}`}}}
	executor, err := New(provider, "native-model", &fakeToolset{definitions: readToolDefinitions()}, Limits{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	frozen := currentDiff()
	frozen.Truncated = true
	result, err := executor.Review(
		t.Context(),
		"e5768a80-a5be-4bda-b21c-0da34b02502c",
		review.Target{Kind: review.TargetCurrent},
		frozen,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || result.Findings[0].LocationState != review.LocationStale ||
		result.Findings[0].StartLine != 0 || result.Findings[1].LocationState != review.LocationUnlocated ||
		result.Findings[1].Path != "" {
		t.Fatalf("bounded result = %#v", result)
	}
}

func TestReviewRequiresStrictBoundedJSON(t *testing.T) {
	tests := []struct {
		name    string
		content string
		limits  Limits
		want    string
	}{
		{name: "markdown", content: "```json\n{}\n```", want: "decode structured response"},
		{name: "unknown field", content: `{"summary":"ok","findings":[],"extra":true}`, want: "unknown field"},
		{
			name:    "missing confidence",
			content: `{"summary":"ok","findings":[{"severity":"minor","title":"x","explanation":"y","location_state":"unlocated"}]}`,
			want:    "confidence is required",
		},
		{
			name:    "unknown location",
			content: `{"summary":"ok","findings":[{"severity":"minor","title":"x","explanation":"y","confidence":0.5,"location_state":"invented","path":"new.go"}]}`,
			want:    "unsupported review finding location state",
		},
		{name: "trailing", content: `{"summary":"ok","findings":[]} {}`, want: "trailing JSON"},
		{
			name:    "too large",
			content: `{"summary":"ok","findings":[]}`,
			limits:  Limits{ResponseBytes: 8},
			want:    "within 8 bytes",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeProvider{responses: []*providers.LLMResponse{{Content: test.content}}}
			executor, err := New(
				provider,
				"native-model",
				&fakeToolset{definitions: readToolDefinitions()},
				test.limits,
				time.Now,
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = executor.Review(
				t.Context(),
				"e5768a80-a5be-4bda-b21c-0da34b02502c",
				review.Target{Kind: review.TargetCurrent},
				currentDiff(),
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Review() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReconcileEvidenceDowngradesCurrentLocations(t *testing.T) {
	frozen := currentDiff()
	result := review.Result{
		SchemaVersion:      review.SchemaVersion,
		ReviewID:           "e5768a80-a5be-4bda-b21c-0da34b02502c",
		Target:             review.Target{Kind: review.TargetCurrent},
		EvidenceGeneration: frozen.EvidenceGeneration,
		Summary:            "One defect found.",
		Findings: []review.Finding{{
			Severity: review.SeverityMajor, Title: "Wrong result", Explanation: "The result is wrong.",
			Confidence: 0.9, LocationState: review.LocationCurrent, Path: "new.go", StartLine: 8, EndLine: 8,
		}},
		CompletedAt: time.Now().UTC(),
	}
	current := frozen
	current.Generation = "changed-generation"
	current.EvidenceGeneration = "changed-evidence"
	result = ReconcileEvidence(result, frozen, current)
	if !result.Stale || result.Findings[0].LocationState != review.LocationStale ||
		result.Findings[0].StartLine != 0 || !strings.Contains(result.Diagnostic, "changed before") {
		t.Fatalf("reconciled result = %#v", result)
	}
	if err := result.ValidateAgainstFrozenDiff(frozen); err != nil {
		t.Fatalf("stale result does not validate against frozen evidence: %v", err)
	}
}

func TestBoundedToolResultIncludesMarkerWithinLimit(t *testing.T) {
	const limit = 64
	result := boundedToolResult(strings.Repeat("x", 100), limit)
	if len(result) > limit || !strings.Contains(result, "truncated by reviewer") {
		t.Fatalf("bounded tool result = %q (%d bytes)", result, len(result))
	}
}

func readToolDefinitions() []providers.ToolDefinition {
	result := make([]providers.ToolDefinition, 0, 3)
	for _, name := range []string{"read_file", "list_dir", "search_files"} {
		result = append(result, providers.ToolDefinition{
			Type: "function",
			Function: providers.ToolFunctionDefinition{
				Name: name, Description: name, Parameters: map[string]any{"type": "object"},
			},
		})
	}
	return result
}

func definitionNames(definitions []providers.ToolDefinition) []string {
	result := make([]string, len(definitions))
	for index, definition := range definitions {
		result[index] = definition.Function.Name
	}
	return result
}

func currentDiff() workspace.DiffResult {
	return workspace.DiffResult{
		SchemaVersion:       workspace.RepositoryDiffSchemaV1,
		Target:              workspace.DiffTarget{Kind: workspace.DiffTargetCurrent},
		RepositoryAvailable: true,
		Head:                "0123456789abcdef",
		Generation:          "generation-1",
		EvidenceGeneration:  "evidence-1",
		Files: []workspace.DiffFile{{
			Path: "new.go", OriginalPath: "old.go", Status: "renamed",
			Hunks: []workspace.DiffHunk{{
				NewStart: 7, NewLines: 2,
				Lines: []workspace.DiffLine{
					{Kind: "context", NewLine: 7, Text: "func value() int {"},
					{Kind: "addition", NewLine: 8, Text: "return 0"},
				},
			}},
		}},
		Additions: 1,
	}
}
