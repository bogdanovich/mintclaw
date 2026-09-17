package document

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type coordinatorTestWriter struct {
	candidate []byte
	state     State
	failure   *Failure
	delay     time.Duration
	started   chan<- struct{}
	calls     atomic.Int32
}

func (writer *coordinatorTestWriter) FillCandidate(
	ctx context.Context,
	snapshot *Snapshot,
	input DocumentRef,
	limits Limits,
	operationID string,
	fill NormalizedFillRequest,
) WorkerResult {
	writer.calls.Add(1)
	if writer.started != nil {
		select {
		case writer.started <- struct{}{}:
		default:
		}
	}
	if writer.delay > 0 {
		timer := time.NewTimer(writer.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return workerFailure(operationID, StateCanceled, FailureCanceled, "document worker was canceled")
		case <-timer.C:
		}
	}
	if writer.state != "" && writer.state != StateSucceeded {
		return WorkerResult{
			SchemaVersion: WorkerResultSchemaVersion, OperationID: operationID,
			State: writer.state, Failure: writer.failure,
		}
	}
	return coordinatorTestWorkerResult(snapshot, input, limits, operationID, fill, writer.candidate)
}

func TestFormWriteCoordinatorPersistsAndReplaysOneVerifiedGeneration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "writes")
	request := normalizedWriteTestRequest(t, "private value")
	owner := writeTestOwner()
	operationID := writeTestOperationID("coordinator-success")
	writer := &coordinatorTestWriter{candidate: []byte("%PDF-1.7\ncoordinator candidate\n")}
	coordinator, err := newFormWriteCoordinator(root, writer)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, input := coordinatorTestInput(t, request, owner)
	outcome := coordinator.Fill(
		t.Context(), operationID, owner, snapshot, input, defaultInspectionLimits(), request,
	)
	if outcome.State != StateSucceeded || outcome.Failure != nil || outcome.Facts == nil ||
		outcome.Artifact == nil || outcome.Record.State != WriteVerified || writer.calls.Load() != 1 {
		t.Fatalf("first outcome = %#v, calls=%d", outcome, writer.calls.Load())
	}
	assertCoordinatorArtifact(t, snapshot, *outcome.Artifact, writer.candidate)
	if err = snapshot.Close(); err != nil {
		t.Fatal(err)
	}

	restartedWriter := &coordinatorTestWriter{
		state: StateFailed, failure: &Failure{Code: FailureWriteFailed, Message: "must not run"},
	}
	restarted, err := newFormWriteCoordinator(root, restartedWriter)
	if err != nil {
		t.Fatal(err)
	}
	retrySnapshot, retryInput := coordinatorTestInput(t, request, owner)
	replayed := restarted.Fill(
		t.Context(), operationID, owner, retrySnapshot, retryInput, defaultInspectionLimits(), request,
	)
	if replayed.State != StateSucceeded || !reflect.DeepEqual(replayed.Record, outcome.Record) ||
		restartedWriter.calls.Load() != 0 {
		t.Fatalf("replayed outcome = %#v, calls=%d", replayed, restartedWriter.calls.Load())
	}
	assertCoordinatorArtifact(t, retrySnapshot, *replayed.Artifact, writer.candidate)
	defer func() { _ = retrySnapshot.Close() }()

	for _, path := range []string{
		filepath.Join(root, "journal", operationID+".json"),
		filepath.Join(root, "generations", operationID, formWriteGenerationFilename),
	} {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, secret := range []string{"private value", owner.ActorID, owner.SessionID, "full_name"} {
			if bytes.Contains(data, []byte(secret)) {
				t.Fatalf("durable metadata %s leaked %q", filepath.Base(path), secret)
			}
		}
	}
}

