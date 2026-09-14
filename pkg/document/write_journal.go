package document

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	WriteOperationSchemaVersion    = "mintclaw.document_write_operation.v1"
	PDFCPUWriteBackendGeneration   = "pdfcpu-v0.15.0-mintclaw-write-v1"
	DefaultMaxWriteJournalRecord   = 16 * 1024
	defaultDocumentWriteGeneration = 1
)

var (
	ErrWriteConflict         = errors.New("document write operation conflicts with durable state")
	ErrWriteJournalFailed    = errors.New("document write journal is unavailable")
	ErrWriteJournalUncertain = errors.New("document write journal durability is uncertain")
	opaqueDeliveryID         = regexp.MustCompile(`^delivery_[a-f0-9]{64}$`)
	opaqueOutboxDeliveryID   = regexp.MustCompile(`^out_[a-f0-9]{32}$`)
	opaqueIdempotentMediaRef = regexp.MustCompile(`^media://node-transfer-[a-f0-9]{32}$`)
)

type writeJournalError struct {
	kind error
}

func (err *writeJournalError) Error() string { return err.kind.Error() }
func (err *writeJournalError) Unwrap() error { return err.kind }

func safeWriteJournalError(kind error, internalCause error) error {
	if internalCause == nil {
		return kind
	}
	// Deliberately discard the internal cause. Journal errors can contain host
	// paths or submitted document metadata and may cross an agent boundary.
	return &writeJournalError{kind: kind}
}

type WriteOperationState string

const (
	WriteAccepted          WriteOperationState = "accepted"
	WriteWriting           WriteOperationState = "writing"
	WriteWritten           WriteOperationState = "written"
	WriteVerifying         WriteOperationState = "verifying"
	WriteVerified          WriteOperationState = "verified"
	WriteRegistered        WriteOperationState = "registered"
	WriteDeliveryPending   WriteOperationState = "delivery_pending"
	WriteCanceled          WriteOperationState = "canceled"
	WriteFailed            WriteOperationState = "failed"
	WriteUncertain         WriteOperationState = "uncertain"
	WriteDelivered         WriteOperationState = "delivered"
	WriteDeliveryFailed    WriteOperationState = "delivery_failed"
	WriteDeliveryAmbiguous WriteOperationState = "delivery_ambiguous"
)

type WriteArtifactEvidence struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type WriteVerificationEvidence struct {
	StructuralAssertions int `json:"structural_assertions"`
	VisualAssertions     int `json:"visual_assertions"`
	CheckedFields        int `json:"checked_fields"`
	CheckedWidgets       int `json:"checked_widgets"`
	UnchangedFields      int `json:"unchanged_fields"`
	RenderedPages        int `json:"rendered_pages"`
}

// WriteOperationRecord contains only path-free, value-free durable metadata.
// Raw fill values live only in the active call and one-shot worker request.
type WriteOperationRecord struct {
	SchemaVersion     string                     `json:"schema_version"`
	Revision          uint64                     `json:"revision"`
	OperationID       string                     `json:"operation_id"`
	OwnerSHA256       string                     `json:"owner_sha256"`
	SourceSHA256      string                     `json:"source_sha256"`
	RequestSHA256     string                     `json:"request_sha256"`
	BackendGeneration string                     `json:"backend_generation"`
	OutputGeneration  uint64                     `json:"output_generation"`
	State             WriteOperationState        `json:"state"`
	Artifact          *WriteArtifactEvidence     `json:"artifact,omitempty"`
	Verification      *WriteVerificationEvidence `json:"verification,omitempty"`
	ArtifactRef       string                     `json:"artifact_ref,omitempty"`
	DeliveryID        string                     `json:"delivery_id"`
	OutboxDeliveryID  string                     `json:"outbox_delivery_id,omitempty"`
	FailureCode       FailureCode                `json:"failure_code,omitempty"`
	CreatedAt         time.Time                  `json:"created_at"`
	UpdatedAt         time.Time                  `json:"updated_at"`
}

