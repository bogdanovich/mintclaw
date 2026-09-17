package document

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	formJobStoreFileName = "form_jobs.v1.json"
	formJobStoreVersion  = 1
)

type FormJobStoreOptions struct {
	StateRoot         string
	KeyRoot           string
	Retention         time.Duration
	TerminalRetention time.Duration
	MaxJobs           int
	MaxEventsPerJob   int
	MaxBytes          int
}

type formJobSourceSecret struct {
	SourceRef string `json:"source_ref"`
}

type formJobValuePayload struct {
	EventID           string             `json:"event_id"`
	FieldID           string             `json:"field_id"`
	Revision          int64              `json:"revision"`
	Value             FormProtectedValue `json:"value"`
	State             FormValueState     `json:"state"`
	Source            FormValueSource    `json:"source"`
	BlankReason       string             `json:"blank_reason,omitempty"`
	ValidationCode    string             `json:"validation_code,omitempty"`
	SupersedesEventID string             `json:"supersedes_event_id,omitempty"`
	IdempotencyDigest string             `json:"idempotency_digest"`
	PreviousDigest    string             `json:"previous_digest"`
	CreatedAt         int64              `json:"created_at"`
}

type formJobStoredRecord struct {
	Public          FormJobRecord      `json:"public"`
	WrappedKey      *formJobWrappedKey `json:"wrapped_key,omitempty"`
	Source          *formJobEnvelope   `json:"source,omitempty"`
	Events          []formJobEnvelope  `json:"events,omitempty"`
	IntegrityDigest string             `json:"integrity_digest"`
}

type formJobStoreDocument struct {
	Version int                            `json:"version"`
	Records map[string]formJobStoredRecord `json:"records"`
}

type FormJobStore struct {
	stateRoot         string
	keyRoot           string
	directory         string
	path              string
	lockPath          string
	retention         time.Duration
	terminalRetention time.Duration
	maxJobs           int
	maxEventsPerJob   int
	maxBytes          int
	now               func() time.Time
	random            io.Reader
	writeFile         func(string, []byte, os.FileMode) error
	profileKey        []byte

	mu sync.Mutex
}

func OpenFormJobStore(options FormJobStoreOptions) (*FormJobStore, error) {
	options, err := normalizeFormJobStoreOptions(options)
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(options.StateRoot, "document_form_jobs")
	if err := fileutil.MkdirAllDurable(options.StateRoot, "document_form_jobs", 0o700); err != nil {
		return nil, fmt.Errorf("%w: create state directory: %w", ErrFormJobStoreUnavailable, err)
	}
	if err := ensureProtectedDirectory(directory); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrFormJobStoreUnavailable, err)
	}
	if err := ensureProtectedDirectory(options.KeyRoot); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrFormJobKeyUnavailable, err)
	}
	store := &FormJobStore{
		stateRoot:         options.StateRoot,
		keyRoot:           options.KeyRoot,
		directory:         directory,
		path:              filepath.Join(directory, formJobStoreFileName),
		lockPath:          filepath.Join(directory, formJobStoreFileName+".lock"),
		retention:         options.Retention,
		terminalRetention: options.TerminalRetention,
		maxJobs:           options.MaxJobs,
		maxEventsPerJob:   options.MaxEventsPerJob,
		maxBytes:          options.MaxBytes,
		now:               time.Now,
		random:            rand.Reader,
		writeFile:         fileutil.WriteFileAtomic,
	}
	if err := store.initialize(context.Background()); err != nil {
		return nil, err
	}
	return store, nil
}

