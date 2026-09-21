package tasks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
)

func TestRegistryPersistsAndClonesCodingProjection(t *testing.T) {
	store := filepath.Join(t.TempDir(), "tasks.json")
	registry := NewRegistry(store)
	record := codingRegistryTestRecord("coding-one")
	if err := registry.Create(record); err != nil {
		t.Fatal(err)
	}
	stored, found := registry.Get(record.TaskID)
	if !found || stored.Coding == nil || stored.Coding.Question == nil {
		t.Fatalf("stored coding task = %#v, %v", stored, found)
	}
	stored.Coding.Question.Options[0].Label = "mutated clone"
	reloaded := NewRegistry(store)
	loaded, found := reloaded.Get(record.TaskID)
	if !found || loaded.Coding.Question.Options[0].Label != "Yes" {
		t.Fatalf("reloaded coding task = %#v, %v", loaded, found)
	}
}

func TestRegistryRejectsInvalidCodingProjection(t *testing.T) {
	tests := map[string]struct {
		mutate func(*Record)
		want   string
	}{
		"missing projection": {
			mutate: func(record *Record) { record.Coding = nil },
			want:   "missing coding projection",
		},
		"legacy schema": {
			mutate: func(record *Record) { record.Coding.SchemaVersion = "coding_task.v3" },
			want:   "invalid coding projection schema",
		},
		"bad digest": {
			mutate: func(record *Record) { record.Coding.RequestDigest = "not-a-digest" },
			want:   "invalid immutable coding authority",
		},
		"deferred machine yolo root": {
			mutate: func(record *Record) { record.Coding.Profile = codingtask.TaskModeMachineYoloRoot },
			want:   "invalid immutable coding authority",
		},
		"missing route": {
			mutate: func(record *Record) { record.Coding.RouteSessionKey = "" },
			want:   "invalid requester identity",
		},
		"missing node result digest": {
			mutate: func(record *Record) { record.Coding.NodeRevision = 1 },
			want:   "invalid node result digest",
		},
		"coding projection on tool task": {
			mutate: func(record *Record) { record.Runtime = RuntimeTool },
			want:   "coding projection for runtime",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			record := codingRegistryTestRecord("coding-" + strings.ReplaceAll(name, " ", "-"))
			test.mutate(&record)
			registry := NewRegistry(filepath.Join(t.TempDir(), "tasks.json"))
			err := registry.Create(record)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Create() error = %v, want %q", err, test.want)
			}
			if _, found := registry.Get(record.TaskID); found {
				t.Fatal("invalid coding task remained after rejected persistence")
			}
		})
	}
}

func TestRegistryRejectsRetainedDeferredCodingProfiles(t *testing.T) {
	for _, profile := range []codingtask.TaskMode{codingtask.TaskModeMachineYoloRoot} {
		t.Run(string(profile), func(t *testing.T) {
			store := filepath.Join(t.TempDir(), "tasks.json")
			registry := NewRegistry(store)
			if err := registry.Create(codingRegistryTestRecord("coding-retained")); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(store)
			if err != nil {
				t.Fatal(err)
			}
			var snapshot Snapshot
			if err = json.Unmarshal(data, &snapshot); err != nil {
				t.Fatal(err)
			}
			snapshot.Tasks[0].Coding.Profile = profile
			data, err = json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(store, data, 0o600); err != nil {
				t.Fatal(err)
			}

			reloaded := NewRegistry(store)
			if err = reloaded.LastLoadError(); err == nil ||
				!strings.Contains(err.Error(), "invalid immutable coding authority") {
				t.Fatalf("LastLoadError() = %v, want deferred profile rejection", err)
			}
			if records := reloaded.List(); len(records) != 0 {
				t.Fatalf("invalid retained tasks published: %#v", records)
			}
		})
	}
}

func TestRegistryAcceptsProjectYoloProjection(t *testing.T) {
	record := codingRegistryTestRecord("coding-project-yolo")
	record.Coding.Profile = codingtask.TaskModeProjectYolo
	registry := NewRegistry(filepath.Join(t.TempDir(), "tasks.json"))
	if err := registry.Create(record); err != nil {
		t.Fatalf("Create() rejected project-yolo projection: %v", err)
	}
}

func TestRegistryAcceptsMachineYoloProjection(t *testing.T) {
	record := codingRegistryTestRecord("coding-machine-yolo")
	record.Coding.Profile = codingtask.TaskModeMachineYolo
	registry := NewRegistry(filepath.Join(t.TempDir(), "tasks.json"))
	if err := registry.Create(record); err != nil {
		t.Fatalf("Create() rejected machine-yolo projection: %v", err)
	}
}

func TestRegistryAcceptsNumericExecutionTargetAlias(t *testing.T) {
	record := codingRegistryTestRecord("coding-numeric-target")
	record.Coding.Target = "1companion"
	registry := NewRegistry(filepath.Join(t.TempDir(), "tasks.json"))
	if err := registry.Create(record); err != nil {
		t.Fatalf("Create() rejected valid execution target alias: %v", err)
	}
}

func codingRegistryTestRecord(taskID string) Record {
	return Record{
		TaskID: taskID, Runtime: RuntimeCoding, TaskKind: "coding_task",
		Task: "Investigate the failure.", Status: StatusQueued,
		Coding: &CodingProjection{
			SchemaVersion: CodingProjectionSchemaV4,
			Alias:         "mintclaw", Target: "companion", Scope: "mintclaw",
			Revision: "project-v1", Profile: codingtask.TaskModeInvestigate,
			RequestDigest:   strings.Repeat("a", 64),
			RouteSessionKey: "telegram:chat:topic", SessionKey: "session-one",
			ActorID: "owner-42", SenderID: "owner-42",
			Question: &CodingQuestionProjection{
				ID: uuid.NewString(), Revision: 1,
				Options: []CodingQuestionOption{{ID: "yes", Label: "Yes"}},
			},
		},
	}
}
