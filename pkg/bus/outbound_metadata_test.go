package bus

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeOutboundMetadataCanonicalizesCurrentContract(t *testing.T) {
	metadata := NormalizeOutboundMetadata(OutboundMetadata{
		MessageKind: " tool_calls ",
		ToolCalls: []OutboundToolCall{{
			ID: " call-1 ", Function: &OutboundToolCallFunction{Name: " read_file "},
		}},
		OutboundKind:      " final ",
		ModelName:         " fallback-model ",
		DefaultModelName:  " primary-model ",
		UsageInputTokens:  -1,
		UsageOutputTokens: 4500,
		UsageTotalTokens:  4500,
		RequestID:         " request-1 ",
	})

	if metadata.MessageKind != OutboundMessageKindToolCalls ||
		len(metadata.ToolCalls) != 1 || metadata.ToolCalls[0].ID != "call-1" ||
		metadata.ToolCalls[0].Function == nil || metadata.ToolCalls[0].Function.Name != "read_file" ||
		metadata.OutboundKind != OutboundKindFinal || metadata.ModelName != "fallback-model" ||
		metadata.DefaultModelName != "primary-model" || metadata.UsageInputTokens != 0 ||
		metadata.UsageOutputTokens != 4500 || metadata.UsageTotalTokens != 4500 ||
		metadata.RequestID != "request-1" {
		t.Fatalf("normalized metadata = %#v", metadata)
	}
	if err := ValidateOutboundMetadata(metadata); err != nil {
		t.Fatalf("ValidateOutboundMetadata() error = %v", err)
	}
	if !metadata.IsFinal() || !metadata.BypassesPlaceholderEdit() {
		t.Fatalf("final metadata semantics = %#v", metadata)
	}
}

func TestValidateOutboundMetadataRejectsNoncanonicalValues(t *testing.T) {
	for name, metadata := range map[string]OutboundMetadata{
		"whitespace": {ModelName: " model "},
		"negative usage": {
			UsageInputTokens: -1,
		},
		"invalid choices":          {Choices: []string{"Yes", ""}},
		"unsupported message kind": {MessageKind: "legacy"},
		"unsupported outbound kind": {
			OutboundKind: "legacy",
		},
		"unsupported interaction kind": {
			InteractionKind: "legacy",
		},
		"unsupported interaction controls": {
			InteractionControls: "legacy",
		},
		"tool calls without kind": {
			ToolCalls: []OutboundToolCall{{Function: &OutboundToolCallFunction{Name: "read_file"}}},
		},
		"tool calls kind without calls": {
			MessageKind: OutboundMessageKindToolCalls,
		},
		"empty tool call": {
			MessageKind: OutboundMessageKindToolCalls,
			ToolCalls:   []OutboundToolCall{{}},
		},
		"noncanonical tool call": {
			MessageKind: OutboundMessageKindToolCalls,
			ToolCalls: []OutboundToolCall{{
				Function: &OutboundToolCallFunction{Name: " read_file "},
			}},
		},
		"prompt without kind": {
			InteractionControls: OutboundInteractionControlsPrompt,
			InteractionID:       "interaction-1",
			InteractionShortID:  "short-1",
		},
		"prompt without identity": {
			InteractionKind:     OutboundInteractionApproval,
			InteractionControls: OutboundInteractionControlsPrompt,
		},
		"choices outside question prompt": {
			Choices: []string{"Yes", "No"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateOutboundMetadata(metadata); err == nil {
				t.Fatalf("ValidateOutboundMetadata(%#v) succeeded", metadata)
			}
		})
	}
}

func TestOutboundMetadataMergeClonesInteractionChoices(t *testing.T) {
	choices := []string{" Yes ", "No"}
	metadata := OutboundMetadata{RequestID: "request-1"}.Merge(
		OutboundMetadata{InteractionKind: OutboundInteractionQuestion}.WithInteractionChoices(choices),
	)
	choices[0] = "changed"
	read := metadata.InteractionChoices()
	read[0] = "also changed"

	if metadata.RequestID != "request-1" || metadata.InteractionKind != OutboundInteractionQuestion ||
		len(metadata.Choices) != 2 || metadata.Choices[0] != "Yes" {
		t.Fatalf("merged metadata = %#v", metadata)
	}
}

func TestOutboundMetadataInteractionControls(t *testing.T) {
	approval := OutboundMetadata{
		InteractionKind:     OutboundInteractionApproval,
		InteractionControls: OutboundInteractionControlsPrompt,
		InteractionID:       "approval-1",
		InteractionShortID:  "short-1",
	}
	if !approval.IsApprovalPrompt() || approval.RemovesInteractionControls() || !approval.BypassesPlaceholderEdit() {
		t.Fatalf("approval prompt metadata = %#v", approval)
	}

	question := OutboundMetadata{
		InteractionKind:     OutboundInteractionQuestion,
		InteractionControls: OutboundInteractionControlsPrompt,
		InteractionID:       "question-1",
		InteractionShortID:  "short-2",
	}.WithInteractionChoices([]string{"Yes", "No"})
	if !question.IsQuestionPrompt() || question.IsApprovalPrompt() || len(question.InteractionChoices()) != 2 {
		t.Fatalf("question prompt metadata = %#v", question)
	}

	removal := OutboundMetadata{
		InteractionKind:     OutboundInteractionQuestion,
		InteractionControls: OutboundInteractionControlsRemove,
	}
	if removal.IsQuestionPrompt() || !removal.RemovesInteractionControls() {
		t.Fatalf("question removal metadata = %#v", removal)
	}
	for _, metadata := range []OutboundMetadata{approval, question, removal} {
		if err := ValidateOutboundMetadata(metadata); err != nil {
			t.Fatalf("ValidateOutboundMetadata(%#v) error = %v", metadata, err)
		}
	}
}

func TestOutboundMessageJSONOmitsZeroMetadata(t *testing.T) {
	encoded, err := json.Marshal(OutboundMessage{Content: "hello"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), `"metadata"`) {
		t.Fatalf("zero metadata was serialized: %s", encoded)
	}

	encoded, err = json.Marshal(OutboundMessage{
		Content:  "hello",
		Metadata: OutboundMetadata{OutboundKind: OutboundKindFinal},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !strings.Contains(string(encoded), `"metadata":{"outbound_kind":"final"}`) {
		t.Fatalf("typed metadata was not serialized: %s", encoded)
	}
}
