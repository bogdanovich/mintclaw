package document

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const writeTestArtifactRef = "media://00000000-0000-4000-8000-000000000001"

func writeTestOperationID(label string) string {
	digest := sha256.Sum256([]byte(label))
	digest[6] = digest[6]&0x0f | 0x40
	digest[8] = digest[8]&0x3f | 0x80
	return "document_write_" + hex.EncodeToString(digest[:16])
}

func TestWriteJournalPersistsValueFreeOwnerScopedAcceptance(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	journal, err := NewWriteJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	request := normalizedWriteTestRequest(t, "private value")
	owner := writeTestOwner()
	record, created, err := journal.Accept(t.Context(), writeTestOperationID("acceptance"), owner, request)
	if err != nil || !created {
		t.Fatalf("accept = %#v, created=%v, err=%v", record, created, err)
	}
	if record.State != WriteAccepted || record.Revision != 1 || record.OutputGeneration != 1 ||
		record.SourceSHA256 != request.SourceSHA256 || record.RequestSHA256 != request.RequestSHA256 ||
		record.BackendGeneration != PDFCPUWriteBackendGeneration || !opaqueDeliveryID.MatchString(record.DeliveryID) {
		t.Fatalf("accepted record = %#v", record)
	}
	repeated, created, err := journal.Accept(t.Context(), record.OperationID, owner, request)
	if err != nil || created || repeated != record {
		t.Fatalf("repeat = %#v, created=%v, err=%v", repeated, created, err)
	}
	reopened, err := NewWriteJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, found, err := reopened.Lookup(t.Context(), record.OperationID, owner)
	if err != nil || !found || loaded != record {
		t.Fatalf("lookup = %#v, found=%v, err=%v", loaded, found, err)
	}

	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := os.ReadFile(filepath.Join(root, record.OperationID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"private value", owner.ActorID, owner.SessionID, "/home/operator/form.pdf", "full_name",
	} {
		if strings.Contains(string(encoded), forbidden) || strings.Contains(string(disk), forbidden) {
			t.Fatalf("journal leaked %q: %s", forbidden, disk)
		}
	}
	assertPrivateDocumentJournalPath(t, root, true)
	assertPrivateDocumentJournalPath(t, filepath.Join(root, record.OperationID+".json"), false)
	assertPrivateDocumentJournalPath(t, journal.lockPath(), false)
}

func TestWriteJournalRejectsNonOpaqueOperationIdentifiersBeforePersistence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	journal, err := NewWriteJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	request := normalizedWriteTestRequest(t, "submitted value")
	owner := writeTestOwner()
	for _, operationID := range []string{
		"document_write_private-actor",
		"document_write_submitted value",
		"document_operation_00000000000040008000000000000001",
		"document_write_00000000000030008000000000000001",
		"document_write_00000000000040000000000000000001",
	} {
		if _, created, acceptErr := journal.Accept(
			t.Context(),
			operationID,
			owner,
			request,
		); !errors.Is(acceptErr, ErrWriteConflict) || created {
			t.Fatalf("accept %q = created=%v error=%v", operationID, created, acceptErr)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid operation identifiers created journal entries: %#v", entries)
	}
	record, created, err := journal.Accept(t.Context(), "", owner, request)
	if err != nil || !created || !validWriteOperationID(record.OperationID) {
		t.Fatalf("generated acceptance = %#v, created=%v, error=%v", record, created, err)
	}
}

func TestWriteJournalRejectsBindingAndCASConflicts(t *testing.T) {
	journal, err := NewWriteJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	request := normalizedWriteTestRequest(t, "one")
	owner := writeTestOwner()
	record, _, err := journal.Accept(t.Context(), writeTestOperationID("conflict"), owner, request)
	if err != nil {
		t.Fatal(err)
	}
	differentRequest := normalizedWriteTestRequest(t, "two")
	if _, _, err = journal.Accept(
		t.Context(),
		record.OperationID,
		owner,
		differentRequest,
	); !errors.Is(
		err,
		ErrWriteConflict,
	) {
		t.Fatalf("request conflict error = %v", err)
	}
	differentOwner := owner
	differentOwner.ActorID = "other-actor"
	if _, _, err = journal.Accept(
		t.Context(),
		record.OperationID,
		differentOwner,
		request,
	); !errors.Is(
		err,
		ErrWriteConflict,
	) {
		t.Fatalf("owner conflict error = %v", err)
	}
	if _, _, err = journal.Lookup(t.Context(), record.OperationID, differentOwner); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("lookup conflict error = %v", err)
	}
	writing, changed, err := journal.Transition(t.Context(), record.OperationID, owner, WriteTransition{
		ExpectedRevision: record.Revision,
		State:            WriteWriting,
	})
	if err != nil || !changed {
		t.Fatalf("writing transition = %#v, changed=%v, err=%v", writing, changed, err)
	}
	idempotent, changed, err := journal.Transition(t.Context(), record.OperationID, owner, WriteTransition{
		ExpectedRevision: record.Revision,
		State:            WriteWriting,
	})
	if err != nil || changed || idempotent != writing {
		t.Fatalf("idempotent transition = %#v, changed=%v, err=%v", idempotent, changed, err)
	}
	if _, _, err = journal.Transition(t.Context(), record.OperationID, owner, WriteTransition{
		ExpectedRevision: record.Revision,
		State:            WriteCanceled,
		FailureCode:      FailureCanceled,
	}); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("stale conflicting transition error = %v", err)
	}
}

