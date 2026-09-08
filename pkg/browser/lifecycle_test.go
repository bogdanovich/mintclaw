package browser

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

func TestFileStorePersistsAndExclusivelyOwnsState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "browser.json")
	store, err := NewFileStore(path, 0, 0)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	if _, err = NewFileStore(path, 0, 0); !errors.Is(err, ErrStoreOwned) {
		t.Fatalf("second NewFileStore() error = %v, want ErrStoreOwned", err)
	}

	broker := lifecycleTestBroker(t, admittedBrowserConfig(), store, &fakeWorkerFactory{})
	session, err := broker.Open(context.Background(), OpenRequest{
		Owner: testOwner(), Target: "gateway", Profile: "managed",
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	store.Close()
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("state file = %v, %v; want mode 0600", info, err)
	}

	reopened, err := NewFileStore(path, 0, 0)
	if err != nil {
		t.Fatalf("reopen FileStore error = %v", err)
	}
	t.Cleanup(reopened.Close)
	if err = store.CreateSession(
		context.Background(),
		testOpeningSession(testOwner()),
	); !errors.Is(
		err,
		ErrStoreClosed,
	) {
		t.Fatalf("closed store CreateSession() error = %v, want ErrStoreClosed", err)
	}
	stored, err := reopened.GetSession(context.Background(), session.ID)
	if err != nil || stored != session {
		t.Fatalf("persisted session = %+v, %v; want %+v", stored, err, session)
	}
}

func TestFileStoreRollsBackRejectedBoundedWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "browser.json")
	store, err := NewFileStore(path, 0, 256)
	if err != nil {
		t.Fatal(err)
	}
	session := testOpeningSession(testOwner())
	if err = store.CreateSession(context.Background(), session); !errors.Is(err, ErrStoreFull) {
		t.Fatalf("CreateSession() error = %v, want ErrStoreFull", err)
	}
	if _, err = store.GetSession(context.Background(), session.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("live GetSession() error = %v, want ErrNotFound", err)
	}
	store.Close()
	reopened, err := NewFileStore(path, 0, 256)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err = reopened.GetSession(context.Background(), session.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reopened GetSession() error = %v, want ErrNotFound", err)
	}
}

func TestFileStoreRejectsUnsafeOrCorruptState(t *testing.T) {
	validEmpty := `{"version":2,"sessions":{},"prepared_actions":{},"invocations":{}}`
	for _, test := range []struct {
		name    string
		content string
		mode    os.FileMode
		bytes   int
	}{
		{name: "unknown field", content: `{"version":2,"sessions":{},"prepared_actions":{},"invocations":{},"secret":"x"}`, mode: 0o600},
		{name: "duplicate field", content: `{"version":2,"version":2,"sessions":{},"prepared_actions":{},"invocations":{}}`, mode: 0o600},
		{name: "trailing value", content: validEmpty + `{}`, mode: 0o600},
		{name: "insecure permissions", content: validEmpty, mode: 0o644},
		{name: "oversized", content: validEmpty, mode: 0o600, bytes: 32},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "browser.json")
			if err := os.WriteFile(path, []byte(test.content), test.mode); err != nil {
				t.Fatal(err)
			}
			maxBytes := 0
			if test.bytes != 0 {
				maxBytes = test.bytes
			}
			store, err := NewFileStore(path, 0, maxBytes)
			if store != nil {
				store.Close()
			}
			if err == nil {
				t.Fatal("NewFileStore() error = nil")
			}
		})
	}
}

func TestBrokerRecoverMarksSessionLostAndAcceptedInvocationUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "browser.json")
	store, err := NewFileStore(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	broker := lifecycleTestBroker(t, admittedBrowserConfig(), store, &fakeWorkerFactory{})
	owner := testOwner()
	session, err := broker.Open(context.Background(), OpenRequest{Owner: owner, Target: "gateway", Profile: "managed"})
	if err != nil {
		t.Fatal(err)
	}
	invocation := preparedInvocation(session, "invocation_restart")
	if err = store.CreateInvocation(context.Background(), invocation); err != nil {
		t.Fatal(err)
	}
	invocation.State = InvocationAccepted
	invocation.AcceptedAt = invocation.UpdatedAt + 1
	invocation.UpdatedAt = invocation.AcceptedAt
	invocation.Revision++
	if err = store.UpdateInvocation(context.Background(), 1, invocation); err != nil {
		t.Fatal(err)
	}
	store.Close() // Simulate gateway process exit after durable acceptance.

	reopened, err := NewFileStore(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	recovered := lifecycleTestBroker(t, admittedBrowserConfig(), reopened, &fakeWorkerFactory{})
	recovered.newID = randomID
	if err = recovered.Recover(context.Background()); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	gotSession, _ := reopened.GetSession(context.Background(), session.ID)
	gotInvocation, _ := reopened.GetInvocation(context.Background(), invocation.ID)
	if gotSession.State != SessionLost || gotSession.SafeFailure != "gateway_restarted" {
		t.Fatalf("recovered session = %+v", gotSession)
	}
	if gotInvocation.State != InvocationUnknown || gotInvocation.SafeFailure != "gateway_restarted" {
		t.Fatalf("recovered invocation = %+v", gotInvocation)
	}
	if err = recovered.Recover(context.Background()); err != nil {
		t.Fatalf("idempotent Recover() error = %v", err)
	}
	if _, err = recovered.Open(
		context.Background(),
		OpenRequest{Owner: owner, Target: "gateway", Profile: "managed"},
	); err != nil {
		t.Fatalf("Open() after recovery error = %v", err)
	}
}

func TestBrokerOpenReconcilesCommittedReadyWarning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "browser.json")
	store, err := NewFileStore(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	originalWrite := store.writeFile
	writes := 0
	store.writeFile = func(path string, data []byte, mode os.FileMode) error {
		writes++
		if writeErr := originalWrite(path, data, mode); writeErr != nil {
			return writeErr
		}
		if writes == 2 {
			return &fileutil.CommittedWriteError{Err: errors.New("directory sync failed")}
		}
		return nil
	}
	factory := &fakeWorkerFactory{}
	broker := lifecycleTestBroker(t, admittedBrowserConfig(), store, factory)
	session, err := broker.Open(
		context.Background(),
		OpenRequest{Owner: testOwner(), Target: "gateway", Profile: "managed"},
	)
	if err == nil || session.State != SessionLost || factory.workers[0].closed != 1 {
		t.Fatalf("Open() = %+v, %v; worker = %+v", session, err, factory.workers[0])
	}
	stored, getErr := store.GetSession(context.Background(), session.ID)
	if getErr != nil || stored.State != SessionLost {
		t.Fatalf("stored session = %+v, %v", stored, getErr)
	}
	if _, err = broker.Open(
		context.Background(),
		OpenRequest{Owner: testOwner(), Target: "gateway", Profile: "managed"},
	); err != nil {
		t.Fatalf("Open() after committed warning reconciliation error = %v", err)
	}
}

