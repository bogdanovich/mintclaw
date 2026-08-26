package seahorse

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestPersonalSummaryPolicyKeepsOriginalPromptContract(t *testing.T) {
	prompt := buildLeafSummaryPrompt(SummaryPolicyPersonal, "segment", "", 200)
	for _, required := range []string{
		"Normal summary policy:",
		"Files: none",
		"Expand for details about:",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("personal prompt missing %q:\n%s", required, prompt)
		}
	}
	for _, codingOnly := range []string{"coding-v1", "Validation must say", "Next action must"} {
		if strings.Contains(prompt, codingOnly) {
			t.Fatalf("personal prompt contains coding-only contract %q:\n%s", codingOnly, prompt)
		}
	}
}

func TestCodingSummaryPolicyRequiresContinuationState(t *testing.T) {
	for name, prompt := range map[string]string{
		"leaf": buildLeafSummaryPrompt(SummaryPolicyCodingV1, "segment", "prior", 200),
		"aggressive": buildAggressiveLeafSummaryPrompt(
			SummaryPolicyCodingV1,
			"segment",
			"prior",
			100,
		),
		"condensed": buildCondensedSummaryPrompt(SummaryPolicyCodingV1, "summaries", 200),
	} {
		t.Run(name, func(t *testing.T) {
			for _, required := range []string{
				"State",
				"Decisions",
				"Files",
				"Validation",
				"Next action",
				"Blockers",
				"artifact",
			} {
				if !strings.Contains(prompt, required) {
					t.Fatalf("coding prompt missing %q:\n%s", required, prompt)
				}
			}
		})
	}
}

func TestCodingToolResultProjectionBoundsSuccessfulOutputAndPreservesEvidence(t *testing.T) {
	largeRead := "read-head\n" + strings.Repeat("r", 3000) +
		"\n[file:/tmp/full-read.log]\n" + strings.Repeat("r", 3000) + "\nread-tail"
	largeExec := "test-head\n" + strings.Repeat("t", 6000) + "\nPASS package/example\ntest-tail"
	largeMutation := "patch applied\n" + strings.Repeat("m", 6000) + "\nmodified pkg/example.go"
	largeFailure := "compile failed\n" + strings.Repeat("f", 6000) + "\nexit status 1"
	messages := retentionTestMessages([]retentionTestCall{
		{id: "read", name: "read_file", status: "success", content: largeRead},
		{id: "exec", name: "exec", status: "success", content: largeExec},
		{id: "mutation", name: "apply_patch", status: "success", content: largeMutation},
		{id: "failure", name: "exec", status: "error", content: largeFailure},
	})

	projected := projectCodingSummaryToolResults(messages, SummaryPolicyCodingV1)
	readResult := toolResultContent(projected, "read")
	if len(readResult) >= len(largeRead) || !strings.Contains(readResult, "status=success") ||
		!strings.Contains(readResult, "[file:/tmp/full-read.log]") ||
		!strings.Contains(readResult, "historical tool output elided") {
		t.Fatalf("bounded read result did not preserve outcome and artifact reference:\n%s", readResult)
	}
	if len(readResult) > 5000 {
		t.Fatalf("bounded read result = %d bytes, want at most 5000", len(readResult))
	}
	execResult := toolResultContent(projected, "exec")
	if len(execResult) >= len(largeExec) || !strings.Contains(execResult, "PASS package/example") ||
		!strings.Contains(execResult, "test-tail") {
		t.Fatalf("bounded exec result did not preserve validation tail:\n%s", execResult)
	}
	if got := toolResultContent(projected, "mutation"); !strings.Contains(got, largeMutation) ||
		!strings.Contains(got, "status=success") || strings.Contains(got, "output elided") {
		t.Fatal("mutation evidence was elided or lost its outcome")
	}
	if got := toolResultContent(projected, "failure"); !strings.Contains(got, largeFailure) ||
		!strings.Contains(got, "status=error") || strings.Contains(got, "output elided") {
		t.Fatal("failure evidence was elided or lost its outcome")
	}
	if got := toolResultContent(messages, "read"); got != largeRead {
		t.Fatal("coding projection mutated its canonical input")
	}
}