type WriteTransition struct {
	ExpectedRevision uint64
	State            WriteOperationState
	Artifact         *WriteArtifactEvidence
	Verification     *WriteVerificationEvidence
	ArtifactRef      string
	OutboxDeliveryID string
	FailureCode      FailureCode
}

type WriteRecoveryAction string

const (
	RecoveryResumeWrite         WriteRecoveryAction = "resume_write"
	RecoveryInspectWrite        WriteRecoveryAction = "inspect_write_generation"
	RecoveryResumeVerification  WriteRecoveryAction = "resume_verification"
	RecoveryInspectVerification WriteRecoveryAction = "inspect_verification"
	RecoveryResumeRegistration  WriteRecoveryAction = "resume_registration"
	RecoveryResumeDelivery      WriteRecoveryAction = "resume_delivery"
	RecoveryInspectDelivery     WriteRecoveryAction = "inspect_delivery"
	RecoveryReturnTerminal      WriteRecoveryAction = "return_terminal"
)

type WriteRecovery struct {
	Record WriteOperationRecord
	Action WriteRecoveryAction
}

type WriteJournal struct {
	root      string
	now       func() time.Time
	writeFile func(string, []byte, os.FileMode) error
}

func NewWriteJournal(root string) (*WriteJournal, error) {
	return newWriteJournal(root, time.Now, fileutil.WriteFileAtomic)
}

func newWriteJournal(
	root string,
	now func() time.Time,
	writeFile func(string, []byte, os.FileMode) error,
) (*WriteJournal, error) {
	absRoot, err := prepareWriteJournalRoot(root)
	if err != nil {
		return nil, safeWriteJournalError(ErrWriteJournalFailed, err)
	}
	if now == nil || writeFile == nil {
		return nil, ErrWriteJournalFailed
	}
	return &WriteJournal{root: absRoot, now: now, writeFile: writeFile}, nil
}

func NewWriteOperationID() string {
	return "document_write_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func (journal *WriteJournal) Accept(
	ctx context.Context,
	operationID string,
	owner Authority,
	request NormalizedFillRequest,
) (WriteOperationRecord, bool, error) {
	if journal == nil {
		return WriteOperationRecord{}, false, ErrWriteJournalFailed
	}
	if strings.TrimSpace(operationID) == "" {
		operationID = NewWriteOperationID()
	}
	ownerDigest, err := documentOwnerDigest(owner)
	if err != nil || !validWriteOperationID(operationID) || !validNormalizedFillRequest(request) {
		return WriteOperationRecord{}, false, ErrWriteConflict
	}
	if err = ctx.Err(); err != nil {
		return WriteOperationRecord{}, false, err
	}
	release, err := acquireDocumentJournalFileLock(ctx, journal.lockPath())
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return WriteOperationRecord{}, false, err
		}
		return WriteOperationRecord{}, false, safeWriteJournalError(ErrWriteJournalFailed, err)
	}
	defer release()
	if err = ctx.Err(); err != nil {
		return WriteOperationRecord{}, false, err
	}
	existing, found, err := journal.load(operationID)
	if err != nil {
		return WriteOperationRecord{}, false, err
	}
	if found {
		if existing.OwnerSHA256 != ownerDigest || existing.SourceSHA256 != request.SourceSHA256 ||
			existing.RequestSHA256 != request.RequestSHA256 ||
			existing.BackendGeneration != PDFCPUWriteBackendGeneration {
			return WriteOperationRecord{}, false, ErrWriteConflict
		}
		return existing, false, nil
	}
	now := journal.now().UTC()
	record := WriteOperationRecord{
		SchemaVersion:     WriteOperationSchemaVersion,
		Revision:          1,
		OperationID:       operationID,
		OwnerSHA256:       ownerDigest,
		SourceSHA256:      request.SourceSHA256,
		RequestSHA256:     request.RequestSHA256,
		BackendGeneration: PDFCPUWriteBackendGeneration,
		OutputGeneration:  defaultDocumentWriteGeneration,
		State:             WriteAccepted,
		DeliveryID:        documentDeliveryID(ownerDigest, operationID),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err = journal.save(record); err != nil {
		return WriteOperationRecord{}, false, err
	}
	return record, true, nil
}

