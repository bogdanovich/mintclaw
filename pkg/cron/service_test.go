package cron

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestRecoveryWakeIsNotDroppedWhileSchedulerSuspended(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "jobs.json")

	cs := NewCronService(storePath, func(job *CronJob) (string, error) {
		return "ok", nil
	})

	// Simulate the loop having just snapshotted a suspended state (failed
	// load latched) and about to sleep for an hour: a recovery notify sent in
	// this snapshot-to-select window must queue a wake rather than be dropped.
	cs.mu.Lock()
	cs.loadErr = errors.New("simulated load failure")
	cs.mu.Unlock()

	cs.notify()

	cs.mu.RLock()
	pending := len(cs.wakeChan)
	cs.mu.RUnlock()
	if pending != 1 {
		t.Fatalf("recovery wake dropped while scheduler suspended: %d pending, want 1", pending)
	}

	// A repaired store must rearm the scheduler: runLoop is the sole receiver
	// of the wake, so the queued signal must be consumed by it on the next
	// select within a deadline.
	cs.mu.Lock()
	cs.loadErr = nil
	cs.running = true
	cs.stopChan = make(chan struct{})
	cs.mu.Unlock()
	go cs.runLoop(cs.stopChan)
	defer cs.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for {
		cs.mu.RLock()
		left := len(cs.wakeChan)
		cs.mu.RUnlock()
		if left == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scheduler did not consume the queued recovery wake")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSaveStore_FilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced on Windows")
	}

	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "cron", "jobs.json")

	cs := NewCronService(storePath, nil)

	_, err := cs.AddJob(
		"test",
		CronSchedule{Kind: "every", EveryMS: int64Ptr(60000)},
		"",
		"hello",
		"cli",
		"direct",
	)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	info, err := os.Stat(storePath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("cron store has permission %04o, want 0600", perm)
	}
}

func int64Ptr(v int64) *int64 {
	return &v
}

func setupService(handler JobHandler) (*CronService, string) {
	tmpFile := fmt.Sprintf("test_cron_%d.json", time.Now().UnixNano())
	cs := NewCronService(tmpFile, handler)
	return cs, tmpFile
}

func TestCronService_CRUD(t *testing.T) {
	cs, path := setupService(nil)
	defer os.Remove(path)

	// Test AddJob
	at := time.Now().Add(time.Hour).UnixMilli()
	job, err := cs.AddJob("Task1", CronSchedule{Kind: "at", AtMS: &at}, "", "msg", "ch", "to")
	if err != nil || job.ID == "" {
		t.Fatalf("AddJob failed: %v", err)
	}

	// Test ListJobs
	if len(cs.ListJobs(true)) != 1 {
		t.Error("ListJobs should return 1 job")
	}

	// Test UpdateJob
	job.Name = "UpdatedName"
	err = cs.UpdateJob(job)
	if err != nil || cs.store.Jobs[0].Name != "UpdatedName" {
		t.Error("UpdateJob failed")
	}

	// Test EnableJob
	if _, err := cs.EnableJob(job.ID, false); err != nil {
		t.Fatalf("EnableJob failed: %v", err)
	}
	if cs.store.Jobs[0].Enabled != false || cs.store.Jobs[0].State.NextRunAtMS != nil {
		t.Error("EnableJob(false) failed to clear state")
	}

	// Test RemoveJob
	removed := cs.RemoveJob(job.ID)
	if !removed || len(cs.store.Jobs) != 0 {
		t.Error("RemoveJob failed")
	}
}

func TestCronService_GetJobReturnsCopy(t *testing.T) {
	cs, path := setupService(nil)
	defer os.Remove(path)

	everyMS := int64(60_000)
	job, err := cs.AddJob(
		"Task1",
		CronSchedule{Kind: "every", EveryMS: &everyMS},
		"",
		"msg",
		"ch",
		"to",
	)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	if job.State.NextRunAtMS == nil {
		t.Fatal("expected initial next run")
	}
	nextRun := *job.State.NextRunAtMS

	got, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("GetJob should find job")
	}
	got.Name = "mutated"
	got.Payload.Message = "changed"
	if got.Schedule.EveryMS != nil {
		*got.Schedule.EveryMS = 120_000
	}
	if got.State.NextRunAtMS != nil {
		*got.State.NextRunAtMS = time.Now().Add(3 * time.Hour).UnixMilli()
	}

	again, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("GetJob should still find job")
	}
	if again.Name != "Task1" || again.Payload.Message != "msg" {
		t.Fatalf("GetJob should return a copy, got %+v", again)
	}
	if again.Schedule.EveryMS == nil || *again.Schedule.EveryMS != everyMS {
		t.Fatalf("GetJob should not alias schedule pointers, got %+v", again.Schedule)
	}
	if again.State.NextRunAtMS == nil || *again.State.NextRunAtMS != nextRun {
		t.Fatalf("GetJob should not alias state pointers, got %+v", again.State)
	}
}