func TestBrokerSweepEnforcesIdleAndAbsoluteExpiry(t *testing.T) {
	for _, test := range []struct {
		name    string
		advance time.Duration
	}{
		{name: "idle", advance: 6 * time.Second},
		{name: "absolute", advance: 21 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := admittedBrowserConfig()
			root.Tools.Browser.Limits.IdleSeconds = 5
			root.Tools.Browser.Limits.SessionSeconds = 20
			root.Tools.Browser.Limits.PreparedSeconds = 5
			factory := &fakeWorkerFactory{}
			broker := lifecycleTestBroker(t, root, NewMemoryStore(), factory)
			now := time.Unix(1_000, 0).UTC()
			broker.now = func() time.Time { return now }
			session, err := broker.Open(
				context.Background(),
				OpenRequest{Owner: testOwner(), Target: "gateway", Profile: "managed"},
			)
			if err != nil {
				t.Fatal(err)
			}
			now = now.Add(test.advance)
			if err = broker.Sweep(context.Background()); err != nil {
				t.Fatalf("Sweep() error = %v", err)
			}
			expired, err := broker.Status(context.Background(), testOwner(), session.ID)
			if err != nil || expired.State != SessionExpired || factory.workers[0].closed != 1 {
				t.Fatalf("expired session = %+v, %v; worker = %+v", expired, err, factory.workers[0])
			}
		})
	}
}

func TestBrokerTouchRenewsIdleButNotAbsoluteLifetime(t *testing.T) {
	root := admittedBrowserConfig()
	root.Tools.Browser.Limits.IdleSeconds = 5
	root.Tools.Browser.Limits.SessionSeconds = 10
	root.Tools.Browser.Limits.PreparedSeconds = 5
	broker := lifecycleTestBroker(t, root, NewMemoryStore(), &fakeWorkerFactory{})
	now := time.Unix(2_000, 0).UTC()
	broker.now = func() time.Time { return now }
	session, err := broker.Open(
		context.Background(),
		OpenRequest{Owner: testOwner(), Target: "gateway", Profile: "managed"},
	)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(4 * time.Second)
	touched, err := broker.Touch(context.Background(), testOwner(), session.ID)
	if err != nil || touched.ExpiresAt != session.ExpiresAt {
		t.Fatalf("Touch() = %+v, %v", touched, err)
	}
	now = now.Add(4 * time.Second)
	if err = broker.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	ready, _ := broker.Status(context.Background(), testOwner(), session.ID)
	if ready.State != SessionReady {
		t.Fatalf("session after renewed idle = %+v", ready)
	}
	now = now.Add(3 * time.Second)
	if err = broker.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	expired, _ := broker.Status(context.Background(), testOwner(), session.ID)
	if expired.State != SessionExpired {
		t.Fatalf("session after absolute expiry = %+v", expired)
	}
}