func (journal *WriteJournal) Lookup(
	ctx context.Context,
	operationID string,
	owner Authority,
) (WriteOperationRecord, bool, error) {
	if journal == nil {
		return WriteOperationRecord{}, false, ErrWriteJournalFailed
	}
	ownerDigest, err := documentOwnerDigest(owner)
	if err != nil || !validWriteOperationID(operationID) {
		return WriteOperationRecord{}, false, ErrWriteConflict
	}
	if err = ctx.Err(); err != nil {
		return WriteOperationRecord{}, false, err
	}
	release, err := acquireDocumentJournalFileLock(ctx, journal.lockPath())
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return WriteOperationRecord{}, false, err
		}
		return WriteOperationRecord{}, false, safeWriteJournalError(ErrWriteJournalFailed, err)
	}
	defer release()
	record, found, err := journal.load(operationID)
	if err != nil || !found {
		return record, found, err
	}
	if record.OwnerSHA256 != ownerDigest {
		return WriteOperationRecord{}, false, ErrWriteConflict
	}
	return record, true, nil
}

func (journal *WriteJournal) Transition(
	ctx context.Context,
	operationID string,
	owner Authority,
	transition WriteTransition,
) (WriteOperationRecord, bool, error) {
	if journal == nil {
		return WriteOperationRecord{}, false, ErrWriteJournalFailed
	}
	ownerDigest, err := documentOwnerDigest(owner)
	if err != nil || !validWriteOperationID(operationID) || transition.ExpectedRevision == 0 {
		return WriteOperationRecord{}, false, ErrWriteConflict
	}
	if err = ctx.Err(); err != nil {
		return WriteOperationRecord{}, false, err
	}
	release, err := acquireDocumentJournalFileLock(ctx, journal.lockPath())
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return WriteOperationRecord{}, false, err
		}
		return WriteOperationRecord{}, false, safeWriteJournalError(ErrWriteJournalFailed, err)
	}
	defer release()
	record, found, err := journal.load(operationID)
	if err != nil {
		return WriteOperationRecord{}, false, err
	}
	if !found || record.OwnerSHA256 != ownerDigest {
		return WriteOperationRecord{}, false, ErrWriteConflict
	}
	if record.Revision != transition.ExpectedRevision {
		if transitionAlreadyApplied(record, transition) {
			return record, false, nil
		}
		return WriteOperationRecord{}, false, ErrWriteConflict
	}
	next, err := nextWriteOperationRecord(record, transition, journal.now().UTC())
	if err != nil {
		return WriteOperationRecord{}, false, err
	}
	if err = journal.save(next); err != nil {
		return WriteOperationRecord{}, false, err
	}
	return next, true, nil
}

func (journal *WriteJournal) Recover(
	ctx context.Context,
	operationID string,
	owner Authority,
) (WriteRecovery, error) {
	record, found, err := journal.Lookup(ctx, operationID, owner)
	if err != nil {
		return WriteRecovery{}, err
	}
	if !found {
		return WriteRecovery{}, ErrWriteConflict
	}
	actions := map[WriteOperationState]WriteRecoveryAction{
		WriteAccepted:          RecoveryResumeWrite,
		WriteWriting:           RecoveryInspectWrite,
		WriteWritten:           RecoveryResumeVerification,
		WriteVerifying:         RecoveryInspectVerification,
		WriteVerified:          RecoveryResumeRegistration,
		WriteRegistered:        RecoveryResumeDelivery,
		WriteDeliveryPending:   RecoveryInspectDelivery,
		WriteCanceled:          RecoveryReturnTerminal,
		WriteFailed:            RecoveryReturnTerminal,
		WriteUncertain:         RecoveryReturnTerminal,
		WriteDelivered:         RecoveryReturnTerminal,
		WriteDeliveryFailed:    RecoveryReturnTerminal,
		WriteDeliveryAmbiguous: RecoveryReturnTerminal,
	}
	action := actions[record.State]
	if action == "" {
		return WriteRecovery{}, safeWriteJournalError(
			ErrWriteJournalFailed,
			errors.New("invalid recovery state"),
		)
	}
	return WriteRecovery{Record: record, Action: action}, nil
}

