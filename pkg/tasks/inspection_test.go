package tasks

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestInspectSnapshotIsBoundedReadOnlyAndValueFreeOnFailure(t *testing.T) {
	registry := NewRegistry("")
	record := Record{TaskID: "task-inspection", Task: "private objective"}
	if err := registry.Upsert(record); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(registry.snapshotLocked())
	if err != nil {
		t.Fatal(err)
	}

	first, err := InspectSnapshot(data)
	if err != nil || len(first.Tasks) != 1 || first.Tasks[0].TaskID != record.TaskID {
		t.Fatalf("InspectSnapshot() = %#v, %v", first, err)
	}
	first.Tasks[0].TaskID = "changed"
	second, err := InspectSnapshot(data)
	if err != nil || second.Tasks[0].TaskID != record.TaskID {
		t.Fatalf("second InspectSnapshot() = %#v, %v", second, err)
	}

	const private = "PRIVATE_TASK_INSPECTION_CONTROL_15f2"
	for _, malformed := range [][]byte{
		nil,
		[]byte(`{"tasks":null,"private":"` + private + `"}`),
		append(bytes.Clone(data), []byte(" trailing "+private)...),
		bytes.Repeat([]byte("x"), DefaultMaxSnapshotBytes+1),
	} {
		if _, inspectErr := InspectSnapshot(malformed); !errors.Is(inspectErr, ErrInvalidInspection) ||
			strings.Contains(inspectErr.Error(), private) {
			t.Fatalf("InspectSnapshot() error = %v", inspectErr)
		}
	}
}
