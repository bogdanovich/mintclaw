package cron

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/adhocore/gronx"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

type CronSchedule struct {
	Kind    string `json:"kind"`
	AtMS    *int64 `json:"atMs,omitempty"`
	EveryMS *int64 `json:"everyMs,omitempty"`
	Expr    string `json:"expr,omitempty"`
	TZ      string `json:"tz,omitempty"`
}

type CronPayload struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Command string `json:"command,omitempty"`
	Channel string `json:"channel,omitempty"`
	To      string `json:"to,omitempty"`
}

type CronJobState struct {
	NextRunAtMS *int64 `json:"nextRunAtMs,omitempty"`
	LastRunAtMS *int64 `json:"lastRunAtMs,omitempty"`
	LastStatus  string `json:"lastStatus,omitempty"`
	LastError   string `json:"lastError,omitempty"`
}

type CronJob struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Enabled        bool         `json:"enabled"`
	Schedule       CronSchedule `json:"schedule"`
	Payload        CronPayload  `json:"payload"`
	State          CronJobState `json:"state"`
	CreatedAtMS    int64        `json:"createdAtMs"`
	UpdatedAtMS    int64        `json:"updatedAtMs"`
	DeleteAfterRun bool         `json:"deleteAfterRun"`
}

type CronStore struct {
	Version int       `json:"version"`
	Jobs    []CronJob `json:"jobs"`
}

type JobHandler func(job *CronJob) (string, error)

type CronService struct {
	storePath  string
	store      *CronStore
	loadErr    error
	reloadWait bool
	onJob      JobHandler
	mu         sync.RWMutex
	dispatchMu sync.Mutex
	running    bool
	stopChan   chan struct{}
	wakeChan   chan struct{}
	gronx      *gronx.Gronx
	activeJobs int
}

func NewCronService(storePath string, onJob JobHandler) *CronService {
	cs := &CronService{
		storePath: storePath,
		onJob:     onJob,
		gronx:     gronx.New(),
		// Capacity-one coalescing wake channel: a notification sent while the
		// loop is not yet in its select stays pending until consumed, so a
		// recovery signal is never dropped.
		wakeChan: make(chan struct{}, 1),
	}
	// Initialize and load store on creation
	_ = cs.loadStore()
	return cs
}

func (cs *CronService) Start() error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.running {
		return nil
	}

	if err := cs.loadStore(); err != nil {
		return fmt.Errorf("failed to load store: %w", err)
	}

	cs.recomputeNextRuns()
	if err := cs.saveStoreUnsafe(); err != nil {
		return fmt.Errorf("failed to save store: %w", err)
	}

	cs.stopChan = make(chan struct{})
	if cs.wakeChan == nil {
		cs.wakeChan = make(chan struct{}, 1)
	}
	cs.running = true
	go cs.runLoop(cs.stopChan)

	return nil
}

func (cs *CronService) Stop() {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if !cs.running {
		return
	}

	cs.running = false
	if cs.stopChan != nil {
		close(cs.stopChan)
		cs.stopChan = nil
	}
}

func (cs *CronService) runLoop(stopChan chan struct{}) {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for {
		// every loop, recalculate the next wake time
		cs.mu.RLock()
		nextWake := cs.getNextWakeMS()
		cs.mu.RUnlock()

		var delay time.Duration
		now := time.Now().UnixMilli()

		if nextWake == nil {
			// no jobs, sleep for a long time (or until a new job is added)
			delay = time.Hour
		} else {
			diff := *nextWake - now
			if diff <= 0 {
				delay = 0
			} else {
				delay = time.Duration(diff) * time.Millisecond
			}
		}

		timer.Reset(delay)

		select {
		case <-stopChan:
			return
		case <-cs.wakeChan: // wake on new job or update
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			continue
		case <-timer.C:
			cs.checkJobs()
		}
	}
}