func normalizeFormJobStoreOptions(options FormJobStoreOptions) (FormJobStoreOptions, error) {
	options.StateRoot = filepath.Clean(strings.TrimSpace(options.StateRoot))
	options.KeyRoot = filepath.Clean(strings.TrimSpace(options.KeyRoot))
	if options.StateRoot == "." || options.KeyRoot == "." || options.StateRoot == string(filepath.Separator) ||
		options.KeyRoot == string(filepath.Separator) {
		return FormJobStoreOptions{}, errors.New("document form job state and key roots are required")
	}
	for _, root := range []string{options.StateRoot, options.KeyRoot} {
		info, err := os.Lstat(root)
		if err != nil {
			return FormJobStoreOptions{}, fmt.Errorf("inspect document form job root: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return FormJobStoreOptions{}, errors.New("document form job root must be a real directory")
		}
	}
	if options.Retention == 0 {
		options.Retention = DefaultFormJobRetention
	}
	if options.TerminalRetention == 0 {
		options.TerminalRetention = DefaultFormJobTerminalRetention
	}
	if options.Retention <= 0 || options.Retention > 30*24*time.Hour ||
		options.TerminalRetention <= 0 || options.TerminalRetention > options.Retention {
		return FormJobStoreOptions{}, errors.New("document form job retention is invalid")
	}
	if options.MaxJobs == 0 {
		options.MaxJobs = DefaultFormJobMaxJobs
	}
	if options.MaxEventsPerJob == 0 {
		options.MaxEventsPerJob = DefaultFormJobMaxEvents
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = DefaultFormJobMaxBytes
	}
	if options.MaxJobs < 1 || options.MaxJobs > 4096 || options.MaxEventsPerJob < 1 ||
		options.MaxEventsPerJob > 4096 || options.MaxBytes < 4096 || options.MaxBytes > 64*1024*1024 {
		return FormJobStoreOptions{}, errors.New("document form job store limits are invalid")
	}
	return options, nil
}

func ensureProtectedDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("protected directory is not a real directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure protected directory: %w", err)
	}
	return nil
}

func (store *FormJobStore) initialize(ctx context.Context) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	release, err := acquireDocumentJournalFileLock(ctx, store.lockPath)
	if err != nil {
		return fmt.Errorf("%w: acquire store lock: %w", ErrFormJobStoreUnavailable, err)
	}
	defer release()
	key, err := loadOrCreateFormJobKey(ctx, store.keyRoot, store.random)
	if err != nil {
		return err
	}
	store.profileKey = key
	document, err := store.loadLocked()
	if err != nil {
		clear(store.profileKey)
		store.profileKey = nil
		return err
	}
	changed := store.expireAndPruneLocked(&document, store.now().UTC())
	if changed {
		if err := store.saveLocked(document); err != nil {
			clear(store.profileKey)
			store.profileKey = nil
			return err
		}
	}
	return nil
}

