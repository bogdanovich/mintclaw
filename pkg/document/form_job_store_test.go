package document

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const protectedFormSentinel = "MINTCLAW_PDF3_PRIVATE_VALUE_7f92c4"

func TestFormJobStoreProtectsValuesAcrossRestart(t *testing.T) {
	store, options := newTestFormJobStore(t)
	owner := testFormJobOwner()
	created, err := store.Create(t.Context(), testFormJobCreateRequest(owner))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.State != FormJobPrepared || created.Revision != 1 || created.LedgerRevision != 0 {
		t.Fatalf("created record = %#v", created)
	}
	if got, sourceErr := store.SourceRef(t.Context(), created.JobID, owner); sourceErr != nil ||
		got != "media://pdf3-source-private" {
		t.Fatalf("SourceRef() = %q, %v", got, sourceErr)
	}

	updated, event, err := store.AppendValue(t.Context(), FormJobAppendValueRequest{
		JobID: created.JobID, ExpectedRevision: created.Revision, Owner: owner,
		FieldID: "field.full_name", IdempotencyKey: "telegram-message-101",
		Value: FormProtectedValue{Kind: ProtectedValueText, Text: protectedFormSentinel},
		State: FormValueConfirmed, Source: FormValueSourceUser,
	})
	if err != nil {
		t.Fatalf("AppendValue() error = %v", err)
	}
	if updated.State != FormJobCollecting || updated.Revision != 2 || updated.LedgerRevision != 1 ||
		len(updated.Fields) != 1 || updated.Fields[0].FieldID != "field.full_name" ||
		updated.Fields[0].EventID != event.EventID {
		t.Fatalf("updated record = %#v, event = %#v", updated, event)
	}
	assertFormStoreContainsNoPlaintext(t, options, protectedFormSentinel, "media://pdf3-source-private")

	store.Close()
	reopened, err := OpenFormJobStore(options)
	if err != nil {
		t.Fatalf("OpenFormJobStore() after restart error = %v", err)
	}
	t.Cleanup(reopened.Close)
	values, err := reopened.ReadValues(t.Context(), created.JobID, owner, []string{"field.full_name"})
	if err != nil {
		t.Fatalf("ReadValues() error = %v", err)
	}
	if got := values["field.full_name"].Value.Text; got != protectedFormSentinel {
		t.Fatalf("protected value = %q", got)
	}
	public, err := reopened.Get(t.Context(), created.JobID, owner)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(protectedFormSentinel)) ||
		bytes.Contains(encoded, []byte("pdf3-source-private")) {
		t.Fatalf("public projection leaked protected data: %s", encoded)
	}
}

