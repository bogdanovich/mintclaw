package bus

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeOutboundMetadataCanonicalizesCurrentContract(t *testing.T) {
	metadata := NormalizeOutboundMetadata(OutboundMetadata{
		MessageKind:        " final_reply ",
		ToolCalls:          json.RawMessage(` [{"id":"call-1"}] `),
		OutboundKind:       " final ",
		ModelName:          " fallback-model ",
		DefaultModelName:   " primary-model ",
		UsageInputTokens:   -1,
		UsageOutputTokens:  4500,
		UsageTotalTokens:   4500,
		InteractionID:      " interaction-1 ",
		InteractionShortID: " short-1 ",
		RequestID:          " request-1 ",
		Choices:            []string{" Yes ", "No"},
	})

	if metadata.MessageKind != OutboundMessageKindFinalReply || string(metadata.ToolCalls) != `[{"id":"call-1"}]` ||
		metadata.OutboundKind != OutboundKindFinal || metadata.ModelName != "fallback-model" ||
		metadata.DefaultModelName != "primary-model" || metadata.UsageInputTokens != 0 ||
		metadata.UsageOutputTokens != 4500 || metadata.UsageTotalTokens != 4500 ||
		metadata.InteractionID != "interaction-1" || metadata.InteractionShortID != "short-1" ||
		metadata.RequestID != "request-1" || len(metadata.Choices) != 2 || metadata.Choices[0] != "Yes" {
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
		"invalid tool calls": {ToolCalls: json.RawMessage("{")},
		"invalid choices":    {Choices: []string{"Yes", ""}},
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
	}
	if !approval.IsApprovalPrompt() || approval.RemovesInteractionControls() || !approval.BypassesPlaceholderEdit() {
		t.Fatalf("approval prompt metadata = %#v", approval)
	}

	question := OutboundMetadata{
		InteractionKind:     OutboundInteractionQuestion,
		InteractionControls: OutboundInteractionControlsPrompt,
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