func TestFormWriteCoordinatorRecoversEveryPredeliveryState(t *testing.T) {
	states := []WriteOperationState{
		WriteAccepted,
		WriteWriting,
		WriteWritten,
		WriteVerifying,
		WriteVerified,
	}
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "writes")
			request := normalizedWriteTestRequest(t, "restart value")
			owner := writeTestOwner()
			operationID := writeTestOperationID("recover-" + string(state))
			candidate := []byte("%PDF-1.7\nrestart candidate\n")
			seedWriter := &coordinatorTestWriter{candidate: candidate}
			seed, err := newFormWriteCoordinator(root, seedWriter)
			if err != nil {
				t.Fatal(err)
			}
			seedSnapshot, input := coordinatorTestInput(t, request, owner)
			seedCoordinatorState(t, seed, state, operationID, owner, seedSnapshot, input, request, candidate)
			_ = seedSnapshot.Close()

			writer := &coordinatorTestWriter{candidate: candidate}
			restarted, err := newFormWriteCoordinator(root, writer)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, retryInput := coordinatorTestInput(t, request, owner)
			defer func() { _ = snapshot.Close() }()
			outcome := restarted.Fill(
				t.Context(), operationID, owner, snapshot, retryInput, defaultInspectionLimits(), request,
			)
			wantCalls := int32(0)
			if state == WriteAccepted || state == WriteWriting {
				wantCalls = 1
			}
			if outcome.State != StateSucceeded || outcome.Record.State != WriteVerified ||
				writer.calls.Load() != wantCalls {
				t.Fatalf("outcome = %#v, calls=%d want=%d", outcome, writer.calls.Load(), wantCalls)
			}
			assertCoordinatorArtifact(t, snapshot, *outcome.Artifact, candidate)
		})
	}
}

