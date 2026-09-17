package document

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormWriteServiceFillsAndVerifiesDurableExpectation(t *testing.T) {
	root := directTempDir(t)
	sourcePath := filepath.Join(root, "form.pdf")
	source := []byte("%PDF-1.7\nsynthetic source\n%%EOF\n")
	writeFixture(t, sourcePath, source)
	fieldsWorker := &recordingFormFieldsWorker{facts: successfulTestFormFields()}
	writer := &coordinatorTestWriter{candidate: []byte("%PDF-1.7\nverified candidate\n%%EOF\n")}
	snapshot, report := acquireOperationForPlatform(
		t.Context(),
		sourcePath,
		AcquireOptions{ScratchRoot: filepath.Join(root, "scratch")},
		"linux",
		"amd64",
		operationFill,
	)
	fieldID := fieldsWorker.facts.Fields[0].ID
	snapshot, report = fillAcquiredSnapshot(
		t.Context(),
		snapshot,
		report,
		validFillMap(FormFillAssignment{FieldID: fieldID, Value: textFormValue("private value")}),
		FormWriteOptions{StateRoot: filepath.Join(root, "state")},
		fieldsWorker,
		writer,
	)
	if snapshot == nil || report.State != StateSucceeded || report.Write == nil ||
		len(report.Artifacts) != 1 || !validWriteOperationID(report.OperationID) || writer.calls.Load() != 1 {
		t.Fatalf("fill report = %#v, snapshot=%#v, calls=%d", report, snapshot, writer.calls.Load())
	}
	artifactBytes := readTestArtifact(t, snapshot, report.Artifacts[0].Ref)
	if !bytes.Equal(artifactBytes, writer.candidate) {
		t.Fatalf("candidate = %q, want %q", artifactBytes, writer.candidate)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private value", root, sourcePath, snapshot.Path()} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("fill report leaked %q: %s", forbidden, encoded)
		}
	}
	expectation, err := DecodeFormWriteExpectation(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if err = snapshot.Close(); err != nil {
		t.Fatal(err)
	}

	candidatePath := filepath.Join(root, "filled.pdf")
	writeFixture(t, candidatePath, artifactBytes)
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
		acceptingWorker{},
	)
	if verifySnapshot == nil || verifyReport.State != StateSucceeded || verifyReport.Write == nil ||
		verifyReport.OperationID != report.OperationID || len(verifyReport.Artifacts) != 0 {
		t.Fatalf("verify report = %#v, snapshot=%#v", verifyReport, verifySnapshot)
	}
	if err = verifySnapshot.Close(); err != nil {
		t.Fatal(err)
	}
	gotSource, err := os.ReadFile(sourcePath)
	if err != nil || !bytes.Equal(gotSource, source) {
		t.Fatalf("source changed: %q, err=%v", gotSource, err)
	}
}

func TestFormWriteVerifyRejectsMissingLineageAndCandidateMismatch(t *testing.T) {
	root := directTempDir(t)
	candidate := []byte("%PDF-1.7\ncandidate\n%%EOF\n")
	candidatePath := filepath.Join(root, "candidate.pdf")
	writeFixture(t, candidatePath, candidate)
	expectation := syntheticFormWriteExpectation(t, candidate)

	t.Run("missing journal", func(t *testing.T) {
		snapshot, report := acquireOperationForPlatform(
			t.Context(),
			candidatePath,
			AcquireOptions{ScratchRoot: filepath.Join(root, "missing-scratch")},
			"linux",
			"amd64",
			operationVerifyFormWrite,
		)
		snapshot, report = verifyFormWriteSnapshot(
			t.Context(), snapshot, report, expectation, filepath.Join(root, "missing-state"), acceptingWorker{},
		)
		if snapshot != nil || report.State != StateFailed || report.Failure == nil ||
			report.Failure.Code != FailureWriteConflict {
			t.Fatalf("missing journal report = %#v, snapshot=%#v", report, snapshot)
		}
	})

	t.Run("candidate digest", func(t *testing.T) {
		stateRoot := filepath.Join(root, "state")
		seedFormWriteExpectation(t, stateRoot, expectation)
		mismatchedPath := filepath.Join(root, "mismatched.pdf")
		writeFixture(t, mismatchedPath, []byte("%PDF-1.7\nother candidate\n%%EOF\n"))
		snapshot, report := acquireOperationForPlatform(
			t.Context(),
			mismatchedPath,
			AcquireOptions{ScratchRoot: filepath.Join(root, "mismatch-scratch")},
			"linux",
			"amd64",
			operationVerifyFormWrite,
		)
		snapshot, report = verifyFormWriteSnapshot(
			t.Context(), snapshot, report, expectation, stateRoot, acceptingWorker{},
		)
		if snapshot != nil || report.State != StateFailed || report.Failure == nil ||
			report.Failure.Code != FailureVerificationStructural {
			t.Fatalf("mismatched candidate report = %#v, snapshot=%#v", report, snapshot)
		}
	})
}