func (store *FormJobStore) Close() {
	if store == nil {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	clear(store.profileKey)
	store.profileKey = nil
}

func (store *FormJobStore) Create(ctx context.Context, request FormJobCreateRequest) (FormJobRecord, error) {
	if err := validateFormJobCreateRequest(request, store.retention); err != nil {
		return FormJobRecord{}, err
	}
	var created FormJobRecord
	err := store.update(ctx, func(document *formJobStoreDocument, now time.Time) (bool, error) {
		ownerDigest, err := store.ownerDigest(request.Owner)
		if err != nil {
			return false, err
		}
		startDigest := keyedDigest(store.profileKey, "start", ownerDigest, request.StartIdempotencyKey)
		for _, existing := range document.Records {
			if existing.Public.OwnerDigest != ownerDigest || existing.Public.StartDigest != startDigest {
				continue
			}
			if existing.Public.SourceDigest != request.SourceDigest ||
				existing.Public.FieldSchemaDigest != request.FieldSchemaDigest ||
				existing.Public.BackendRevision != request.BackendRevision ||
				existing.Public.AuditPolicyRevision != request.AuditPolicyRevision {
				return false, ErrFormJobConflict
			}
			created = cloneFormJobRecord(existing.Public)
			return false, nil
		}
		if len(document.Records) >= store.maxJobs {
			return false, ErrFormJobCapacityExceeded
		}
		jobID, err := randomFormJobID(store.random)
		if err != nil {
			return false, fmt.Errorf("generate document form job identity: %w", err)
		}
		jobKey, err := newFormJobDataKey(store.random)
		if err != nil {
			return false, fmt.Errorf("generate document form job data key: %w", err)
		}
		defer clear(jobKey)
		wrapped, err := wrapFormJobKey(store.profileKey, jobKey, jobID, ownerDigest, store.random)
		if err != nil {
			return false, fmt.Errorf("wrap document form job data key: %w", err)
		}
		nowMillis := now.UnixMilli()
		retention := request.Retention
		if retention == 0 {
			retention = store.retention
		}
		public := FormJobRecord{
			SchemaVersion:       FormJobSnapshotVersion,
			JobID:               jobID,
			State:               FormJobPrepared,
			Revision:            1,
			OwnerDigest:         ownerDigest,
			StartDigest:         startDigest,
			SourceDigest:        strings.TrimSpace(request.SourceDigest),
			FieldSchemaDigest:   strings.TrimSpace(request.FieldSchemaDigest),
			BackendRevision:     strings.TrimSpace(request.BackendRevision),
			AuditPolicyRevision: strings.TrimSpace(request.AuditPolicyRevision),
			CreatedAt:           nowMillis,
			UpdatedAt:           nowMillis,
			ExpiresAt:           now.Add(retention).UnixMilli(),
		}
		source, err := sealFormJobEnvelope(jobKey, formJobEnvelope{
			Kind:     "source",
			JobID:    jobID,
			Revision: 1,
		}, formJobSourceSecret{SourceRef: strings.TrimSpace(request.SourceRef)}, store.random)
		if err != nil {
			return false, fmt.Errorf("seal document form source: %w", err)
		}
		document.Records[jobID] = formJobStoredRecord{
			Public:     public,
			WrappedKey: &wrapped,
			Source:     &source,
		}
		created = cloneFormJobRecord(public)
		return true, nil
	})
	return created, err
}

func (store *FormJobStore) Get(
	ctx context.Context,
	jobID string,
	owner FormJobOwner,
) (FormJobRecord, error) {
	var result FormJobRecord
	err := store.update(ctx, func(document *formJobStoreDocument, _ time.Time) (bool, error) {
		record, jobKey, err := store.authorizedRecord(document, jobID, owner)
		if err != nil {
			return false, err
		}
		clear(jobKey)
		result = cloneFormJobRecord(record.Public)
		return false, nil
	})
	return result, err
}

func (store *FormJobStore) SourceRef(
	ctx context.Context,
	jobID string,
	owner FormJobOwner,
) (string, error) {
	var sourceRef string
	err := store.update(ctx, func(document *formJobStoreDocument, _ time.Time) (bool, error) {
		record, jobKey, err := store.authorizedRecord(document, jobID, owner)
		if err != nil {
			return false, err
		}
		defer clear(jobKey)
		if record.Public.State.terminal() || record.Source == nil {
			if record.Public.State == FormJobExpired {
				return false, ErrFormJobExpired
			}
			return false, ErrFormJobTerminal
		}
		var source formJobSourceSecret
		if err := openFormJobEnvelope(jobKey, *record.Source, &source); err != nil {
			return false, err
		}
		sourceRef = source.SourceRef
		return false, nil
	})
	return sourceRef, err
}

func (store *FormJobStore) AppendValue(
	ctx context.Context,
	request FormJobAppendValueRequest,
) (FormJobRecord, FormJobValueEvent, error) {
	if err := validateFormJobAppendValueRequest(request); err != nil {
		return FormJobRecord{}, FormJobValueEvent{}, err
	}
	var result FormJobRecord
	var eventResult FormJobValueEvent
	err := store.update(ctx, func(document *formJobStoreDocument, now time.Time) (bool, error) {
		record, jobKey, err := store.authorizedRecord(document, request.JobID, request.Owner)
		if err != nil {
			return false, err
		}
		defer clear(jobKey)
		idempotencyDigest := keyedDigest(
			store.profileKey,
			"answer",
			record.Public.JobID,
			request.IdempotencyKey,
		)
		eventID := "form_value_" + idempotencyDigest[:32]
		bindingDigest, err := formJobJSONKeyedDigest(store.profileKey, struct {
			FieldID           string
			Value             FormProtectedValue
			State             FormValueState
			Source            FormValueSource
			BlankReason       string
			ValidationCode    string
			SupersedesEventID string
		}{
			FieldID:           request.FieldID,
			Value:             request.Value,
			State:             request.State,
			Source:            request.Source,
			BlankReason:       request.BlankReason,
			ValidationCode:    request.ValidationCode,
			SupersedesEventID: request.SupersedesEventID,
		})
		if err != nil {
			return false, err
		}
		for _, envelope := range record.Events {
			if envelope.EventID != eventID {
				continue
			}
			if envelope.BindingDigest != bindingDigest {
				return false, ErrFormJobAnswerConflict
			}
			payload, payloadErr := openFormJobValuePayload(jobKey, envelope)
			if payloadErr != nil {
				return false, payloadErr
			}
			result = cloneFormJobRecord(record.Public)
			eventResult = publicFormJobValueEvent(payload)
			return false, nil
		}
		if record.Public.State == FormJobExpired {
			return false, ErrFormJobExpired
		}
		if record.Public.State.terminal() {
			return false, ErrFormJobTerminal
		}
		if record.Public.State != FormJobPrepared && record.Public.State != FormJobCollecting &&
			record.Public.State != FormJobReviewReady {
			return false, ErrFormJobConflict
		}
		if request.ExpectedRevision != record.Public.Revision {
			return false, ErrFormJobConflict
		}
		if len(record.Events) >= store.maxEventsPerJob {
			return false, ErrFormJobCapacityExceeded
		}
		currentIndex := slices.IndexFunc(record.Public.Fields, func(field FormJobFieldState) bool {
			return field.FieldID == request.FieldID
		})
		if request.SupersedesEventID != "" {
			if currentIndex < 0 || record.Public.Fields[currentIndex].EventID != request.SupersedesEventID {
				return false, ErrFormJobConflict
			}
		} else if currentIndex >= 0 {
			return false, ErrFormJobConflict
		}
		previousDigest := record.Public.LedgerDigest
		nextRevision := record.Public.Revision + 1
		payload := formJobValuePayload{
			EventID:           eventID,
			FieldID:           request.FieldID,
			Revision:          nextRevision,
			Value:             cloneProtectedValue(request.Value),
			State:             request.State,
			Source:            request.Source,
			BlankReason:       strings.TrimSpace(request.BlankReason),
			ValidationCode:    strings.TrimSpace(request.ValidationCode),
			SupersedesEventID: strings.TrimSpace(request.SupersedesEventID),
			IdempotencyDigest: idempotencyDigest,
			PreviousDigest:    previousDigest,
			CreatedAt:         now.UnixMilli(),
		}
		envelope, err := sealFormJobEnvelope(jobKey, formJobEnvelope{
			Kind:          "value",
			JobID:         record.Public.JobID,
			FieldID:       request.FieldID,
			EventID:       eventID,
			Revision:      nextRevision,
			BindingDigest: bindingDigest,
		}, payload, store.random)
		if err != nil {
			return false, fmt.Errorf("seal document form value: %w", err)
		}
		record.Events = append(record.Events, envelope)
		record.Public.State = FormJobCollecting
		record.Public.Revision = nextRevision
		record.Public.LedgerRevision++
		record.Public.LedgerDigest, err = formJobJSONDigest(envelope)
		if err != nil {
			return false, err
		}
		record.Public.UpdatedAt = now.UnixMilli()
		fieldState := FormJobFieldState{
			FieldID:        request.FieldID,
			EventID:        eventID,
			ValueKind:      request.Value.Kind,
			State:          request.State,
			Source:         request.Source,
			BlankReason:    strings.TrimSpace(request.BlankReason),
			ValidationCode: strings.TrimSpace(request.ValidationCode),
			UpdatedAt:      now.UnixMilli(),
		}
		if currentIndex >= 0 {
			record.Public.Fields[currentIndex] = fieldState
		} else {
			record.Public.Fields = append(record.Public.Fields, fieldState)
			slices.SortFunc(record.Public.Fields, func(a, b FormJobFieldState) int {
				return cmp.Compare(a.FieldID, b.FieldID)
			})
		}
		document.Records[record.Public.JobID] = record
		result = cloneFormJobRecord(record.Public)
		eventResult = publicFormJobValueEvent(payload)
		return true, nil
	})
	return result, eventResult, err
}

func (store *FormJobStore) ReadValues(
	ctx context.Context,
	jobID string,
	owner FormJobOwner,
	fieldIDs []string,
) (map[string]FormJobValueEvent, error) {
	if len(fieldIDs) == 0 || len(fieldIDs) > 128 {
		return nil, errors.New("document protected form field selection is invalid")
	}
	wanted := make(map[string]struct{}, len(fieldIDs))
	for _, fieldID := range fieldIDs {
		fieldID = strings.TrimSpace(fieldID)
		if fieldID == "" || len(fieldID) > maxFormJobFieldIDLength {
			return nil, errors.New("document protected form field selection is invalid")
		}
		wanted[fieldID] = struct{}{}
	}
	result := make(map[string]FormJobValueEvent, len(wanted))
	err := store.update(ctx, func(document *formJobStoreDocument, _ time.Time) (bool, error) {
		record, jobKey, err := store.authorizedRecord(document, jobID, owner)
		if err != nil {
			return false, err
		}
		defer clear(jobKey)
		if record.Public.State.terminal() {
			if record.Public.State == FormJobExpired {
				return false, ErrFormJobExpired
			}
			return false, ErrFormJobTerminal
		}
		current := make(map[string]string, len(record.Public.Fields))
		for _, field := range record.Public.Fields {
			if _, selected := wanted[field.FieldID]; selected {
				current[field.FieldID] = field.EventID
			}
		}
		for _, envelope := range record.Events {
			if current[envelope.FieldID] != envelope.EventID {
				continue
			}
			payload, err := openFormJobValuePayload(jobKey, envelope)
			if err != nil {
				return false, err
			}
			result[payload.FieldID] = publicFormJobValueEvent(payload)
		}
		if len(result) != len(wanted) {
			return false, ErrFormJobNotFound
		}
		return false, nil
	})
	return result, err
}

func (store *FormJobStore) Cancel(
	ctx context.Context,
	jobID string,
	expectedRevision int64,
	owner FormJobOwner,
) (FormJobRecord, error) {
	return store.erase(ctx, jobID, expectedRevision, owner, FormJobCanceled, "")
}

func (store *FormJobStore) Delete(
	ctx context.Context,
	jobID string,
	expectedRevision int64,
	owner FormJobOwner,
) (FormJobRecord, error) {
	return store.erase(ctx, jobID, expectedRevision, owner, FormJobDeleted, "")
}

func (store *FormJobStore) Prune(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		now = store.now().UTC()
	}
	return store.updateAt(ctx, now.UTC(), func(_ *formJobStoreDocument, _ time.Time) (bool, error) {
		return false, nil
	})
}

