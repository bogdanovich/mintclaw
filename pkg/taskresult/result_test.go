package taskresult

import (
	"encoding/json"
	"reflect"
	"strings"
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
			UserSummary: "Yakima published; source photos were missing for Vissani.",
			CompletedItems: []Item{{
				Item: "Yakima published", Kind: "external_action",
				Receipts: []Receipt{{
					ID: "inv-yakima", Kind: "external_action", Tool: "browser_act",
					Metadata: map[string]string{"effect": "external_commit"},
				}},
			}},
			MissingItems:   []string{"Vissani could not be verified"},
			NotNeededItems: []string{"authentication handoff"},
		},
		LifecycleReceipts: []Receipt{{
			ID: "browser_cleanup_receipt", Kind: ReceiptKindResourceCleanup,
			Target: "browser:gateway/managed", Action: "close", Tool: "browser_session",
			Summary:  "Browser session cleanup reached terminal state.",
			Metadata: map[string]string{"state": "closed"},
		}},
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

func TestCloneDeliverableRetainsPrivateRecoveryWithoutPathsOrAliases(t *testing.T) {
	original := &Deliverable{Artifacts: []Artifact{{
		Ref: "media://retained", LocalPath: "/private/runtime/image.png",
		Recovery: &ArtifactRecovery{
			Kind: "browser_screenshot_claim", MediaRef: "media://retained",
			Additional: []ArtifactRecovery{{MediaRef: "media://second"}},
		},
	}}}
	clone := CloneDeliverable(original)
	if clone.Artifacts[0].LocalPath != "" || clone.Artifacts[0].Recovery == original.Artifacts[0].Recovery {
		t.Fatalf("clone exposed a path or aliased private recovery: %#v", clone)
	}
	clone.Artifacts[0].Recovery.Additional[0].MediaRef = "mutated"
	if original.Artifacts[0].Recovery.Additional[0].MediaRef != "media://second" {
		t.Fatal("clone aliases the recovery batch")
	}
	encoded, err := json.Marshal(clone)
	if err != nil {
		t.Fatal(err)
	}
	var recovered Deliverable
	if err = json.Unmarshal(encoded, &recovered); err != nil || recovered.Artifacts[0].Recovery == nil {
		t.Fatalf("durable artifact recovery lost: %#v, %v", recovered, err)
	}
}

func TestCloneOutcomeDetachesNotNeededItems(t *testing.T) {
	original := &Outcome{
		Status: OutcomeSucceeded, NotNeededItems: []string{"authentication handoff"}, UserSummary: "Already signed in.",
	}
	cloned := CloneOutcome(original)
	cloned.NotNeededItems[0] = "mutated"
	cloned.UserSummary = "mutated"
	if original.NotNeededItems[0] != "authentication handoff" || original.UserSummary != "Already signed in." {
		t.Fatal("conditional lifecycle disposition escaped outcome cloning")
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
		LifecycleReceipts: []Receipt{{
			ID: "browser_cleanup_receipt", Metadata: map[string]string{"state": "closed"},
		}},
	}
	cloned := CloneDeliverable(original)

	cloned.Metadata["producer"] = "mutated"
	cloned.Report.Claims[0].SourceRefs[0] = "mutated"
	cloned.Report.Claims[0].Metadata["key"] = "mutated"
	cloned.ObjectiveOutcome.CompletedItems[0].Receipts[0].Metadata["effect"] = "mutated"
	cloned.ObjectiveOutcome.CompletedItems[0].Output.Records[0]["title"] = "mutated"
	cloned.ObjectiveOutcome.CompletedItems[0].Output.ArtifactRefs[0] = "mutated"
	cloned.LifecycleReceipts[0].Metadata["state"] = "mutated"

	if original.Metadata["producer"] != "browser" ||
		original.Report.Claims[0].SourceRefs[0] != "source" ||
		original.Report.Claims[0].Metadata["key"] != "value" ||
		original.ObjectiveOutcome.CompletedItems[0].Receipts[0].Metadata["effect"] != "external_commit" ||
		original.ObjectiveOutcome.CompletedItems[0].Output.Records[0]["title"] != "Desk" ||
		original.ObjectiveOutcome.CompletedItems[0].Output.ArtifactRefs[0] != "file:/tmp/report.json" ||
		original.LifecycleReceipts[0].Metadata["state"] != "closed" {
		t.Fatalf("clone aliased original state: %#v", original)
	}
}

