package browseraction

import (
	"strings"
	"testing"
)

func inputTargetTestObservation() InputTargetObservation {
	hash := strings.Repeat("a", 64)
	return InputTargetObservation{
		Version: InputTargetVersion, BindingSHA256: hash,
		ActorID: "actor_test", AgentID: "agent_test", SessionKey: "session_key", ExecutionID: "execution_test",
		RequestID: "request_test", SessionID: "session_test", TabID: "tab_test", SnapshotID: "snapshot_test",
		SnapshotGeneration: 1, ControllerGeneration: 1, Ref: "ref_test", ElementRole: "textbox",
		ElementNameSHA256: hash, ElementPosition: 1, URLSHA256: hash, OriginSHA256: hash,
		PageStateSHA256: hash, CatalogRevision: hash, IssuedAt: 10, ExpiresAt: 20,
	}
}

func TestInputTargetObservationRejectsUnboundedOrContentMetadata(t *testing.T) {
	valid := inputTargetTestObservation()
	if !valid.Valid() {
		t.Fatal("bounded native target was rejected")
	}
	for _, test := range []struct {
		name string
		edit func(*InputTargetObservation)
	}{
		{"version", func(o *InputTargetObservation) { o.Version = "other" }},
		{"missing owner", func(o *InputTargetObservation) { o.ActorID = "" }},
		{"raw route", func(o *InputTargetObservation) { o.SessionKey = "telegram:private" }},
		{"missing session", func(o *InputTargetObservation) { o.SessionID = "" }},
		{"long request", func(o *InputTargetObservation) { o.RequestID = strings.Repeat("a", 129) }},
		{"raw ref", func(o *InputTargetObservation) { o.Ref = "#private input" }},
		{"missing snapshot generation", func(o *InputTargetObservation) { o.SnapshotGeneration = 0 }},
		{"missing controller", func(o *InputTargetObservation) { o.ControllerGeneration = 0 }},
		{"unpaired context", func(o *InputTargetObservation) { o.ContextGeneration = 1 }},
		{"unbound frame", func(o *InputTargetObservation) { o.FrameID = "frame_1" }},
		{"incomplete catalog", func(o *InputTargetObservation) { o.ContextCatalogID = "catalog_1" }},
		{"missing position", func(o *InputTargetObservation) { o.ElementPosition = 0 }},
		{"other role", func(o *InputTargetObservation) { o.ElementRole = "button" }},
		{"raw label", func(o *InputTargetObservation) { o.ElementNameSHA256 = "Private name" }},
		{"raw URL", func(o *InputTargetObservation) { o.URLSHA256 = "https://private.example/?secret=value" }},
		{"uppercase digest", func(o *InputTargetObservation) { o.BindingSHA256 = strings.Repeat("A", 64) }},
		{"nonhex digest", func(o *InputTargetObservation) { o.CatalogRevision = strings.Repeat("z", 64) }},
		{"missing issue time", func(o *InputTargetObservation) { o.IssuedAt = 0 }},
		{"invalid deadline", func(o *InputTargetObservation) { o.ExpiresAt = o.IssuedAt }},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := valid
			test.edit(&invalid)
			if invalid.Valid() {
				t.Fatal("invalid target metadata was accepted")
			}
		})
	}
	valid.FrameID, valid.ContextCatalogID, valid.ContextGeneration = "frame_1", "catalog_1", 1
	if !valid.Valid() {
		t.Fatal("complete context authority was rejected")
	}
}