func (journal *WriteJournal) save(record WriteOperationRecord) error {
	if !validWriteOperationRecord(record) {
		return safeWriteJournalError(ErrWriteJournalFailed, errors.New("invalid document write operation"))
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil || len(data)+1 > DefaultMaxWriteJournalRecord {
		return safeWriteJournalError(
			ErrWriteJournalFailed,
			errors.New("document write journal record exceeds limit"),
		)
	}
	data = append(data, '\n')
	path := journal.recordPath(record.OperationID)
	err = journal.writeFile(path, data, 0o600)
	committed := fileutil.IsCommittedWriteError(err)
	if err != nil && !committed {
		return safeWriteJournalError(ErrWriteJournalFailed, err)
	}
	if securityErr := secureDocumentJournalRecord(path); securityErr != nil {
		return safeWriteJournalError(ErrWriteJournalUncertain, securityErr)
	}
	if committed {
		return safeWriteJournalError(ErrWriteJournalUncertain, err)
	}
	return nil
}

func (journal *WriteJournal) load(operationID string) (WriteOperationRecord, bool, error) {
	path := journal.recordPath(operationID)
	file, err := openSourceNoFollow(path)
	if errors.Is(err, os.ErrNotExist) {
		return WriteOperationRecord{}, false, nil
	}
	if err != nil {
		return WriteOperationRecord{}, false, safeWriteJournalError(ErrWriteJournalFailed, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() ||
		info.Size() <= 0 || info.Size() > DefaultMaxWriteJournalRecord {
		return WriteOperationRecord{}, false, safeWriteJournalError(
			ErrWriteJournalFailed,
			errors.New("document write journal record is unsafe"),
		)
	}
	if err = validateDocumentJournalRecordSecurity(path, file, info); err != nil {
		return WriteOperationRecord{}, false, safeWriteJournalError(ErrWriteJournalFailed, err)
	}
	data, err := io.ReadAll(io.LimitReader(file, DefaultMaxWriteJournalRecord+1))
	if err != nil || len(data) > DefaultMaxWriteJournalRecord {
		return WriteOperationRecord{}, false, safeWriteJournalError(
			ErrWriteJournalFailed,
			errors.New("read journal record"),
		)
	}
	var record WriteOperationRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&record); err != nil {
		return WriteOperationRecord{}, false, safeWriteJournalError(
			ErrWriteJournalFailed,
			errors.New("decode journal record"),
		)
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) || !validWriteOperationRecord(record) ||
		record.OperationID != operationID {
		return WriteOperationRecord{}, false, safeWriteJournalError(
			ErrWriteJournalFailed,
			errors.New("invalid journal record"),
		)
	}
	return record, true, nil
}

func (journal *WriteJournal) lockPath() string {
	return filepath.Join(journal.root, ".write-journal.lock")
}

func (journal *WriteJournal) recordPath(operationID string) string {
	return filepath.Join(journal.root, operationID+".json")
}

func prepareWriteJournalRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("document write journal root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absRoot)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.MkdirAll(absRoot, 0o700); err != nil {
			return "", err
		}
		info, err = os.Lstat(absRoot)
	}
	if err != nil || info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("document write journal root must be a direct directory")
	}
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", err
	}
	if err = secureDocumentJournalRoot(resolvedRoot); err != nil {
		return "", err
	}
	return resolvedRoot, nil
}

func documentOwnerDigest(owner Authority) (string, error) {
	if !validDocumentAuthority(owner) {
		return "", errors.New("document owner scope is invalid")
	}
	data, err := json.Marshal(owner)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("mintclaw.document.owner.v1\x00"), data...))
	return hex.EncodeToString(digest[:]), nil
}

