package document

import (
	"crypto/rand"
	"errors"
	"testing"
)

func TestFormIntakePriorResolutionEnvelopeRequiresNewReaderOnlyAfterFirstSettlement(t *testing.T) {
	// Model the relevant pre-settlement outcome shape, using the real authenticated
	// envelope decoder. Unknown journal properties must not be silently discarded.
	type oldOutcome struct {
		Digest          string   `json:"digest"`
		AppliedRevision int64    `json:"applied_revision"`
		AcceptedIDs     []string `json:"accepted_ids,omitempty"`
		Consumed        bool     `json:"consumed"`
	}
	key := make([]byte, 32)
	for _, withSettlement := range []bool{false, true} {
		payload := formIntakeStoredOutcome{
			Digest:          "test-digest",
			AppliedRevision: 2,
			AcceptedIDs:     []string{fillFieldID(1)},
		}
		if withSettlement {
			payload.PriorResolutions = []formIntakeStoredResolution{{
				InputID: "old", ThroughPasses: 1, Span: formIntakeSpan{Start: 0, End: 4, FieldID: fillFieldID(1)},
			}}
		}
		envelope, err := sealFormJobEnvelope(
			key,
			formJobEnvelope{Kind: "test", JobID: "test-job", Revision: 2},
			payload,
			rand.Reader,
		)
		if err != nil {
			t.Fatal(err)
		}
		var legacy oldOutcome
		err = openFormJobEnvelope(key, envelope, &legacy)
		if withSettlement && !errors.Is(err, ErrFormJobRecordCorrupt) || !withSettlement && err != nil {
			t.Fatal("legacy decoder did not preserve its fail-closed upgrade boundary")
		}
		var current formIntakeStoredOutcome
		if err = openFormJobEnvelope(
			key,
			envelope,
			&current,
		); err != nil ||
			len(current.PriorResolutions) != len(payload.PriorResolutions) {
			t.Fatal("new reader lost legacy compatibility or current settlement metadata")
		}
	}
}

func TestFormIntakeStoredPriorResolutionsFailClosedOnMalformedLinks(t *testing.T) {
	for _, name := range []string{
		"valid", "old payload", "unknown input", "current input", "zero through", "future through",
		"scope overlap", "unoffered field", "no current acceptance", "stale admission", "duplicate", "unaccounted span",
	} {
		t.Run(name, func(t *testing.T) {
			field := fillFieldID(1)
			span := formIntakeSpan{Start: 0, End: 4, FieldID: field}
			outcome := &formIntakeStoredOutcome{
				AcceptedIDs:      []string{field},
				PriorResolutions: []formIntakeStoredResolution{{InputID: "old", ThroughPasses: 1, Span: span}},
			}
			mode := &formIntakeStoredMode{Inputs: []formIntakeStoredInput{
				{ID: "old", Accounted: []formIntakeSpan{span}, Reservations: []formIntakeStoredPass{{}}},
				{ID: "new", Reservations: []formIntakeStoredPass{{
					ReceiptCount: 2, CandidateIDs: []string{field}, Outcome: outcome,
				}}},
			}}
			switch name {
			case "old payload":
				outcome.PriorResolutions = nil
			case "unknown input":
				outcome.PriorResolutions[0].InputID = "absent"
			case "current input":
				outcome.PriorResolutions[0].InputID = "new"
			case "zero through":
				outcome.PriorResolutions[0].ThroughPasses = 0
			case "future through":
				outcome.PriorResolutions[0].ThroughPasses = 2
			case "scope overlap":
				outcome.PriorResolutions[0].Span.ScopedInstruction = true
			case "unoffered field":
				mode.Inputs[1].Reservations[0].CandidateIDs = []string{fillFieldID(2)}
			case "no current acceptance":
				outcome.AcceptedIDs = nil
			case "stale admission":
				mode.Inputs[1].Reservations[0].ReceiptCount = 1
			case "duplicate":
				outcome.PriorResolutions = append(outcome.PriorResolutions, outcome.PriorResolutions[0])
			case "unaccounted span":
				mode.Inputs[0].Accounted = nil
			}
			err := validateFormIntakePriorResolutions(mode)
			if name == "valid" || name == "old payload" {
				if err != nil {
					t.Fatalf("valid or legacy source rejected: %v", err)
				}
			} else if !errors.Is(err, ErrFormJobRecordCorrupt) {
				t.Fatal("malformed immutable prior link was accepted")
			}
		})
	}
}