func (cs *CronService) checkJobs() {
	// Hold dispatchMu across selection/claiming, persistence, and dispatch so
	// a reload cannot interleave after a due job's next run has been cleared
	// and persisted: a failed reload must never drop an already claimed run.
	cs.dispatchMu.Lock()
	defer cs.dispatchMu.Unlock()

	cs.mu.Lock()

	if !cs.running {
		cs.mu.Unlock()
		return
	}

	// A failed reload latches loadErr and a missing-store reload sets
	// reloadWait; in both cases block selection/persistence/execution so the
	// live snapshot cannot overwrite or recreate the authoritative file.
	if cs.writesBlocked() {
		cs.mu.Unlock()
		return
	}

	now := time.Now().UnixMilli()
	var dueJobIDs []string

	// Collect jobs that are due (we need to copy them to execute outside lock)
	for i := range cs.store.Jobs {
		job := &cs.store.Jobs[i]
		if job.Enabled && job.State.NextRunAtMS != nil && *job.State.NextRunAtMS <= now {
			dueJobIDs = append(dueJobIDs, job.ID)
		}
	}

	// Reset next run for due jobs before unlocking to avoid duplicate execution.
	dueMap := make(map[string]bool, len(dueJobIDs))
	for _, jobID := range dueJobIDs {
		dueMap[jobID] = true
	}
	for i := range cs.store.Jobs {
		if dueMap[cs.store.Jobs[i].ID] {
			cs.store.Jobs[i].State.NextRunAtMS = nil
		}
	}

	if err := cs.saveStoreUnsafe(); err != nil {
		log.Printf("[cron] failed to save store: %v", err)
	}

	cs.mu.Unlock()

	// Execute jobs outside lock.
	for _, jobID := range dueJobIDs {
		cs.executeJobByID(jobID)
	}
}

func (cs *CronService) executeJobByID(jobID string) {
	startTime := time.Now().UnixMilli()

	cs.mu.RLock()
	var callbackJob *CronJob
	for i := range cs.store.Jobs {
		job := &cs.store.Jobs[i]
		if job.ID == jobID {
			jobCopy := *job
			callbackJob = &jobCopy
			break
		}
	}
	cs.mu.RUnlock()

	if callbackJob == nil {
		log.Printf("[cron] job %s not found, skipping", jobID)
		return
	}

	// Log job execution start
	log.Printf("[cron] ▶ executing job '%s' (id: %s, schedule: %s, channel: %s)",
		callbackJob.Name, jobID, callbackJob.Schedule.Kind, callbackJob.Payload.Channel)

	var err error
	if cs.onJob != nil {
		cs.incrementActiveJobs()
		defer cs.decrementActiveJobs()
		_, err = cs.onJob(callbackJob)
	}

	execDuration := time.Now().UnixMilli() - startTime

	// Now acquire lock to update state
	cs.mu.Lock()
	defer cs.mu.Unlock()

	var job *CronJob
	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == jobID {
			job = &cs.store.Jobs[i]
			break
		}
	}
	if job == nil {
		log.Printf("[cron] job %s disappeared before state update", jobID)
		return
	}

	job.State.LastRunAtMS = &startTime
	job.UpdatedAtMS = time.Now().UnixMilli()

	if err != nil {
		job.State.LastStatus = "error"
		job.State.LastError = err.Error()
		log.Printf("[cron] ✗ job '%s' failed after %dms: %v", job.Name, execDuration, err)
	} else {
		job.State.LastStatus = "ok"
		job.State.LastError = ""
	}

	// Compute next run time
	var nextRunStr string
	if job.Schedule.Kind == "at" {
		if job.DeleteAfterRun {
			cs.removeJobUnsafe(job.ID)
			nextRunStr = "(deleted)"
		} else {
			job.Enabled = false
			job.State.NextRunAtMS = nil
			nextRunStr = "(disabled)"
		}
	} else {
		nextRun := cs.computeNextRun(&job.Schedule, time.Now().UnixMilli())
		job.State.NextRunAtMS = nextRun
		if nextRun != nil {
			nextRunStr = time.UnixMilli(*nextRun).Format("2006-01-02 15:04:05")
		} else {
			nextRunStr = "(none)"
		}
	}

	if err == nil {
		log.Printf("[cron] ✓ job '%s' completed in %dms, next run: %s", job.Name, execDuration, nextRunStr)
	}

	if !cs.writesBlocked() {
		if err := cs.saveStoreUnsafe(); err != nil {
			log.Printf("[cron] failed to save store: %v", err)
		}
	}
}

func (cs *CronService) computeNextRun(schedule *CronSchedule, nowMS int64) *int64 {
	switch schedule.Kind {
	case "at":
		if schedule.AtMS != nil && *schedule.AtMS > nowMS {
			return schedule.AtMS
		}
		return nil
	case "every":
		if schedule.EveryMS == nil || *schedule.EveryMS <= 0 {
			return nil
		}
		next := nowMS + *schedule.EveryMS
		return &next
	case "cron":
		if schedule.Expr == "" {
			return nil
		}

		// Use the schedule timezone when provided so cron expressions are
		// interpreted in the intended wall-clock timezone instead of the host's
		// local timezone.
		now := time.UnixMilli(nowMS).In(scheduleLocation(schedule))
		nextTime, err := gronx.NextTickAfter(schedule.Expr, now, false)
		if err != nil {
			log.Printf("[cron] failed to compute next run for expr '%s': %v", schedule.Expr, err)
			return nil
		}

		nextMS := nextTime.UnixMilli()
		return &nextMS
	default:
		log.Printf("[cron] unknown schedule kind '%s'", schedule.Kind)
		return nil
	}
}

