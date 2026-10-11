package interactions

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestInspectSnapshotPreservesProtectedBindingsWithoutPruning(t *testing.T) {
	registry, clock, path := newTestRegistry(t)
	request := validCreate(clock, "interaction_inspect11111111", "session-inspect")
	request.ProtectedAnswer = &ProtectedAnswerBinding{
		Namespace: "document.form.v1",
		Token:     "opaque-local-binding",
		Actions:   []ProtectedAnswerAction{ProtectedAnswerActionClarify, ProtectedAnswerActionBack},
	}
	record, err := registry.Create(request)
	if err != nil {
		t.Fatal(err)
	}
	record = bindPromptDelivery(t, registry, record)
	record, err = registry.MarkWaiting(record.ID, record.Revision)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Hour)
	_, err = registry.Cancel(record.ID, record.Revision, "test_cancel")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(DefaultRetention + time.Hour)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(data)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	want := registry.snapshotLocked()
	got, err := InspectSnapshot(data)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("inspection did not preserve native retained state: %v", err)
	}
	if len(got.Records) != 1 || got.Records[0].Status != StatusCancelled ||
		got.Records[0].ProtectedAnswer.Token != "opaque-local-binding" {
		t.Fatal("inspection pruned or replaced the protected record")
	}
	got.Records[0].ProtectedAnswer.Token = "changed"
	got.Records[0].ProtectedAnswer.Actions[0] = ProtectedAnswerActionSkip
	got.Records[0].Questions[0].Options[0].Label = "changed"
	got.Events[0].Code = "changed"
	again, err := InspectSnapshot(data)
	if err != nil || !reflect.DeepEqual(again, want) || !bytes.Equal(data, original) {
		t.Fatal("inspection result aliases another decode, registry, or input")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("inspection changed the persisted bytes")
	}
	current, err := os.Stat(path)
	if err != nil || !current.ModTime().Equal(info.ModTime()) {
		t.Fatal("inspection changed the store modification time")
	}
}

func TestInspectSnapshotRejectsInvalidAndOversizedData(t *testing.T) {
	registry, clock, path := newTestRegistry(t)
	if _, err := registry.Create(validCreate(clock, "interaction_inspect22222222", "session-inspect")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var valid Snapshot
	if err := json.Unmarshal(data, &valid); err != nil {
		t.Fatal(err)
	}
	invalidRecord := valid
	invalidRecord.Records = append([]Record(nil), valid.Records...)
	invalidRecord.Records[0].ShortID = "foreign"
	duplicateRecord := valid
	duplicateRecord.Records = append(append([]Record(nil), valid.Records...), valid.Records[0])
	invalidEvent := valid
	invalidEvent.Events = cloneEvents(valid.Events)
	invalidEvent.Events[0].CommitSequence = 0
	for name, snapshot := range map[string]Snapshot{
		"invalid record":                   invalidRecord,
		"duplicate record":                 duplicateRecord,
		"invalid event after valid record": invalidEvent,
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			got, err := InspectSnapshot(encoded)
			if err == nil || !reflect.DeepEqual(got, Snapshot{}) {
				t.Fatal("invalid snapshot exposed partial records")
			}
		})
	}
	for name, data := range map[string][]byte{
		"empty":            nil,
		"unknown":          []byte(`{"schema_version":"interaction_snapshot.v1","commit_sequence":0,"records":[],"old":true}`),
		"missing sequence": []byte(`{"schema_version":"interaction_snapshot.v1","records":[]}`),
		"trailing":         append(bytes.Clone(data), []byte(` {}`)...),
		"oversized":        bytes.Repeat([]byte(" "), DefaultMaxBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := InspectSnapshot(data)
			if err == nil || !reflect.DeepEqual(got, Snapshot{}) {
				t.Fatal("invalid input did not return an empty snapshot and error")
			}
			if name == "oversized" && !errors.Is(err, ErrSnapshotOverBudget) {
				t.Fatal("oversized input did not preserve the native budget error")
			}
		})
	}
}

func TestInspectSnapshotAcceptsEmptyCurrentSnapshot(t *testing.T) {
	data := []byte(`{"schema_version":"interaction_snapshot.v1","commit_sequence":0,"records":[]}`)
	got, err := InspectSnapshot(data)
	if err != nil || got.SchemaVersion != SnapshotSchemaVersion || got.CommitSequence != 0 ||
		len(got.Records) != 0 || len(got.Events) != 0 {
		t.Fatal("empty current snapshot was not admitted")
	}
}
