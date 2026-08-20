package browser

import (
	"errors"
	"strings"
	"testing"
)

func TestOrdinaryInteractionActionContracts(t *testing.T) {
	valid := []Action{
		{Kind: ActionDialog, DialogID: "dialog_authority_1", Decision: "dismiss"},
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
		{Kind: ActionKind("upload"), Ref: "ref_file", ArtifactRef: "transfer-artifact://artifact_1"},
		{Kind: ActionDialog, DialogID: "not an identifier", Decision: "dismiss"},
		{Kind: ActionDialog, DialogID: "dialog_" + strings.Repeat("x", MaxIdentifierBytes), Decision: "dismiss"},
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

func TestPreparedFileChooserRequiresArtifactBinding(t *testing.T) {
	owner := Owner{ActorID: "actor_1", AgentID: "agent_1", SessionKey: "session_key_1", ExecutionID: "execution_1"}
	actions := []Action{
		{Kind: ActionFileChooser, Ref: "ref_file", ArtifactRef: "transfer-artifact://artifact_1"},
	}
	for _, action := range actions {
		prepared := PreparedAction{
			RequestID: "request_1", SessionID: "browser_1", Owner: owner,
			Target: "gateway", Profile: "managed", ControllerGeneration: 1, TabID: "tab_1",
			SnapshotID: "snapshot_1", SnapshotGeneration: 1, CurrentOrigin: "https://example.com",
			Action: action, Effect: EffectRead, PolicyRevision: "policy_1",
			CatalogRevision: strings.Repeat("a", 64), CreatedAt: 1, ExpiresAt: 2,
		}
		prepared.ID = derivedIdentifier("prepared", owner, prepared.SessionID, prepared.RequestID)
		var err error
		prepared.ActionHash, err = hashPreparedAction(prepared)
		if err != nil {
			t.Fatalf("hashPreparedAction(%s) error = %v", action.Kind, err)
		}
		if err = prepared.Validate(1024); !errors.Is(err, ErrInvalid) {
			t.Fatalf("PreparedAction.Validate(%s) error = %v, want ErrInvalid", action.Kind, err)
		}
	}
}