func TestCronService_UpdateJobRecomputesNextRunOnScheduleOrEnabledChange(t *testing.T) {
	cs, path := setupService(nil)
	defer os.Remove(path)

	at := time.Now().Add(time.Hour).UnixMilli()
	job, err := cs.AddJob("Task1", CronSchedule{Kind: "at", AtMS: &at}, "", "msg", "ch", "to")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	if job.State.NextRunAtMS == nil {
		t.Fatal("expected initial next run")
	}
	initialNextRun := *job.State.NextRunAtMS

	everyMS := int64(30_000)
	job.Schedule = CronSchedule{Kind: "every", EveryMS: &everyMS}
	if err := cs.UpdateJob(job); err != nil {
		t.Fatalf("UpdateJob schedule failed: %v", err)
	}
	updated, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("updated job not found")
	}
	if updated.State.NextRunAtMS == nil {
		t.Fatal("expected recomputed next run after schedule change")
	}
	if *updated.State.NextRunAtMS == initialNextRun {
		t.Fatalf("next run should be recomputed, still %d", initialNextRun)
	}

	if _, err := cs.EnableJob(job.ID, false); err != nil {
		t.Fatalf("EnableJob(false) failed: %v", err)
	}
	disabled, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("disabled job not found")
	}
	disabled.Enabled = true
	if err := cs.UpdateJob(disabled); err != nil {
		t.Fatalf("UpdateJob enabled failed: %v", err)
	}
	reenabled, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("reenabled job not found")
	}
	if !reenabled.Enabled || reenabled.State.NextRunAtMS == nil {
		t.Fatalf("expected enabled job with next run, got %+v", reenabled)
	}
}

func TestCronService_UpdateJobPreservesRunStateOnPayloadOnlyChange(t *testing.T) {
	cs, path := setupService(nil)
	defer os.Remove(path)

	everyMS := int64(60_000)
	job, err := cs.AddJob(
		"Task1",
		CronSchedule{Kind: "every", EveryMS: &everyMS},
		"",
		"msg",
		"ch",
		"to",
	)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	lastRun := time.Now().Add(-time.Minute).UnixMilli()
	job.State.LastRunAtMS = &lastRun
	job.State.LastStatus = "ok"
	job.State.LastError = "previous"
	if job.State.NextRunAtMS == nil {
		t.Fatal("expected next run before update")
	}
	nextRun := *job.State.NextRunAtMS

	job.Payload.Message = "updated msg"
	if err := cs.UpdateJob(job); err != nil {
		t.Fatalf("UpdateJob failed: %v", err)
	}

	updated, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("updated job not found")
	}
	if updated.State.LastRunAtMS == nil || *updated.State.LastRunAtMS != lastRun {
		t.Fatalf("last run changed: %+v", updated.State)
	}
	if updated.State.LastStatus != "ok" || updated.State.LastError != "previous" {
		t.Fatalf("last status changed: %+v", updated.State)
	}
	if updated.State.NextRunAtMS == nil || *updated.State.NextRunAtMS != nextRun {
		t.Fatalf(
			"next run should be preserved: before=%d after=%+v",
			nextRun,
			updated.State.NextRunAtMS,
		)
	}
}

// 2. Test Cron Expression Calculation Logic
func TestCronService_ComputeNextRun(t *testing.T) {
	cs, path := setupService(nil)
	defer os.Remove(path)

	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC).UnixMilli()

	tests := []struct {
		name     string
		schedule CronSchedule
		wantNil  bool
	}{
		{"Valid Cron", CronSchedule{Kind: "cron", Expr: "0 * * * *"}, false},
		{"Invalid Cron", CronSchedule{Kind: "cron", Expr: "invalid"}, true},
		{"Every MS", CronSchedule{Kind: "every", EveryMS: int64Ptr(5000)}, false},
		{"At Future", CronSchedule{Kind: "at", AtMS: int64Ptr(now + 1000)}, false},
		{"At Past", CronSchedule{Kind: "at", AtMS: int64Ptr(now - 1000)}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cs.computeNextRun(&tt.schedule, now)
			if (got == nil) != tt.wantNil {
				t.Errorf("%s: got %v, wantNil %v", tt.name, got, tt.wantNil)
			}
		})
	}
}

func TestCronService_ComputeNextRun_UsesScheduleTimezone(t *testing.T) {
	cs, path := setupService(nil)
	defer os.Remove(path)

	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	schedule := CronSchedule{
		Kind: "cron",
		Expr: "30 19 * * *",
		TZ:   "Europe/Moscow",
	}

	got := cs.computeNextRun(&schedule, now)
	if got == nil {
		t.Fatal("expected next run")
	}

	want := time.Date(2024, 1, 1, 16, 30, 0, 0, time.UTC).UnixMilli()
	if *got != want {
		t.Fatalf(
			"next run = %s, want %s",
			time.UnixMilli(*got).UTC().Format(time.RFC3339),
			time.UnixMilli(want).UTC().Format(time.RFC3339),
		)
	}
}

