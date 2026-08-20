package browseraction

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestActionValidate(t *testing.T) {
	t.Parallel()

	valid := []Action{
		{Kind: ActionNavigate, URL: "https://example.com"},
		{Kind: ActionClick, Ref: "ref_click"},
		{Kind: ActionFill, Ref: "ref_fill", Value: "hello"},
		{Kind: ActionSelect, Ref: "ref_select", Value: "one"},
		{Kind: ActionPress, Target: "document", Key: "Enter"},
		{Kind: ActionScroll, Direction: "down", Amount: MaxScrollAmount},
		{Kind: ActionDialog, DialogID: "dialog_1", Decision: "dismiss"},
		{Kind: ActionCheck, Ref: "ref_check"},
		{Kind: ActionUncheck, Ref: "ref_uncheck"},
		{Kind: ActionHover, Ref: "ref_hover"},
		{Kind: ActionDrag, SourceRef: "ref_source", DestinationRef: "ref_destination"},
		{Kind: ActionFileChooser, Ref: "ref_file", ArtifactRef: "transfer-artifact://artifact_1"},
		{Kind: ActionDownload, Ref: "ref_download", Deliver: true},
	}
	for _, action := range valid {
		if err := action.Validate(1024); err != nil {
			t.Fatalf("Action.Validate(%+v) error = %v", action, err)
		}
	}

	invalid := []Action{
		{Kind: ActionKind("upload"), Ref: "ref_file", ArtifactRef: "transfer-artifact://artifact_1"},
		{Kind: ActionNavigate},
		{Kind: ActionClick, Ref: "css:#submit"},
		{Kind: ActionDialog, Decision: "dismiss"},
		{Kind: ActionScroll, Direction: "down", Amount: MaxScrollAmount + 1},
		{Kind: ActionDrag, SourceRef: "ref_same", DestinationRef: "ref_same"},
		{Kind: ActionFileChooser, Ref: "ref_file", ArtifactRef: "/tmp/private"},
		{Kind: ActionDownload, Ref: "ref_download", ArtifactRef: "transfer-artifact://artifact_1"},
	}
	for _, action := range invalid {
		if err := action.Validate(1024); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Action.Validate(%+v) error = %v, want ErrInvalid", action, err)
		}
	}
}

func TestActionUnmarshalJSONAllowsAdditiveFields(t *testing.T) {
	t.Parallel()

	var action Action
	err := json.Unmarshal(
		[]byte(`{"kind":"scroll","direction":"up","amount":1e0,"future_option":true}`),
		&action,
	)
	if err != nil || action != (Action{Kind: ActionScroll, Direction: "up", Amount: 1}) {
		t.Fatalf("json.Unmarshal() action = %+v, error = %v", action, err)
	}
	if err = json.Unmarshal(
		[]byte(`{"kind":"scroll","direction":"up","amount":1.5}`),
		&action,
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("json.Unmarshal(fractional amount) error = %v, want ErrInvalid", err)
	}
}

func TestCurrentVocabularyDrivesValidationAndSchema(t *testing.T) {
	t.Parallel()

	kinds := Kinds()
	seen := make(map[ActionKind]struct{}, len(kinds))
	for _, kind := range kinds {
		if !kind.Valid() {
			t.Fatalf("Kinds() returned invalid action %q", kind)
		}
		if _, duplicate := seen[kind]; duplicate {
			t.Fatalf("Kinds() returned duplicate action %q", kind)
		}
		seen[kind] = struct{}{}
	}
	if ActionKind("upload").Valid() {
		t.Fatal("removed upload action remains valid")
	}
	kinds[0] = "mutated"
	if Kinds()[0] == "mutated" {
		t.Fatal("Kinds() exposed mutable protocol state")
	}

	strict := Schema([]ActionKind{ActionNavigate, ActionScroll}, 1024, false)
	properties := strict["properties"].(map[string]any)
	for _, field := range []string{"kind", "url", "direction", "amount"} {
		if _, ok := properties[field]; !ok {
			t.Fatalf("strict schema omitted %q: %#v", field, properties)
		}
	}
	for _, field := range []string{"ref", "value", "prompt_provided", "deliver"} {
		if _, ok := properties[field]; ok {
			t.Fatalf("strict schema exposed unrelated field %q: %#v", field, properties)
		}
	}
	if strict["additionalProperties"] != false {
		t.Fatalf("strict schema allows additive fields: %#v", strict)
	}

	tolerant := Schema([]ActionKind{ActionDialog}, 1024, true)
	properties = tolerant["properties"].(map[string]any)
	if tolerant["additionalProperties"] != true || properties["prompt_provided"] == nil {
		t.Fatalf("transport schema is not additively tolerant: %#v", tolerant)
	}
}