func TestBrokerExecutePreparedPersistsAcceptanceAndReturnsTerminalResultIdempotently(t *testing.T) {
	store := NewMemoryStore()
	broker := lifecycleTestBroker(t, admittedBrowserConfig(), store, &fakeWorkerFactory{})
	owner := testOwner()
	session, err := broker.Open(context.Background(), OpenRequest{Owner: owner, Target: "gateway", Profile: "managed"})
	if err != nil {
		t.Fatal(err)
	}
	invocation := preparedInvocation(session, "invocation_once")
	if err = store.CreateInvocation(context.Background(), invocation); err != nil {
		t.Fatal(err)
	}
	calls := 0
	execute := func(context.Context) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"ok":true}`), nil
	}
	first, err := broker.ExecutePrepared(context.Background(), owner, invocation.ID, invocation.ActionHash, execute)
	if err != nil || first.State != InvocationSucceeded || calls != 1 {
		t.Fatalf("first ExecutePrepared() = %+v, %v; calls = %d", first, err, calls)
	}
	second, err := broker.ExecutePrepared(context.Background(), owner, invocation.ID, invocation.ActionHash, execute)
	if err != nil || second.State != InvocationSucceeded || calls != 1 ||
		string(second.TerminalResult) != `{"ok":true}` {
		t.Fatalf("second ExecutePrepared() = %+v, %v; calls = %d", second, err, calls)
	}
}

func TestBrokerExecutePreparedNeverReplaysAcceptedInvocation(t *testing.T) {
	store := NewMemoryStore()
	broker := lifecycleTestBroker(t, admittedBrowserConfig(), store, &fakeWorkerFactory{})
	owner := testOwner()
	session, err := broker.Open(context.Background(), OpenRequest{Owner: owner, Target: "gateway", Profile: "managed"})
	if err != nil {
		t.Fatal(err)
	}
	invocation := preparedInvocation(session, "invocation_accepted")
	if err = store.CreateInvocation(context.Background(), invocation); err != nil {
		t.Fatal(err)
	}
	invocation.State = InvocationAccepted
	invocation.Revision = 2
	invocation.AcceptedAt = invocation.UpdatedAt + 1
	invocation.UpdatedAt = invocation.AcceptedAt
	if err = store.UpdateInvocation(context.Background(), 1, invocation); err != nil {
		t.Fatal(err)
	}
	calls := 0
	got, err := broker.ExecutePrepared(
		context.Background(),
		owner,
		invocation.ID,
		invocation.ActionHash,
		func(context.Context) (json.RawMessage, error) {
			calls++
			return json.RawMessage(`{"unexpected":true}`), nil
		},
	)
	if err != nil || got.State != InvocationUnknown || calls != 0 || got.Diagnostic == nil ||
		got.Diagnostic.FailureClass != OutcomeFailureWorkerUnavailable {
		t.Fatalf("ExecutePrepared() = %+v, %v; calls = %d", got, err, calls)
	}
}

func TestBrokerExecutePreparedDoesNotDispatchBeforeDurableAcceptance(t *testing.T) {
	store := &failInvocationAcceptanceStore{MemoryStore: NewMemoryStore()}
	broker := lifecycleTestBroker(t, admittedBrowserConfig(), store, &fakeWorkerFactory{})
	owner := testOwner()
	session, err := broker.Open(context.Background(), OpenRequest{Owner: owner, Target: "gateway", Profile: "managed"})
	if err != nil {
		t.Fatal(err)
	}
	invocation := preparedInvocation(session, "invocation_persist_failure")
	if err = store.CreateInvocation(context.Background(), invocation); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = broker.ExecutePrepared(
		context.Background(),
		owner,
		invocation.ID,
		invocation.ActionHash,
		func(context.Context) (json.RawMessage, error) {
			calls++
			return json.RawMessage(`{"unexpected":true}`), nil
		},
	)
	if !errors.Is(err, ErrStale) || calls != 0 {
		t.Fatalf("ExecutePrepared() error = %v, calls = %d; want durable failure and no dispatch", err, calls)
	}
}

func TestBrokerExecutePreparedDoesNotDispatchAfterCommittedAcceptanceWarning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "browser.json")
	store, err := NewFileStore(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	originalWrite := store.writeFile
	writes := 0
	store.writeFile = func(path string, data []byte, mode os.FileMode) error {
		writes++
		if writeErr := originalWrite(path, data, mode); writeErr != nil {
			return writeErr
		}
		if writes == 4 {
			return &fileutil.CommittedWriteError{Err: errors.New("directory sync failed")}
		}
		return nil
	}
	broker := lifecycleTestBroker(t, admittedBrowserConfig(), store, &fakeWorkerFactory{})
	owner := testOwner()
	session, err := broker.Open(context.Background(), OpenRequest{Owner: owner, Target: "gateway", Profile: "managed"})
	if err != nil {
		t.Fatal(err)
	}
	invocation := preparedInvocation(session, "invocation_committed_acceptance")
	if err = store.CreateInvocation(context.Background(), invocation); err != nil {
		t.Fatal(err)
	}
	calls := 0
	execute := func(context.Context) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"unexpected":true}`), nil
	}
	if _, err = broker.ExecutePrepared(
		context.Background(),
		owner,
		invocation.ID,
		invocation.ActionHash,
		execute,
	); !fileutil.IsCommittedWriteError(
		err,
	) {
		t.Fatalf("first ExecutePrepared() error = %v, want committed write warning", err)
	}
	if calls != 0 {
		t.Fatalf("executor calls after committed acceptance warning = %d", calls)
	}
	got, err := broker.ExecutePrepared(context.Background(), owner, invocation.ID, invocation.ActionHash, execute)
	if err != nil || got.State != InvocationUnknown || calls != 0 || got.Diagnostic == nil ||
		got.Diagnostic.FailureClass != OutcomeFailureWorkerUnavailable {
		t.Fatalf("second ExecutePrepared() = %+v, %v; calls = %d", got, err, calls)
	}
}