// 3. Test Execution Flow
func TestCronService_ExecutionFlow(t *testing.T) {
	var mu sync.Mutex
	executedJobs := make(map[string]bool)

	handler := func(job *CronJob) (string, error) {
		mu.Lock()
		executedJobs[job.ID] = true
		mu.Unlock()
		return "ok", nil
	}

	cs, path := setupService(handler)
	defer os.Remove(path)

	// Start the service
	if err := cs.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer cs.Stop()

	// Add a job then runs 100ms from now
	target := time.Now().Add(100 * time.Millisecond).UnixMilli()
	job, _ := cs.AddJob("FastJob", CronSchedule{Kind: "at", AtMS: &target}, "", "", "", "")

	// Check for job execution with a timeout
	success := false
	for range 20 {
		mu.Lock()
		if executedJobs[job.ID] {
			success = true
			mu.Unlock()
			break
		}
		mu.Unlock()
		time.Sleep(100 * time.Millisecond)
	}

	if !success {
		t.Error("Job was not executed in time")
	}

	// check that the job is removed after execution (DeleteAfterRun = true)
	status := cs.Status()
	if status["jobs"].(int) != 0 {
		t.Errorf("Job should be deleted after run, got count: %v", status["jobs"])
	}
}

func TestCronService_PersistenceIntegrity(t *testing.T) {
	tmpFile := "persist_test.json"
	defer os.Remove(tmpFile)

	// write a job and persist
	cs1 := NewCronService(tmpFile, nil)
	at := int64(2000000000000)
	if _, err := cs1.AddJob("PersistMe", CronSchedule{Kind: "at", AtMS: &at}, "", "payload", "ch1", ""); err != nil {
		t.Fatal(err)
	}

	// check file exists
	if _, err := os.Stat(tmpFile); os.IsNotExist(err) {
		t.Fatal("Store file was not created")
	}

	// reload and check data integrity
	cs2 := NewCronService(tmpFile, nil)
	if err := cs2.Load(); err != nil {
		t.Fatalf("Failed to load store: %v", err)
	}

	jobs := cs2.ListJobs(true)
	if len(jobs) != 1 || jobs[0].Name != "PersistMe" {
		t.Errorf("Data corruption after reload. Got: %+v", jobs)
	}

	// test loading invalid JSON
	if err := os.WriteFile(tmpFile, []byte("{invalid json}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cs3 := NewCronService(tmpFile, nil)
	err := cs3.loadStore()
	if err == nil {
		t.Error("Should return error when loading invalid JSON")
	}
}

func TestCronService_ConcurrentAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	cs := NewCronService(path, nil)
	at := time.Now().Add(time.Hour).UnixMilli()
	seed, err := cs.AddJob("seed", CronSchedule{Kind: "at", AtMS: &at}, "", "", "", "")
	if err != nil {
		t.Fatalf("AddJob(seed): %v", err)
	}

	var wg sync.WaitGroup
	const workers = 4
	const iterations = 4
	start := make(chan struct{})
	errCh := make(chan error, workers*iterations*2)

	for i := range workers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			for j := range iterations {
				jobAt := time.Now().Add(time.Hour).UnixMilli()
				if _, addErr := cs.AddJob(
					fmt.Sprintf("Job-%d-%d", id, j),
					CronSchedule{Kind: "at", AtMS: &jobAt},
					"",
					"",
					"",
					"",
				); addErr != nil {
					errCh <- fmt.Errorf("worker %d AddJob(%d): %w", id, j, addErr)
				}
			}
		}(i)
	}

	for i := range workers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			for j := range iterations {
				job, ok := cs.GetJob(seed.ID)
				if !ok {
					errCh <- fmt.Errorf("worker %d GetJob(%d): seed missing", id, j)
					continue
				}
				job.Enabled = (id+j)%2 == 0
				if updateErr := cs.UpdateJob(job); updateErr != nil {
					errCh <- fmt.Errorf("worker %d UpdateJob(%d): %w", id, j, updateErr)
				}
			}
		}(i)
	}

	close(start)
	wg.Wait()
	close(errCh)
	for operationErr := range errCh {
		t.Error(operationErr)
	}
	if t.Failed() {
		return
	}

	job, ok := cs.GetJob(seed.ID)
	if !ok {
		t.Fatal("seed job missing after concurrent operations")
	}
	job.Enabled = true
	if err := cs.UpdateJob(job); err != nil {
		t.Fatalf("normalize seed state: %v", err)
	}

	const wantJobs = 1 + workers*iterations
	jobs := cs.ListJobs(true)
	if len(jobs) != wantJobs {
		t.Fatalf("jobs after concurrent operations = %d, want %d", len(jobs), wantJobs)
	}
	ids := make(map[string]struct{}, len(jobs))
	for _, persistedJob := range jobs {
		if persistedJob.ID == "" {
			t.Fatal("job with empty ID after concurrent operations")
		}
		if _, duplicate := ids[persistedJob.ID]; duplicate {
			t.Fatalf("duplicate job ID %q after concurrent operations", persistedJob.ID)
		}
		ids[persistedJob.ID] = struct{}{}
	}

	reloaded := NewCronService(path, nil)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load persisted concurrent state: %v", err)
	}
	if got := len(reloaded.ListJobs(true)); got != wantJobs {
		t.Fatalf("persisted jobs = %d, want %d", got, wantJobs)
	}
	reloadedSeed, ok := reloaded.GetJob(seed.ID)
	if !ok || !reloadedSeed.Enabled {
		t.Fatalf("persisted seed state = (%+v, %t), want enabled seed", reloadedSeed, ok)
	}
}

