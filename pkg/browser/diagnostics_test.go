package browser

import (
	"strings"
	"testing"
)

func TestNormalizeDiagnosticCategoriesUsesDeterministicOrder(t *testing.T) {
	got, err := NormalizeDiagnosticCategories([]DiagnosticCategory{
		DiagnosticPageCrashes, DiagnosticConsoleErrors, DiagnosticFailedRequests,
	})
	want := []DiagnosticCategory{
		DiagnosticConsoleErrors, DiagnosticFailedRequests, DiagnosticPageCrashes,
	}
	if err != nil || !equalDiagnosticCategories(got, want) {
		t.Fatalf("NormalizeDiagnosticCategories() = %#v, %v", got, err)
	}
	if _, err = NormalizeDiagnosticCategories([]DiagnosticCategory{
		DiagnosticConsoleErrors, DiagnosticConsoleErrors,
	}); err == nil {
		t.Fatal("duplicate diagnostic categories were accepted")
	}
}

func TestValidateDiagnosticSummaryEnforcesPrivacyAndIndependentByteLimits(t *testing.T) {
	hash := strings.Repeat("a", 64)
	valid := DiagnosticSummary{Categories: []DiagnosticCategorySummary{{
		Category: DiagnosticFailedRequests, Count: 1,
		Entries: []DiagnosticEntry{{
			Timestamp: 1, ResourceClass: "fetch", FailureCode: "http_error",
			Origin: "https://example.com", Path: "/safe", MessageHash: hash,
		}},
	}}}
	if err := ValidateDiagnosticSummary(valid, []DiagnosticCategory{DiagnosticFailedRequests}); err != nil {
		t.Fatalf("ValidateDiagnosticSummary(valid) error = %v", err)
	}

	unsafe := valid
	unsafe.Categories = append([]DiagnosticCategorySummary(nil), valid.Categories...)
	unsafe.Categories[0].Entries = append([]DiagnosticEntry(nil), valid.Categories[0].Entries...)
	unsafe.Categories[0].Entries[0].Path = "/safe?credential=canary"
	if err := ValidateDiagnosticSummary(unsafe, []DiagnosticCategory{DiagnosticFailedRequests}); err == nil {
		t.Fatal("query-bearing diagnostic path was accepted")
	}

	oversized := valid
	oversized.Categories = append([]DiagnosticCategorySummary(nil), valid.Categories...)
	oversized.Categories[0].Count = 5
	oversized.Categories[0].Entries = make([]DiagnosticEntry, 5)
	for index := range oversized.Categories[0].Entries {
		oversized.Categories[0].Entries[index] = DiagnosticEntry{
			Timestamp: int64(index + 1), ResourceClass: "fetch", FailureCode: "network_failed",
			Origin: "https://example.com", Path: "/" + strings.Repeat("x", 4090), MessageHash: hash,
		}
	}
	if err := ValidateDiagnosticSummary(oversized, []DiagnosticCategory{DiagnosticFailedRequests}); err == nil {
		t.Fatal("oversized diagnostic category was accepted")
	}
}

func equalDiagnosticCategories(left, right []DiagnosticCategory) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