func TestPersonalSummaryPolicyDoesNotElideToolOutput(t *testing.T) {
	content := strings.Repeat("personal", 1000)
	messages := retentionTestMessages([]retentionTestCall{
		{id: "read", name: "read_file", status: "success", content: content},
	})
	projected := projectCodingSummaryToolResults(messages, SummaryPolicyPersonal)
	if got := toolResultContent(projected, "read"); got != content {
		t.Fatal("personal policy changed historical tool output")
	}
}

func TestCodingDeterministicFallbackHasContinuationFields(t *testing.T) {
	result := truncateSummary(
		[]Message{{Role: "tool", Content: strings.Repeat("output", 1000)}},
		SummaryPolicyCodingV1,
	)
	for _, required := range []string{"Validation: unknown", "Next action:", "Files:", "Blockers:"} {
		if !strings.Contains(result, required) {
			t.Fatalf("coding fallback missing %q:\n%s", required, result)
		}
	}
	if len(result) > 2048 {
		t.Fatalf("coding fallback is not bounded: %d bytes", len(result))
	}
}

func TestSummaryPolicyReconciliationGenerationIsVersioned(t *testing.T) {
	const base = 2
	if got := SummaryPolicyPersonal.ReconciliationGeneration(base); got != base {
		t.Fatalf("personal generation = %d, want %d", got, base)
	}
	if got := SummaryPolicyCodingV1.ReconciliationGeneration(base); got == base {
		t.Fatalf("coding generation = %d, want distinct versioned generation", got)
	}
}

func TestCodingLeafCompactionKeepsToolBatchTogetherAcrossTokenTarget(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	conversation, err := store.GetOrCreateConversation(ctx, "coding-tool-batch")
	if err != nil {
		t.Fatal(err)
	}

	calls := make([]retentionTestCall, 0, 10)
	for index := 0; index < 10; index++ {
		calls = append(calls, retentionTestCall{
			id:     "call-" + string(rune('a'+index)),
			name:   "read_file",
			status: "success",
			content: "result-head\n" + strings.Repeat(string(rune('a'+index)), 6000) +
				fmt.Sprintf("\nresult-tail-%d", index),
		})
	}
	messages := retentionTestMessages(calls)
	messages = append(messages, Message{Role: "assistant", Content: "inspection complete", TokenCount: 100})
	messages = append(messages, Message{Role: "user", Content: "next turn", TokenCount: 100})
	for index := range messages {
		switch messages[index].Role {
		case "tool":
			messages[index].TokenCount = 3000
		default:
			messages[index].TokenCount = 100
		}
		added, addErr := addRetentionTestMessage(ctx, store, conversation.ConversationID, messages[index])
		if addErr != nil {
			t.Fatal(addErr)
		}
		if appendErr := store.AppendContextMessage(ctx, conversation.ConversationID, added.ID); appendErr != nil {
			t.Fatal(appendErr)
		}
	}

	var prompt string
	engine := &CompactionEngine{
		store:  store,
		config: Config{SummaryPolicy: SummaryPolicyCodingV1},
		complete: func(_ context.Context, input string, _ CompleteOptions) (string, error) {
			prompt = input
			return "State: inspected\nDecisions: none\nFiles: none\nValidation: not run\n" +
				"Next action: continue\nBlockers: none\nExpand for details about: tool output", nil
		},
	}
	if _, err := engine.compactLeaf(ctx, conversation.ConversationID, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "coding tool result: read_file; status=success") ||
		!strings.Contains(prompt, "historical tool output elided") ||
		!strings.Contains(prompt, "result-tail-9") || strings.Contains(prompt, strings.Repeat("j", 6000)) {
		t.Fatalf("coding compaction split or failed to bound the tool batch:\n%s", prompt)
	}
	if strings.Contains(prompt, "next turn") {
		t.Fatal("coding compaction crossed into the next user turn")
	}
}
