package seahorse

import (
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
