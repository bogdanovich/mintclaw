package companion

import (
	"errors"
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
	acceptedAfter, found := recovered.Lookup(accepted.JobID)
	if !found || acceptedAfter.State != JobAccepted {
		t.Fatalf("accepted recovery = %#v, found %v", acceptedAfter, found)
	}
	launchedAfter, found := recovered.Lookup(launched.JobID)
	if !found || launchedAfter.State != JobUnknown || launchedAfter.Stdout.Bytes != 13 ||
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
	if _, _, err := store.Accept(testAcceptedJobRecord("active")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Accept(testAcceptedJobRecord("second")); !errors.Is(err, ErrJobStoreFull) {
		t.Fatalf("second Accept() error = %v", err)
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
		ProfileRevision: "jobs-v1",
		Owner: JobOwner{
			AgentID: "agent_test", SessionID: "session_test", ActorID: "actor_test",
		},
		State: JobAccepted, CancelGuarantee: JobCancelProcessGroup,
		CreatedAt: now, UpdatedAt: now,
	}
}