func TestWriteJournalRecoversEveryForwardTransition(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	journal, err := NewWriteJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	owner := writeTestOwner()
	request := normalizedWriteTestRequest(t, "recovery value")
	record, _, err := journal.Accept(t.Context(), writeTestOperationID("recovery"), owner, request)
	if err != nil {
		t.Fatal(err)
	}
	deliveryID := record.DeliveryID
	assertWriteRecovery(t, root, owner, record, RecoveryResumeWrite, deliveryID)
	transitions := []struct {
		transition WriteTransition
		action     WriteRecoveryAction
	}{
		{transition: WriteTransition{State: WriteWriting}, action: RecoveryInspectWrite},
		{
			transition: WriteTransition{
				State:    WriteWritten,
				Artifact: &WriteArtifactEvidence{SHA256: strings.Repeat("b", 64), Size: 4096},
			},
			action: RecoveryResumeVerification,
		},
		{transition: WriteTransition{State: WriteVerifying}, action: RecoveryInspectVerification},
		{
			transition: WriteTransition{
				State: WriteVerified,
				Verification: &WriteVerificationEvidence{
					StructuralAssertions: 12,
					VisualAssertions:     8,
					CheckedFields:        1,
					CheckedWidgets:       2,
					UnchangedFields:      7,
					RenderedPages:        2,
				},
			},
			action: RecoveryResumeRegistration,
		},
		{
			transition: WriteTransition{State: WriteRegistered, ArtifactRef: writeTestArtifactRef},
			action:     RecoveryResumeDelivery,
		},
		{transition: WriteTransition{State: WriteDeliveryPending}, action: RecoveryInspectDelivery},
		{transition: WriteTransition{State: WriteDelivered}, action: RecoveryReturnTerminal},
	}
	for _, item := range transitions {
		item.transition.ExpectedRevision = record.Revision
		record, _, err = journal.Transition(t.Context(), record.OperationID, owner, item.transition)
		if err != nil {
			t.Fatalf("transition to %s: %v", item.transition.State, err)
		}
		assertWriteRecovery(t, root, owner, record, item.action, deliveryID)
		journal, err = NewWriteJournal(root)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestWriteJournalTerminalRecoveryBranches(t *testing.T) {
	tests := []struct {
		state WriteOperationState
		code  FailureCode
	}{
		{state: WriteCanceled, code: FailureCanceled},
		{state: WriteFailed, code: FailureWriteFailed},
		{state: WriteUncertain, code: FailureRecoveryUncertain},
	}
	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "journal")
			journal, err := NewWriteJournal(root)
			if err != nil {
				t.Fatal(err)
			}
			owner := writeTestOwner()
			record, _, err := journal.Accept(
				t.Context(), writeTestOperationID("terminal_"+string(test.state)), owner,
				normalizedWriteTestRequest(t, "terminal value"),
			)
			if err != nil {
				t.Fatal(err)
			}
			record, _, err = journal.Transition(t.Context(), record.OperationID, owner, WriteTransition{
				ExpectedRevision: record.Revision,
				State:            test.state,
				FailureCode:      test.code,
			})
			if err != nil {
				t.Fatal(err)
			}
			assertWriteRecovery(t, root, owner, record, RecoveryReturnTerminal, record.DeliveryID)
		})
	}
}