func scheduleLocation(schedule *CronSchedule) *time.Location {
	if schedule == nil || schedule.TZ == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(schedule.TZ)
	if err != nil {
		log.Printf("[cron] failed to load timezone '%s', falling back to local timezone: %v", schedule.TZ, err)
		return time.Local
	}
	return loc
}

// wake up the loop to re-evaluate next wake time immediately (e.g. after add/update/remove jobs)
func (cs *CronService) notify() {
	select {
	case cs.wakeChan <- struct{}{}:
	default:
		// if the channel is full, it means the loop will wake up soon anyway, so we can skip sending
	}
}

func (cs *CronService) recomputeNextRuns() {
	now := time.Now().UnixMilli()
	for i := range cs.store.Jobs {
		job := &cs.store.Jobs[i]
		if job.Enabled {
			job.State.NextRunAtMS = cs.computeNextRun(&job.Schedule, now)
		}
	}
}

func (cs *CronService) getNextWakeMS() *int64 {
	// Suspend the scheduler while the store is unavailable: an overdue job
	// would otherwise keep the wake timer at zero and busy-spin the loop.
	if cs.loadErr != nil {
		return nil
	}

	var nextWake *int64
	for _, job := range cs.store.Jobs {
		if job.Enabled && job.State.NextRunAtMS != nil {
			if nextWake == nil || *job.State.NextRunAtMS < *nextWake {
				nextWake = job.State.NextRunAtMS
			}
		}
	}
	return nextWake
}

func (cs *CronService) Load() error {
	// Probe the authoritative file while holding cs.mu so the write barrier
	// (loadErr/reloadWait) is latched atomically with the probe: an in-flight
	// handler completion or CRUD caller cannot slip in between the probe and
	// the latch and overwrite a corrupt or deleted file with the stale live
	// snapshot. Release cs.mu before serializing with dispatch.
	cs.mu.Lock()
	probe, probeErr := cs.readStore()
	if probeErr != nil {
		cs.loadErr = probeErr
		cs.notify()
		cs.mu.Unlock()
		return probeErr
	}
	if probe == nil {
		// The file is missing: suppress dispatch writes and mutations until
		// the locked re-read is published so the deleted file cannot be
		// recreated with stale live state.
		cs.reloadWait = true
	}
	cs.mu.Unlock()

	cs.dispatchMu.Lock()
	defer cs.dispatchMu.Unlock()

	// Hold cs.mu across the authoritative re-read and publication so a CRUD
	// mutation cannot commit between the read and the publish.
	cs.mu.Lock()
	defer cs.mu.Unlock()

	// Re-read after in-flight dispatch finishes and publish only that fresh
	// snapshot: a handler may have committed deletion, disablement, or the
	// next recurring run while we waited. Do not clear loadErr if this read
	// fails.
	store, readErr := cs.readStore()
	if readErr != nil {
		cs.reloadWait = false
		cs.loadErr = readErr
		cs.notify()
		return readErr
	}

	if store == nil {
		// A missing authoritative file is an empty store: replace the live
		// state so jobs deleted from disk cannot run or recreate the file.
		cs.store = &CronStore{
			Version: 1,
			Jobs:    []CronJob{},
		}
	} else {
		cs.store = store
	}
	cs.loadErr = nil
	cs.reloadWait = false
	// Re-evaluate the scheduler wake time after every reload outcome so a
	// repaired store resumes promptly and a failed one suspends.
	cs.notify()
	return nil
}

// writesBlocked reports whether the live store must not be persisted: either
// the authoritative store failed to load or a reload is being serialized with
// an in-flight dispatch.
func (cs *CronService) writesBlocked() bool {
	return cs.loadErr != nil || cs.reloadWait
}

// storeUnavailableErr reports why mutations must be rejected while the
// authoritative store cannot be written: either a failed load or a reload
// that is pending serialization with an in-flight dispatch.
func (cs *CronService) storeUnavailableErr() error {
	if cs.loadErr != nil {
		return fmt.Errorf("cron store unavailable: %w", cs.loadErr)
	}
	return errors.New("cron store unavailable: reload in progress")
}