func TestAddJobWithPayload_RollsBackLiveStoreOnSaveFailure(t *testing.T) {
	tmpDir := t.TempDir()
	// Make the store parent a regular file so every save attempt fails.
	blocker := filepath.Join(tmpDir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	cs := NewCronService(filepath.Join(blocker, "jobs.json"), nil)

	_, err := cs.AddJobWithPayload(
		"cmd",
		CronSchedule{Kind: "at", AtMS: int64Ptr(time.Now().UnixMilli() + 60000)},
		CronPayload{Kind: "agent_turn", Message: "msg", Command: "echo hi", Channel: "internal", To: "me"},
	)
	if err == nil {
		t.Fatal("AddJobWithPayload succeeded, want persistence failure")
	}
	if jobs := cs.ListJobs(false); len(jobs) != 0 {
		t.Fatalf("live store still contains %d job(s) after failed persistence, want 0", len(jobs))
	}
}

func TestAddJob_DoesNotOverwriteMalformedStore(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "jobs.json")
	malformed := []byte("{not valid json")
	if err := os.WriteFile(storePath, malformed, 0o600); err != nil {
		t.Fatalf("write malformed store: %v", err)
	}

	cs := NewCronService(storePath, nil)

	_, err := cs.AddJob("test", CronSchedule{Kind: "every", EveryMS: int64Ptr(60000)}, "", "hello", "cli", "direct")
	if err == nil {
		t.Fatal("AddJob succeeded, want load failure")
	}

	got, readErr := os.ReadFile(storePath)
	if readErr != nil {
		t.Fatalf("read store: %v", readErr)
	}
	if string(got) != string(malformed) {
		t.Fatalf("malformed store was overwritten: got %q, want original %q", got, malformed)
	}
}

func TestEnableJob_DoesNotOverwriteMalformedStore(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "jobs.json")
	malformed := []byte("{not valid json")
	if err := os.WriteFile(storePath, malformed, 0o600); err != nil {
		t.Fatalf("write malformed store: %v", err)
	}

	cs := NewCronService(storePath, nil)

	if _, err := cs.EnableJob("job-1", true); err == nil {
		t.Fatal("EnableJob succeeded, want load failure")
	}
	if _, err := cs.EnableJob("job-1", false); err == nil {
		t.Fatal("DisableJob succeeded, want load failure")
	}

	got, readErr := os.ReadFile(storePath)
	if readErr != nil {
		t.Fatalf("read store: %v", readErr)
	}
	if string(got) != string(malformed) {
		t.Fatalf("malformed store was overwritten: got %q, want original %q", got, malformed)
	}
}

func TestLoadErrRefreshesOnReload(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "jobs.json")
	malformed := []byte("{not valid json")
	if err := os.WriteFile(storePath, malformed, 0o600); err != nil {
		t.Fatalf("write malformed store: %v", err)
	}

	cs := NewCronService(storePath, nil)

	if _, err := cs.AddJob(
		"task",
		CronSchedule{Kind: "every", EveryMS: int64Ptr(60000)},
		"",
		"hello",
		"cli",
		"direct",
	); err == nil {
		t.Fatal("AddJob succeeded against malformed store, want load failure")
	}

	// Repair the store on disk and reload: the latched error must clear.
	if err := os.WriteFile(storePath, []byte(`{"version":1,"jobs":[]}`), 0o600); err != nil {
		t.Fatalf("repair store: %v", err)
	}
	if err := cs.Load(); err != nil {
		t.Fatalf("Load after repair failed: %v", err)
	}
	if _, err := cs.AddJob(
		"task",
		CronSchedule{Kind: "every", EveryMS: int64Ptr(60000)},
		"",
		"hello",
		"cli",
		"direct",
	); err != nil {
		t.Fatalf("AddJob after repair failed: %v", err)
	}

	// Corrupt the store again and reload: the new error must latch and
	// mutations must fail closed without overwriting the bad file.
	if err := os.WriteFile(storePath, malformed, 0o600); err != nil {
		t.Fatalf("corrupt store: %v", err)
	}
	if err := cs.Load(); err == nil {
		t.Fatal("Load over corrupted store succeeded, want error")
	}
	if _, err := cs.AddJob(
		"task2",
		CronSchedule{Kind: "every", EveryMS: int64Ptr(60000)},
		"",
		"hello",
		"cli",
		"direct",
	); err == nil {
		t.Fatal("AddJob succeeded with latched load error, want failure")
	}

	got, readErr := os.ReadFile(storePath)
	if readErr != nil {
		t.Fatalf("read store: %v", readErr)
	}
	if string(got) != string(malformed) {
		t.Fatalf("malformed store was overwritten: got %q, want original %q", got, malformed)
	}
}

