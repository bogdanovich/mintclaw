package protocoltypes

import (
	"encoding/json"
	"testing"
)

func TestPromptSectionsNeverChangeProviderJSON(t *testing.T) {
	block := ContentBlock{Type: "text", Text: "same provider text", CacheControl: &CacheControl{Type: "ephemeral"}}
	before, err := json.Marshal(block)
	if err != nil {
		t.Fatal(err)
	}
	block.PromptSections = []PromptSection{
		{Start: 0, End: len(block.Text), Layer: "instruction", Slot: "workspace", Source: "workspace.definition"},
	}
	after, err := json.Marshal(block)
	if err != nil || string(before) != string(after) {
		t.Fatal("internal provenance changed provider JSON", err)
	}
}
