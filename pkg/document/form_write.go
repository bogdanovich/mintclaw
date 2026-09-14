package document

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"runtime"

	"github.com/bogdanovich/mintclaw/pkg/media"
)

const (
	operationFill            = "fill"
	operationVerifyFormWrite = "verify"
)

type FormWriteOptions struct {
	Acquire     AcquireOptions
	StateRoot   string
	OperationID string
}

// FormWriteExpectation is the path-free, value-free proof extracted from one
// successful fill report. Verify also requires the matching private journal
// and generation; this structure alone cannot bless a PDF.
type FormWriteExpectation struct {
	OperationID string
	Artifact    Artifact
	Facts       FormWriteFacts
}

// DecodeFormWriteExpectation accepts only a bounded successful fill report.
func DecodeFormWriteExpectation(reader io.Reader) (FormWriteExpectation, error) {
	var report Report
	if reader == nil || decodeBoundedJSON(reader, DefaultMaxFormReportBytes, &report) != nil ||
		report.SchemaVersion != ReportSchemaVersion || report.Operation != operationFill ||
		report.State != StateSucceeded || !validWriteOperationID(report.OperationID) || report.Input == nil ||
		report.Write == nil || report.Failure != nil || len(report.Artifacts) != 1 ||
		report.Input.SHA256 != report.Write.SourceSHA256 {
		return FormWriteExpectation{}, errors.New("document fill report is invalid")
	}
	expectation := FormWriteExpectation{
		OperationID: report.OperationID,
		Artifact:    report.Artifacts[0],
		Facts:       *report.Write,
	}
	if !validFormWriteExpectation(expectation) {
		return FormWriteExpectation{}, errors.New("document fill report is invalid")
	}
	return expectation, nil
}

// Fill acquires one local operator PDF and returns only a structurally and
// visually verified private candidate. Source bytes are never modified.
func Fill(
	ctx context.Context,
	inputPath string,
	fill FillMap,
	options FormWriteOptions,
) (*Snapshot, Report) {
	snapshot, report := acquireOperationForPlatform(
		ctx,
		inputPath,
		options.Acquire,
		runtime.GOOS,
		runtime.GOARCH,
		operationFill,
	)
	return fillAcquiredSnapshot(
		ctx,
		snapshot,
		report,
		fill,
		options,
		NewProcessFormFieldsWorker(),
		NewProcessFormWriterWorker(),
	)
}

// FillMedia applies the same write contract to one exact authority-bound
// MediaStore input. Registration and delivery remain caller-owned.
func FillMedia(
	ctx context.Context,
	resolver OwnedMediaResolver,
	ref string,
	owner media.MediaOwner,
	fill FillMap,
	options FormWriteOptions,
) (*Snapshot, Report) {
	snapshot, report := acquireMediaOperationForPlatform(
		ctx,
		resolver,
		ref,
		owner,
		options.Acquire,
		runtime.GOOS,
		runtime.GOARCH,
		operationFill,
	)
	return fillAcquiredSnapshot(
		ctx,
		snapshot,
		report,
		fill,
		options,
		NewProcessFormFieldsWorker(),
		NewProcessFormWriterWorker(),
	)
}

func fillAcquiredSnapshot(
	ctx context.Context,
	snapshot *Snapshot,
	report Report,
	fill FillMap,
	options FormWriteOptions,
	fieldsWorker FormFieldsWorker,
	writer FormWriterWorker,
) (*Snapshot, Report) {
	if snapshot == nil || report.State != StateSucceeded || report.Input == nil {
		return snapshot, report
	}
	snapshot, report = fieldsAcquiredSnapshot(ctx, snapshot, report, fieldsWorker)
	if snapshot == nil || report.State != StateSucceeded || report.Input == nil || report.Fields == nil {
		return snapshot, report
	}
	normalized, err := NormalizeFillMap(*report.Input, *report.Fields, fill)
	if err != nil {
		return cleanupFormWriteFailure(
			snapshot,
			failFormWriteReport(report, StateFailed, FillRequestFailureCode(err), err.Error()),
		)
	}
	coordinator, err := newFormWriteCoordinator(options.StateRoot, writer)
	if err != nil {
		return cleanupFormWriteFailure(
			snapshot,
			failFormWriteReport(
				report,
				StateFailed,
				FailureJournalFailed,
				"document form write journal is unavailable",
			),
		)
	}
	outcome := coordinator.Fill(
		ctx,
		options.OperationID,
		report.Input.Authority,
		snapshot,
		*report.Input,
		report.Limits,
		normalized,
	)
	if outcome.Record.OperationID != "" {
		report.OperationID = outcome.Record.OperationID
	}
	if outcome.State != StateSucceeded || outcome.Facts == nil || outcome.Artifact == nil {
		code := FailureInternal
		message := "document form write failed"
		if outcome.Failure != nil {
			code = outcome.Failure.Code
			message = outcome.Failure.Message
		}
		return cleanupFormWriteFailure(snapshot, failFormWriteReport(report, outcome.State, code, message))
	}
	report.State = StateSucceeded
	report.Inspection = nil
	report.Fields = nil
	report.Write = outcome.Facts
	report.Artifacts = []Artifact{*outcome.Artifact}
	report.Failure = nil
	return snapshot, report
}

// Verify checks one caller-visible candidate against the durable fill record,
// committed generation, and value-free report expectation.
func Verify(
	ctx context.Context,
	inputPath string,
	expectation FormWriteExpectation,
	options FormWriteOptions,
) (*Snapshot, Report) {
	snapshot, report := acquireOperationForPlatform(
		ctx,
		inputPath,
		options.Acquire,
		runtime.GOOS,
		runtime.GOARCH,
		operationVerifyFormWrite,
	)
	return verifyFormWriteSnapshot(
		ctx,
		snapshot,
		report,
		expectation,
		options.StateRoot,
		NewProcessWorker(),
	)
}

