//go:build linux && amd64

package document

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFormWriteServiceUsesRealProcessesAndDurableVerification(t *testing.T) {
	if !readBackendAvailable() {
		t.Skip("pinned Poppler 24.02.0 visual backend is unavailable")
	}
	root := directTempDir(t)
	sourcePath := filepath.Join(root, "form.pdf")
	source, err := os.ReadFile(filepath.Join("testdata", "acroform-fields.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, sourcePath, source)
	worker := testProcessWorker("serve")
	// The race-instrumented helper has to start several fresh document
	// processes during this end-to-end service test.
	worker.timeout = 30 * time.Second

	fieldsSnapshot, fieldsReport := fieldsWithWorker(
		t.Context(),
		sourcePath,
		AcquireOptions{ScratchRoot: filepath.Join(root, "fields-scratch")},
		"linux",
		"amd64",
		worker,
	)
	if fieldsSnapshot == nil || fieldsReport.State != StateSucceeded || fieldsReport.Fields == nil {
		t.Fatalf("fields report = %#v, snapshot=%#v", fieldsReport, fieldsSnapshot)
	}
	var fieldID string
	for _, field := range fieldsReport.Fields.Fields {
		if field.Name == "full_name" {
			fieldID = field.ID
			break
		}
	}
	if err = fieldsSnapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if fieldID == "" {
		t.Fatal("full_name field is missing")
	}

	fillSnapshot, fillReport := acquireOperationForPlatform(
		t.Context(),
		sourcePath,
		AcquireOptions{ScratchRoot: filepath.Join(root, "fill-scratch")},
		"linux",
		"amd64",
		operationFill,
	)
	value := "MintClaw Service"
	fillSnapshot, fillReport = fillAcquiredSnapshot(
		t.Context(),
		fillSnapshot,
		fillReport,
		validFillMap(FormFillAssignment{FieldID: fieldID, Value: FormValue{Type: FormValueText, Text: &value}}),
		FormWriteOptions{StateRoot: filepath.Join(root, "state")},
		worker,
		worker,
	)
	if fillSnapshot == nil || fillReport.State != StateSucceeded || fillReport.Write == nil ||
		len(fillReport.Artifacts) != 1 {
		t.Fatalf("fill report = %#v, snapshot=%#v", fillReport, fillSnapshot)
	}
	encoded, err := json.Marshal(fillReport)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(value)) || bytes.Contains(encoded, []byte(root)) {
		t.Fatalf("fill report leaked protected data: %s", encoded)
	}
	expectation, err := DecodeFormWriteExpectation(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	candidate := readTestArtifact(t, fillSnapshot, fillReport.Artifacts[0].Ref)
	if err = fillSnapshot.Close(); err != nil {
		t.Fatal(err)
	}
	candidatePath := filepath.Join(root, "filled.pdf")
	writeFixture(t, candidatePath, candidate)

	verifySnapshot, verifyReport := acquireOperationForPlatform(
		t.Context(),
		candidatePath,
		AcquireOptions{ScratchRoot: filepath.Join(root, "verify-scratch")},
		"linux",
		"amd64",
		operationVerifyFormWrite,
	)
	verifySnapshot, verifyReport = verifyFormWriteSnapshot(
		t.Context(),
		verifySnapshot,
		verifyReport,
		expectation,
		filepath.Join(root, "state"),
		worker,
	)
	if verifySnapshot == nil || verifyReport.State != StateSucceeded || verifyReport.Write == nil ||
		verifyReport.OperationID != fillReport.OperationID {
		t.Fatalf("verify report = %#v, snapshot=%#v", verifyReport, verifySnapshot)
	}
	if err = verifySnapshot.Close(); err != nil {
		t.Fatal(err)
	}
	sourceAfter, err := os.ReadFile(sourcePath)
	if err != nil || !bytes.Equal(sourceAfter, source) {
		t.Fatalf("source changed: %v", err)
	}
}