func validDocumentAuthority(owner Authority) bool {
	if !validJournalText(owner.Kind, 64, false) {
		return false
	}
	for _, value := range []string{
		owner.WorkspaceID,
		owner.AgentID,
		owner.ActorID,
		owner.RouteID,
		owner.SessionID,
	} {
		if !validJournalText(value, 256, true) {
			return false
		}
	}
	return true
}

func validJournalText(value string, maximum int, empty bool) bool {
	if (!empty && value == "") || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func documentDeliveryID(ownerDigest, operationID string) string {
	digest := sha256.Sum256([]byte("mintclaw.document.delivery.v1\x00" + ownerDigest + "\x00" + operationID))
	return "delivery_" + hex.EncodeToString(digest[:])
}

func nextWriteOperationRecord(
	record WriteOperationRecord,
	transition WriteTransition,
	now time.Time,
) (WriteOperationRecord, error) {
	if !writeTransitionAllowed(record.State, transition.State) || !validWriteTransition(record, transition) {
		return WriteOperationRecord{}, ErrWriteConflict
	}
	next := record
	next.Revision++
	next.State = transition.State
	if !now.After(record.UpdatedAt) {
		now = record.UpdatedAt.Add(time.Nanosecond)
	}
	next.UpdatedAt = now
	if transition.Artifact != nil {
		artifact := *transition.Artifact
		next.Artifact = &artifact
	}
	if transition.Verification != nil {
		verification := *transition.Verification
		next.Verification = &verification
	}
	if transition.ArtifactRef != "" {
		next.ArtifactRef = transition.ArtifactRef
	}
	if transition.OutboxDeliveryID != "" {
		next.OutboxDeliveryID = transition.OutboxDeliveryID
	}
	next.FailureCode = transition.FailureCode
	if !validWriteOperationRecord(next) {
		return WriteOperationRecord{}, ErrWriteConflict
	}
	return next, nil
}

func writeTransitionAllowed(from, to WriteOperationState) bool {
	if to == WriteCanceled || to == WriteFailed || to == WriteUncertain {
		return from == WriteAccepted || from == WriteWriting || from == WriteWritten || from == WriteVerifying ||
			from == WriteVerified
	}
	allowed := map[WriteOperationState]WriteOperationState{
		WriteAccepted:        WriteWriting,
		WriteWriting:         WriteWritten,
		WriteWritten:         WriteVerifying,
		WriteVerifying:       WriteVerified,
		WriteVerified:        WriteRegistered,
		WriteRegistered:      WriteDeliveryPending,
		WriteDeliveryPending: WriteDelivered,
	}
	if allowed[from] == to {
		return true
	}
	return from == WriteDeliveryPending && (to == WriteDeliveryFailed || to == WriteDeliveryAmbiguous)
}

func validWriteTransition(record WriteOperationRecord, transition WriteTransition) bool {
	if !validWriteTransitionPayload(transition) {
		return false
	}
	switch transition.State {
	case WriteWritten:
		return record.Artifact == nil
	case WriteVerified:
		return record.Artifact != nil && record.Verification == nil
	case WriteRegistered:
		return record.Verification != nil && record.ArtifactRef == ""
	default:
		return true
	}
}

func validWriteTransitionPayload(transition WriteTransition) bool {
	artifact := transition.Artifact != nil
	verification := transition.Verification != nil
	artifactRef := transition.ArtifactRef != ""
	outboxDelivery := transition.OutboxDeliveryID != ""
	failure := transition.FailureCode != ""
	switch transition.State {
	case WriteWritten:
		return artifact && !verification && !artifactRef && !outboxDelivery && !failure
	case WriteVerified:
		return !artifact && verification && !artifactRef && !outboxDelivery && !failure
	case WriteRegistered:
		return !artifact && !verification && artifactRef && !outboxDelivery && !failure &&
			validDurableArtifactRef(transition.ArtifactRef)
	case WriteDeliveryPending:
		return !artifact && !verification && !artifactRef && !failure &&
			opaqueOutboxDeliveryID.MatchString(transition.OutboxDeliveryID)
	case WriteCanceled:
		return !artifact && !verification && !artifactRef && !outboxDelivery &&
			transition.FailureCode == FailureCanceled
	case WriteFailed:
		return !artifact && !verification && !artifactRef && !outboxDelivery &&
			validWriteTerminalFailure(transition.FailureCode)
	case WriteUncertain:
		return !artifact && !verification && !artifactRef && !outboxDelivery &&
			transition.FailureCode == FailureRecoveryUncertain
	case WriteDeliveryFailed:
		return !artifact && !verification && !artifactRef && !outboxDelivery &&
			transition.FailureCode == FailureDeliveryFailed
	case WriteDeliveryAmbiguous:
		return !artifact && !verification && !artifactRef && !outboxDelivery &&
			transition.FailureCode == FailureDeliveryAmbiguous
	default:
		return !artifact && !verification && !artifactRef && !outboxDelivery && !failure
	}
}

func transitionAlreadyApplied(record WriteOperationRecord, transition WriteTransition) bool {
	if record.Revision != transition.ExpectedRevision+1 || record.State != transition.State ||
		record.FailureCode != transition.FailureCode || !validWriteTransitionPayload(transition) {
		return false
	}
	switch transition.State {
	case WriteWritten:
		return record.Artifact != nil && *record.Artifact == *transition.Artifact
	case WriteVerified:
		return record.Verification != nil && *record.Verification == *transition.Verification
	case WriteRegistered:
		return record.ArtifactRef == transition.ArtifactRef
	case WriteDeliveryPending:
		return record.OutboxDeliveryID == transition.OutboxDeliveryID
	default:
		return true
	}
}

func validWriteOperationRecord(record WriteOperationRecord) bool {
	if record.SchemaVersion != WriteOperationSchemaVersion || record.Revision == 0 ||
		!validWriteOperationID(record.OperationID) || !validDocumentDigest(record.OwnerSHA256) ||
		!validDocumentDigest(record.SourceSHA256) || !validDocumentDigest(record.RequestSHA256) ||
		record.BackendGeneration != PDFCPUWriteBackendGeneration ||
		record.OutputGeneration != defaultDocumentWriteGeneration || !opaqueDeliveryID.MatchString(record.DeliveryID) ||
		record.DeliveryID != documentDeliveryID(record.OwnerSHA256, record.OperationID) ||
		record.CreatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) || !validWriteState(record.State) {
		return false
	}
	if record.Artifact != nil && (!validDocumentDigest(record.Artifact.SHA256) || record.Artifact.Size <= 0 ||
		record.Artifact.Size > DefaultMaxArtifactBytes) {
		return false
	}
	if record.Verification != nil && !validWriteVerification(*record.Verification) {
		return false
	}
	if (record.Verification != nil && record.Artifact == nil) ||
		(record.ArtifactRef != "" && record.Verification == nil) {
		return false
	}
	if record.ArtifactRef != "" && !validDurableArtifactRef(record.ArtifactRef) {
		return false
	}
	if record.OutboxDeliveryID != "" && !opaqueOutboxDeliveryID.MatchString(record.OutboxDeliveryID) {
		return false
	}
	return validWriteStateEvidence(record)
}