func verifyFormWriteSnapshot(
	ctx context.Context,
	snapshot *Snapshot,
	report Report,
	expectation FormWriteExpectation,
	stateRoot string,
	worker Worker,
) (*Snapshot, Report) {
	if snapshot == nil || report.State != StateSucceeded || report.Input == nil {
		return snapshot, report
	}
	snapshot, report = verifyAcquiredSnapshot(ctx, snapshot, report, worker)
	if snapshot == nil || report.State != StateSucceeded || report.Input == nil {
		return snapshot, report
	}
	report.OperationID = expectation.OperationID
	if !validFormWriteExpectation(expectation) {
		return cleanupFormWriteFailure(
			snapshot,
			failFormWriteReport(report, StateFailed, FailureInvalidInput, "document fill report is invalid"),
		)
	}
	resolved, err := prepareWriteJournalRoot(stateRoot)
	if err != nil {
		return cleanupFormWriteFailure(
			snapshot,
			failFormWriteReport(
				report,
				StateFailed,
				FailureJournalFailed,
				"document form write journal is unavailable",
			),
		)
	}
	journal, err := NewWriteJournal(filepath.Join(resolved, "journal"))
	if err != nil {
		return cleanupFormWriteFailure(
			snapshot,
			failFormWriteReport(
				report,
				StateFailed,
				FailureJournalFailed,
				"document form write journal is unavailable",
			),
		)
	}
	store, err := newFormWriteStore(filepath.Join(resolved, "generations"))
	if err != nil {
		return cleanupFormWriteFailure(
			snapshot,
			failFormWriteReport(
				report,
				StateFailed,
				FailureJournalFailed,
				"document form write journal is unavailable",
			),
		)
	}
	release, err := acquireDocumentJournalFileLock(ctx, store.executionLockPath(expectation.OperationID))
	if err != nil {
		return cleanupFormWriteFailure(snapshot, formWriteJournalReport(report, err))
	}
	defer release()
	record, found, err := journal.Lookup(ctx, expectation.OperationID, report.Input.Authority)
	if err != nil {
		return cleanupFormWriteFailure(snapshot, formWriteJournalReport(report, err))
	}
	if !found {
		return cleanupFormWriteFailure(
			snapshot,
			failFormWriteReport(report, StateFailed, FailureWriteConflict, "document fill report has no durable state"),
		)
	}
	generation, generationFound, err := store.Load(expectation.OperationID)
	if err != nil {
		return cleanupFormWriteFailure(
			snapshot,
			failFormWriteReport(
				report,
				StateUncertain,
				FailureRecoveryUncertain,
				"document form write recovery is uncertain",
			),
		)
	}
	if !verifiedWriteState(record.State) ||
		!generationMatchesRecord(generation, generationFound, record, true) ||
		!equalFormWriteGeneration(generation, expectationGeneration(expectation)) {
		return cleanupFormWriteFailure(
			snapshot,
			failFormWriteReport(
				report,
				StateFailed,
				FailureWriteConflict,
				"document fill report conflicts with durable state",
			),
		)
	}
	if report.Input.SHA256 != generation.Artifact.SHA256 || report.Input.Size != generation.Artifact.Size {
		return cleanupFormWriteFailure(
			snapshot,
			failFormWriteReport(
				report,
				StateFailed,
				FailureVerificationStructural,
				"document form candidate failed structural verification",
			),
		)
	}
	facts := generation.Facts
	report.State = StateSucceeded
	report.Inspection = nil
	report.Write = &facts
	report.Artifacts = nil
	report.Failure = nil
	return snapshot, report
}

func validFormWriteExpectation(expectation FormWriteExpectation) bool {
	return validFormWriteGeneration(expectationGeneration(expectation))
}

func expectationGeneration(expectation FormWriteExpectation) formWriteGeneration {
	return formWriteGeneration{
		SchemaVersion: formWriteGenerationSchemaVersion,
		OperationID:   expectation.OperationID,
		SourceSHA256:  expectation.Facts.SourceSHA256,
		RequestSHA256: expectation.Facts.RequestSHA256,
		Artifact:      expectation.Artifact,
		Facts:         expectation.Facts,
	}
}

func verifiedWriteState(state WriteOperationState) bool {
	switch state {
	case WriteVerified, WriteRegistered, WriteDeliveryPending, WriteDelivered,
		WriteDeliveryFailed, WriteDeliveryAmbiguous:
		return true
	default:
		return false
	}
}

func failFormWriteReport(report Report, state State, code FailureCode, message string) Report {
	report.State = state
	report.Write = nil
	report.Artifacts = nil
	report.Failure = &Failure{Code: code, Message: message}
	return report
}

func formWriteJournalReport(report Report, err error) Report {
	code := WriteJournalFailureCode(err)
	state := StateFailed
	switch code {
	case FailureCanceled:
		state = StateCanceled
	case FailureRecoveryUncertain:
		state = StateUncertain
	}
	failure := safeFormWriteFailure(code)
	return failFormWriteReport(report, state, failure.Code, failure.Message)
}

func cleanupFormWriteFailure(snapshot *Snapshot, report Report) (*Snapshot, Report) {
	if snapshot == nil {
		return nil, report
	}
	if err := snapshot.Close(); err != nil {
		return nil, failFormWriteReport(
			report,
			StateFailed,
			FailureInternal,
			"protected scratch cleanup failed",
		)
	}
	return nil, report
}