func TestFormWriteCoordinatorTreatsPartialGenerationAsUncertain(t *testing.T) {
	root := filepath.Join(t.TempDir(), "writes")
	request := normalizedWriteTestRequest(t, "uncertain value")
	owner := writeTestOwner()
	operationID := writeTestOperationID("partial-generation")
	writer := &coordinatorTestWriter{candidate: []byte("%PDF-1.7\nunused\n")}
	coordinator, err := newFormWriteCoordinator(root, writer)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := coordinator.journal.Accept(t.Context(), operationID, owner, request)
	if err != nil {
		t.Fatal(err)
	}
	record, err = coordinator.transition(t.Context(), owner, record, WriteTransition{State: WriteWriting})
	if err != nil {
		t.Fatal(err)
	}
	operationRoot := filepath.Join(root, "generations", operationID)
	if err = os.Mkdir(operationRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(
		filepath.Join(operationRoot, formWriteCandidateFilename),
		[]byte("partial"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	snapshot, input := coordinatorTestInput(t, request, owner)
	defer func() { _ = snapshot.Close() }()
	outcome := coordinator.Fill(
		t.Context(), operationID, owner, snapshot, input, defaultInspectionLimits(), request,
	)
	if outcome.State != StateUncertain || outcome.Failure == nil ||
		outcome.Failure.Code != FailureRecoveryUncertain || outcome.Record.State != WriteUncertain ||
		writer.calls.Load() != 0 {
		t.Fatalf("uncertain outcome = %#v, calls=%d, prior=%#v", outcome, writer.calls.Load(), record)
	}
}

func TestFormWriteCoordinatorTreatsCorruptCommittedCandidateAsUncertain(t *testing.T) {
	root := filepath.Join(t.TempDir(), "writes")
	request := normalizedWriteTestRequest(t, "corrupt value")
	owner := writeTestOwner()
	operationID := writeTestOperationID("corrupt-generation")
	writer := &coordinatorTestWriter{candidate: []byte("%PDF-1.7\noriginal candidate\n")}
	coordinator, err := newFormWriteCoordinator(root, writer)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, input := coordinatorTestInput(t, request, owner)
	outcome := coordinator.Fill(
		t.Context(), operationID, owner, snapshot, input, defaultInspectionLimits(), request,
	)
	if outcome.State != StateSucceeded || writer.calls.Load() != 1 {
		t.Fatalf("initial outcome = %#v, calls=%d", outcome, writer.calls.Load())
	}
	if err = os.WriteFile(coordinator.store.candidatePath(operationID), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertCoordinatorArtifact(t, snapshot, *outcome.Artifact, writer.candidate)
	_ = snapshot.Close()

	restartedWriter := &coordinatorTestWriter{candidate: []byte("%PDF-1.7\nreplacement\n")}
	restarted, err := newFormWriteCoordinator(root, restartedWriter)
	if err != nil {
		t.Fatal(err)
	}
	retrySnapshot, retryInput := coordinatorTestInput(t, request, owner)
	defer func() { _ = retrySnapshot.Close() }()
	replayed := restarted.Fill(
		t.Context(), operationID, owner, retrySnapshot, retryInput, defaultInspectionLimits(), request,
	)
	if replayed.State != StateUncertain || replayed.Record.State != WriteUncertain || replayed.Failure == nil ||
		replayed.Failure.Code != FailureRecoveryUncertain || restartedWriter.calls.Load() != 0 {
		t.Fatalf("corrupt replay = %#v, calls=%d", replayed, restartedWriter.calls.Load())
	}
}

func TestFormWriteCoordinatorPersistsCancellationAfterRequestContextEnds(t *testing.T) {
	root := filepath.Join(t.TempDir(), "writes")
	request := normalizedWriteTestRequest(t, "canceled value")
	owner := writeTestOwner()
	operationID := writeTestOperationID("coordinator-canceled")
	started := make(chan struct{}, 1)
	writer := &coordinatorTestWriter{
		candidate: []byte("%PDF-1.7\nunused\n"), delay: time.Second, started: started,
	}
	coordinator, err := newFormWriteCoordinator(root, writer)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, input := coordinatorTestInput(t, request, owner)
	defer func() { _ = snapshot.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	outcomeReady := make(chan formWriteOutcome, 1)
	go func() {
		outcomeReady <- coordinator.Fill(
			ctx, operationID, owner, snapshot, input, defaultInspectionLimits(), request,
		)
	}()
	select {
	case <-started:
		cancel()
	case <-t.Context().Done():
		t.Fatal("form writer did not start")
	}
	outcome := <-outcomeReady
	if outcome.State != StateCanceled || outcome.Record.State != WriteCanceled || outcome.Failure == nil ||
		outcome.Failure.Code != FailureCanceled || writer.calls.Load() != 1 {
		t.Fatalf("canceled outcome = %#v, calls=%d", outcome, writer.calls.Load())
	}

	restartedWriter := &coordinatorTestWriter{candidate: []byte("%PDF-1.7\nreplacement\n")}
	restarted, err := newFormWriteCoordinator(root, restartedWriter)
	if err != nil {
		t.Fatal(err)
	}
	retrySnapshot, retryInput := coordinatorTestInput(t, request, owner)
	defer func() { _ = retrySnapshot.Close() }()
	replayed := restarted.Fill(
		t.Context(), operationID, owner, retrySnapshot, retryInput, defaultInspectionLimits(), request,
	)
	if replayed.State != StateCanceled || replayed.Record.State != WriteCanceled || restartedWriter.calls.Load() != 0 {
		t.Fatalf("canceled replay = %#v, calls=%d", replayed, restartedWriter.calls.Load())
	}
}

func TestFormWriteCoordinatorPersistsAndReplaysTypedWorkerFailures(t *testing.T) {
	tests := []struct {
		name  string
		state State
		code  FailureCode
	}{
		{name: "unavailable", state: StateUnavailable, code: FailureBackendUnavailable},
		{name: "unsupported", state: StateUnsupported, code: FailureFormNotPresent},
		{name: "failed", state: StateFailed, code: FailureFieldValueInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "writes")
			request := normalizedWriteTestRequest(t, "failed value")
			owner := writeTestOwner()
			operationID := writeTestOperationID("coordinator-" + test.name)
			writer := &coordinatorTestWriter{
				state: test.state, failure: &Failure{Code: test.code, Message: "unsafe /private/value"},
			}
			coordinator, err := newFormWriteCoordinator(root, writer)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, input := coordinatorTestInput(t, request, owner)
			outcome := coordinator.Fill(
				t.Context(), operationID, owner, snapshot, input, defaultInspectionLimits(), request,
			)
			_ = snapshot.Close()
			if outcome.State != test.state || outcome.Record.State != WriteFailed || outcome.Failure == nil ||
				outcome.Failure.Code != test.code || outcome.Failure.Message == writer.failure.Message ||
				writer.calls.Load() != 1 {
				t.Fatalf("terminal outcome = %#v, calls=%d", outcome, writer.calls.Load())
			}

			restartedWriter := &coordinatorTestWriter{candidate: []byte("%PDF-1.7\nreplacement\n")}
			restarted, restartErr := newFormWriteCoordinator(root, restartedWriter)
			if restartErr != nil {
				t.Fatal(restartErr)
			}
			retrySnapshot, retryInput := coordinatorTestInput(t, request, owner)
			defer func() { _ = retrySnapshot.Close() }()
			replayed := restarted.Fill(
				t.Context(), operationID, owner, retrySnapshot, retryInput, defaultInspectionLimits(), request,
			)
			if replayed.State != test.state || replayed.Record.State != WriteFailed || replayed.Failure == nil ||
				replayed.Failure.Code != test.code || restartedWriter.calls.Load() != 0 {
				t.Fatalf("terminal replay = %#v, calls=%d", replayed, restartedWriter.calls.Load())
			}
		})
	}
}

func TestFormWriteCoordinatorRejectsFailureFromAnotherWorkerOperation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "writes")
	request := normalizedWriteTestRequest(t, "bad value")
	owner := writeTestOwner()
	operationID := writeTestOperationID("coordinator-invalid-failure")
	writer := &coordinatorTestWriter{
		state: StateUnsupported,
		failure: &Failure{
			Code: FailureTextUnavailable, Message: "failure belongs to extract, not fill",
		},
	}
	coordinator, err := newFormWriteCoordinator(root, writer)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, input := coordinatorTestInput(t, request, owner)
	defer func() { _ = snapshot.Close() }()
	outcome := coordinator.Fill(
		t.Context(), operationID, owner, snapshot, input, defaultInspectionLimits(), request,
	)
	if outcome.State != StateFailed || outcome.Record.State != WriteFailed || outcome.Failure == nil ||
		outcome.Failure.Code != FailureWorkerProtocol || writer.calls.Load() != 1 {
		t.Fatalf("invalid failure outcome = %#v, calls=%d", outcome, writer.calls.Load())
	}
}

func TestFormWriteCoordinatorSerializesConcurrentRetry(t *testing.T) {
	root := filepath.Join(t.TempDir(), "writes")
	request := normalizedWriteTestRequest(t, "concurrent value")
	owner := writeTestOwner()
	operationID := writeTestOperationID("coordinator-concurrent")
	writer := &coordinatorTestWriter{
		candidate: []byte("%PDF-1.7\nconcurrent candidate\n"), delay: 25 * time.Millisecond,
	}
	coordinator, err := newFormWriteCoordinator(root, writer)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	outcomes := make(chan formWriteOutcome, 2)
	snapshots := make([]*Snapshot, 2)
	inputs := make([]DocumentRef, 2)
	for index := range snapshots {
		snapshots[index], inputs[index] = coordinatorTestInput(t, request, owner)
	}
	for index := range snapshots {
		wait.Add(1)
		go func(snapshot *Snapshot, input DocumentRef) {
			defer wait.Done()
			defer func() { _ = snapshot.Close() }()
			outcomes <- coordinator.Fill(
				t.Context(), operationID, owner, snapshot, input, defaultInspectionLimits(), request,
			)
		}(snapshots[index], inputs[index])
	}
	wait.Wait()
	close(outcomes)
	for outcome := range outcomes {
		if outcome.State != StateSucceeded || outcome.Record.State != WriteVerified {
			t.Fatalf("concurrent outcome = %#v", outcome)
		}
	}
	if writer.calls.Load() != 1 {
		t.Fatalf("writer calls = %d, want 1", writer.calls.Load())
	}
}

func seedCoordinatorState(
	t *testing.T,
	coordinator *formWriteCoordinator,
	state WriteOperationState,
	operationID string,
	owner Authority,
	snapshot *Snapshot,
	input DocumentRef,
	request NormalizedFillRequest,
	candidate []byte,
) {
	t.Helper()
	record, _, err := coordinator.journal.Accept(t.Context(), operationID, owner, request)
	if err != nil || state == WriteAccepted {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	record, err = coordinator.transition(t.Context(), owner, record, WriteTransition{State: WriteWriting})
	if err != nil || state == WriteWriting {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	result := coordinatorTestWorkerResult(
		snapshot, input, defaultInspectionLimits(), operationID, request, candidate,
	)
	generation, err := coordinator.store.Commit(operationID, snapshot, result)
	if err != nil {
		t.Fatal(err)
	}
	artifact := WriteArtifactEvidence{SHA256: generation.Artifact.SHA256, Size: generation.Artifact.Size}
	record, err = coordinator.transition(t.Context(), owner, record, WriteTransition{
		State: WriteWritten, Artifact: &artifact,
	})
	if err != nil || state == WriteWritten {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	record, err = coordinator.transition(t.Context(), owner, record, WriteTransition{State: WriteVerifying})
	if err != nil || state == WriteVerifying {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	verification := formWriteVerificationEvidence(generation.Facts)
	_, err = coordinator.transition(t.Context(), owner, record, WriteTransition{
		State: WriteVerified, Verification: &verification,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func coordinatorTestInput(
	t *testing.T,
	request NormalizedFillRequest,
	owner Authority,
) (*Snapshot, DocumentRef) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve coordinator temp directory: %v", err)
	}
	path := filepath.Join(dir, "snapshot.pdf")
	source := []byte("%PDF-1.7\nsource\n")
	if err = os.WriteFile(path, source, 0o400); err != nil {
		t.Fatal(err)
	}
	return &Snapshot{path: path, dir: dir}, DocumentRef{
		Ref: "document://test/source", ContentType: "application/pdf", Size: int64(len(source)),
		SHA256: request.SourceSHA256, Authority: owner, SourceKind: "test", CleanupPolicy: "test",
	}
}

func coordinatorTestWorkerResult(
	snapshot *Snapshot,
	input DocumentRef,
	limits Limits,
	operationID string,
	fill NormalizedFillRequest,
	candidate []byte,
) WorkerResult {
	digest := sha256.Sum256(candidate)
	outputDigest := hex.EncodeToString(digest[:])
	artifact := Artifact{
		Ref: workerArtifactRef(operationID, filledCandidateArtifactName), Kind: filledCandidateArtifactKind,
		ContentType: "application/pdf", Size: int64(len(candidate)), SHA256: outputDigest,
		SourceSHA256: input.SHA256, Pages: append([]int(nil), fill.AffectedPages...),
	}
	artifactRoot := filepath.Join(snapshot.dir, "artifacts")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		panic(err)
	}
	artifactPath := filepath.Join(artifactRoot, filledCandidateArtifactName)
	if err := os.WriteFile(artifactPath, candidate, 0o400); err != nil {
		panic(err)
	}
	if snapshot.artifactPaths == nil {
		snapshot.artifactPaths = make(map[string]string, 1)
	}
	snapshot.artifactPaths[artifact.Ref] = artifactPath
	facts := &FormWriteFacts{
		Backend: BackendIdentity{
			Name: PDFCPUBackendName, Version: PDFCPUBackendVersion, Role: "production",
			IsolationMode: "one_shot_process",
		},
		VisualBackend: popplerIdentity(), SourceSHA256: input.SHA256, RequestSHA256: fill.RequestSHA256,
		OutputSHA256: outputDigest, OutputSize: int64(len(candidate)),
		AffectedPages:        append([]int(nil), fill.AffectedPages...),
		StructuralAssertions: formWriteStructuralAssertionCount,
		CheckedFields:        len(fill.Assignments), CheckedWidgets: 2, UnchangedFields: 0, AppearanceWidgets: 2,
		VisualAssertions: len(fill.AffectedPages) + 2, RenderedPages: len(fill.AffectedPages),
	}
	request := newWorkerOperationRequest(input, limits, workerOperationFillCandidate)
	request.OperationID = operationID
	request.Fill = &fill
	return WorkerResult{
		SchemaVersion: WorkerResultSchemaVersion, OperationID: operationID, State: StateSucceeded,
		Input: &request.Input, Write: facts,
		Artifacts: []WorkerArtifact{{Name: filledCandidateArtifactName, Artifact: artifact}},
	}
}

func assertCoordinatorArtifact(t *testing.T, snapshot *Snapshot, artifact Artifact, want []byte) {
	t.Helper()
	file, err := snapshot.OpenArtifact(artifact.Ref)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, want) {
		t.Fatalf("artifact = %q, want %q", data, want)
	}
}