func TestWriteJournalDeliveryFailureBranchesAreTerminal(t *testing.T) {
	tests := []struct {
		state WriteOperationState
		code  FailureCode
	}{
		{state: WriteDeliveryFailed, code: FailureDeliveryFailed},
		{state: WriteDeliveryAmbiguous, code: FailureDeliveryAmbiguous},
	}
	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "journal")
			journal, err := NewWriteJournal(root)
			if err != nil {
				t.Fatal(err)
			}
			owner := writeTestOwner()
			record := advanceWriteToDeliveryPending(t, journal, owner, writeTestOperationID(string(test.state)))
			record, changed, err := journal.Transition(t.Context(), record.OperationID, owner, WriteTransition{
				ExpectedRevision: record.Revision,
				State:            test.state,
				FailureCode:      test.code,
			})
			if err != nil || !changed {
				t.Fatalf("delivery terminal = %#v, changed=%v, err=%v", record, changed, err)
			}
			assertWriteRecovery(t, root, owner, record, RecoveryReturnTerminal, record.DeliveryID)
		})
	}
}

func TestWriteJournalCannotCancelRegisteredArtifact(t *testing.T) {
	journal, err := NewWriteJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	owner := writeTestOwner()
	record := advanceWriteToRegistered(t, journal, owner, writeTestOperationID("registered"))
	if _, _, err = journal.Transition(t.Context(), record.OperationID, owner, WriteTransition{
		ExpectedRevision: record.Revision,
		State:            WriteCanceled,
		FailureCode:      FailureCanceled,
	}); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("registered cancellation error = %v", err)
	}
}

func TestWriteJournalRejectsSensitiveArtifactReference(t *testing.T) {
	journal, err := NewWriteJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	owner := writeTestOwner()
	record := advanceWriteToVerified(t, journal, owner, writeTestOperationID("sensitive_ref"))
	for _, artifactRef := range []string{
		"media:///home/operator/private value.pdf",
		"media://private-actor",
		"media://00000000-0000-4000-0000-000000000001",
		"document-artifact://document_write_sensitive_ref/output.pdf",
	} {
		if _, _, err = journal.Transition(t.Context(), record.OperationID, owner, WriteTransition{
			ExpectedRevision: record.Revision,
			State:            WriteRegistered,
			ArtifactRef:      artifactRef,
		}); !errors.Is(err, ErrWriteConflict) {
			t.Fatalf("artifact ref %q error = %v", artifactRef, err)
		}
	}
}

func TestWriteJournalStaleRetryRequiresExactEvidence(t *testing.T) {
	journal, err := NewWriteJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	owner := writeTestOwner()
	record, _, err := journal.Accept(
		t.Context(), writeTestOperationID("retry_evidence"), owner,
		normalizedWriteTestRequest(t, "retry value"),
	)
	if err != nil {
		t.Fatal(err)
	}
	states := []WriteTransition{
		{State: WriteWriting},
		{State: WriteWritten, Artifact: &WriteArtifactEvidence{SHA256: strings.Repeat("b", 64), Size: 4096}},
		{State: WriteVerifying},
		{
			State: WriteVerified,
			Verification: &WriteVerificationEvidence{
				StructuralAssertions: 12,
				VisualAssertions:     8,
				CheckedFields:        1,
				CheckedWidgets:       2,
				UnchangedFields:      7,
				RenderedPages:        2,
			},
		},
		{State: WriteRegistered, ArtifactRef: writeTestArtifactRef},
	}
	for _, transition := range states {
		expectedRevision := record.Revision
		transition.ExpectedRevision = expectedRevision
		record, _, err = journal.Transition(t.Context(), record.OperationID, owner, transition)
		if err != nil {
			t.Fatal(err)
		}
		if transition.State != WriteWritten && transition.State != WriteVerified &&
			transition.State != WriteRegistered {
			continue
		}
		if _, _, err = journal.Transition(t.Context(), record.OperationID, owner, WriteTransition{
			ExpectedRevision: expectedRevision,
			State:            transition.State,
		}); !errors.Is(err, ErrWriteConflict) {
			t.Fatalf("evidence-free retry for %s error = %v", transition.State, err)
		}
		different := transition
		switch transition.State {
		case WriteWritten:
			artifact := *transition.Artifact
			artifact.Size++
			different.Artifact = &artifact
		case WriteVerified:
			verification := *transition.Verification
			verification.VisualAssertions++
			different.Verification = &verification
		case WriteRegistered:
			different.ArtifactRef = "media://00000000-0000-4000-8000-000000000002"
		}
		if _, _, err = journal.Transition(
			t.Context(), record.OperationID, owner, different,
		); !errors.Is(err, ErrWriteConflict) {
			t.Fatalf("mismatched retry for %s error = %v", transition.State, err)
		}
	}
}