func TestBrokerExecutePreparedCancellationBoundary(t *testing.T) {
	for _, test := range []struct {
		name           string
		cancelBefore   bool
		wantState      InvocationState
		wantAcceptedAt bool
	}{
		{name: "before acceptance", cancelBefore: true, wantState: InvocationCanceled},
		{name: "after acceptance", wantState: InvocationUnknown, wantAcceptedAt: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore()
			broker := lifecycleTestBroker(t, admittedBrowserConfig(), store, &fakeWorkerFactory{})
			owner := testOwner()
			session, err := broker.Open(
				context.Background(),
				OpenRequest{Owner: owner, Target: "gateway", Profile: "managed"},
			)
			if err != nil {
				t.Fatal(err)
			}
			invocation := preparedInvocation(session, "invocation_cancel_"+strings.ReplaceAll(test.name, " ", "_"))
			if err = store.CreateInvocation(context.Background(), invocation); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			if test.cancelBefore {
				cancel()
			}
			calls := 0
			got, err := broker.ExecutePrepared(
				ctx,
				owner,
				invocation.ID,
				invocation.ActionHash,
				func(context.Context) (json.RawMessage, error) {
					calls++
					cancel()
					return nil, context.Canceled
				},
			)
			if err != nil || got.State != test.wantState || (got.AcceptedAt != 0) != test.wantAcceptedAt {
				t.Fatalf("ExecutePrepared() = %+v, %v", got, err)
			}
			wantCalls := 1
			if test.cancelBefore {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("executor calls = %d, want %d", calls, wantCalls)
			}
		})
	}
}

func TestBrokerExecutePreparedUsesConfiguredDeadline(t *testing.T) {
	root := admittedBrowserConfig()
	root.Tools.Browser.Limits.ActionSeconds = 1
	store := NewMemoryStore()
	broker := lifecycleTestBroker(t, root, store, &fakeWorkerFactory{})
	owner := testOwner()
	session, err := broker.Open(context.Background(), OpenRequest{Owner: owner, Target: "gateway", Profile: "managed"})
	if err != nil {
		t.Fatal(err)
	}
	invocation := preparedInvocation(session, "invocation_deadline")
	if err = store.CreateInvocation(context.Background(), invocation); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	got, err := broker.ExecutePrepared(
		context.Background(),
		owner,
		invocation.ID,
		invocation.ActionHash,
		func(ctx context.Context) (json.RawMessage, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)
	if err != nil || got.State != InvocationUnknown || got.AcceptedAt == 0 {
		t.Fatalf("ExecutePrepared() = %+v, %v", got, err)
	}
	if elapsed := time.Since(started); elapsed < 900*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("configured action deadline elapsed = %s", elapsed)
	}
}

