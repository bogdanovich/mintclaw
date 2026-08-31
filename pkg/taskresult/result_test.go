package taskresult

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDeliverableJSONRoundTrip(t *testing.T) {
	want := &Deliverable{
		Text: "Yakima published; Vissani could not be verified",
		Artifacts: []Artifact{{
			Ref: "media://proof", LocalPath: "/tmp/proof.png",
			Kind: "image", Filename: "proof.png", ContentType: "image/png",
		}},
		Metadata: map[string]string{"producer": "browser"},
		ObjectiveOutcome: &Outcome{
			Status:      OutcomePartial,
			Explanation: "source photos were missing",
			CompletedItems: []Item{{
				Item: "Yakima published", Kind: "external_action",
				Receipts: []Receipt{{
					ID: "inv-yakima", Kind: "external_action", Tool: "browser_act",
					Metadata: map[string]string{"effect": "external_commit"},
				}},
			}},
			MissingItems: []string{"Vissani could not be verified"},
		},
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Deliverable
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&got, want) {
		t.Fatalf("round trip = %#v, want %#v", &got, want)
	}
}

func TestDeliverableAcceptsAdditiveFields(t *testing.T) {
	data := []byte(`{
		"text":"done",
		"future_top_level":true,
		"artifacts":[{"ref":"media://proof","future_artifact":"hint"}],
		"report":{
			"schema_version":"deliverable_report.v1",
			"report_id":"report-1",
			"future_report_field":{"hint":"value"}
		},
		"objective_outcome":{
			"status":"succeeded",
			"future_outcome":42,
			"completed_items":[{
				"item":"published",
				"receipts":[{"id":"inv-1","future_receipt":"evidence"}]
			}]
		}
	}`)

	var got Deliverable
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("additive fields should be tolerated: %v", err)
	}
	if got.Text != "done" || len(got.Artifacts) != 1 ||
		got.Report == nil || got.Report.ReportID != "report-1" ||
		got.ObjectiveOutcome == nil || got.ObjectiveOutcome.Status != OutcomeSucceeded ||
		len(got.ObjectiveOutcome.CompletedItems) != 1 ||
		got.ObjectiveOutcome.CompletedItems[0].Receipts[0].ID != "inv-1" {
		t.Fatalf("known fields were not preserved: %#v", got)
	}
}

func TestCloneDeliverableDetachesNestedState(t *testing.T) {
	original := &Deliverable{
		Metadata: map[string]string{"producer": "browser"},
		Report: &Report{
			Claims: []Claim{{SourceRefs: []string{"source"}, Metadata: map[string]string{"key": "value"}}},
		},
		ObjectiveOutcome: &Outcome{CompletedItems: []Item{{
			Receipts: []Receipt{{Metadata: map[string]string{"effect": "external_commit"}}},
			Output: &ObjectiveOutput{
				Kind: "records", Records: []map[string]string{{"title": "Desk"}},
				ArtifactRefs: []string{"file:/tmp/report.json"},
			},
		}}},
	}
	cloned := CloneDeliverable(original)

	cloned.Metadata["producer"] = "mutated"
	cloned.Report.Claims[0].SourceRefs[0] = "mutated"
	cloned.Report.Claims[0].Metadata["key"] = "mutated"
	cloned.ObjectiveOutcome.CompletedItems[0].Receipts[0].Metadata["effect"] = "mutated"
	cloned.ObjectiveOutcome.CompletedItems[0].Output.Records[0]["title"] = "mutated"
	cloned.ObjectiveOutcome.CompletedItems[0].Output.ArtifactRefs[0] = "mutated"

	if original.Metadata["producer"] != "browser" ||
		original.Report.Claims[0].SourceRefs[0] != "source" ||
		original.Report.Claims[0].Metadata["key"] != "value" ||
		original.ObjectiveOutcome.CompletedItems[0].Receipts[0].Metadata["effect"] != "external_commit" ||
		original.ObjectiveOutcome.CompletedItems[0].Output.Records[0]["title"] != "Desk" ||
		original.ObjectiveOutcome.CompletedItems[0].Output.ArtifactRefs[0] != "file:/tmp/report.json" {
		t.Fatalf("clone aliased original state: %#v", original)
	}
}