func (cs *CronService) SetOnJob(handler JobHandler) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.onJob = handler
}

func (cs *CronService) loadStore() error {
	store, err := cs.readStore()
	if err != nil {
		cs.ensureStore()
		cs.loadErr = err
		return err
	}
	if store == nil {
		// A missing authoritative file is an empty store.
		cs.store = &CronStore{
			Version: 1,
			Jobs:    []CronJob{},
		}
		cs.loadErr = nil
		return nil
	}
	cs.store = store
	cs.loadErr = nil
	return nil
}

// readStore reads and decodes the authoritative store without mutating the
// live state. It returns a nil store when the file does not exist.
func (cs *CronService) readStore() (*CronStore, error) {
	data, err := os.ReadFile(cs.storePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	store := &CronStore{
		Version: 1,
		Jobs:    []CronJob{},
	}
	if err := json.Unmarshal(data, store); err != nil {
		return nil, err
	}
	return store, nil
}

func (cs *CronService) ensureStore() {
	if cs.store == nil {
		cs.store = &CronStore{
			Version: 1,
			Jobs:    []CronJob{},
		}
	}
}

func (cs *CronService) saveStoreUnsafe() error {
	data, err := json.MarshalIndent(cs.store, "", "  ")
	if err != nil {
		return err
	}

	// Use unified atomic write utility with explicit sync for flash storage reliability.
	return fileutil.WriteFileAtomic(cs.storePath, data, 0o600)
}

func (cs *CronService) AddJob(
	name string,
	schedule CronSchedule,
	payloadKind string,
	message string,
	channel, to string,
) (*CronJob, error) {
	return cs.AddJobWithPayload(name, schedule, CronPayload{
		Kind:    payloadKind,
		Message: message,
		Channel: channel,
		To:      to,
	})
}

// AddJobWithPayload persists a fully populated payload atomically. Callers
// that set optional fields (e.g. command jobs) must use this so a failed
// follow-up write cannot leave a partial job on disk that reappears after
// restart.
func (cs *CronService) AddJobWithPayload(
	name string,
	schedule CronSchedule,
	payload CronPayload,
) (*CronJob, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.writesBlocked() {
		return nil, cs.storeUnavailableErr()
	}

	now := time.Now().UnixMilli()

	// One-time tasks (at) should be deleted after execution
	deleteAfterRun := (schedule.Kind == "at")
	if payload.Kind == "" {
		payload.Kind = "agent_turn"
	}

	job := CronJob{
		ID:       generateID(),
		Name:     name,
		Enabled:  true,
		Schedule: schedule,
		Payload:  payload,
		State: CronJobState{
			NextRunAtMS: cs.computeNextRun(&schedule, now),
		},
		CreatedAtMS:    now,
		UpdatedAtMS:    now,
		DeleteAfterRun: deleteAfterRun,
	}

	cs.store.Jobs = append(cs.store.Jobs, job)
	if err := cs.saveStoreUnsafe(); err != nil {
		if fileutil.IsCommittedWriteError(err) {
			// The atomic rename already installed the new store: the job is
			// durable on disk (only its sync is unconfirmed). Keep it live so
			// it cannot reappear unexpectedly after restart, and surface the
			// uncertain durability so a retry cannot create a duplicate.
			cs.notify()
			return &job, fmt.Errorf("job %s added but durability was not confirmed: %w", job.ID, err)
		}
		// Pre-commit failure: roll back the in-memory append so a job reported
		// as not added cannot run in the live scheduler for this process.
		cs.store.Jobs = cs.store.Jobs[:len(cs.store.Jobs)-1]
		return nil, err
	}

	cs.notify()

	return &job, nil
}

func (cs *CronService) GetJob(jobID string) (*CronJob, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == jobID {
			jobCopy := cloneCronJob(cs.store.Jobs[i])
			return &jobCopy, true
		}
	}
	return nil, false
}

func (cs *CronService) UpdateJob(job *CronJob) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.writesBlocked() {
		return cs.storeUnavailableErr()
	}

	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == job.ID {
			previous := cs.store.Jobs[i]
			updated := cloneCronJob(*job)
			now := time.Now().UnixMilli()
			updated.UpdatedAtMS = now
			if updated.Enabled {
				if previous.Enabled != updated.Enabled || !sameSchedule(previous.Schedule, updated.Schedule) {
					updated.State.NextRunAtMS = cs.computeNextRun(&updated.Schedule, now)
				}
			} else {
				updated.State.NextRunAtMS = nil
			}
			cs.store.Jobs[i] = updated

			cs.notify()

			return cs.saveStoreUnsafe()
		}
	}
	return fmt.Errorf("job not found")
}

