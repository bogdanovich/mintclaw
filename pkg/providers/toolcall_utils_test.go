package providers

import "testing"

func TestNormalizeToolCallInitializesCanonicalArguments(t *testing.T) {
	tc := NormalizeToolCall(ToolCall{
		ID:               "call_1",
		Name:             "search",
		ThoughtSignature: "sig-1",
	})

	if tc.ThoughtSignature != "sig-1" {
		t.Fatalf("ThoughtSignature = %q, want sig-1", tc.ThoughtSignature)
	}
	if tc.Arguments == nil {
		t.Fatal("Arguments is nil")
	}
}
