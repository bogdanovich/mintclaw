package browser

import (
	"errors"
	"testing"
)

func TestOrdinaryInteractionActionContracts(t *testing.T) {
	valid := []Action{
		{Kind: ActionCheck, Ref: "ref_check"},
		{Kind: ActionUncheck, Ref: "ref_uncheck"},
		{Kind: ActionHover, Ref: "ref_hover"},
		{Kind: ActionDrag, SourceRef: "ref_source", DestinationRef: "ref_destination"},
		{Kind: ActionFileChooser, Ref: "ref_file", ArtifactRef: "transfer-artifact://artifact_1"},
	}
	for _, action := range valid {
		if err := action.Validate(1024); err != nil {
			t.Fatalf("Action.Validate(%+v) error = %v", action, err)
		}
	}

	invalid := []Action{
		{Kind: ActionCheck, Ref: "ref_check", SourceRef: "ref_source"},
		{Kind: ActionUncheck, Ref: ""},
		{Kind: ActionHover, Ref: "css:#menu"},
		{Kind: ActionDrag, SourceRef: "ref_same", DestinationRef: "ref_same"},
		{Kind: ActionDrag, SourceRef: "ref_source", DestinationRef: ""},
		{Kind: ActionFileChooser, Ref: "ref_file", ArtifactRef: "/tmp/private"},
	}
	for _, action := range invalid {
		if err := action.Validate(1024); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Action.Validate(%+v) error = %v, want ErrInvalid", action, err)
		}
	}
}