func cloneCronJob(job CronJob) CronJob {
	clone := job
	if job.Schedule.AtMS != nil {
		atMS := *job.Schedule.AtMS
		clone.Schedule.AtMS = &atMS
	}
	if job.Schedule.EveryMS != nil {
		everyMS := *job.Schedule.EveryMS
		clone.Schedule.EveryMS = &everyMS
	}
	if job.State.NextRunAtMS != nil {
		nextRunAtMS := *job.State.NextRunAtMS
		clone.State.NextRunAtMS = &nextRunAtMS
	}
	if job.State.LastRunAtMS != nil {
		lastRunAtMS := *job.State.LastRunAtMS
		clone.State.LastRunAtMS = &lastRunAtMS
	}
	return clone
}

func sameSchedule(a, b CronSchedule) bool {
	return a.Kind == b.Kind &&
		sameInt64(a.AtMS, b.AtMS) &&
		sameInt64(a.EveryMS, b.EveryMS) &&
		a.Expr == b.Expr &&
		a.TZ == b.TZ
}

func sameInt64(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (cs *CronService) RemoveJob(jobID string) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.writesBlocked() {
		return false
	}

	return cs.removeJobUnsafe(jobID)
}

func (cs *CronService) removeJobUnsafe(jobID string) bool {
	before := len(cs.store.Jobs)
	var jobs []CronJob
	for _, job := range cs.store.Jobs {
		if job.ID != jobID {
			jobs = append(jobs, job)
		}
	}
	cs.store.Jobs = jobs
	removed := len(cs.store.Jobs) < before

	if removed {
		if !cs.writesBlocked() {
			if err := cs.saveStoreUnsafe(); err != nil {
				log.Printf("[cron] failed to save store after remove: %v", err)
			}
		}
	}

	cs.notify()

	return removed
}

func (cs *CronService) EnableJob(jobID string, enabled bool) (*CronJob, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.writesBlocked() {
		return nil, cs.storeUnavailableErr()
	}

	for i := range cs.store.Jobs {
		job := &cs.store.Jobs[i]
		if job.ID == jobID {
			previous := cs.store.Jobs[i]
			job.Enabled = enabled
			job.UpdatedAtMS = time.Now().UnixMilli()

			if enabled {
				job.State.NextRunAtMS = cs.computeNextRun(&job.Schedule, time.Now().UnixMilli())
			} else {
				job.State.NextRunAtMS = nil
			}

			if err := cs.saveStoreUnsafe(); err != nil {
				if fileutil.IsCommittedWriteError(err) {
					// The atomic rename already installed the new store: keep
					// the in-memory change so it matches durable state and
					// surface the uncertain durability.
					cs.notify()
					return job, fmt.Errorf("job %s updated but durability was not confirmed: %w", jobID, err)
				}
				// Pre-commit failure: restore the previous state so a job
				// reported as not updated cannot run in the live scheduler.
				cs.store.Jobs[i] = previous
				return nil, fmt.Errorf("failed to save store after enable: %w", err)
			}

			cs.notify()

			return job, nil
		}
	}

	return nil, fmt.Errorf("job %s not found", jobID)
}

func (cs *CronService) ListJobs(includeDisabled bool) []CronJob {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	if includeDisabled {
		return cs.store.Jobs
	}

	var enabled []CronJob
	for _, job := range cs.store.Jobs {
		if job.Enabled {
			enabled = append(enabled, job)
		}
	}

	return enabled
}

func (cs *CronService) Status() map[string]any {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	var enabledCount int
	for _, job := range cs.store.Jobs {
		if job.Enabled {
			enabledCount++
		}
	}

	return map[string]any{
		"enabled":      cs.running,
		"jobs":         len(cs.store.Jobs),
		"activeJobs":   cs.activeJobs,
		"nextWakeAtMS": cs.getNextWakeMS(),
	}
}

func (cs *CronService) ActiveJobCount() int {
	if cs == nil {
		return 0
	}
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.activeJobs
}

func (cs *CronService) incrementActiveJobs() {
	cs.mu.Lock()
	cs.activeJobs++
	cs.mu.Unlock()
}

func (cs *CronService) decrementActiveJobs() {
	cs.mu.Lock()
	if cs.activeJobs > 0 {
		cs.activeJobs--
	}
	cs.mu.Unlock()
}

func generateID() string {
	// Use crypto/rand for better uniqueness under concurrent access
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fallback to time-based if crypto/rand fails
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
