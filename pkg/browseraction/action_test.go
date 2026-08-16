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
		{Kind: ActionUpload, Ref: "ref_file", ArtifactRef: "transfer-artifact://artifact_1"},
		{Kind: ActionDownload, Ref: "ref_download", Deliver: true},
	}
	for _, action := range valid {
		if err := action.Validate(1024); err != nil {
			t.Fatalf("Action.Validate(%+v) error = %v", action, err)
		}
	}

	invalid := []Action{
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
	if !ActionUpload.Valid() {
		t.Fatal("upload action is missing from the current vocabulary")
	}
	kinds[0] = "mutated"
	if Kinds()[0] == "mutated" {
		t.Fatal("Kinds() exposed mutable protocol state")
	}

	strict := Schema([]ActionKind{ActionNavigate, ActionScroll}, 1024, false)
	branches := strict["oneOf"].([]any)
	if len(branches) != 2 {
		t.Fatalf("strict schema branches = %#v", branches)
	}
	navigate := strictSchemaTestBranch(t, strict, ActionNavigate)
	if properties := navigate["properties"].(map[string]any); properties["url"] == nil ||
		properties["direction"] != nil {
		t.Fatalf("navigate schema mixes action fields: %#v", navigate)
	}
	scroll := strictSchemaTestBranch(t, strict, ActionScroll)
	properties := scroll["properties"].(map[string]any)
	if properties["direction"] == nil || properties["amount"] == nil || properties["url"] != nil {
		t.Fatalf("scroll schema mixes action fields: %#v", scroll)
	}
	if amount := properties["amount"].(map[string]any); amount["minimum"] != 1 ||
		amount["maximum"] != MaxScrollAmount {
		t.Fatalf("scroll amount schema = %#v", amount)
	}
	textActions := Schema([]ActionKind{ActionFill, ActionDialog}, 1024, false)
	fillValue := strictSchemaTestBranch(t, textActions, ActionFill)["properties"].(map[string]any)["value"]
	dialogValue := strictSchemaTestBranch(t, textActions, ActionDialog)["properties"].(map[string]any)["value"]
	if fillValue.(map[string]any)["minLength"] != 1 || dialogValue.(map[string]any)["minLength"] != nil {
		t.Fatalf("strict text action schemas = %#v", textActions)
	}

	tolerant := Schema([]ActionKind{ActionDialog}, 1024, true)
	properties = tolerant["properties"].(map[string]any)
	if tolerant["additionalProperties"] != true || properties["prompt_provided"] == nil {
		t.Fatalf("transport schema is not additively tolerant: %#v", tolerant)
	}
}

func strictSchemaTestBranch(t *testing.T, schema map[string]any, kind ActionKind) map[string]any {
	t.Helper()
	for _, candidate := range schema["oneOf"].([]any) {
		branch := candidate.(map[string]any)
		properties := branch["properties"].(map[string]any)
		if properties["kind"].(map[string]any)["const"] == string(kind) {
			return branch
		}
	}
	t.Fatalf("strict schema omitted %q: %#v", kind, schema)
	return nil
}

func TestDecodeModelActionPreservesTypedInputAndDialogPresence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args map[string]any
		want Action
	}{
		{map[string]any{"kind": "check", "ref": "element_1"}, Action{Kind: ActionCheck, Ref: "element_1"}},
		{map[string]any{
			"kind": "drag", "source_ref": "element_1", "destination_ref": "element_2",
		}, Action{Kind: ActionDrag, SourceRef: "element_1", DestinationRef: "element_2"}},
		{map[string]any{
			"kind": "fill", "ref": "element_1", "value": "draft text",
		}, Action{Kind: ActionFill, Ref: "element_1", Value: "draft text"}},
		{map[string]any{
			"kind": "dialog", "dialog_id": "dialog_1", "decision": "accept", "value": "",
		}, Action{Kind: ActionDialog, DialogID: "dialog_1", Decision: "accept", PromptProvided: true}},
		{map[string]any{
			"kind": "dialog", "dialog_id": "dialog_1", "decision": "dismiss",
		}, Action{Kind: ActionDialog, DialogID: "dialog_1", Decision: "dismiss"}},
	}
	for _, test := range tests {
		got, err := DecodeModelAction(test.args, 1024)
		if err != nil || got != test.want {
			t.Fatalf("DecodeModelAction(%#v) = %#v, %v; want %#v", test.args, got, err, test.want)
		}
	}
}

func TestDecodeModelActionRejectsInvalidWireFields(t *testing.T) {
	t.Parallel()
	invalid := []map[string]any{
		{"kind": "scroll", "direction": "down", "amount": 1, "target": "document"},
		{"kind": "scroll", "direction": "down", "amount": 1, "unexpected": true},
		{"kind": "scroll", "direction": false, "amount": 1},
		{"kind": "scroll", "direction": "down", "amount": 1.5},
		{"kind": "fill", "ref": "element_1", "value": ""},
		{"kind": "select", "ref": "element_1", "value": ""},
		{"kind": "dialog", "dialog_id": "dialog_1", "decision": "accept", "value": false},
		{"kind": "dialog", "decision": "dismiss"},
		{"kind": "download", "ref": "download_1", "deliver": "true"},
	}
	for _, args := range invalid {
		if _, err := DecodeModelAction(args, 1024); !errors.Is(err, ErrInvalid) {
			t.Fatalf("DecodeModelAction(%#v) error = %v, want ErrInvalid", args, err)
		}
	}
}