func TestBrokerCloseMarksAcceptedInvocationUnknown(t *testing.T) {
	store := NewMemoryStore()
	broker := lifecycleTestBroker(t, admittedBrowserConfig(), store, &fakeWorkerFactory{})
	owner := testOwner()
	session, err := broker.Open(context.Background(), OpenRequest{Owner: owner, Target: "gateway", Profile: "managed"})
	if err != nil {
		t.Fatal(err)
	}
	invocation := preparedInvocation(session, "invocation_close")
	if err = store.CreateInvocation(context.Background(), invocation); err != nil {
		t.Fatal(err)
	}
	invocation.State = InvocationAccepted
	invocation.Revision++
	invocation.AcceptedAt = invocation.UpdatedAt + 1
	invocation.UpdatedAt = invocation.AcceptedAt
	if err = store.UpdateInvocation(context.Background(), 1, invocation); err != nil {
		t.Fatal(err)
	}
	if _, err = broker.Close(context.Background(), owner, session.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetInvocation(context.Background(), invocation.ID)
	if err != nil || stored.State != InvocationUnknown || stored.SafeFailure != "session_closed" {
		t.Fatalf("invocation after close = %+v, %v", stored, err)
	}
}

func TestBrokerSweepPrunesOnlyExpiredTerminalInvocations(t *testing.T) {
	root := admittedBrowserConfig()
	root.Tools.Browser.Limits.RetentionSecs = 5
	store := NewMemoryStore()
	broker := lifecycleTestBroker(t, root, store, &fakeWorkerFactory{})
	now := time.Unix(3_000, 0).UTC()
	broker.now = func() time.Time { return now }
	owner := testOwner()
	session, err := broker.Open(context.Background(), OpenRequest{Owner: owner, Target: "gateway", Profile: "managed"})
	if err != nil {
		t.Fatal(err)
	}
	invocation := preparedInvocation(session, "invocation_retention")
	if err = store.CreateInvocation(context.Background(), invocation); err != nil {
		t.Fatal(err)
	}
	invocation.State = InvocationCanceled
	invocation.Revision++
	invocation.UpdatedAt = invocation.CreatedAt + 1
	invocation.CompletedAt = invocation.UpdatedAt
	invocation.SafeFailure = "canceled"
	if err = store.UpdateInvocation(context.Background(), 1, invocation); err != nil {
		t.Fatal(err)
	}
	now = time.Unix(0, invocation.CompletedAt).Add(6 * time.Second)
	if err = broker.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = store.GetInvocation(context.Background(), invocation.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetInvocation() after retention error = %v, want ErrNotFound", err)
	}
}

func TestBrokerSweepPrunesTerminalSessionsAfterRetention(t *testing.T) {
	root := admittedBrowserConfig()
	root.Tools.Browser.Limits.RetentionSecs = 5
	store := NewMemoryStore()
	broker := lifecycleTestBroker(t, root, store, &fakeWorkerFactory{})
	now := time.Unix(4_000, 0).UTC()
	broker.now = func() time.Time { return now }
	owner := testOwner()
	session, err := broker.Open(context.Background(), OpenRequest{
		Owner: owner, Target: "gateway", Profile: "managed",
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation := preparedInvocation(session, "invocation_session_retention")
	if err = store.CreateInvocation(context.Background(), invocation); err != nil {
		t.Fatal(err)
	}
	invocation.State = InvocationCanceled
	invocation.Revision++
	invocation.UpdatedAt = invocation.CreatedAt + 1
	invocation.CompletedAt = invocation.UpdatedAt
	invocation.SafeFailure = "canceled"
	if err = store.UpdateInvocation(context.Background(), 1, invocation); err != nil {
		t.Fatal(err)
	}
	session, err = broker.Close(context.Background(), owner, session.ID)
	if err != nil {
		t.Fatal(err)
	}

	now = time.Unix(0, session.UpdatedAt).Add(4 * time.Second)
	if err = broker.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = store.GetSession(context.Background(), session.ID); err != nil {
		t.Fatalf("GetSession() before retention error = %v", err)
	}

	now = time.Unix(0, session.UpdatedAt).Add(6 * time.Second)
	if err = broker.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = store.GetInvocation(context.Background(), invocation.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetInvocation() after retention error = %v, want ErrNotFound", err)
	}
	if _, err = store.GetSession(context.Background(), session.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSession() after retention error = %v, want ErrNotFound", err)
	}
}

func TestBrokerSweepRecoversFileStoreCapacityFromRetainedTerminalSession(t *testing.T) {
	root := admittedBrowserConfig()
	root.Tools.Browser.Limits.RetentionSecs = 5
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(filepath.Join(directory, "browser.json"), 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	broker := lifecycleTestBroker(t, root, store, &fakeWorkerFactory{})
	now := time.Unix(5_000, 0).UTC()
	broker.now = func() time.Time { return now }
	owner := testOwner()
	session, err := broker.Open(context.Background(), OpenRequest{
		Owner: owner, Target: "gateway", Profile: "managed",
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation := preparedInvocation(session, "invocation_capacity_retention")
	if err = store.CreateInvocation(context.Background(), invocation); err != nil {
		t.Fatal(err)
	}
	invocation.State = InvocationCanceled
	invocation.Revision++
	invocation.UpdatedAt = invocation.CreatedAt + 1
	invocation.CompletedAt = invocation.UpdatedAt
	invocation.SafeFailure = "canceled"
	if err = store.UpdateInvocation(context.Background(), 1, invocation); err != nil {
		t.Fatal(err)
	}
	session, err = broker.Close(context.Background(), owner, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = broker.Open(context.Background(), OpenRequest{
		Owner: owner, Target: "gateway", Profile: "managed",
	}); !errors.Is(err, ErrStoreFull) {
		t.Fatalf("Open() at capacity error = %v, want ErrStoreFull", err)
	}

	now = time.Unix(0, session.UpdatedAt).Add(6 * time.Second)
	if err = broker.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = broker.Open(context.Background(), OpenRequest{
		Owner: owner, Target: "gateway", Profile: "managed",
	}); err != nil {
		t.Fatalf("Open() after retention sweep error = %v", err)
	}
}

func TestBrokerPolicyChangeInvalidatesSession(t *testing.T) {
	factory := &fakeWorkerFactory{}
	broker := lifecycleTestBroker(t, admittedBrowserConfig(), NewMemoryStore(), factory)
	session, err := broker.Open(
		context.Background(),
		OpenRequest{Owner: testOwner(), Target: "gateway", Profile: "managed"},
	)
	if err != nil {
		t.Fatal(err)
	}
	broker.policyRevision = strings.Repeat("b", 64)
	got, err := broker.Status(context.Background(), testOwner(), session.ID)
	if err != nil || got.State != SessionLost || got.SafeFailure != "policy_changed" || factory.workers[0].closed != 1 {
		t.Fatalf("Status() = %+v, %v; worker = %+v", got, err, factory.workers[0])
	}
}

func TestBrokerProfileRevisionChangeInvalidatesSession(t *testing.T) {
	factory := &fakeWorkerFactory{}
	broker := lifecycleTestBroker(t, admittedBrowserConfig(), NewMemoryStore(), factory)
	session, err := broker.Open(
		context.Background(),
		OpenRequest{Owner: testOwner(), Target: "gateway", Profile: "managed"},
	)
	if err != nil {
		t.Fatal(err)
	}
	target := broker.config.Targets["gateway"]
	profile := target.Profiles["managed"]
	profile.Revision = "managed-v2"
	target.Profiles["managed"] = profile
	broker.config.Targets["gateway"] = target

	got, err := broker.Status(context.Background(), testOwner(), session.ID)
	if err != nil || got.State != SessionLost || got.SafeFailure != "policy_changed" ||
		factory.workers[0].closed != 1 {
		t.Fatalf("Status() = %+v, %v; worker = %+v", got, err, factory.workers[0])
	}
}

type managedRevocationTestCase struct {
	name             string
	mutate           func(*config.BrowserToolsConfig)
	replacementOwner Owner
	replacementReady bool
}

func managedRevocationTestCases() []managedRevocationTestCase {
	owner := testOwner()
	actorReplacement := owner
	actorReplacement.ActorID = OpaqueActorID("telegram:replacement")
	agentReplacement := owner
	agentReplacement.AgentID = OpaqueAgentID("replacement")
	return []managedRevocationTestCase{
		{
			name: "profile disabled",
			mutate: func(browser *config.BrowserToolsConfig) {
				browser.Enabled = false
				browser.Agents = nil
				browser.Targets = nil
			},
		},
		{
			name: "profile revision changed",
			mutate: func(browser *config.BrowserToolsConfig) {
				target := browser.Targets["gateway"]
				profile := target.Profiles["managed"]
				profile.Revision = "managed-v2"
				target.Profiles["managed"] = profile
				browser.Targets["gateway"] = target
			},
			replacementOwner: owner, replacementReady: true,
		},
		{
			name: "actor grant removed",
			mutate: func(browser *config.BrowserToolsConfig) {
				target := browser.Targets["gateway"]
				profile := target.Profiles["managed"]
				profile.AllowedActors = []string{"telegram:replacement"}
				target.Profiles["managed"] = profile
				browser.Targets["gateway"] = target
			},
			replacementOwner: actorReplacement, replacementReady: true,
		},
		{
			name: "agent grant removed",
			mutate: func(browser *config.BrowserToolsConfig) {
				browser.Agents = []string{"replacement"}
				target := browser.Targets["gateway"]
				profile := target.Profiles["managed"]
				profile.AllowedAgents = []string{"replacement"}
				target.Profiles["managed"] = profile
				browser.Targets["gateway"] = target
			},
			replacementOwner: agentReplacement, replacementReady: true,
		},
		{
			name: "runtime mapping changed",
			mutate: func(browser *config.BrowserToolsConfig) {
				target := browser.Targets["gateway"]
				profile := target.Profiles["managed"]
				profile.Runtime.ProfileDirectory = "/var/lib/mintclaw/browser/replacement"
				profile.Runtime.LockFile = "/var/lib/mintclaw/browser-replacement.lock"
				target.Profiles["managed"] = profile
				browser.Targets["gateway"] = target
			},
			replacementOwner: owner, replacementReady: true,
		},
	}
}

func applyManagedRevocation(t *testing.T, broker *Broker, test managedRevocationTestCase) {
	t.Helper()
	next := admittedBrowserConfig()
	test.mutate(&next.Tools.Browser)
	replacement, err := NewBroker(next, NewMemoryStore(), &fakeWorkerFactory{})
	if err != nil {
		t.Fatal(err)
	}
	broker.config = replacement.config
	broker.policyRevision = replacement.policyRevision
}

func TestBrokerManagedRevocationClosesBeforeReplacementAuthority(t *testing.T) {
	for _, test := range managedRevocationTestCases() {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore()
			factory := &fakeWorkerFactory{}
			broker := lifecycleTestBroker(t, admittedBrowserConfig(), store, factory)
			owner := testOwner()
			session, err := broker.Open(t.Context(), OpenRequest{
				Owner: owner, Target: "gateway", Profile: "managed",
			})
			if err != nil {
				t.Fatal(err)
			}
			applyManagedRevocation(t, broker, test)

			lost, err := broker.Status(t.Context(), owner, session.ID)
			if err != nil || lost.State != SessionLost || lost.SafeFailure != "policy_changed" ||
				factory.workers[0].closed != 1 {
				t.Fatalf("revoked Status() = %+v, %v; worker = %+v", lost, err, factory.workers[0])
			}
			if _, err = broker.Open(t.Context(), OpenRequest{
				Owner: owner, Target: "gateway", Profile: "managed",
			}); test.replacementReady && test.replacementOwner.Equal(owner) {
				if err != nil {
					t.Fatalf("replacement Open() error = %v", err)
				}
			} else if !errors.Is(err, ErrDenied) {
				t.Fatalf("revoked owner Open() error = %v, want denied", err)
			}
			if test.replacementReady && !test.replacementOwner.Equal(owner) {
				if _, err = broker.Open(t.Context(), OpenRequest{
					Owner: test.replacementOwner, Target: "gateway", Profile: "managed",
				}); err != nil {
					t.Fatalf("replacement principal Open() error = %v", err)
				}
			}
		})
	}
}

func TestBrokerManagedRevocationCleanupFailureQuarantinesReplacement(t *testing.T) {
	for _, test := range managedRevocationTestCases() {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore()
			factory := &fakeWorkerFactory{}
			broker := lifecycleTestBroker(t, admittedBrowserConfig(), store, factory)
			owner := testOwner()
			session, err := broker.Open(t.Context(), OpenRequest{
				Owner: owner, Target: "gateway", Profile: "managed",
			})
			if err != nil {
				t.Fatal(err)
			}
			factory.workers[0].closeErr = errors.New("cleanup outcome unknown")
			applyManagedRevocation(t, broker, test)

			if _, err = broker.Status(t.Context(), owner, session.ID); !errors.Is(err, ErrWorkerUnavailable) {
				t.Fatalf("revoked Status() error = %v, want worker unavailable", err)
			}
			stored, err := store.GetSession(t.Context(), session.ID)
			if err != nil || stored.State != SessionClosing {
				t.Fatalf("quarantined session = %+v, %v", stored, err)
			}
			if test.replacementReady {
				if _, err = broker.Open(t.Context(), OpenRequest{
					Owner: test.replacementOwner, Target: "gateway", Profile: "managed",
				}); !errors.Is(err, ErrBusy) {
					t.Fatalf("replacement Open() during quarantine error = %v, want busy", err)
				}
			}

			factory.workers[0].closeErr = nil
			lost, err := broker.Status(t.Context(), owner, session.ID)
			if err != nil || lost.State != SessionLost || lost.SafeFailure != "policy_changed" {
				t.Fatalf("cleanup retry Status() = %+v, %v", lost, err)
			}
			if test.replacementReady {
				if _, err = broker.Open(t.Context(), OpenRequest{
					Owner: test.replacementOwner, Target: "gateway", Profile: "managed",
				}); err != nil {
					t.Fatalf("replacement Open() after cleanup error = %v", err)
				}
			}
		})
	}
}

func TestBrokerSessionAuthorityRechecksEveryManagedGrant(t *testing.T) {
	profileMutation := func(mutate func(*config.BrowserProfileConfig)) func(*config.BrowserToolsConfig) {
		return func(browser *config.BrowserToolsConfig) {
			target := browser.Targets["gateway"]
			profile := target.Profiles["managed"]
			mutate(&profile)
			target.Profiles["managed"] = profile
			browser.Targets["gateway"] = target
		}
	}
	tests := []struct {
		name   string
		mutate func(*config.BrowserToolsConfig)
	}{
		{name: "browser disabled", mutate: func(browser *config.BrowserToolsConfig) { browser.Enabled = false }},
		{name: "target disabled", mutate: func(browser *config.BrowserToolsConfig) {
			target := browser.Targets["gateway"]
			target.Enabled = false
			browser.Targets["gateway"] = target
		}},
		{name: "profile disabled", mutate: profileMutation(func(profile *config.BrowserProfileConfig) {
			profile.Enabled = false
		})},
		{name: "profile revision changed", mutate: profileMutation(func(profile *config.BrowserProfileConfig) {
			profile.Revision = "managed-v2"
		})},
		{name: "global agent grant removed", mutate: func(browser *config.BrowserToolsConfig) {
			browser.Agents = []string{OpaqueAgentID("replacement")}
		}},
		{name: "profile agent grant removed", mutate: profileMutation(func(profile *config.BrowserProfileConfig) {
			profile.AllowedAgents = []string{OpaqueAgentID("replacement")}
		})},
		{name: "profile actor grant removed", mutate: profileMutation(func(profile *config.BrowserProfileConfig) {
			profile.AllowedActors = []string{OpaqueActorID("telegram:replacement")}
		})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broker := lifecycleTestBroker(
				t, admittedBrowserConfig(), NewMemoryStore(), &fakeWorkerFactory{},
			)
			session, err := broker.Open(t.Context(), OpenRequest{
				Owner: testOwner(), Target: "gateway", Profile: "managed",
			})
			if err != nil {
				t.Fatal(err)
			}
			if !broker.sessionAuthorityCurrent(session) {
				t.Fatal("initial session authority is not current")
			}
			test.mutate(&broker.config)
			if broker.sessionAuthorityCurrent(session) {
				t.Fatal("isolated revoked authority remained current")
			}
		})
	}
}

func TestBrokerManagedRevocationBlocksControllerTransitions(t *testing.T) {
	for _, test := range managedRevocationTestCases() {
		t.Run(test.name+"/handoff", func(t *testing.T) {
			factory := &fakeWorkerFactory{}
			broker := lifecycleTestBroker(t, admittedBrowserConfig(), NewMemoryStore(), factory)
			owner := testOwner()
			session, err := broker.Open(t.Context(), OpenRequest{
				Owner: owner, Target: "gateway", Profile: "managed",
			})
			if err != nil {
				t.Fatal(err)
			}
			applyManagedRevocation(t, broker, test)
			lost, err := broker.Handoff(t.Context(), owner, session.ID)
			if err != nil || lost.State != SessionLost || lost.SafeFailure != "policy_changed" ||
				factory.workers[0].beginHumanCalls != 0 || factory.workers[0].closed != 1 {
				t.Fatalf("revoked Handoff() = %+v, %v; worker = %+v", lost, err, factory.workers[0])
			}
		})

		t.Run(test.name+"/release", func(t *testing.T) {
			factory := &fakeWorkerFactory{}
			broker := lifecycleTestBroker(t, admittedBrowserConfig(), NewMemoryStore(), factory)
			owner := testOwner()
			session, err := broker.Open(t.Context(), OpenRequest{
				Owner: owner, Target: "gateway", Profile: "managed",
			})
			if err != nil {
				t.Fatal(err)
			}
			human, err := broker.Handoff(t.Context(), owner, session.ID)
			if err != nil {
				t.Fatal(err)
			}
			applyManagedRevocation(t, broker, test)
			lost, err := broker.ReleaseHandoff(t.Context(), owner, human.ID)
			if err != nil || lost.State != SessionLost || lost.SafeFailure != "policy_changed" ||
				factory.workers[0].beginHumanCalls != 1 || factory.workers[0].endHumanCalls != 0 ||
				factory.workers[0].closed != 1 {
				t.Fatalf("revoked ReleaseHandoff() = %+v, %v; worker = %+v", lost, err, factory.workers[0])
			}
		})

		t.Run(test.name+"/resume", func(t *testing.T) {
			factory := &fakeWorkerFactory{}
			broker := lifecycleTestBroker(t, admittedBrowserConfig(), NewMemoryStore(), factory)
			owner := testOwner()
			session, err := broker.Open(t.Context(), OpenRequest{
				Owner: owner, Target: "gateway", Profile: "managed",
			})
			if err != nil {
				t.Fatal(err)
			}
			human, err := broker.Handoff(t.Context(), owner, session.ID)
			if err != nil {
				t.Fatal(err)
			}
			pending, err := broker.ReleaseHandoff(t.Context(), owner, human.ID)
			if err != nil || pending.Controller != ControllerResumePending {
				t.Fatalf("ReleaseHandoff() = %+v, %v", pending, err)
			}
			applyManagedRevocation(t, broker, test)
			lost, err := broker.Resume(t.Context(), owner, pending.ID)
			if err != nil || lost.State != SessionLost || lost.SafeFailure != "policy_changed" ||
				factory.workers[0].beginHumanCalls != 1 || factory.workers[0].endHumanCalls != 1 ||
				factory.workers[0].closed != 1 {
				t.Fatalf("revoked Resume() = %+v, %v; worker = %+v", lost, err, factory.workers[0])
			}
		})
	}
}

func TestBrokerManagedRevocationRestartDoesNotRestoreRetiredAuthority(t *testing.T) {
	for _, test := range managedRevocationTestCases() {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state", "browser.json")
			store, err := NewFileStore(path, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			owner := testOwner()
			original := lifecycleTestBroker(t, admittedBrowserConfig(), store, &fakeWorkerFactory{})
			session, err := original.Open(t.Context(), OpenRequest{
				Owner: owner, Target: "gateway", Profile: "managed",
			})
			if err != nil {
				store.Close()
				t.Fatal(err)
			}
			store.Close() // Simulate process loss before the in-memory worker can be reconciled.

			next := admittedBrowserConfig()
			test.mutate(&next.Tools.Browser)
			reopened, err := NewFileStore(path, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(reopened.Close)
			recovered, err := NewBroker(next, reopened, &fakeWorkerFactory{})
			if err != nil {
				t.Fatal(err)
			}
			recovered.newID = func() (string, error) { return "browser_session_replacement", nil }
			if err = recovered.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			lost, err := reopened.GetSession(t.Context(), session.ID)
			if err != nil || lost.State != SessionLost || lost.SafeFailure != "gateway_restarted" {
				t.Fatalf("recovered retired session = %+v, %v", lost, err)
			}

			if _, err = recovered.Open(t.Context(), OpenRequest{
				Owner: owner, Target: "gateway", Profile: "managed",
			}); test.replacementReady && test.replacementOwner.Equal(owner) {
				if err != nil {
					t.Fatalf("replacement Open() error = %v", err)
				}
			} else if !errors.Is(err, ErrDenied) {
				t.Fatalf("retired owner Open() error = %v, want denied", err)
			}
			if test.replacementReady && !test.replacementOwner.Equal(owner) {
				if _, err = recovered.Open(t.Context(), OpenRequest{
					Owner: test.replacementOwner, Target: "gateway", Profile: "managed",
				}); err != nil {
					t.Fatalf("replacement principal Open() error = %v", err)
				}
			}
		})
	}
}

func lifecycleTestBroker(t *testing.T, cfg *config.Config, store Store, factory WorkerFactory) *Broker {
	t.Helper()
	broker, err := NewBroker(cfg, store, factory)
	if err != nil {
		t.Fatal(err)
	}
	counter := 0
	broker.newID = func() (string, error) {
		counter++
		return "lifecycle_session_" + string(rune('a'+counter)), nil
	}
	return broker
}

func preparedInvocation(session Session, id string) Invocation {
	created := session.UpdatedAt + 1
	return Invocation{
		ID: id, SessionID: session.ID, Owner: session.Owner,
		ActionHash: strings.Repeat("a", 64), Effect: EffectRead,
		State: InvocationPrepared, Revision: 1, CreatedAt: created,
		UpdatedAt: created, ExpiresAt: created + int64(time.Minute),
	}
}

type failInvocationAcceptanceStore struct {
	*MemoryStore
}

func (store *failInvocationAcceptanceStore) UpdateInvocation(
	ctx context.Context,
	expected uint64,
	next Invocation,
) error {
	if next.State == InvocationAccepted {
		return ErrStale
	}
	return store.MemoryStore.UpdateInvocation(ctx, expected, next)
}