func TestLoadFailurePreservesLiveStore(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "jobs.json")

	cs := NewCronService(storePath, nil)
	job, err := cs.AddJob("task", CronSchedule{Kind: "every", EveryMS: int64Ptr(60000)}, "", "hello", "cli", "direct")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	// A later reload over a corrupted file must fail closed but preserve the
	// known-good live store instead of replacing it with empty state.
	if err := os.WriteFile(storePath, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("corrupt store: %v", err)
	}
	if err := cs.Load(); err == nil {
		t.Fatal("Load over corrupted store succeeded, want error")
	}
	if _, ok := cs.GetJob(job.ID); !ok {
		t.Fatal("live store lost known-good job after failed reload")
	}
	if _, err := cs.AddJob(
		"task2",
		CronSchedule{Kind: "every", EveryMS: int64Ptr(60000)},
		"",
		"hello",
		"cli",
		"direct",
	); err == nil {
		t.Fatal("AddJob succeeded with latched load error, want failure")
	}
}

func TestCheckJobsSkipsDueJobsAfterFailedReload(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "jobs.json")

	handlerCalled := false
	cs := NewCronService(storePath, func(job *CronJob) (string, error) {
		handlerCalled = true
		return "ok", nil
	})

	job, err := cs.AddJobWithPayload(
		"due",
		CronSchedule{Kind: "at", AtMS: int64Ptr(time.Now().UnixMilli() + 60000)},
		CronPayload{Kind: "agent_turn", Message: "msg"},
	)
	if err != nil {
		t.Fatalf("AddJobWithPayload failed: %v", err)
	}
	if job.State.NextRunAtMS == nil {
		t.Fatal("expected next run")
	}

	// Make the job due, then latch a load error over the now-corrupt store
	// while keeping the known-good job in the live store.
	cs.mu.Lock()
	past := time.Now().UnixMilli() - 1000
	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == job.ID {
			cs.store.Jobs[i].State.NextRunAtMS = &past
		}
	}
	cs.mu.Unlock()

	malformed := []byte("{not valid json")
	if err := os.WriteFile(storePath, malformed, 0o600); err != nil {
		t.Fatalf("corrupt store: %v", err)
	}
	if err := cs.Load(); err == nil {
		t.Fatal("Load over corrupted store succeeded, want error")
	}

	cs.running = true
	cs.checkJobs()

	if handlerCalled {
		t.Fatal("due job executed while the store is unavailable")
	}

	got, readErr := os.ReadFile(storePath)
	if readErr != nil {
		t.Fatalf("read store: %v", readErr)
	}
	if string(got) != string(malformed) {
		t.Fatalf("corrupt store was overwritten by scheduler: got %q, want original %q", got, malformed)
	}
}

func TestCheckJobsDoesNotClaimWhenStoreUnavailable(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "jobs.json")

	handlerCalled := false
	cs := NewCronService(storePath, func(job *CronJob) (string, error) {
		handlerCalled = true
		return "ok", nil
	})

	job, err := cs.AddJob(
		"due",
		CronSchedule{Kind: "at", AtMS: int64Ptr(time.Now().UnixMilli() + 60000)},
		"",
		"msg",
		"cli",
		"direct",
	)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	cs.mu.Lock()
	past := time.Now().UnixMilli() - 1000
	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == job.ID {
			cs.store.Jobs[i].State.NextRunAtMS = &past
		}
	}
	cs.mu.Unlock()
	cs.running = true

	// A failed reload latched before the scheduler tick must not claim the
	// due job: its next run must be preserved so the run is not lost.
	cs.mu.Lock()
	cs.loadErr = fmt.Errorf("simulated reload failure")
	cs.mu.Unlock()

	cs.checkJobs()

	if handlerCalled {
		t.Fatal("job dispatched while the store is unavailable")
	}
	cs.mu.RLock()
	preserved := cs.store.Jobs[0].State.NextRunAtMS
	cs.mu.RUnlock()
	if preserved == nil || *preserved != past {
		t.Fatalf("due job was claimed despite unavailable store: next run %v, want %d", preserved, past)
	}
}

