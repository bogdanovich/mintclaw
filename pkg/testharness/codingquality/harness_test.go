package codingquality

import (
	"encoding/json"
	"testing"
)

func TestCodingQualityGate(t *testing.T) {
	for _, family := range []ToolCallFamily{ObjectArguments, FunctionJSON} {
		for _, scale := range []FixtureScale{SmallFixture, LargeFixture} {
			t.Run(string(family)+"/"+string(scale), func(t *testing.T) {
				report, err := Evaluate(t.Context(), t.TempDir(), t.TempDir(), family, scale)
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(report)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("coding quality metrics: %s", encoded)
				assertPassingReport(t, report)
			})
		}
	}
}

func assertPassingReport(t *testing.T, report Report) {
	t.Helper()
	if report.SchemaVersion != SchemaVersion || !report.FirstAttemptEditCorrect ||
		!report.PatchWriteAuditVerified ||
		(report.Fixture == SmallFixture && report.FixtureFiles != 11) ||
		(report.Fixture == LargeFixture && report.FixtureFiles != 411) ||
		!report.StalePatchRejected || report.SearchExpectedFiles != 2 ||
		report.SearchUnexpectedFiles != 0 || !report.SearchExactFiles || report.SearchOutputBytes <= 0 ||
		report.SearchOutputBytes > 2000 || report.SearchEstimatedTokens > 500 ||
		!report.LongReadBounded || !report.UnicodeRoundTrip || !report.AwkwardPathRoundTrip ||
		!report.IgnoredGeneratedExcluded || !report.BinaryContentExcluded ||
		!report.RenameVisible || !report.DeletedReadActionable ||
		report.CommandOutputBytes <= 0 || report.CommandOutputBytes > maxExpectedCommandContext ||
		report.CommandEstimatedTokens > 3000 || !report.CommandArtifactRetained ||
		!report.CancellationClassified || !report.RecoverySucceeded {
		t.Fatalf("coding quality gate failed: %+v", report)
	}
}