func TestWriteJournalRejectsSkippedOrUnverifiedPublication(t *testing.T) {
	journal, err := NewWriteJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	owner := writeTestOwner()
	record, _, err := journal.Accept(
		t.Context(), writeTestOperationID("invalid_transition"), owner,
		normalizedWriteTestRequest(t, "invalid transition"),
	)
	if err != nil {
		t.Fatal(err)
	}
	invalid := []WriteTransition{
		{ExpectedRevision: record.Revision, State: WriteWritten},
		{ExpectedRevision: record.Revision, State: WriteVerified},
		{ExpectedRevision: record.Revision, State: WriteRegistered, ArtifactRef: "media://invalid"},
	}
	for _, transition := range invalid {
		if _, _, err = journal.Transition(
			t.Context(),
			record.OperationID,
			owner,
			transition,
		); !errors.Is(
			err,
			ErrWriteConflict,
		) {
			t.Fatalf("transition to %q error = %v", transition.State, err)
		}
	}
	record, _, err = journal.Transition(t.Context(), record.OperationID, owner, WriteTransition{
		ExpectedRevision: record.Revision,
		State:            WriteWriting,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, _, err = journal.Transition(t.Context(), record.OperationID, owner, WriteTransition{
		ExpectedRevision: record.Revision,
		State:            WriteWritten,
		Artifact:         &WriteArtifactEvidence{SHA256: strings.Repeat("b", 64), Size: 4096},
	})
	if err != nil {
		t.Fatal(err)
	}
	record, _, err = journal.Transition(t.Context(), record.OperationID, owner, WriteTransition{
		ExpectedRevision: record.Revision,
		State:            WriteVerifying,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = journal.Transition(t.Context(), record.OperationID, owner, WriteTransition{
		ExpectedRevision: record.Revision,
		State:            WriteVerified,
		Verification: &WriteVerificationEvidence{
			StructuralAssertions: 5,
			CheckedFields:        1,
			CheckedWidgets:       1,
			RenderedPages:        1,
		},
	}); !errors.Is(err, ErrWriteConflict) {
		t.Fatalf("unverified publication error = %v", err)
	}
}

func TestWriteJournalSerializesConcurrentAcceptance(t *testing.T) {
	journal, err := NewWriteJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	request := normalizedWriteTestRequest(t, "concurrent value")
	owner := writeTestOwner()
	const workers = 32
	var created atomic.Int32
	errorsFound := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, wasCreated, acceptErr := journal.Accept(
				context.Background(), writeTestOperationID("concurrent"), owner, request,
			)
			if wasCreated {
				created.Add(1)
			}
			errorsFound <- acceptErr
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err = range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	if created.Load() != 1 {
		t.Fatalf("created count = %d, want 1", created.Load())
	}
}

func TestWriteJournalSerializesCrossProcessAcceptance(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	const workers = 8
	type result struct {
		output []byte
		err    error
	}
	results := make(chan result, workers)
	for range workers {
		go func() {
			command := exec.Command(os.Args[0], "-test.v", "-test.run=^TestWriteJournalProcessHelper$")
			command.Env = append(os.Environ(),
				"MINTCLAW_DOCUMENT_JOURNAL_HELPER=1",
				"MINTCLAW_DOCUMENT_JOURNAL_ROOT="+root,
			)
			output, commandErr := command.CombinedOutput()
			results <- result{output: output, err: commandErr}
		}()
	}
	created := 0
	for range workers {
		result := <-results
		if result.err != nil {
			t.Fatalf("journal helper failed: %v: %s", result.err, result.output)
		}
		if bytes.Contains(result.output, []byte("created=true")) {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("cross-process created count = %d, want 1", created)
	}
}

func TestWriteJournalLockWaitHonorsContext(t *testing.T) {
	journal, err := NewWriteJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	release, err := acquireDocumentJournalFileLock(t.Context(), journal.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, _, err = journal.Accept(
		ctx,
		writeTestOperationID("lock_cancel"),
		writeTestOwner(),
		normalizedWriteTestRequest(t, "safe value"),
	)
	if !errors.Is(err, context.DeadlineExceeded) || WriteJournalFailureCode(err) != FailureCanceled {
		t.Fatalf("lock wait error = %v (%q)", err, WriteJournalFailureCode(err))
	}
}

func TestWriteJournalProcessHelper(t *testing.T) {
	if os.Getenv("MINTCLAW_DOCUMENT_JOURNAL_HELPER") != "1" {
		return
	}
	journal, err := NewWriteJournal(os.Getenv("MINTCLAW_DOCUMENT_JOURNAL_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	record, created, err := journal.Accept(
		t.Context(),
		writeTestOperationID("process"),
		writeTestOwner(),
		normalizedWriteTestRequest(t, "process value"),
	)
	if err != nil || record.State != WriteAccepted {
		t.Fatalf("process accept = %#v, err=%v", record, err)
	}
	t.Logf("created=%t", created)
}

func TestWriteJournalClassifiesPrecommitAndPostcommitFailures(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	precommit, err := newWriteJournal(root, time.Now, func(string, []byte, os.FileMode) error {
		return errors.New("disk unavailable")
	})
	if err != nil {
		t.Fatal(err)
	}
	request := normalizedWriteTestRequest(t, "fault value")
	owner := writeTestOwner()
	if _, _, err = precommit.Accept(
		t.Context(),
		writeTestOperationID("precommit"),
		owner,
		request,
	); !errors.Is(err, ErrWriteJournalFailed) ||
		WriteJournalFailureCode(err) != FailureJournalFailed {
		t.Fatalf("precommit error = %v", err)
	}
	if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "disk unavailable") {
		t.Fatalf("journal error leaked internal cause: %q", err)
	}
	normal, err := NewWriteJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, lookupErr := normal.Lookup(
		t.Context(),
		writeTestOperationID("precommit"),
		owner,
	); lookupErr != nil || found {
		t.Fatalf("precommit record found=%v, err=%v", found, lookupErr)
	}

	postcommit, err := newWriteJournal(root, time.Now, func(path string, data []byte, mode os.FileMode) error {
		if writeErr := fileutil.WriteFileAtomic(path, data, mode); writeErr != nil {
			return writeErr
		}
		return &fileutil.CommittedWriteError{Err: errors.New("directory sync uncertain")}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = postcommit.Accept(
		t.Context(),
		writeTestOperationID("postcommit"),
		owner,
		request,
	); !errors.Is(err, ErrWriteJournalUncertain) ||
		WriteJournalFailureCode(err) != FailureRecoveryUncertain {
		t.Fatalf("postcommit error = %v", err)
	}
	recovered, created, err := normal.Accept(t.Context(), writeTestOperationID("postcommit"), owner, request)
	if err != nil || created || recovered.State != WriteAccepted {
		t.Fatalf("postcommit recovery = %#v, created=%v, err=%v", recovered, created, err)
	}

	record, _, err := normal.Accept(t.Context(), writeTestOperationID("transition_postcommit"), owner, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = postcommit.Transition(t.Context(), record.OperationID, owner, WriteTransition{
		ExpectedRevision: record.Revision,
		State:            WriteWriting,
	}); !errors.Is(err, ErrWriteJournalUncertain) {
		t.Fatalf("postcommit transition error = %v", err)
	}
	recovered, changed, err := normal.Transition(t.Context(), record.OperationID, owner, WriteTransition{
		ExpectedRevision: record.Revision,
		State:            WriteWriting,
	})
	if err != nil || changed || recovered.State != WriteWriting || recovered.Revision != record.Revision+1 {
		t.Fatalf("postcommit transition recovery = %#v, changed=%v, err=%v", recovered, changed, err)
	}
}

func TestWriteJournalRejectsCorruptOrUnsafeRecords(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	journal, err := NewWriteJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	owner := writeTestOwner()
	operationID := writeTestOperationID("corrupt")
	if err = os.WriteFile(
		filepath.Join(root, operationID+".json"),
		[]byte(`{"schema_version":"wrong"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err = journal.Lookup(t.Context(), operationID, owner); !errors.Is(err, ErrWriteJournalFailed) {
		t.Fatalf("corrupt lookup error = %v", err)
	}
	if _, err = NewWriteJournal(""); !errors.Is(err, ErrWriteJournalFailed) {
		t.Fatalf("empty root error = %v", err)
	}
}

func TestWriteJournalClassifiesCanceledContext(t *testing.T) {
	journal, err := NewWriteJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = journal.Accept(
		ctx,
		writeTestOperationID("canceled_context"),
		writeTestOwner(),
		normalizedWriteTestRequest(t, "safe value"),
	)
	if !errors.Is(err, context.Canceled) || WriteJournalFailureCode(err) != FailureCanceled {
		t.Fatalf("canceled context error = %v (%q)", err, WriteJournalFailureCode(err))
	}
}

func assertWriteRecovery(
	t *testing.T,
	root string,
	owner Authority,
	want WriteOperationRecord,
	action WriteRecoveryAction,
	deliveryID string,
) {
	t.Helper()
	journal, err := NewWriteJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := journal.Recover(t.Context(), want.OperationID, owner)
	if err != nil || !reflect.DeepEqual(recovery.Record, want) || recovery.Action != action ||
		recovery.Record.OutputGeneration != 1 || recovery.Record.DeliveryID != deliveryID {
		t.Fatalf("recovery = %#v, err=%v, want record=%#v action=%q", recovery, err, want, action)
	}
}

func advanceWriteToDeliveryPending(
	t *testing.T,
	journal *WriteJournal,
	owner Authority,
	operationID string,
) WriteOperationRecord {
	t.Helper()
	record := advanceWriteToRegistered(t, journal, owner, operationID)
	var err error
	record, _, err = journal.Transition(t.Context(), operationID, owner, WriteTransition{
		ExpectedRevision: record.Revision,
		State:            WriteDeliveryPending,
	})
	if err != nil {
		t.Fatalf("advance to %s: %v", WriteDeliveryPending, err)
	}
	return record
}

func advanceWriteToRegistered(
	t *testing.T,
	journal *WriteJournal,
	owner Authority,
	operationID string,
) WriteOperationRecord {
	t.Helper()
	record := advanceWriteToVerified(t, journal, owner, operationID)
	var err error
	record, _, err = journal.Transition(t.Context(), operationID, owner, WriteTransition{
		ExpectedRevision: record.Revision,
		State:            WriteRegistered,
		ArtifactRef:      writeTestArtifactRef,
	})
	if err != nil {
		t.Fatalf("advance to %s: %v", WriteRegistered, err)
	}
	return record
}

func advanceWriteToVerified(
	t *testing.T,
	journal *WriteJournal,
	owner Authority,
	operationID string,
) WriteOperationRecord {
	t.Helper()
	record, _, err := journal.Accept(
		t.Context(), operationID, owner, normalizedWriteTestRequest(t, "delivery value"),
	)
	if err != nil {
		t.Fatal(err)
	}
	transitions := []WriteTransition{
		{State: WriteWriting},
		{State: WriteWritten, Artifact: &WriteArtifactEvidence{SHA256: strings.Repeat("b", 64), Size: 4096}},
		{State: WriteVerifying},
		{
			State: WriteVerified,
			Verification: &WriteVerificationEvidence{
				StructuralAssertions: 12,
				VisualAssertions:     8,
				CheckedFields:        1,
				CheckedWidgets:       2,
				UnchangedFields:      7,
				RenderedPages:        2,
			},
		},
	}
	for _, transition := range transitions {
		transition.ExpectedRevision = record.Revision
		record, _, err = journal.Transition(t.Context(), operationID, owner, transition)
		if err != nil {
			t.Fatalf("advance to %s: %v", transition.State, err)
		}
	}
	return record
}

func normalizedWriteTestRequest(t *testing.T, value string) NormalizedFillRequest {
	t.Helper()
	input, facts := fillNormalizationFixture()
	request, err := NormalizeFillMap(input, facts, validFillMap(fillTextAssignment(1, value)))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func writeTestOwner() Authority {
	return Authority{
		Kind:        "agent_attachment",
		WorkspaceID: "workspace-main",
		AgentID:     "main",
		ActorID:     "private-actor",
		RouteID:     "telegram:private-chat",
		SessionID:   "private-session",
	}
}
