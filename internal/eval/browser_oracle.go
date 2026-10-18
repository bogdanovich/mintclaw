package eval

import (
	"bytes"
	"encoding/csv"
	"strings"
)

// This is a fixture-only witness, not native browser authority, context,
// delivery, privacy or cleanup evidence. It never serializes expected values.
type browserFixtureOracle struct {
	Version         int     `json:"version"`
	Scope           string  `json:"scope"`
	PageSHA256      string  `json:"page_sha256"`
	CSVSourceSHA256 string  `json:"csv_source_sha256"`
	Visits          int     `json:"visits"`
	Downloads       int     `json:"downloads"`
	Submissions     int     `json:"submissions"`
	Removals        int     `json:"removals"`
	FixtureClosed   bool    `json:"fixture_closed"` // Sealed trial, not listener release or native quiescence.
	Effects         Verdict `json:"effects"`
	EffectsCode     string  `json:"effects_code"`
	Read            Verdict `json:"read"`
	ReadCode        string  `json:"read_code"`
	Artifact        Verdict `json:"artifact"`
	ArtifactCode    string  `json:"artifact_code"`
	ArtifactSHA256  string  `json:"artifact_sha256,omitempty"`
}

func inspectBrowserFixture(
	item Case, fixture *browserFixture, events []Event, artifact []byte,
) (browserFixtureOracle, error) {
	if item.Validate() != nil || item.Browser == nil || fixture == nil {
		return browserFixtureOracle{}, ErrBrowserFixture
	}
	expected := browserFixtureSubmission{}
	for _, field := range item.Browser.Fields {
		value := field.Value
		if field.Fact != "" {
			value = item.Driver.Facts[field.Fact]
			if value == "{{generate}}" {
				return browserFixtureOracle{}, ErrBrowserFixture
			}
		}
		switch field.Name {
		case "name":
			expected.name = value
		case "color":
			expected.color = value
		case "note":
			expected.note = value
		}
	}
	state := fixture.snapshot()
	settled := state.closed && !state.failed && !state.overflow
	result := browserFixtureOracle{
		Version: Version, Scope: "local_fixture_only", PageSHA256: digest(fixture.page),
		CSVSourceSHA256: digest(fixture.file), Visits: state.visits, Downloads: state.downloads,
		Submissions: len(state.submissions), Removals: len(state.removals), FixtureClosed: state.closed,
		Effects: Inconclusive, EffectsCode: "fixture_unsettled",
		Read: Inconclusive, ReadCode: "not_required", Artifact: Inconclusive, ArtifactCode: "not_required",
	}
	matched := len(state.submissions) == 0 && len(state.removals) == 0
	contradicted := !matched
	switch item.Browser.Scenario {
	case "submit", "followup":
		matched = len(state.submissions) == 1 && state.submissions[0] == expected && len(state.removals) == 0
		contradicted = len(state.removals) != 0 || len(state.submissions) > 1 ||
			len(state.submissions) == 1 && state.submissions[0] != expected
	case "ambiguous":
		matched = len(state.submissions) == 0 && len(state.removals) == 1 && state.removals[0] == "south"
		contradicted = len(state.submissions) != 0 || len(state.removals) > 1 ||
			len(state.removals) == 1 && state.removals[0] != "south"
	}
	// An observed wrong effect remains a failure even if later evidence is lost
	// or the fixture fails. Absence of effects requires a closed, healthy witness.
	if contradicted {
		result.Effects, result.EffectsCode = Fail, "fixture_effect_mismatch"
	} else if settled {
		result.Effects, result.EffectsCode = Fail, "fixture_effect_missing"
		if matched {
			result.Effects, result.EffectsCode = Pass, "matched"
		}
	}
	if item.Browser.Scenario == "read" {
		last := lastOutcome(events)
		result.ReadCode = "read_evidence_missing"
		if last.Kind == "final" && last.ID != "" && state.visits > 0 {
			result.Read, result.ReadCode = Fail, "fixture_reference_mismatch"
			if strings.Contains(last.Content, fixture.code) {
				result.Read, result.ReadCode = Inconclusive, "fixture_unsettled"
				if settled {
					result.Read, result.ReadCode = Pass, "matched"
				}
			}
		}
	}
	if item.Browser.Scenario == "artifact" {
		result.ArtifactCode = "artifact_missing"
		if len(artifact) > 0 {
			result.Artifact, result.ArtifactCode = Fail, "fixture_artifact_mismatch"
			if len(artifact) == len(fixture.file) {
				reader := csv.NewReader(bytes.NewReader(artifact))
				reader.FieldsPerRecord = 2
				rows, err := reader.ReadAll()
				if err == nil && len(rows) == 2 && rows[0][0] == "session" && rows[0][1] == "reference" &&
					fixture.path == "/eval/"+rows[1][0]+"/" && rows[1][1] == fixture.code &&
					digest(artifact) == result.CSVSourceSHA256 {
					result.Artifact, result.ArtifactCode = Inconclusive, "fixture_download_missing"
					if state.downloads > 0 {
						result.ArtifactCode = "fixture_unsettled"
						if settled {
							result.Artifact, result.ArtifactCode = Pass, "matched"
						}
					}
				}
			}
			if result.Artifact == Pass {
				result.ArtifactSHA256 = digest(artifact)
			}
		}
	}
	return result, nil
}