func TestDecodeFormWriteExpectationRejectsValuesAndTampering(t *testing.T) {
	expectation := syntheticFormWriteExpectation(t, []byte("%PDF-1.7\ncandidate\n%%EOF\n"))
	report := syntheticFormWriteReport(expectation)
	valid, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeFormWriteExpectation(bytes.NewReader(valid)); err != nil {
		t.Fatalf("valid report: %v", err)
	}

	report.Write.OutputSHA256 = strings.Repeat("f", 64)
	tampered, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeFormWriteExpectation(bytes.NewReader(tampered)); err == nil {
		t.Fatal("tampered report was accepted")
	}
	if _, err = DecodeFormWriteExpectation(strings.NewReader(string(valid) + string(valid))); err == nil {
		t.Fatal("multiple JSON values were accepted")
	}
	if _, err = DecodeFormWriteExpectation(strings.NewReader(`{"unknown":true}`)); err == nil {
		t.Fatal("unknown report field was accepted")
	}
}

func syntheticFormWriteExpectation(t *testing.T, candidate []byte) FormWriteExpectation {
	t.Helper()
	request := normalizedWriteTestRequest(t, "test value")
	owner := Authority{Kind: "local_operator"}
	snapshot, input := coordinatorTestInput(t, request, owner)
	defer func() { _ = snapshot.Close() }()
	operationID := writeTestOperationID("expectation")
	result := coordinatorTestWorkerResult(
		snapshot, input, defaultInspectionLimits(), operationID, request, candidate,
	)
	return FormWriteExpectation{OperationID: operationID, Artifact: result.Artifacts[0].Artifact, Facts: *result.Write}
}

func syntheticFormWriteReport(expectation FormWriteExpectation) Report {
	return Report{
		SchemaVersion: ReportSchemaVersion,
		OperationID:   expectation.OperationID,
		Operation:     operationFill,
		State:         StateSucceeded,
		Input: &DocumentRef{
			ContentType: "application/pdf",
			Size:        1,
			SHA256:      expectation.Facts.SourceSHA256,
			Authority:   Authority{Kind: "local_operator"},
		},
		Limits:    defaultInspectionLimits(),
		Write:     &expectation.Facts,
		Artifacts: []Artifact{expectation.Artifact},
	}
}

func seedFormWriteExpectation(t *testing.T, stateRoot string, expectation FormWriteExpectation) {
	t.Helper()
	request := normalizedWriteTestRequest(t, "test value")
	coordinator, err := newFormWriteCoordinator(stateRoot, &coordinatorTestWriter{candidate: []byte("unused")})
	if err != nil {
		t.Fatal(err)
	}
	owner := Authority{Kind: "local_operator"}
	record, _, err := coordinator.journal.Accept(t.Context(), expectation.OperationID, owner, request)
	if err != nil {
		t.Fatal(err)
	}
	if record.SourceSHA256 != expectation.Facts.SourceSHA256 ||
		record.RequestSHA256 != expectation.Facts.RequestSHA256 {
		t.Fatalf("seed request identity = %#v, expectation=%#v", record, expectation)
	}
	operationRoot := filepath.Join(stateRoot, "generations", expectation.OperationID)
	if err = os.Mkdir(operationRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(operationRoot, formWriteCandidateFilename), readExpectationCandidate(t, expectation))
	if err = os.Chmod(filepath.Join(operationRoot, formWriteCandidateFilename), 0o600); err != nil {
		t.Fatal(err)
	}
	generation := expectationGeneration(expectation)
	encoded, err := json.Marshal(generation)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(operationRoot, formWriteGenerationFilename), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	record, err = coordinator.transition(t.Context(), owner, record, WriteTransition{State: WriteWriting})
	if err != nil {
		t.Fatal(err)
	}
	artifact := WriteArtifactEvidence{SHA256: expectation.Artifact.SHA256, Size: expectation.Artifact.Size}
	record, err = coordinator.transition(t.Context(), owner, record, WriteTransition{
		State: WriteWritten, Artifact: &artifact,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err = coordinator.transition(t.Context(), owner, record, WriteTransition{State: WriteVerifying})
	if err != nil {
		t.Fatal(err)
	}
	verification := formWriteVerificationEvidence(expectation.Facts)
	if _, err = coordinator.transition(t.Context(), owner, record, WriteTransition{
		State: WriteVerified, Verification: &verification,
	}); err != nil {
		t.Fatal(err)
	}
}

func readExpectationCandidate(t *testing.T, expectation FormWriteExpectation) []byte {
	t.Helper()
	switch expectation.Artifact.Size {
	case int64(len("%PDF-1.7\ncandidate\n%%EOF\n")):
		return []byte("%PDF-1.7\ncandidate\n%%EOF\n")
	default:
		t.Fatalf("unknown synthetic expectation size %d", expectation.Artifact.Size)
		return nil
	}
}

func readTestArtifact(t *testing.T, snapshot *Snapshot, ref string) []byte {
	t.Helper()
	reader, err := snapshot.OpenArtifact(ref)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