func (store *FormJobStore) erase(
	ctx context.Context,
	jobID string,
	expectedRevision int64,
	owner FormJobOwner,
	state FormJobState,
	failureCode string,
) (FormJobRecord, error) {
	var result FormJobRecord
	err := store.update(ctx, func(document *formJobStoreDocument, now time.Time) (bool, error) {
		record, jobKey, err := store.authorizedRecord(document, jobID, owner)
		if err != nil {
			return false, err
		}
		clear(jobKey)
		if record.Public.Revision != expectedRevision {
			return false, ErrFormJobConflict
		}
		if record.Public.State.terminal() {
			if record.Public.State == state {
				result = cloneFormJobRecord(record.Public)
				return false, nil
			}
			return false, ErrFormJobTerminal
		}
		store.eraseStoredRecord(&record, state, failureCode, now)
		document.Records[jobID] = record
		result = cloneFormJobRecord(record.Public)
		return true, nil
	})
	return result, err
}

func (store *FormJobStore) eraseStoredRecord(
	record *formJobStoredRecord,
	state FormJobState,
	failureCode string,
	now time.Time,
) {
	record.WrappedKey = nil
	record.Source = nil
	clear(record.Events)
	record.Events = nil
	record.Public.Fields = nil
	record.Public.LedgerDigest = ""
	record.Public.State = state
	record.Public.Revision++
	record.Public.UpdatedAt = now.UnixMilli()
	record.Public.TerminalAt = now.UnixMilli()
	record.Public.CleanupAfter = now.Add(store.terminalRetention).UnixMilli()
	record.Public.FailureCode = strings.TrimSpace(failureCode)
}

