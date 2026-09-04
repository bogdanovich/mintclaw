package agent

import (
	"context"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/bus"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/taskresult"
)

func TestNewFinalizationContextCapturesTerminalSnapshot(t *testing.T) {
	ts := &turnState{
		opts: freezeTurnInput(turnSpec{
			SendResponse:                false,
			AllowInterimMintClawPublish: true,
			EnableSummary:               true,
		}),
		followUps: []bus.InboundMessage{{Content: "follow up"}},
	}
	ts.RecordLLMUsage(&providers.UsageInfo{
		PromptTokens:     125,
		CompletionTokens: 25,
		TotalTokens:      150,
	})
	publisher := &streamingChunkPublisher{}
	exec := &turnExecution{
		model: turnExecutionModel{
			llmModelName:     "fallback-model",
			defaultModelName: "primary-model",
		},
		deliverable: &taskresult.Deliverable{
			Text:      "tool-owned result",
			Artifacts: []taskresult.Artifact{{Ref: "media://result"}},
			Metadata:  map[string]string{"producer": "test-tool"},
			Report: &taskresult.Report{
				SchemaVersion: taskresult.ReportSchemaV1,
				ReportID:      "report-1",
				Metadata:      map[string]string{"format": "summary"},
			},
		},
		sawAdditionalUserInput: true,
	}
	llm := &LLMIterationState{
		response:           &providers.LLMResponse{ReasoningContent: "reasoning"},
		streamingPublisher: publisher,
		streamingFallback:  true,
		llmModel:           "provider/fallback-model",
	}

	finalization := newFinalizationContext(
		ts, exec, llm, TurnEndStatusCompleted, terminalContent{content: "final answer"},
	)

	if finalization.disposition != finalResponsePending {
		t.Fatalf("disposition = %v, want pending", finalization.disposition)
	}
	if finalization.historyMessage == nil {
		t.Fatal("history message is nil")
	}
	if finalization.historyMessage.Content != "final answer" ||
		finalization.historyMessage.ModelName != "fallback-model" ||
		finalization.historyMessage.ReasoningContent != "reasoning" {
		t.Fatalf("history message = %+v", *finalization.historyMessage)
	}
	if finalization.usage != (finalizationUsage{inputTokens: 125, outputTokens: 25, totalTokens: 150}) {
		t.Fatalf("usage = %+v", finalization.usage)
	}
	if finalization.stream.publisher != publisher || !finalization.stream.fallback ||
		finalization.stream.modelName != "provider/fallback-model" {
		t.Fatalf("stream = %+v", finalization.stream)
	}
	if !finalization.delivery.allowInterimMintClawPublish ||
		!finalization.delivery.preferNewOutboundReply ||
		!finalization.delivery.compactAfterDelivery {
		t.Fatalf("delivery = %+v", finalization.delivery)
	}

	exec.deliverable.Text = "mutated"
	exec.deliverable.Artifacts[0].Ref = "media://mutated"
	exec.deliverable.Metadata["producer"] = "mutated"
	exec.deliverable.Report.Metadata["format"] = "mutated"
	ts.followUps[0].Content = "mutated"
	if finalization.deliverable == nil || finalization.deliverable.Text != "tool-owned result" ||
		finalization.deliverable.Artifacts[0].Ref != "media://result" ||
		finalization.deliverable.Metadata["producer"] != "test-tool" ||
		finalization.deliverable.Report.Metadata["format"] != "summary" {
		t.Fatalf("deliverable changed after capture: %+v", finalization.deliverable)
	}
	if finalization.historyMessage.Deliverable == nil ||
		finalization.historyMessage.Deliverable.Text != "tool-owned result" ||
		finalization.historyMessage.Deliverable.Artifacts[0].Ref != "media://result" ||
		finalization.historyMessage.Deliverable.Metadata["producer"] != "test-tool" ||
		finalization.historyMessage.Deliverable.Report.Metadata["format"] != "summary" {
		t.Fatalf("history message deliverable changed after capture: %+v", finalization.historyMessage.Deliverable)
	}
	if finalization.followUps[0].Content != "follow up" {
		t.Fatalf("follow ups changed after capture: %+v", finalization.followUps)
	}
}

func TestFinalizationContextAlreadyHandledSkipsHistoryAndCompaction(t *testing.T) {
	ts := &turnState{
		opts: freezeTurnInput(turnSpec{EnableSummary: true}),
	}
	exec := &turnExecution{
		model: turnExecutionModel{
			llmModelName:     "active-model",
			defaultModelName: "default-model",
		},
	}
	llm := &LLMIterationState{toolResponseDisposition: toolResponseHandled}
	finalization := newFinalizationContext(ts, exec, llm, TurnEndStatusCompleted, terminalContent{})
	if finalization.disposition != finalResponseAlreadyHandled {
		t.Fatalf("disposition = %v, want already handled", finalization.disposition)
	}
	if finalization.historyMessage != nil {
		t.Fatalf("history message = %+v, want nil", finalization.historyMessage)
	}

	result, err := (&Pipeline{}).Finalize(context.Background(), ts, finalization)
	if err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if result.compactAfterDelivery {
		t.Fatal("already-handled response requested compaction")
	}
	if result.modelName != "active-model" || result.defaultModelName != "default-model" {
		t.Fatalf("result models = (%q, %q)", result.modelName, result.defaultModelName)
	}
}

func TestFinalizationResultDetachesCompleteDeliverable(t *testing.T) {
	finalization := FinalizationContext{
		deliverable: &taskresult.Deliverable{
			Text:      "tool-owned result",
			Artifacts: []taskresult.Artifact{{Ref: "file:/tmp/result.txt", Kind: "file"}},
			Metadata:  map[string]string{"producer": "test-tool"},
			Report: &taskresult.Report{
				SchemaVersion: taskresult.ReportSchemaV1,
				ReportID:      "report-1",
				Provenance:    map[string]string{"source": "tool"},
			},
			ObjectiveOutcome: &taskresult.Outcome{
				Status:       taskresult.OutcomePartial,
				MissingItems: []string{"verification"},
			},
		},
	}

	result := finalization.result(false)
	finalization.deliverable.Artifacts[0].Ref = "mutated"
	finalization.deliverable.Metadata["producer"] = "mutated"
	finalization.deliverable.Report.Provenance["source"] = "mutated"
	finalization.deliverable.ObjectiveOutcome.MissingItems[0] = "mutated"

	if result.deliverable == nil || result.deliverable.Text != "tool-owned result" ||
		result.deliverable.Artifacts[0].Ref != "file:/tmp/result.txt" ||
		result.deliverable.Metadata["producer"] != "test-tool" ||
		result.deliverable.Report.Provenance["source"] != "tool" ||
		result.deliverable.ObjectiveOutcome.MissingItems[0] != "verification" {
		t.Fatalf("result deliverable was not detached: %+v", result.deliverable)
	}
}

func TestNewFinalizationContextSuppressesOnlyBackgroundCompaction(t *testing.T) {
	ts := &turnState{opts: freezeTurnInput(turnSpec{
		EnableSummary:                true,
		SuppressBackgroundCompaction: true,
	})}
	finalization := newFinalizationContext(
		ts,
		&turnExecution{},
		&LLMIterationState{},
		TurnEndStatusCompleted,
		terminalContent{content: "done"},
	)
	if finalization.delivery.compactAfterDelivery {
		t.Fatal("short-lived caller requested post-delivery compaction")
	}
	if !ts.opts.EnableSummary {
		t.Fatal("foreground compaction was disabled")
	}
}
