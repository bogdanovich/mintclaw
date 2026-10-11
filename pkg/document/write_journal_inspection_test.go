package document

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectWriteOperationSnapshotUsesTheAdmittedOwnerBoundBytes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	journal, err := NewWriteJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	owner := writeTestOwner()
	record, _, err := journal.Accept(t.Context(), writeTestOperationID("snapshot"), owner,
		normalizedWriteTestRequest(t, "private-canary"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, record.OperationID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(data)
	got, err := InspectWriteOperationSnapshot(data, record.OperationID, owner)
	after, statErr := os.Stat(path)
	if err != nil || statErr != nil || got != record || !bytes.Equal(data, original) ||
		!before.ModTime().Equal(after.ModTime()) ||
		bytes.Contains(data, []byte("private-canary")) {
		t.Fatal("snapshot inspection changed storage, lost native identity or leaked values", err)
	}
	// A later record replacement cannot change the already admitted snapshot.
	if err = os.WriteFile(path, []byte(`{"private":"replacement-canary"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = InspectWriteOperationSnapshot(data, record.OperationID, owner)
	if err != nil || got != record {
		t.Fatal("inspection reopened the replaced journal", err)
	}
	if _, _, err = journal.Lookup(t.Context(), record.OperationID, owner); !errors.Is(err, ErrWriteJournalFailed) {
		t.Fatal("native lookup accepted the replacement")
	}
}

func TestInspectWriteOperationSnapshotPreservesNativeRefusals(t *testing.T) {
	journal, err := NewWriteJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	owner := writeTestOwner()
	record, _, err := journal.Accept(t.Context(), writeTestOperationID("refusal"), owner,
		normalizedWriteTestRequest(t, "private-control"))
	if err != nil {
		t.Fatal(err)
	}
	valid, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"empty", "oversize", "unknown", "trailing", "state", "digest", "id", "owner", "authority"} {
		t.Run(mode, func(t *testing.T) {
			data, id, authority, expected := bytes.Clone(valid), record.OperationID, owner, ErrWriteJournalFailed
			switch mode {
			case "empty":
				data = nil
			case "oversize":
				data = bytes.Repeat([]byte(" "), DefaultMaxWriteJournalRecord+1)
			case "unknown":
				data = bytes.Replace(data, []byte(`"revision":1`), []byte(`"revision":1,"private":"canary"`), 1)
			case "trailing":
				data = append(data, []byte(`{}`)...)
			case "state":
				data = bytes.Replace(data, []byte(`"state":"accepted"`), []byte(`"state":"unknown"`), 1)
			case "digest":
				data = bytes.Replace(data, []byte(record.SourceSHA256), []byte("not-a-digest"), 1)
			case "id":
				id = writeTestOperationID("different")
			case "owner":
				authority.ActorID = "different"
				expected = ErrWriteConflict
			case "authority":
				authority.Kind = ""
				expected = ErrWriteConflict
			}
			got, err := InspectWriteOperationSnapshot(data, id, authority)
			if !errors.Is(err, expected) || got != (WriteOperationRecord{}) || strings.Contains(err.Error(), "canary") {
				t.Fatal("snapshot admitted invalid native evidence or echoed input", err)
			}
		})
	}
}
