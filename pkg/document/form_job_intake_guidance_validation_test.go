package document

import (
	"crypto/rand"
	"errors"
	"testing"
)

func TestFormIntakeAdvisoryResolutionEnvelopeRequiresNewReader(t *testing.T) {
	type legacyResolution struct {
		InputID       string         `json:"input_id"`
		ThroughPasses int            `json:"through_passes"`
		Span          formIntakeSpan `json:"span"`
	}
	key := make([]byte, 32)
	for _, advisory := range []bool{false, true} {
		payload := formIntakeStoredResolution{
			InputID: "old", ThroughPasses: 1, Span: formIntakeSpan{Start: 0, End: 4, FieldID: fillFieldID(1)},
		}
		if advisory {
			payload.Span, payload.GuidanceFieldID = formIntakeSpan{}, fillFieldID(1)
		}
		envelope, err := sealFormJobEnvelope(
			key,
			formJobEnvelope{Kind: "test", JobID: "test", Revision: 2},
			payload,
			rand.Reader,
		)
		if err != nil {
			t.Fatal(err)
		}
		var legacy legacyResolution
		err = openFormJobEnvelope(key, envelope, &legacy)
		if advisory && !errors.Is(err, ErrFormJobRecordCorrupt) || !advisory && err != nil {
			t.Fatal("advisory upgrade did not preserve legacy fail-closed behavior")
		}
		var current formIntakeStoredResolution
		if err = openFormJobEnvelope(key, envelope, &current); err != nil || current != payload {
			t.Fatal("new reader lost a legacy span or advisory resolution")
		}
	}
}

func TestFormIntakeAdvisoryResolutionValidation(t *testing.T) {
	for _, name := range []string{
		"valid", "both variants", "neither variant", "unknown field", "no current acceptance", "unaccounted",
		"duplicate", "future snapshot", "metadata overflow",
	} {
		t.Run(name, func(t *testing.T) {
			field := fillFieldID(1)
			outcome := &formIntakeStoredOutcome{
				AcceptedIDs: []string{field},
				PriorResolutions: []formIntakeStoredResolution{
					{InputID: "old", ThroughPasses: 1, GuidanceFieldID: field},
				},
			}
			mode := &formIntakeStoredMode{Inputs: []formIntakeStoredInput{
				{
					ID:           "old",
					State:        formIntakeInputCommitted,
					Text:         "data",
					Accounted:    []formIntakeSpan{{Start: 0, End: 4}},
					Reservations: []formIntakeStoredPass{{}},
				},
				{
					ID: "new",
					Reservations: []formIntakeStoredPass{
						{ReceiptCount: 2, CandidateIDs: []string{field}, Outcome: outcome},
					},
				},
			}}
			switch name {
			case "both variants":
				outcome.PriorResolutions[0].Span = formIntakeSpan{Start: 0, End: 4, FieldID: field}
			case "neither variant":
				outcome.PriorResolutions[0].GuidanceFieldID = ""
			case "unknown field":
				outcome.PriorResolutions[0].GuidanceFieldID = fillFieldID(2)
			case "no current acceptance":
				outcome.AcceptedIDs = nil
			case "unaccounted":
				mode.Inputs[0].Accounted = nil
			case "duplicate":
				outcome.PriorResolutions = append(outcome.PriorResolutions, outcome.PriorResolutions[0])
			case "future snapshot":
				outcome.PriorResolutions[0].ThroughPasses = 2
			case "metadata overflow":
				outcome.PriorResolutions = make(
					[]formIntakeStoredResolution,
					MaxFormIntakeCandidates*MaxFormIntakePendingInputs+1,
				)
			}
			err := validateFormIntakePriorResolutions(mode)
			if name == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrFormJobRecordCorrupt) {
				t.Fatal("malformed advisory journal was accepted")
			}
		})
	}
}

func TestFormIntakeAdvisoryResolutionDoesNotHideNativeOrLaterFindings(t *testing.T) {
	for _, code := range []string{"field_invalid", "field_conflicting", "field_unsupported", "field_ambiguous"} {
		for _, later := range []bool{false, true} {
			field := fillFieldID(1)
			input := formIntakeStoredInput{
				ID: "old",
				Reservations: []formIntakeStoredPass{{Outcome: &formIntakeStoredOutcome{
					Findings: []FormFieldMappingBlocker{{FieldID: field, Code: code}},
				}}},
			}
			if later {
				input.Reservations = append([]formIntakeStoredPass{{}}, input.Reservations...)
			}
			mode := &formIntakeStoredMode{
				Inputs: []formIntakeStoredInput{input, {ID: "new", Reservations: []formIntakeStoredPass{
					{
						Outcome: &formIntakeStoredOutcome{
							PriorResolutions: []formIntakeStoredResolution{
								{InputID: "old", ThroughPasses: 1, GuidanceFieldID: field},
							},
						},
					},
				}}},
			}
			findings := formIntakeCurrentModeFindings(mode, input)
			want := 1
			if code == "field_ambiguous" && !later {
				want = 0
			}
			if len(findings) != want {
				t.Fatal("advisory snapshot hid a native or later finding")
			}
		}
	}
}