func (store *FormJobStore) update(
	ctx context.Context,
	mutation func(*formJobStoreDocument, time.Time) (bool, error),
) error {
	return store.updateAt(ctx, store.now().UTC(), mutation)
}

func (store *FormJobStore) updateAt(
	ctx context.Context,
	now time.Time,
	mutation func(*formJobStoreDocument, time.Time) (bool, error),
) error {
	if store == nil {
		return ErrFormJobStoreUnavailable
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.profileKey) != 32 {
		return ErrFormJobKeyUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	release, err := acquireDocumentJournalFileLock(ctx, store.lockPath)
	if err != nil {
		return fmt.Errorf("%w: acquire store lock: %w", ErrFormJobStoreUnavailable, err)
	}
	defer release()
	document, err := store.loadLocked()
	if err != nil {
		return err
	}
	changed := store.expireAndPruneLocked(&document, now)
	mutationChanged, err := mutation(&document, now)
	if err != nil {
		if changed {
			if saveErr := store.saveLocked(document); saveErr != nil {
				return errors.Join(err, saveErr)
			}
		}
		return err
	}
	if changed || mutationChanged {
		return store.saveLocked(document)
	}
	return nil
}

func (store *FormJobStore) ownerDigest(owner FormJobOwner) (string, error) {
	canonical, err := owner.canonical()
	if err != nil {
		return "", err
	}
	return keyedDigest(store.profileKey, "owner", canonical), nil
}

func (store *FormJobStore) authorizedRecord(
	document *formJobStoreDocument,
	jobID string,
	owner FormJobOwner,
) (formJobStoredRecord, []byte, error) {
	jobID = strings.TrimSpace(jobID)
	record, found := document.Records[jobID]
	if !found {
		return formJobStoredRecord{}, nil, ErrFormJobNotFound
	}
	ownerDigest, err := store.ownerDigest(owner)
	if err != nil {
		return formJobStoredRecord{}, nil, err
	}
	if !constantTimeStringEqual(record.Public.OwnerDigest, ownerDigest) {
		return formJobStoredRecord{}, nil, ErrFormJobUnauthorized
	}
	if record.WrappedKey == nil {
		return record, nil, nil
	}
	jobKey, err := unwrapFormJobKey(store.profileKey, *record.WrappedKey)
	if err != nil {
		return formJobStoredRecord{}, nil, err
	}
	return record, jobKey, nil
}

func (store *FormJobStore) expireAndPruneLocked(document *formJobStoreDocument, now time.Time) bool {
	changed := false
	for jobID, record := range document.Records {
		if !record.Public.State.terminal() && !now.Before(time.UnixMilli(record.Public.ExpiresAt)) {
			store.eraseStoredRecord(&record, FormJobExpired, "form_job_expired", now)
			document.Records[jobID] = record
			changed = true
			continue
		}
		if record.Public.State.terminal() && record.Public.CleanupAfter > 0 &&
			!now.Before(time.UnixMilli(record.Public.CleanupAfter)) {
			delete(document.Records, jobID)
			changed = true
		}
	}
	return changed
}

func (store *FormJobStore) loadLocked() (formJobStoreDocument, error) {
	document := formJobStoreDocument{Version: formJobStoreVersion, Records: make(map[string]formJobStoredRecord)}
	data, err := readProtectedRegularFile(store.path, int64(store.maxBytes))
	if errors.Is(err, os.ErrNotExist) {
		return document, nil
	}
	if err != nil {
		return formJobStoreDocument{}, fmt.Errorf("%w: read snapshot: %w", ErrFormJobStoreUnavailable, err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return formJobStoreDocument{}, fmt.Errorf("%w: decode snapshot", ErrFormJobRecordCorrupt)
	}
	if err := ensureFormJobJSONEOF(decoder); err != nil {
		return formJobStoreDocument{}, err
	}
	if document.Version != formJobStoreVersion || document.Records == nil || len(document.Records) > store.maxJobs {
		return formJobStoreDocument{}, ErrFormJobRecordCorrupt
	}
	for jobID, record := range document.Records {
		if jobID != record.Public.JobID {
			return formJobStoreDocument{}, ErrFormJobRecordCorrupt
		}
		if err := store.validateStoredRecord(record); err != nil {
			return formJobStoreDocument{}, err
		}
	}
	return document, nil
}

func (store *FormJobStore) validateStoredRecord(record formJobStoredRecord) error {
	expectedIntegrity, err := store.storedRecordIntegrity(record)
	if err != nil || !constantTimeStringEqual(record.IntegrityDigest, expectedIntegrity) {
		return ErrFormJobRecordCorrupt
	}
	if validationErr := validateFormJobRecord(record.Public); validationErr != nil {
		return validationErr
	}
	if record.Public.State.terminal() {
		if record.WrappedKey != nil || record.Source != nil || len(record.Events) != 0 ||
			len(record.Public.Fields) != 0 || record.Public.LedgerDigest != "" {
			return ErrFormJobRecordCorrupt
		}
		return nil
	}
	if record.WrappedKey == nil || record.Source == nil || len(record.Events) > store.maxEventsPerJob ||
		record.WrappedKey.JobID != record.Public.JobID ||
		record.WrappedKey.OwnerDigest != record.Public.OwnerDigest ||
		record.Public.LedgerRevision != int64(len(record.Events)) {
		return ErrFormJobRecordCorrupt
	}
	jobKey, err := unwrapFormJobKey(store.profileKey, *record.WrappedKey)
	if err != nil {
		return err
	}
	defer clear(jobKey)
	var source formJobSourceSecret
	if record.Source.JobID != record.Public.JobID || record.Source.Kind != "source" ||
		openFormJobEnvelope(jobKey, *record.Source, &source) != nil || strings.TrimSpace(source.SourceRef) == "" {
		return ErrFormJobRecordCorrupt
	}
	previousDigest := ""
	events := make(map[string]formJobValuePayload, len(record.Events))
	for _, envelope := range record.Events {
		if envelope.JobID != record.Public.JobID || envelope.Kind != "value" {
			return ErrFormJobRecordCorrupt
		}
		payload, err := openFormJobValuePayload(jobKey, envelope)
		if err != nil || payload.PreviousDigest != previousDigest || payload.EventID != envelope.EventID ||
			payload.FieldID != envelope.FieldID || payload.Revision != envelope.Revision {
			return ErrFormJobRecordCorrupt
		}
		if _, duplicate := events[payload.EventID]; duplicate {
			return ErrFormJobRecordCorrupt
		}
		events[payload.EventID] = payload
		previousDigest, err = formJobJSONDigest(envelope)
		if err != nil {
			return ErrFormJobRecordCorrupt
		}
	}
	for _, payload := range events {
		if payload.SupersedesEventID == "" {
			continue
		}
		previous, found := events[payload.SupersedesEventID]
		if !found || previous.FieldID != payload.FieldID || previous.Revision >= payload.Revision {
			return ErrFormJobRecordCorrupt
		}
	}
	if record.Public.LedgerDigest != previousDigest {
		return ErrFormJobRecordCorrupt
	}
	for _, field := range record.Public.Fields {
		payload, found := events[field.EventID]
		if !found || payload.FieldID != field.FieldID || payload.Value.Kind != field.ValueKind ||
			payload.State != field.State || payload.Source != field.Source {
			return ErrFormJobRecordCorrupt
		}
	}
	return nil
}

func (store *FormJobStore) saveLocked(document formJobStoreDocument) error {
	for jobID, record := range document.Records {
		integrity, err := store.storedRecordIntegrity(record)
		if err != nil {
			return fmt.Errorf("authenticate document form job snapshot: %w", err)
		}
		record.IntegrityDigest = integrity
		document.Records[jobID] = record
	}
	data, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode document form job snapshot: %w", err)
	}
	if len(data)+1 > store.maxBytes {
		return ErrFormJobCapacityExceeded
	}
	if err := store.writeFile(store.path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("%w: persist snapshot: %w", ErrFormJobStoreUnavailable, err)
	}
	return nil
}

func (store *FormJobStore) storedRecordIntegrity(record formJobStoredRecord) (string, error) {
	data, err := json.Marshal(struct {
		Public     FormJobRecord      `json:"public"`
		WrappedKey *formJobWrappedKey `json:"wrapped_key,omitempty"`
		Source     *formJobEnvelope   `json:"source,omitempty"`
		Events     []formJobEnvelope  `json:"events,omitempty"`
	}{
		Public:     record.Public,
		WrappedKey: record.WrappedKey,
		Source:     record.Source,
		Events:     record.Events,
	})
	if err != nil {
		return "", err
	}
	defer clear(data)
	return keyedDigestBytes(store.profileKey, "stored_record", data), nil
}

func openFormJobValuePayload(jobKey []byte, envelope formJobEnvelope) (formJobValuePayload, error) {
	var payload formJobValuePayload
	if err := openFormJobEnvelope(jobKey, envelope, &payload); err != nil {
		return formJobValuePayload{}, err
	}
	if payload.EventID == "" || payload.FieldID == "" || payload.Revision <= 0 || payload.CreatedAt <= 0 ||
		payload.IdempotencyDigest == "" || len(payload.IdempotencyDigest) > maxFormJobDigestLength ||
		payload.State == "" || payload.Source == "" || payload.Value.validate() != nil {
		return formJobValuePayload{}, ErrFormJobRecordCorrupt
	}
	return payload, nil
}

func publicFormJobValueEvent(payload formJobValuePayload) FormJobValueEvent {
	return FormJobValueEvent{
		EventID:           payload.EventID,
		FieldID:           payload.FieldID,
		Revision:          payload.Revision,
		Value:             cloneProtectedValue(payload.Value),
		State:             payload.State,
		Source:            payload.Source,
		BlankReason:       payload.BlankReason,
		ValidationCode:    payload.ValidationCode,
		SupersedesEventID: payload.SupersedesEventID,
		CreatedAt:         payload.CreatedAt,
	}
}

func validateFormJobCreateRequest(request FormJobCreateRequest, maxRetention time.Duration) error {
	if _, err := request.Owner.canonical(); err != nil {
		return err
	}
	if strings.TrimSpace(request.StartIdempotencyKey) == "" ||
		len(
			request.StartIdempotencyKey,
		) > maxFormJobIdempotencyLength || !utf8.ValidString(request.StartIdempotencyKey) ||
		strings.TrimSpace(request.SourceRef) == "" || len(request.SourceRef) > maxFormJobIdentityLength ||
		!utf8.ValidString(request.SourceRef) || strings.TrimSpace(request.SourceDigest) == "" ||
		len(request.SourceDigest) > maxFormJobDigestLength || strings.TrimSpace(request.FieldSchemaDigest) == "" ||
		len(request.FieldSchemaDigest) > maxFormJobDigestLength || strings.TrimSpace(request.BackendRevision) == "" ||
		len(request.BackendRevision) > maxFormJobRevisionLength ||
		strings.TrimSpace(request.AuditPolicyRevision) == "" ||
		len(request.AuditPolicyRevision) > maxFormJobRevisionLength {
		return errors.New("document form job create request is invalid")
	}
	if request.Retention < 0 || request.Retention > maxRetention {
		return errors.New("document form job retention exceeds policy")
	}
	return nil
}

func validateFormJobAppendValueRequest(request FormJobAppendValueRequest) error {
	if strings.TrimSpace(request.JobID) == "" || len(request.JobID) > maxFormJobIDLength ||
		request.ExpectedRevision <= 0 || strings.TrimSpace(request.FieldID) == "" ||
		len(request.FieldID) > maxFormJobFieldIDLength || !utf8.ValidString(request.FieldID) ||
		strings.TrimSpace(request.IdempotencyKey) == "" ||
		len(request.IdempotencyKey) > maxFormJobIdempotencyLength || !utf8.ValidString(request.IdempotencyKey) ||
		len(
			request.SupersedesEventID,
		) > maxFormJobEventIDLength || len(request.BlankReason) > maxFormJobRevisionLength ||
		len(request.ValidationCode) > maxFormJobRevisionLength {
		return errors.New("document protected form answer request is invalid")
	}
	if _, err := request.Owner.canonical(); err != nil {
		return err
	}
	if err := request.Value.validate(); err != nil {
		return err
	}
	switch request.State {
	case FormValueSupplied, FormValueConfirmed, FormValueBlanked, FormValueNotApplicable,
		FormValueConflicting, FormValueInvalid, FormValueModelSuggested:
	default:
		return errors.New("document protected form answer state is invalid")
	}
	switch request.Source {
	case FormValueSourceUser, FormValueSourceDocument, FormValueSourceDeterministic, FormValueSourceModel:
	default:
		return errors.New("document protected form answer source is invalid")
	}
	if (request.State == FormValueBlanked || request.State == FormValueNotApplicable) !=
		(request.Value.Kind == ProtectedValueBlank) {
		return errors.New("document protected blank state and value disagree")
	}
	return nil
}

func ensureFormJobJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrFormJobRecordCorrupt
	}
	return nil
}

func constantTimeStringEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}