func TestLoadWaitsForInFlightDispatch(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "jobs.json")

	released := make(chan struct{})
	handlerStarted := make(chan struct{})
	var startedOnce sync.Once
	var handlerCalls int
	var cs *CronService
	cs = NewCronService(storePath, func(job *CronJob) (string, error) {
		cs.mu.Lock()
		handlerCalls++
		cs.mu.Unlock()
		startedOnce.Do(func() { close(handlerStarted) })
		<-released
		return "ok", nil
	})

	job, err := cs.AddJob(
		"due",
		CronSchedule{Kind: "at", AtMS: int64Ptr(time.Now().UnixMilli() + 60000)},
		"",
		"msg",
		"cli",
		"direct",
	)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	cs.mu.Lock()
	past := time.Now().UnixMilli() - 1000
	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == job.ID {
			cs.store.Jobs[i].State.NextRunAtMS = &past
		}
	}
	cs.mu.Unlock()
	cs.running = true

	dispatchDone := make(chan struct{})
	go func() {
		cs.checkJobs()
		close(dispatchDone)
	}()

	// Selection and dispatch are in flight (the handler is blocked), so a
	// reload issued now must wait for dispatch to finish.
	<-handlerStarted
	loadDone := make(chan error, 1)
	go func() {
		loadDone <- cs.Load()
	}()

	select {
	case err := <-loadDone:
		t.Fatalf("Load completed during in-flight dispatch: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(released)
	<-dispatchDone

	if err := <-loadDone; err != nil {
		t.Fatalf("Load after dispatch failed: %v", err)
	}

	cs.mu.RLock()
	calls := handlerCalls
	cs.mu.RUnlock()
	if calls != 1 {
		t.Fatalf("job dispatched %d times, want 1", calls)
	}
}

func TestStopAndDrainWaitsForClaimedDispatchStoreUpdate(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "jobs.json")
	releaseHandler := make(chan struct{})
	handlerStarted := make(chan struct{})
	cs := NewCronService(storePath, func(*CronJob) (string, error) {
		close(handlerStarted)
		<-releaseHandler
		return "ok", nil
	})
	job, err := cs.AddJob(
		"due",
		CronSchedule{Kind: "every", EveryMS: int64Ptr(time.Minute.Milliseconds())},
		"",
		"msg",
		"cli",
		"direct",
	)
	if err != nil {
		t.Fatalf("AddJob() error = %v", err)
	}
	cs.mu.Lock()
	past := time.Now().Add(-time.Second).UnixMilli()
	for index := range cs.store.Jobs {
		if cs.store.Jobs[index].ID == job.ID {
			cs.store.Jobs[index].State.NextRunAtMS = &past
		}
	}
	cs.running = true
	cs.mu.Unlock()

	dispatchDone := make(chan struct{})
	go func() {
		cs.checkJobs()
		close(dispatchDone)
	}()
	<-handlerStarted

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelDrain()
	if err = cs.StopAndDrain(drainCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StopAndDrain() error = %v, want deadline exceeded", err)
	}
	close(releaseHandler)
	select {
	case <-dispatchDone:
	case <-time.After(time.Second):
		t.Fatal("claimed dispatch did not finish after handler release")
	}
	if err = cs.StopAndDrain(context.Background()); err != nil {
		t.Fatalf("StopAndDrain() after dispatch error = %v", err)
	}

	reloaded := NewCronService(storePath, nil)
	persisted, ok := reloaded.GetJob(job.ID)
	if !ok {
		t.Fatal("claimed job was not persisted before drain completed")
	}
	if persisted.State.LastRunAtMS == nil || persisted.State.LastStatus != "ok" {
		t.Fatalf("persisted claimed job state = %#v, want completed run", persisted.State)
	}
}

func TestLoadLatchesCorruptionDuringInFlightDispatch(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "jobs.json")

	released := make(chan struct{})
	handlerStarted := make(chan struct{})
	var startedOnce sync.Once
	var handlerCalls int
	var cs *CronService
	cs = NewCronService(storePath, func(job *CronJob) (string, error) {
		cs.mu.Lock()
		handlerCalls++
		cs.mu.Unlock()
		startedOnce.Do(func() { close(handlerStarted) })
		<-released
		return "ok", nil
	})

	job, err := cs.AddJob(
		"due",
		CronSchedule{Kind: "at", AtMS: int64Ptr(time.Now().UnixMilli() + 60000)},
		"",
		"msg",
		"cli",
		"direct",
	)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	cs.mu.Lock()
	past := time.Now().UnixMilli() - 1000
	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == job.ID {
			cs.store.Jobs[i].State.NextRunAtMS = &past
		}
	}
	cs.mu.Unlock()
	cs.running = true

	dispatchDone := make(chan struct{})
	go func() {
		cs.checkJobs()
		close(dispatchDone)
	}()

	// Corrupt the authoritative file while the handler is in flight: Load must
	// latch the corruption immediately instead of waiting for dispatch.
	<-handlerStarted
	malformed := []byte("{not valid json")
	if err := os.WriteFile(storePath, malformed, 0o600); err != nil {
		t.Fatalf("corrupt store: %v", err)
	}
	loadDone := make(chan error, 1)
	go func() {
		loadDone <- cs.Load()
	}()
	select {
	case err := <-loadDone:
		if err == nil {
			t.Fatal("Load over corrupted store succeeded, want error")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Load waited for dispatch instead of latching corruption immediately")
	}

	// The in-flight completion must suppress its save so the malformed file
	// survives.
	close(released)
	<-dispatchDone

	got, readErr := os.ReadFile(storePath)
	if readErr != nil {
		t.Fatalf("read store: %v", readErr)
	}
	if string(got) != string(malformed) {
		t.Fatalf("corrupt store was overwritten by in-flight dispatch: got %q, want original %q", got, malformed)
	}

	cs.mu.RLock()
	calls := handlerCalls
	cs.mu.RUnlock()
	if calls != 1 {
		t.Fatalf("job dispatched %d times, want 1", calls)
	}
}