func validWriteState(state WriteOperationState) bool {
	switch state {
	case WriteAccepted, WriteWriting, WriteWritten, WriteVerifying, WriteVerified, WriteRegistered,
		WriteDeliveryPending, WriteCanceled, WriteFailed, WriteUncertain, WriteDelivered,
		WriteDeliveryFailed, WriteDeliveryAmbiguous:
		return true
	default:
		return false
	}
}

func validWriteStateEvidence(record WriteOperationRecord) bool {
	if record.State == WriteAccepted || record.State == WriteWriting {
		return record.Artifact == nil && record.Verification == nil && record.ArtifactRef == "" &&
			record.OutboxDeliveryID == "" && record.FailureCode == ""
	}
	if record.State == WriteWritten || record.State == WriteVerifying {
		return record.Artifact != nil && record.Verification == nil && record.ArtifactRef == "" &&
			record.OutboxDeliveryID == "" && record.FailureCode == ""
	}
	if record.State == WriteVerified {
		return record.Artifact != nil && record.Verification != nil && record.ArtifactRef == "" &&
			record.OutboxDeliveryID == "" && record.FailureCode == ""
	}
	if record.State == WriteRegistered {
		return record.Artifact != nil && record.Verification != nil && record.ArtifactRef != "" &&
			record.OutboxDeliveryID == "" && record.FailureCode == ""
	}
	if record.State == WriteDeliveryPending || record.State == WriteDelivered {
		return record.Artifact != nil && record.Verification != nil && record.ArtifactRef != "" &&
			opaqueOutboxDeliveryID.MatchString(record.OutboxDeliveryID) && record.FailureCode == ""
	}
	switch record.State {
	case WriteCanceled:
		return record.OutboxDeliveryID == "" && record.FailureCode == FailureCanceled
	case WriteFailed:
		return record.OutboxDeliveryID == "" && validWriteTerminalFailure(record.FailureCode)
	case WriteUncertain:
		return record.OutboxDeliveryID == "" && record.FailureCode == FailureRecoveryUncertain
	case WriteDeliveryFailed:
		return record.Artifact != nil && record.Verification != nil && record.ArtifactRef != "" &&
			opaqueOutboxDeliveryID.MatchString(record.OutboxDeliveryID) &&
			record.FailureCode == FailureDeliveryFailed
	case WriteDeliveryAmbiguous:
		return record.Artifact != nil && record.Verification != nil && record.ArtifactRef != "" &&
			opaqueOutboxDeliveryID.MatchString(record.OutboxDeliveryID) &&
			record.FailureCode == FailureDeliveryAmbiguous
	default:
		return false
	}
}

