package companion

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJobStoreDeduplicatesExactStartAndRejectsChangedBinding(t *testing.T) {
	store := newTestJobStore(t)
	record := testAcceptedJobRecord("one")
	accepted, existing, err := store.Accept(record)
	if err != nil || existing || accepted.JobID != record.JobID {
		t.Fatalf("Accept() = %#v, existing %v, error %v", accepted, existing, err)
	}
	duplicate, existing, err := store.Accept(record)
	if err != nil || !existing || duplicate.JobID != record.JobID {
		t.Fatalf("duplicate Accept() = %#v, existing %v, error %v", duplicate, existing, err)
	}
	changed := record
	changed.JobID = "job_changed"
	changed.PlanHash = strings.Repeat("b", 64)
	if _, _, err := store.Accept(changed); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("changed Accept() error = %v", err)
	}
}

func TestJobStoreRestartPreservesAcceptedAndReconcilesLaunchBoundary(t *testing.T) {
	root := filepath.Join(t.TempDir(), "jobs")
	store, err := NewJobStore(root, JobStoreLimits{
		Records: 8, IndexBytes: 1024 * 1024, PayloadBytes: 1024 * 1024,
		Retention: DefaultJobRetention,
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted := testAcceptedJobRecord("accepted")
	if _, _, err := store.Accept(accepted); err != nil {
		t.Fatal(err)
	}
	launched := testAcceptedJobRecord("launched")
	if _, _, err := store.Accept(launched); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkLaunchAttempted(launched.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkRunning(launched.JobID, time.Minute); err != nil {
		t.Fatal(err)
	}
	stdout, err := store.CreateFile(jobLogFileName(launched.JobID, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdout.WriteString("retained log\n"); err != nil {
		t.Fatal(err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	store.Close()

	recovered, err := NewJobStore(root, JobStoreLimits{
		Records: 8, IndexBytes: 1024 * 1024, PayloadBytes: 1024 * 1024,
		Retention: DefaultJobRetention,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(recovered.Close)
	acceptedAfter, found, err := recovered.Lookup(accepted.JobID)
	if err != nil || !found || acceptedAfter.State != JobAccepted {
		t.Fatalf("accepted recovery = %#v, found %v", acceptedAfter, found)
	}
	launchedAfter, found, err := recovered.Lookup(launched.JobID)
	if err != nil || !found || launchedAfter.State != JobUnknown || launchedAfter.Stdout.Bytes != 13 ||
		launchedAfter.FailureCode != "COMPANION_RESTART" {
		t.Fatalf("launched recovery = %#v, found %v", launchedAfter, found)
	}
	duplicate, existing, err := recovered.Accept(launched)
	if err != nil || !existing || duplicate.State != JobUnknown {
		t.Fatalf("recovered duplicate = %#v, existing %v, error %v", duplicate, existing, err)
	}
}

func TestJobStoreBoundsAggregatePayloadReservations(t *testing.T) {
	root := filepath.Join(t.TempDir(), "jobs")
	store, err := NewJobStore(root, JobStoreLimits{
		Records: 8, IndexBytes: 1024 * 1024, PayloadBytes: 1024,
		Retention: DefaultJobRetention,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.ReservePayload("job_one", 800); err != nil {
		t.Fatal(err)
	}
	if err := store.ReservePayload("job_two", 300); !errors.Is(err, ErrJobStoreFull) {
		t.Fatalf("over-capacity reservation error = %v", err)
	}
	if err := store.ReleasePayload("job_one"); err != nil {
		t.Fatal(err)
	}
	if err := store.ReservePayload("job_two", 300); err != nil {
		t.Fatalf("reservation after release: %v", err)
	}
}

func TestJobStoreDoesNotPruneProtectedActiveRecords(t *testing.T) {
	root := filepath.Join(t.TempDir(), "jobs")
	store, err := NewJobStore(root, JobStoreLimits{
		Records: 1, IndexBytes: 1024 * 1024, PayloadBytes: 1024 * 1024,
		Retention: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	active := testAcceptedJobRecord("active")
	active.RetentionSeconds = int(time.Minute / time.Second)
	if _, _, err := store.Accept(active); err != nil {
		t.Fatal(err)
	}
	second := testAcceptedJobRecord("second")
	second.RetentionSeconds = int(time.Minute / time.Second)
	if _, _, err := store.Accept(second); !errors.Is(err, ErrJobStoreFull) {
		t.Fatalf("second Accept() error = %v", err)
	}
}

func TestJobStoreLookupPrunesExpiredWithoutAnotherAccept(t *testing.T) {
	root := filepath.Join(t.TempDir(), "jobs")
	store, err := NewJobStore(root, JobStoreLimits{
		Records: 8, IndexBytes: 1024 * 1024, PayloadBytes: 1024 * 1024,
		Retention: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	base := time.Unix(1000, 0)
	store.now = func() time.Time { return base }
	record := testAcceptedJobRecord("expired-read")
	record.CreatedAt = base.UnixNano()
	record.UpdatedAt = base.UnixNano()
	record.RetentionSeconds = int(time.Minute / time.Second)
	if _, _, err := store.Accept(record); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkFailedBeforeLaunch(record.JobID, "TEST_COMPLETE"); err != nil {
		t.Fatal(err)
	}
	log, err := store.CreateFile(jobLogFileName(record.JobID, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.WriteString("expired log"); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return base.Add(time.Minute + time.Second) }
	if _, found, err := store.Lookup(record.JobID); err != nil || found {
		t.Fatalf("expired Lookup() found = %v, error %v", found, err)
	}
	if _, err := os.Stat(filepath.Join(root, jobLogFileName(record.JobID, false))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired log retained after lookup: %v", err)
	}
	index, err := os.ReadFile(filepath.Join(root, "index.json"))
	if err != nil || strings.Contains(string(index), record.JobID) {
		t.Fatalf("expired job retained in index %q, error %v", index, err)
	}
}

func TestJobStoreStartupPrunesExpiredWithoutAnotherAccept(t *testing.T) {
	root := filepath.Join(t.TempDir(), "jobs")
	store, err := NewJobStore(root, JobStoreLimits{
		Records: 8, IndexBytes: 1024 * 1024, PayloadBytes: 1024 * 1024,
		Retention: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-2 * time.Minute)
	store.now = func() time.Time { return base }
	record := testAcceptedJobRecord("expired-restart")
	record.CreatedAt = base.UnixNano()
	record.UpdatedAt = base.UnixNano()
	record.RetentionSeconds = int(time.Minute / time.Second)
	if _, _, err := store.Accept(record); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkFailedBeforeLaunch(record.JobID, "TEST_COMPLETE"); err != nil {
		t.Fatal(err)
	}
	store.Close()
	reloaded, err := NewJobStore(root, JobStoreLimits{
		Records: 8, IndexBytes: 1024 * 1024, PayloadBytes: 1024 * 1024,
		Retention: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reloaded.Close)
	if _, found, err := reloaded.Lookup(record.JobID); err != nil || found {
		t.Fatalf("startup retained expired job, found = %v, error %v", found, err)
	}
}

func newTestJobStore(t *testing.T) *JobStore {
	t.Helper()
	store, err := NewJobStore(filepath.Join(t.TempDir(), "jobs"), JobStoreLimits{
		Records: 8, IndexBytes: 1024 * 1024, PayloadBytes: 1024 * 1024,
		Retention: DefaultJobRetention,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

func testAcceptedJobRecord(suffix string) JobRecord {
	now := time.Now().UnixNano()
	return JobRecord{
		JobID: "job_" + suffix, StartInvocationID: "inv_" + suffix,
		StartIdempotencyKey: "idem_" + suffix, PlanHash: strings.Repeat("a", 64),
		ProfileAlias: "test-jobs", ProfileRevision: "jobs-v1",
		RetentionSeconds: int(DefaultJobRetention / time.Second),
		Owner: JobOwner{
			AgentID: "agent_test", SessionID: "session_test", ActorID: "actor_test",
		},
		State: JobAccepted, CancelGuarantee: JobCancelProcessGroup,
		CreatedAt: now, UpdatedAt: now,
	}
}