func TestLoadPublishesFreshSnapshotAfterDispatch(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "jobs.json")

	released := make(chan struct{})
	handlerStarted := make(chan struct{})
	var startedOnce sync.Once
	cs := NewCronService(storePath, nil)
	cs.SetOnJob(func(job *CronJob) (string, error) {
		startedOnce.Do(func() { close(handlerStarted) })
		<-released
		return "ok", nil
	})

	// One-shot "at" job: DeleteAfterRun removes it from the store once run.
	job, err := cs.AddJob(
		"once",
		CronSchedule{Kind: "at", AtMS: int64Ptr(time.Now().UnixMilli() + 60000)},
		"",
		"msg",
		"cli",
		"direct",
	)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	if !job.DeleteAfterRun {
		t.Fatal("expected DeleteAfterRun for at job")
	}
	cs.mu.Lock()
	past := time.Now().UnixMilli() - 1000
	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == job.ID {
			cs.store.Jobs[i].State.NextRunAtMS = &past
		}
	}
	cs.mu.Unlock()
	cs.running = true

	dispatchDone := make(chan struct{})
	go func() {
		cs.checkJobs()
		close(dispatchDone)
	}()

	// A reload issued while the handler is in flight must publish the state
	// committed by the dispatch (the job is deleted), not the older snapshot.
	<-handlerStarted
	loadDone := make(chan error, 1)
	go func() {
		loadDone <- cs.Load()
	}()
	select {
	case err := <-loadDone:
		t.Fatalf("Load completed during in-flight dispatch: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(released)
	<-dispatchDone

	if err := <-loadDone; err != nil {
		t.Fatalf("Load after dispatch failed: %v", err)
	}

	if _, ok := cs.GetJob(job.ID); ok {
		t.Fatal("completed one-shot job was resurrected in live state by reload")
	}
}

func TestLoadMissingFileClearsLiveJobs(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "jobs.json")

	cs := NewCronService(storePath, nil)
	job, err := cs.AddJob("task", CronSchedule{Kind: "every", EveryMS: int64Ptr(60000)}, "", "hello", "cli", "direct")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	// Removing the authoritative file must clear the live store, not leave
	// stale jobs that could execute and recreate the file.
	if err := os.Remove(storePath); err != nil {
		t.Fatalf("remove store: %v", err)
	}
	if err := cs.Load(); err != nil {
		t.Fatalf("Load with missing store failed: %v", err)
	}
	if _, ok := cs.GetJob(job.ID); ok {
		t.Fatal("job survived reload after its authoritative file was removed")
	}
	if len(cs.ListJobs(true)) != 0 {
		t.Fatalf("expected empty live store, got %d jobs", len(cs.ListJobs(true)))
	}
}