func TestStandaloneResultOutputProjectsOnlyOneBoundedSucceededResult(t *testing.T) {
	output := &ObjectiveOutput{
		Kind:    "records",
		Records: []map[string]string{{"state": "ready"}},
	}
	deliverable := &Deliverable{ObjectiveOutcome: &Outcome{
		Status: OutcomeSucceeded,
		CompletedItems: []Item{{
			Item: "Return the probe result", Kind: ObjectiveKindResult, Output: output,
		}},
	}}

	projected := StandaloneResultOutput(deliverable)
	if projected == nil || projected.Kind != "records" || projected.Records[0]["state"] != "ready" {
		t.Fatalf("StandaloneResultOutput() = %#v", projected)
	}
	projected.Records[0]["state"] = "mutated"
	if output.Records[0]["state"] != "ready" {
		t.Fatal("standalone projection mutated the canonical deliverable")
	}

	for name, mutate := range map[string]func(*Deliverable){
		"partial": func(value *Deliverable) { value.ObjectiveOutcome.Status = OutcomePartial },
		"missing": func(value *Deliverable) { value.ObjectiveOutcome.MissingItems = []string{"missing"} },
		"mixed": func(value *Deliverable) {
			value.ObjectiveOutcome.CompletedItems = append(value.ObjectiveOutcome.CompletedItems, Item{
				Item: "commit", Kind: ObjectiveKindExternalAction,
			})
		},
		"oversized": func(value *Deliverable) {
			value.ObjectiveOutcome.CompletedItems[0].Output = &ObjectiveOutput{
				Kind: "text", Text: strings.Repeat("x", MaxStandaloneResultOutputBytes),
			}
		},
		"invalid output": func(value *Deliverable) {
			value.ObjectiveOutcome.CompletedItems[0].Output = &ObjectiveOutput{Kind: "records"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := CloneDeliverable(deliverable)
			mutate(candidate)
			if got := StandaloneResultOutput(candidate); got != nil {
				t.Fatalf("StandaloneResultOutput() = %#v, want nil", got)
			}
		})
	}
}

func TestStandaloneResultOutputProjectsOneResultWithReceiptedObjectives(t *testing.T) {
	for _, kind := range []string{ObjectiveKindExternalAction, ObjectiveKindLiveHandoff} {
		t.Run(kind, func(t *testing.T) {
			value := &Deliverable{Text: "Human-readable answer", ObjectiveOutcome: &Outcome{
				Status: OutcomeSucceeded,
				CompletedItems: []Item{
					{Item: "Complete the action", Kind: kind, Receipts: []Receipt{{ID: "runtime-receipt", Kind: kind}}},
					{Item: "Return data", Kind: ObjectiveKindResult, Output: &ObjectiveOutput{
						Kind: "records", Records: []map[string]string{{"state": "uploaded"}},
					}},
				},
			}}
			output := StandaloneResultOutput(value)
			if output == nil || output.Records[0]["state"] != "uploaded" {
				t.Fatalf("StandaloneResultOutput() = %#v", output)
			}
			output.Records[0]["state"] = "mutated"
			if value.Text != "Human-readable answer" ||
				value.ObjectiveOutcome.CompletedItems[1].Output.Records[0]["state"] != "uploaded" {
				t.Fatal("projection changed the canonical result or human presentation")
			}
			for name, mutate := range map[string]func(*Deliverable){
				"partial":    func(v *Deliverable) { v.ObjectiveOutcome.Status = OutcomePartial },
				"blocked":    func(v *Deliverable) { v.ObjectiveOutcome.Status = OutcomeBlocked },
				"missing":    func(v *Deliverable) { v.ObjectiveOutcome.MissingItems = []string{"verification"} },
				"no receipt": func(v *Deliverable) { v.ObjectiveOutcome.CompletedItems[0].Receipts = nil },
				"empty receipt ID": func(v *Deliverable) {
					v.ObjectiveOutcome.CompletedItems[0].Receipts[0].ID = " "
				},
				"wrong receipt kind": func(v *Deliverable) {
					v.ObjectiveOutcome.CompletedItems[0].Receipts[0].Kind = "unverified"
				},
				"unknown objective": func(v *Deliverable) { v.ObjectiveOutcome.CompletedItems[0].Kind = "unknown" },
				"effect with output": func(v *Deliverable) {
					v.ObjectiveOutcome.CompletedItems[0].Output = v.ObjectiveOutcome.CompletedItems[1].Output
				},
				"multiple results": func(v *Deliverable) {
					v.ObjectiveOutcome.CompletedItems = append(
						v.ObjectiveOutcome.CompletedItems, v.ObjectiveOutcome.CompletedItems[1],
					)
				},
				"no result":         func(v *Deliverable) { v.ObjectiveOutcome.CompletedItems = v.ObjectiveOutcome.CompletedItems[:1] },
				"no result payload": func(v *Deliverable) { v.ObjectiveOutcome.CompletedItems[1].Output = nil },
			} {
				t.Run(name, func(t *testing.T) {
					candidate := CloneDeliverable(value)
					mutate(candidate)
					if got := StandaloneResultOutput(candidate); got != nil {
						t.Fatalf("StandaloneResultOutput() = %#v, want nil", got)
					}
				})
			}
			// The result may precede the effect without changing the projection.
			items := value.ObjectiveOutcome.CompletedItems
			items[0], items[1] = items[1], items[0]
			if output := StandaloneResultOutput(value); output == nil || output.Records[0]["state"] != "uploaded" {
				t.Fatalf("result-first projection = %#v", output)
			}
		})
	}
}