func validWriteTerminalFailure(code FailureCode) bool {
	switch code {
	case FailureUnsupportedPlatform, FailureWorkerUnavailable, FailureWorkerProtocol, FailureWorkerCrashed,
		FailureWorkerOutputLimit, FailureWorkerTimeout, FailureWorkerInputMismatch, FailureMalformedPDF,
		FailurePasswordRequired, FailureInspectionLimit, FailureRenderLimit, FailureLimitExceeded,
		FailureArtifactInvalid, FailureArtifactRegistration, FailureUnsupportedFeature,
		FailureBackendUnavailable, FailureFormNotPresent, FailureFormUnsupported, FailureFieldUnsupported, FailureFieldNotFound,
		FailureFieldAmbiguous, FailureFieldReadOnly, FailureFieldValueInvalid, FailureChoiceInvalid,
		FailureWriteFailed, FailureAppearanceUnavailable, FailureAppearanceStale, FailureContentClipped,
		FailureVerificationStructural, FailureVerificationVisual, FailureInternal:
		return true
	default:
		return false
	}
}

func validWriteVerification(value WriteVerificationEvidence) bool {
	return value.StructuralAssertions > 0 && value.VisualAssertions > 0 && value.CheckedFields > 0 &&
		value.CheckedWidgets > 0 && value.UnchangedFields >= 0 && value.RenderedPages > 0
}

func validDurableArtifactRef(value string) bool {
	if opaqueIdempotentMediaRef.MatchString(value) {
		return true
	}
	const prefix = "media://"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	id := strings.TrimPrefix(value, prefix)
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.Version() == 4 && parsed.Variant() == uuid.RFC4122 && parsed.String() == id
}

func validWriteOperationID(value string) bool {
	const prefix = "document_write_"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	id := strings.TrimPrefix(value, prefix)
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.Version() == 4 && parsed.Variant() == uuid.RFC4122 &&
		strings.ReplaceAll(parsed.String(), "-", "") == id
}

func WriteJournalFailureCode(err error) FailureCode {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return FailureCanceled
	case errors.Is(err, ErrWriteConflict):
		return FailureWriteConflict
	case errors.Is(err, ErrWriteJournalUncertain):
		return FailureRecoveryUncertain
	case errors.Is(err, ErrWriteJournalFailed):
		return FailureJournalFailed
	default:
		return FailureInternal
	}
}