func TestLoadMissingFileSuppressesInFlightDispatchWrites(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "jobs.json")

	released := make(chan struct{})
	handlerStarted := make(chan struct{})
	var startedOnce sync.Once
	var handlerCalls int
	var cs *CronService
	cs = NewCronService(storePath, func(job *CronJob) (string, error) {
		cs.mu.Lock()
		handlerCalls++
		cs.mu.Unlock()
		startedOnce.Do(func() { close(handlerStarted) })
		<-released
		return "ok", nil
	})

	// Recurring job: its completion would persist a next run and recreate a
	// deleted store if writes were not suppressed during the reload.
	job, err := cs.AddJob("tick", CronSchedule{Kind: "every", EveryMS: int64Ptr(50)}, "", "msg", "cli", "direct")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	cs.mu.Lock()
	past := time.Now().UnixMilli() - 1000
	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == job.ID {
			cs.store.Jobs[i].State.NextRunAtMS = &past
		}
	}
	cs.mu.Unlock()
	cs.running = true

	dispatchDone := make(chan struct{})
	go func() {
		cs.checkJobs()
		close(dispatchDone)
	}()

	// Delete the authoritative file while the handler is in flight.
	<-handlerStarted
	if err := os.Remove(storePath); err != nil {
		t.Fatalf("remove store: %v", err)
	}

	loadDone := make(chan error, 1)
	go func() {
		loadDone <- cs.Load()
	}()
	select {
	case err := <-loadDone:
		t.Fatalf("Load completed during in-flight dispatch: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	// The in-flight completion must not recreate the deleted file.
	close(released)
	<-dispatchDone

	if err := <-loadDone; err != nil {
		t.Fatalf("Load after dispatch failed: %v", err)
	}
	if _, err := os.Stat(storePath); !os.IsNotExist(err) {
		t.Fatalf("deleted store was recreated by in-flight dispatch: %v", err)
	}
	if len(cs.ListJobs(true)) != 0 {
		t.Fatalf("stale job survived missing-store reload, %d job(s) live", len(cs.ListJobs(true)))
	}

	cs.mu.RLock()
	calls := handlerCalls
	cs.mu.RUnlock()
	if calls != 1 {
		t.Fatalf("job dispatched %d times, want 1", calls)
	}
}

func TestLoadMissingFileBlocksMutationsDuringReload(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "jobs.json")

	released := make(chan struct{})
	handlerStarted := make(chan struct{})
	var startedOnce sync.Once
	cs := NewCronService(storePath, nil)
	cs.SetOnJob(func(job *CronJob) (string, error) {
		startedOnce.Do(func() { close(handlerStarted) })
		<-released
		return "ok", nil
	})

	job, err := cs.AddJob("tick", CronSchedule{Kind: "every", EveryMS: int64Ptr(50)}, "", "msg", "cli", "direct")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	cs.mu.Lock()
	past := time.Now().UnixMilli() - 1000
	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == job.ID {
			cs.store.Jobs[i].State.NextRunAtMS = &past
		}
	}
	cs.mu.Unlock()
	cs.running = true

	dispatchDone := make(chan struct{})
	go func() {
		cs.checkJobs()
		close(dispatchDone)
	}()

	// Delete the authoritative file while the handler is in flight.
	<-handlerStarted
	if err := os.Remove(storePath); err != nil {
		t.Fatalf("remove store: %v", err)
	}

	loadDone := make(chan error, 1)
	go func() {
		loadDone <- cs.Load()
	}()
	select {
	case err := <-loadDone:
		t.Fatalf("Load completed during in-flight dispatch: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	// The write barrier must be latched atomically with the probe, before
	// Load blocks on dispatchMu; otherwise a mutation could recreate the
	// deleted file with the stale live snapshot.
	cs.mu.RLock()
	pending := cs.reloadWait
	cs.mu.RUnlock()
	if !pending {
		t.Fatalf("reloadWait was not latched while dispatch was in flight")
	}

	// While the missing-store reload is pending, public mutations must be
	// rejected instead of persisting the stale live snapshot.
	if _, err := cs.AddJob(
		"late",
		CronSchedule{Kind: "every", EveryMS: int64Ptr(50)},
		"",
		"msg",
		"cli",
		"direct",
	); err == nil {
		t.Fatalf("AddJob succeeded while missing-store reload was pending")
	}
	if err := cs.UpdateJob(job); err == nil {
		t.Fatalf("UpdateJob succeeded while missing-store reload was pending")
	}
	if cs.RemoveJob(job.ID) {
		t.Fatalf("RemoveJob succeeded while missing-store reload was pending")
	}
	if _, err := cs.EnableJob(job.ID, false); err == nil {
		t.Fatalf("EnableJob succeeded while missing-store reload was pending")
	}

	close(released)
	<-dispatchDone

	if err := <-loadDone; err != nil {
		t.Fatalf("Load after dispatch failed: %v", err)
	}
	if _, err := os.Stat(storePath); !os.IsNotExist(err) {
		t.Fatalf("deleted store was recreated during missing-store reload: %v", err)
	}
	if got := cs.ListJobs(true); len(got) != 0 {
		t.Fatalf("stale or newly added job survived missing-store reload, %d job(s) live", len(got))
	}
}

func TestRunLoopSuspendsAndResumesAfterReload(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "jobs.json")

	handlerCalls := make(chan string, 4)
	cs := NewCronService(storePath, func(job *CronJob) (string, error) {
		handlerCalls <- job.Name
		return "ok", nil
	})

	// Long-interval job so the loop parks on a distant wake and cannot fire
	// during the failure/repair sequence.
	if _, err := cs.AddJob(
		"tick",
		CronSchedule{Kind: "every", EveryMS: int64Ptr(60_000)},
		"",
		"msg",
		"cli",
		"direct",
	); err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	if err := cs.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer cs.Stop()

	// Corrupt the authoritative store and latch the load error.
	malformed := []byte("{not valid json")
	if err := os.WriteFile(storePath, malformed, 0o600); err != nil {
		t.Fatalf("corrupt store: %v", err)
	}
	if err := cs.Load(); err == nil {
		t.Fatal("Load over corrupted store succeeded, want error")
	}

	// While unavailable the scheduler must not wake for the due job (no
	// busy-spin), must not execute it, and must not overwrite the corrupt file.
	cs.mu.RLock()
	wake := cs.getNextWakeMS()
	cs.mu.RUnlock()
	if wake != nil {
		t.Fatalf("expected suspended scheduler, got wake %v", wake)
	}
	select {
	case name := <-handlerCalls:
		t.Fatalf("job %q ran while the store is unavailable", name)
	case <-time.After(200 * time.Millisecond):
	}
	got, readErr := os.ReadFile(storePath)
	if readErr != nil {
		t.Fatalf("read store: %v", readErr)
	}
	if string(got) != string(malformed) {
		t.Fatalf("corrupt store was overwritten: got %q, want original %q", got, malformed)
	}

	// Repair: restore a valid store containing the overdue job and reload; the
	// scheduler must rearm and run it.
	cs.mu.Lock()
	past := time.Now().UnixMilli() - 1000
	for i := range cs.store.Jobs {
		cs.store.Jobs[i].State.NextRunAtMS = &past
	}
	cs.mu.Unlock()
	if err := cs.saveStoreUnsafe(); err != nil {
		t.Fatalf("repair store: %v", err)
	}
	if err := cs.Load(); err != nil {
		t.Fatalf("Load after repair failed: %v", err)
	}
	select {
	case <-handlerCalls:
	case <-time.After(2 * time.Second):
		t.Fatal("expected the overdue job to run after repair")
	}
}