func TestNormalizeObjectiveOutputRejectsInvalidKindPayloads(t *testing.T) {
	for name, output := range map[string]*ObjectiveOutput{
		"missing":            nil,
		"empty":              {},
		"unsupported":        {Kind: "unknown"},
		"empty records":      {Kind: "records"},
		"records with text":  {Kind: "records", Text: "extra", Records: []map[string]string{{"state": "ready"}}},
		"artifact with text": {Kind: "artifact", Text: "extra", ArtifactRefs: []string{"artifact-1"}},
		"truncated text":     {Kind: "text", Text: "partial", Truncated: true},
		"empty record value": {Kind: "records", Records: []map[string]string{{"state": ""}}},
		"empty artifact ref": {Kind: "artifact", ArtifactRefs: []string{""}},
	} {
		t.Run(name, func(t *testing.T) {
			if normalized, reason := NormalizeObjectiveOutput(output, nil); normalized != nil || reason == "" {
				t.Fatalf("NormalizeObjectiveOutput() = (%#v, %q), want rejection", normalized, reason)
			}
		})
	}
}

func TestNormalizeObjectiveOutputCanonicalizesDetachedRecords(t *testing.T) {
	original := &ObjectiveOutput{
		Kind: " records ", Records: []map[string]string{{" state ": " ready "}},
	}
	normalized, reason := NormalizeObjectiveOutput(original, &ObjectiveAcceptance{
		OutputKind: "records", RequiredFields: []string{"state"}, MinItems: 1,
	})
	if reason != "" || normalized == nil || normalized.Kind != "records" ||
		normalized.Records[0]["state"] != "ready" {
		t.Fatalf("NormalizeObjectiveOutput() = (%#v, %q)", normalized, reason)
	}
	normalized.Records[0]["state"] = "mutated"
	if original.Records[0][" state "] != " ready " {
		t.Fatal("normalized output aliases its input")
	}
}

func TestNormalizeObjectiveOutputEnforcesDeclaredExactJSON(t *testing.T) {
	acceptance := &ObjectiveAcceptance{OutputKind: "text", ExactJSON: true}
	if normalized, reason := NormalizeObjectiveOutput(
		&ObjectiveOutput{Kind: "text", Text: `{"ok":true,"safe_error":null}`},
		acceptance,
	); reason != "" || normalized == nil {
		t.Fatalf("valid exact JSON = (%#v, %q)", normalized, reason)
	}
	if normalized, reason := NormalizeObjectiveOutput(
		&ObjectiveOutput{Kind: "text", Text: "ok: true"},
		acceptance,
	); normalized != nil || !strings.Contains(reason, "valid exact JSON") {
		t.Fatalf("invalid exact JSON = (%#v, %q)", normalized, reason)
	}
}

func TestCloneObjectiveAcceptancePreservesExactJSONIntent(t *testing.T) {
	original := &ObjectiveAcceptance{OutputKind: "text", ExactJSON: true}
	cloned := CloneObjectiveAcceptance(original)
	if cloned == nil || !cloned.ExactJSON || cloned.OutputKind != "text" {
		t.Fatalf("cloned acceptance = %#v", cloned)
	}
}
