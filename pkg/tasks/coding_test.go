package tasks

import (
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
		"bad digest": {
			mutate: func(record *Record) { record.Coding.RequestDigest = "not-a-digest" },
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
			SchemaVersion: CodingProjectionSchemaV1,
			Alias:         "mintclaw", Target: "companion", Project: "mintclaw",
			Revision: "project-v1", Mode: codingtask.TaskModeInvestigate,
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