func TestFormJobStoreIdempotentAnswerAndAppendOnlyCorrection(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	owner := testFormJobOwner()
	created, err := store.Create(t.Context(), testFormJobCreateRequest(owner))
	if err != nil {
		t.Fatal(err)
	}
	request := FormJobAppendValueRequest{
		JobID: created.JobID, ExpectedRevision: created.Revision, Owner: owner,
		FieldID: "field.city", IdempotencyKey: "message-1",
		Value: FormProtectedValue{Kind: ProtectedValueText, Text: "Seattle"},
		State: FormValueConfirmed, Source: FormValueSourceUser,
	}
	first, firstEvent, err := store.AppendValue(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	replayed, replayedEvent, err := store.AppendValue(t.Context(), request)
	if err != nil {
		t.Fatalf("idempotent AppendValue() error = %v", err)
	}
	if replayed.Revision != first.Revision || replayedEvent.EventID != firstEvent.EventID {
		t.Fatalf("replay = (%#v, %#v), want (%#v, %#v)", replayed, replayedEvent, first, firstEvent)
	}
	conflict := request
	conflict.Value.Text = "Portland"
	if _, _, err := store.AppendValue(t.Context(), conflict); !errors.Is(err, ErrFormJobAnswerConflict) {
		t.Fatalf("conflicting idempotency error = %v", err)
	}
	correction := request
	correction.ExpectedRevision = first.Revision
	correction.IdempotencyKey = "message-2"
	correction.Value.Text = "Portland"
	correction.SupersedesEventID = firstEvent.EventID
	corrected, correctedEvent, err := store.AppendValue(t.Context(), correction)
	if err != nil {
		t.Fatalf("correction error = %v", err)
	}
	if corrected.Revision != first.Revision+1 || correctedEvent.SupersedesEventID != firstEvent.EventID ||
		corrected.Fields[0].EventID != correctedEvent.EventID {
		t.Fatalf("corrected = %#v, event = %#v", corrected, correctedEvent)
	}
	values, err := store.ReadValues(t.Context(), created.JobID, owner, []string{"field.city"})
	if err != nil || values["field.city"].Value.Text != "Portland" {
		t.Fatalf("ReadValues() = %#v, %v", values, err)
	}
}

func TestFormJobStoreRejectsWrongAuthorityAndRevision(t *testing.T) {
	store, _ := newTestFormJobStore(t)
	owner := testFormJobOwner()
	created, err := store.Create(t.Context(), testFormJobCreateRequest(owner))
	if err != nil {
		t.Fatal(err)
	}
	other := owner
	other.SenderID = "other-user"
	if _, err := store.Get(t.Context(), created.JobID, other); !errors.Is(err, ErrFormJobUnauthorized) {
		t.Fatalf("Get() wrong owner error = %v", err)
	}
	request := FormJobAppendValueRequest{
		JobID: created.JobID, ExpectedRevision: created.Revision + 1, Owner: owner,
		FieldID: "field.name", IdempotencyKey: "message-wrong-revision",
		Value: FormProtectedValue{Kind: ProtectedValueText, Text: "Ada"},
		State: FormValueSupplied, Source: FormValueSourceUser,
	}
	if _, _, err := store.AppendValue(t.Context(), request); !errors.Is(err, ErrFormJobConflict) {
		t.Fatalf("AppendValue() wrong revision error = %v", err)
	}
}

func TestFormJobStoreCancelDeleteAndExpiryEraseProtectedMaterial(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		store, options := newTestFormJobStore(t)
		owner := testFormJobOwner()
		created, err := store.Create(t.Context(), testFormJobCreateRequest(owner))
		if err != nil {
			t.Fatal(err)
		}
		updated, _, err := appendTestFormValue(t, store, created, owner, "cancel-private")
		if err != nil {
			t.Fatal(err)
		}
		canceled, err := store.Cancel(t.Context(), created.JobID, updated.Revision, owner)
		if err != nil {
			t.Fatal(err)
		}
		if canceled.State != FormJobCanceled || len(canceled.Fields) != 0 || canceled.LedgerDigest != "" {
			t.Fatalf("canceled record = %#v", canceled)
		}
		replayed, err := store.Cancel(t.Context(), created.JobID, updated.Revision, owner)
		if err != nil || replayed.Revision != canceled.Revision || replayed.State != FormJobCanceled {
			t.Fatalf("idempotent Cancel() = %#v, %v", replayed, err)
		}
		assertStoredJobHasNoCiphertext(t, options, created.JobID)
		if _, err := store.SourceRef(t.Context(), created.JobID, owner); !errors.Is(err, ErrFormJobTerminal) {
			t.Fatalf("SourceRef() after cancel error = %v", err)
		}
	})

	t.Run("delete", func(t *testing.T) {
		store, options := newTestFormJobStore(t)
		owner := testFormJobOwner()
		created, err := store.Create(t.Context(), testFormJobCreateRequest(owner))
		if err != nil {
			t.Fatal(err)
		}
		deleted, err := store.Delete(t.Context(), created.JobID, created.Revision, owner)
		if err != nil || deleted.State != FormJobDeleted {
			t.Fatalf("Delete() = %#v, %v", deleted, err)
		}
		assertStoredJobHasNoCiphertext(t, options, created.JobID)
	})

	t.Run("expiry and prune", func(t *testing.T) {
		store, options := newTestFormJobStore(t)
		base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
		store.now = func() time.Time { return base }
		owner := testFormJobOwner()
		request := testFormJobCreateRequest(owner)
		request.Retention = time.Minute
		created, err := store.Create(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		store.now = func() time.Time { return base.Add(2 * time.Minute) }
		expired, err := store.Get(t.Context(), created.JobID, owner)
		if err != nil || expired.State != FormJobExpired {
			t.Fatalf("expired Get() = %#v, %v", expired, err)
		}
		assertStoredJobHasNoCiphertext(t, options, created.JobID)
		if _, err := store.SourceRef(t.Context(), created.JobID, owner); !errors.Is(err, ErrFormJobExpired) {
			t.Fatalf("SourceRef() expired error = %v", err)
		}
		if err := store.Prune(t.Context(), base.Add(2*time.Minute+DefaultFormJobTerminalRetention)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(t.Context(), created.JobID, owner); !errors.Is(err, ErrFormJobNotFound) {
			t.Fatalf("Get() after prune error = %v", err)
		}
	})
}

func TestFormJobStoreFailsClosedOnTamperWrongKeyAndBroadPermissions(t *testing.T) {
	t.Run("public snapshot tamper", func(t *testing.T) {
		store, options := newTestFormJobStore(t)
		owner := testFormJobOwner()
		created, err := store.Create(t.Context(), testFormJobCreateRequest(owner))
		if err != nil {
			t.Fatal(err)
		}
		store.Close()
		path := formJobStatePath(options)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var document formJobStoreDocument
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		record := document.Records[created.JobID]
		record.Public.SourceDigest = strings.Repeat("f", 64)
		document.Records[created.JobID] = record
		data, err = json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenFormJobStore(options); !errors.Is(err, ErrFormJobRecordCorrupt) {
			t.Fatalf("OpenFormJobStore() public tamper error = %v", err)
		}
	})

	t.Run("ciphertext tamper", func(t *testing.T) {
		store, options := newTestFormJobStore(t)
		owner := testFormJobOwner()
		created, err := store.Create(t.Context(), testFormJobCreateRequest(owner))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := appendTestFormValue(t, store, created, owner, "tamper-private"); err != nil {
			t.Fatal(err)
		}
		store.Close()
		path := formJobStatePath(options)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		index := bytes.Index(data, []byte(`"ciphertext":"`))
		if index < 0 {
			t.Fatal("snapshot has no ciphertext")
		}
		index += len(`"ciphertext":"`)
		data[index] = differentBase64Byte(data[index])
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenFormJobStore(options); !errors.Is(err, ErrFormJobRecordCorrupt) {
			t.Fatalf("OpenFormJobStore() tamper error = %v", err)
		}
	})

	t.Run("wrong key", func(t *testing.T) {
		store, options := newTestFormJobStore(t)
		owner := testFormJobOwner()
		if _, err := store.Create(t.Context(), testFormJobCreateRequest(owner)); err != nil {
			t.Fatal(err)
		}
		store.Close()
		wrongKey := bytes.Repeat([]byte{0x5a}, 32)
		if err := os.WriteFile(filepath.Join(options.KeyRoot, formJobKeyFileName), wrongKey, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenFormJobStore(options); !errors.Is(err, ErrFormJobRecordCorrupt) {
			t.Fatalf("OpenFormJobStore() wrong key error = %v", err)
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("broad key mode", func(t *testing.T) {
			store, options := newTestFormJobStore(t)
			store.Close()
			keyPath := filepath.Join(options.KeyRoot, formJobKeyFileName)
			if err := os.Chmod(keyPath, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenFormJobStore(options); !errors.Is(err, ErrFormJobKeyUnavailable) {
				t.Fatalf("OpenFormJobStore() broad key error = %v", err)
			}
		})
	}
}

func TestFormJobStoreCoordinatesSharedProfileKeyCreation(t *testing.T) {
	root := t.TempDir()
	keyRoot := filepath.Join(root, "profile-keys")
	if err := os.Mkdir(keyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	options := make([]FormJobStoreOptions, 2)
	for index := range options {
		stateRoot := filepath.Join(root, "state-"+string(rune('a'+index)))
		if err := os.Mkdir(stateRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		options[index] = FormJobStoreOptions{StateRoot: stateRoot, KeyRoot: keyRoot}
	}
	type result struct {
		store *FormJobStore
		err   error
	}
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for _, option := range options {
		wait.Add(1)
		go func(option FormJobStoreOptions) {
			defer wait.Done()
			store, err := OpenFormJobStore(option)
			results <- result{store: store, err: err}
		}(option)
	}
	wait.Wait()
	close(results)
	var stores []*FormJobStore
	for opened := range results {
		if opened.err != nil {
			t.Fatalf("OpenFormJobStore() shared key error = %v", opened.err)
		}
		stores = append(stores, opened.store)
		t.Cleanup(opened.store.Close)
	}
	if len(stores) != 2 || !bytes.Equal(stores[0].profileKey, stores[1].profileKey) {
		t.Fatal("stores did not converge on one profile key")
	}
}

func TestFormJobStoreSerializesConcurrentDuplicateAnswer(t *testing.T) {
	first, options := newTestFormJobStore(t)
	owner := testFormJobOwner()
	created, err := first.Create(t.Context(), testFormJobCreateRequest(owner))
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Close)
	request := FormJobAppendValueRequest{
		JobID: created.JobID, ExpectedRevision: created.Revision, Owner: owner,
		FieldID: "field.concurrent", IdempotencyKey: "same-message",
		Value: FormProtectedValue{Kind: ProtectedValueText, Text: "one-value"},
		State: FormValueConfirmed, Source: FormValueSourceUser,
	}
	type outcome struct {
		record FormJobRecord
		event  FormJobValueEvent
		err    error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	for _, store := range []*FormJobStore{first, second} {
		wait.Add(1)
		go func(store *FormJobStore) {
			defer wait.Done()
			record, event, appendErr := store.AppendValue(context.Background(), request)
			results <- outcome{record: record, event: event, err: appendErr}
		}(store)
	}
	wait.Wait()
	close(results)
	var outcomes []outcome
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent AppendValue() error = %v", result.err)
		}
		outcomes = append(outcomes, result)
	}
	if outcomes[0].record.Revision != 2 || outcomes[1].record.Revision != 2 ||
		outcomes[0].event.EventID != outcomes[1].event.EventID {
		t.Fatalf("concurrent outcomes = %#v", outcomes)
	}
	values, err := first.ReadValues(t.Context(), created.JobID, owner, []string{"field.concurrent"})
	if err != nil || len(values) != 1 {
		t.Fatalf("ReadValues() = %#v, %v", values, err)
	}
}

func TestFormJobStoreCreateIsIdempotentAndBounded(t *testing.T) {
	store, _ := newTestFormJobStoreWithLimits(t, 1, 2)
	owner := testFormJobOwner()
	request := testFormJobCreateRequest(owner)
	first, err := store.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(t.Context(), request)
	if err != nil || second.JobID != first.JobID {
		t.Fatalf("idempotent Create() = %#v, %v", second, err)
	}
	conflict := request
	conflict.SourceDigest = strings.Repeat("b", 64)
	if _, err := store.Create(t.Context(), conflict); !errors.Is(err, ErrFormJobConflict) {
		t.Fatalf("conflicting Create() error = %v", err)
	}
	capacity := request
	capacity.StartIdempotencyKey = "start-2"
	if _, err := store.Create(t.Context(), capacity); !errors.Is(err, ErrFormJobCapacityExceeded) {
		t.Fatalf("capacity Create() error = %v", err)
	}
	updated, event, err := appendTestFormValue(t, store, first, owner, "first")
	if err != nil {
		t.Fatal(err)
	}
	correction := FormJobAppendValueRequest{
		JobID: first.JobID, ExpectedRevision: updated.Revision, Owner: owner,
		FieldID: "field.test", IdempotencyKey: "answer-2",
		Value: FormProtectedValue{Kind: ProtectedValueText, Text: "second"},
		State: FormValueConfirmed, Source: FormValueSourceUser, SupersedesEventID: event.EventID,
	}
	updated, event, err = store.AppendValue(t.Context(), correction)
	if err != nil {
		t.Fatal(err)
	}
	correction.ExpectedRevision = updated.Revision
	correction.IdempotencyKey = "answer-3"
	correction.Value.Text = "third"
	correction.SupersedesEventID = event.EventID
	if _, _, err := store.AppendValue(t.Context(), correction); !errors.Is(err, ErrFormJobCapacityExceeded) {
		t.Fatalf("event capacity error = %v", err)
	}
}

func newTestFormJobStore(t *testing.T) (*FormJobStore, FormJobStoreOptions) {
	t.Helper()
	return newTestFormJobStoreWithLimits(t, DefaultFormJobMaxJobs, DefaultFormJobMaxEvents)
}

func newTestFormJobStoreWithLimits(t *testing.T, maxJobs, maxEvents int) (*FormJobStore, FormJobStoreOptions) {
	t.Helper()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state")
	keyRoot := filepath.Join(root, "profile-keys")
	for _, directory := range []string{stateRoot, keyRoot} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	options := FormJobStoreOptions{
		StateRoot:         stateRoot,
		KeyRoot:           keyRoot,
		Retention:         DefaultFormJobRetention,
		TerminalRetention: DefaultFormJobTerminalRetention,
		MaxJobs:           maxJobs,
		MaxEventsPerJob:   maxEvents,
		MaxBytes:          DefaultFormJobMaxBytes,
	}
	store, err := OpenFormJobStore(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store, options
}

func testFormJobOwner() FormJobOwner {
	return FormJobOwner{
		AgentID: "main", WorkspaceID: "workspace-main", RouteSessionKey: "route-session",
		Channel: "telegram", AccountID: "primary", ChatID: "chat-1", SenderID: "user-1",
	}
}

func testFormJobCreateRequest(owner FormJobOwner) FormJobCreateRequest {
	return FormJobCreateRequest{
		Owner:               owner,
		StartIdempotencyKey: "start-message-1",
		SourceRef:           "media://pdf3-source-private",
		SourceDigest:        strings.Repeat("a", 64),
		FieldSchemaDigest:   strings.Repeat("c", 64),
		BackendRevision:     "pdfcpu-v0.15.0-mintclaw-write-v1",
		AuditPolicyRevision: "document-audit-v1",
	}
}

func appendTestFormValue(
	t *testing.T,
	store *FormJobStore,
	record FormJobRecord,
	owner FormJobOwner,
	value string,
) (FormJobRecord, FormJobValueEvent, error) {
	t.Helper()
	return store.AppendValue(t.Context(), FormJobAppendValueRequest{
		JobID: record.JobID, ExpectedRevision: record.Revision, Owner: owner,
		FieldID: "field.test", IdempotencyKey: "answer-" + value,
		Value: FormProtectedValue{Kind: ProtectedValueText, Text: value},
		State: FormValueConfirmed, Source: FormValueSourceUser,
	})
}

func assertFormStoreContainsNoPlaintext(t *testing.T, options FormJobStoreOptions, sentinels ...string) {
	t.Helper()
	for _, path := range []string{formJobStatePath(options), filepath.Join(options.KeyRoot, formJobKeyFileName)} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, sentinel := range sentinels {
			if bytes.Contains(data, []byte(sentinel)) {
				t.Fatalf("protected file %s contains plaintext sentinel %q", path, sentinel)
			}
		}
	}
}

func assertStoredJobHasNoCiphertext(t *testing.T, options FormJobStoreOptions, jobID string) {
	t.Helper()
	data, err := os.ReadFile(formJobStatePath(options))
	if err != nil {
		t.Fatal(err)
	}
	var document formJobStoreDocument
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	record := document.Records[jobID]
	if record.WrappedKey != nil || record.Source != nil || len(record.Events) != 0 {
		t.Fatalf("terminal stored record retained protected material: %#v", record)
	}
}

func formJobStatePath(options FormJobStoreOptions) string {
	return filepath.Join(options.StateRoot, "document_form_jobs", formJobStoreFileName)
}

func differentBase64Byte(value byte) byte {
	if value == 'A' {
		return 'B'
	}
	return 'A'
}
