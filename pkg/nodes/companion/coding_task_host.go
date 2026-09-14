package companion

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
	"github.com/bogdanovich/mintclaw/pkg/coding/worker"
	"github.com/bogdanovich/mintclaw/pkg/coding/worktree"
)

const defaultCodingControlTimeout = 5 * time.Second

var (
	ErrCodingTaskBusy       = errors.New("coding project is busy")
	ErrCodingTaskNotRunning = errors.New("coding task worker is not running")
	ErrCodingTaskNotIdle    = errors.New("coding task is not resumable from idle")
	ErrCodingTaskHostClosed = errors.New("coding task host is closed")
	ErrCodingTaskUnsettled  = errors.New("coding task has no live host owner")
	errCodingTaskNoChange   = errors.New("coding task projection is already settled")
)

type codingTaskOutcome string

const (
	codingTaskOutcomeCompleted codingTaskOutcome = "completed"
	codingTaskOutcomeCanceled  codingTaskOutcome = "canceled"
	codingTaskOutcomeIdle      codingTaskOutcome = "idle"
	codingTaskOutcomeFailed    codingTaskOutcome = "failed"
	codingTaskOutcomeUncertain codingTaskOutcome = "uncertain"
)

type codingTaskProcessResult struct {
	outcome codingTaskOutcome
	handoff *worktree.Handoff
	report  *codingtask.TerminalReport
}

type codingTaskProcess interface {
	StartTurn(context.Context, string, string, []worker.TurnAttachment) error
	Steer(context.Context, string, string, *worker.QuestionAnswerRef) error
	HardCancel(context.Context, string) error
	Shutdown(context.Context, string) error
	Snapshot(context.Context) (worker.SnapshotResult, error)
	EventsAfter(uint64) worker.EventPage
	Wake() <-chan struct{}
	Done() <-chan struct{}
	Wait(context.Context) (codingTaskProcessResult, error)
	Terminate(context.Context) error
	Close() error
}

type codingPreparedTask struct {
	record codingtask.Record
	launch func(context.Context) (codingTaskProcess, error)
	abort  func() error
}

type codingTaskBackend interface {
	Prepare(context.Context, CodingProjectPolicy, codingtask.Record, string) (codingPreparedTask, error)
}

type activeCodingTask struct {
	invocationID  string
	projectAlias  string
	taskID        string
	generationID  string
	workerID      string
	threadID      string
	process       codingTaskProcess
	taskContext   context.Context
	cancelTask    context.CancelFunc
	settled       chan struct{}
	settleOnce    sync.Once
	settlementMu  sync.Mutex
	settlementErr error
	reportMu      sync.Mutex
	reportItems   map[string]worker.Item
}

// CodingTaskHost is the node-local owner of live channel-originated coding
// workers. Durable task identity and lifecycle remain in InvocationLedger;
// this host retains only process-scoped control handles and concurrency slots.
type CodingTaskHost struct {
	catalog       *CodingProjectCatalog
	ledger        *InvocationLedger
	parentBuildID string
	backend       codingTaskBackend
	newID         func() string
	startContext  context.Context
	cancelStarts  context.CancelFunc

	mu             sync.Mutex
	active         map[string]*activeCodingTask
	projectActive  map[string]int
	starting       map[string]chan struct{}
	startingTasks  map[string]string
	closed         bool
	controlTimeout time.Duration
	settlementErr  error
}

func NewCodingTaskHost(
	catalog *CodingProjectCatalog,
	ledger *InvocationLedger,
	parentBuildID string,
) (*CodingTaskHost, error) {
	return newCodingTaskHost(catalog, ledger, parentBuildID, nativeCodingTaskBackend{})
}

func newCodingTaskHost(
	catalog *CodingProjectCatalog,
	ledger *InvocationLedger,
	parentBuildID string,
	backend codingTaskBackend,
) (*CodingTaskHost, error) {
	if catalog == nil || ledger == nil || backend == nil || parentBuildID == "" {
		return nil, errors.New("coding task host requires catalog, ledger, build identity, and backend")
	}
	startContext, cancelStarts := context.WithCancel(context.Background())
	host := &CodingTaskHost{
		catalog: catalog, ledger: ledger, parentBuildID: parentBuildID, backend: backend,
		newID: uuid.NewString, active: make(map[string]*activeCodingTask),
		projectActive: make(map[string]int), starting: make(map[string]chan struct{}),
		startingTasks: make(map[string]string), controlTimeout: defaultCodingControlTimeout,
		startContext: startContext, cancelStarts: cancelStarts,
	}
	if err := host.recoverUnfinished(); err != nil {
		return nil, err
	}
	return host, nil
}

// Start binds one accepted start invocation to a native thread and worker
// generation before repository preparation or process launch. A duplicate
// identical invocation returns its retained mapping and never replays text.
func (host *CodingTaskHost) Start(
	ctx context.Context,
	invocationID string,
	request codingtask.StartRequest,
) (codingtask.Record, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !codingtask.ValidIdentifier(invocationID) || request.Validate() != nil {
		return codingtask.Record{}, false, codingtask.ErrInvalidRequest
	}
	for {
		record, found, err := host.lookupStart(invocationID, request)
		if err != nil {
			return codingtask.Record{}, false, err
		}
		if found {
			classified, wait, classifyErr := host.classifyRetainedStart(record, true)
			if classifyErr != nil {
				return codingtask.Record{}, false, classifyErr
			}
			if wait == nil {
				return classified, true, nil
			}
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return codingtask.Record{}, false, ctx.Err()
			}
		}
		policy, err := host.catalog.resolve(ctx, request.ProjectAlias, request.ProjectRevision, request.Mode)
		if err != nil {
			return codingtask.Record{}, false, err
		}
		wait, active, err := host.reserveStart(
			invocationID,
			request.TaskID,
			request.TaskGenerationID,
			request.ProjectAlias,
			policy.MaxConcurrentTasks,
		)
		if err != nil {
			return codingtask.Record{}, false, err
		}
		if wait != nil {
			if active {
				continue
			}
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return codingtask.Record{}, false, ctx.Err()
			}
		}
		record, found, err = host.lookupStart(invocationID, request)
		if err != nil || found {
			host.releaseStart(
				invocationID,
				request.TaskID,
				request.TaskGenerationID,
				request.ProjectAlias,
				false,
			)
			if err != nil {
				return codingtask.Record{}, false, err
			}
			classified, _, classifyErr := host.classifyRetainedStart(record, false)
			if classifyErr != nil {
				return codingtask.Record{}, false, classifyErr
			}
			return classified, true, nil
		}
		return host.startReserved(ctx, invocationID, request, policy)
	}
}

func (host *CodingTaskHost) startReserved(
	ctx context.Context,
	invocationID string,
	request codingtask.StartRequest,
	policy CodingProjectPolicy,
) (record codingtask.Record, existing bool, err error) {
	activated := false
	defer func() {
		host.releaseStart(
			invocationID,
			request.TaskID,
			request.TaskGenerationID,
			request.ProjectAlias,
			activated,
		)
	}()
	setupContext, cancelSetup := context.WithCancel(ctx)
	stopHostCancellation := context.AfterFunc(host.startContext, cancelSetup)
	if host.startContext.Err() != nil {
		cancelSetup()
	}
	defer func() {
		stopHostCancellation()
		cancelSetup()
	}()

	record = host.initialRecord(invocationID, request, policy)
	record, existing, err = host.ledger.bindCodingTask(invocationID, record)
	if err != nil {
		return codingtask.Record{}, false, host.failBeforeActivation(
			invocationID,
			"TASK_BIND_UNCERTAIN",
			"coding task binding is uncertain",
			true,
			err,
		)
	}
	if existing {
		return record, true, nil
	}
	record, err = host.updateTask(invocationID, func(next *codingtask.Record, _ int64) error {
		next.State = codingtask.StatePreparing
		return nil
	})
	if err != nil {
		return codingtask.Record{}, false, host.failBeforeActivation(
			invocationID,
			"TASK_PREPARATION_UNCERTAIN",
			"coding task preparation is uncertain",
			true,
			err,
		)
	}

	record, activated, err = host.activatePreparedTask(
		setupContext,
		record,
		policy,
		request.TurnIdempotencyKey,
		request.Prompt(),
	)
	return record, false, err
}

func (host *CodingTaskHost) activatePreparedTask(
	ctx context.Context,
	record codingtask.Record,
	policy CodingProjectPolicy,
	turnIdempotencyKey string,
	text string,
) (result codingtask.Record, activated bool, err error) {
	invocationID := record.InvocationID
	prepared, err := host.backend.Prepare(ctx, policy, record, host.parentBuildID)
	if err != nil {
		uncertain := codingPreparationUncertain(err)
		return codingtask.Record{}, false, host.failBeforeActivation(
			invocationID,
			codingFailureCode(uncertain, "TASK_PREPARATION_FAILED", "TASK_PREPARATION_UNCERTAIN"),
			codingFailureMessage(uncertain, "coding task preparation failed", "coding task preparation is uncertain"),
			uncertain,
			err,
		)
	}
	if prepared.launch == nil || prepared.abort == nil || prepared.record.InvocationID != invocationID {
		invalidErr := errors.New("coding task backend returned invalid preparation")
		var abortErr error
		if prepared.abort != nil {
			abortErr = prepared.abort()
		}
		uncertain := prepared.abort == nil || abortErr != nil
		return codingtask.Record{}, false, host.failBeforeActivation(
			invocationID,
			codingFailureCode(uncertain, "TASK_PREPARATION_FAILED", "TASK_PREPARATION_UNCERTAIN"),
			codingFailureMessage(uncertain, "coding task preparation failed", "coding task preparation is uncertain"),
			uncertain,
			errors.Join(invalidErr, abortErr),
		)
	}
	record, err = host.updateTask(invocationID, func(next *codingtask.Record, _ int64) error {
		next.ExecutionRoot = prepared.record.ExecutionRoot
		next.ExecutionRootIdentity = prepared.record.ExecutionRootIdentity
		next.Branch = prepared.record.Branch
		return nil
	})
	if err != nil {
		abortErr := prepared.abort()
		return codingtask.Record{}, false, host.failBeforeActivation(
			invocationID,
			"TASK_PREPARATION_UNCERTAIN",
			"coding task preparation is uncertain",
			true,
			errors.Join(err, abortErr),
		)
	}

	taskContext, cancelTask := context.WithTimeout(context.Background(), policy.taskTimeout)
	process, err := prepared.launch(ctx)
	if err != nil {
		cancelTask()
		abortErr := prepared.abort()
		uncertain := codingLaunchUncertain(err) || abortErr != nil
		return codingtask.Record{}, false, host.failBeforeActivation(
			invocationID,
			codingFailureCode(uncertain, "WORKER_LAUNCH_FAILED", "WORKER_LAUNCH_UNCERTAIN"),
			codingFailureMessage(uncertain, "coding worker launch failed", "coding worker launch is uncertain"),
			uncertain,
			errors.Join(err, abortErr),
		)
	}
	active := &activeCodingTask{
		invocationID: invocationID, projectAlias: record.ProjectAlias,
		taskID: record.TaskID, generationID: record.TaskGenerationID,
		workerID: record.WorkerGenerationID, threadID: record.ThreadID, process: process,
		taskContext: taskContext, cancelTask: cancelTask, settled: make(chan struct{}),
		reportItems: make(map[string]worker.Item),
	}
	host.installActive(active)
	if err = process.StartTurn(ctx, turnIdempotencyKey, text, nil); err != nil {
		host.settleUncertainStart(active)
		return codingtask.Record{}, true, err
	}
	record, err = host.updateTask(invocationID, func(next *codingtask.Record, _ int64) error {
		next.State = codingtask.StateRunning
		next.Activity = codingtask.ActivityRunning
		next.Status = "coding task running"
		return nil
	})
	if err != nil {
		host.settleUncertainStart(active)
		return codingtask.Record{}, true, err
	}
	go host.watch(active)
	return record, true, nil
}

func (host *CodingTaskHost) initialRecord(
	invocationID string,
	request codingtask.StartRequest,
	policy CodingProjectPolicy,
) codingtask.Record {
	threadID := host.newID()
	record := codingtask.Record{
		SchemaVersion: codingtask.SchemaVersion, InvocationID: invocationID,
		RequestDigest: request.RequestDigest, TaskID: request.TaskID,
		TaskGenerationID: request.TaskGenerationID, ProjectAlias: request.ProjectAlias,
		ProjectRevision: request.ProjectRevision, Mode: request.Mode, ThreadID: threadID,
		ThreadOpenMode: codingtask.ThreadOpenNew, WorkerGenerationID: "worker-" + host.newID(),
		Project: policy.project, ProviderProfile: policy.ProviderProfile,
		Model: policy.Model, Provider: policy.Provider, ExpectedWorkerBuildID: policy.workerBuildID,
		State: codingtask.StateAccepted,
	}
	if request.Mode == codingtask.TaskModeInvestigate {
		record.ExecutionRoot = policy.project.ProjectRoot
		record.ExecutionRootIdentity = codingtask.ExecutionRootIdentity(record.ExecutionRoot)
	} else {
		record.WorktreeID = codingtask.WorktreeIDForThread(threadID)
	}
	return record
}

func (host *CodingTaskHost) Status(taskID string, generationID string) (codingtask.Record, error) {
	record, found := host.taskByIdentity(taskID, generationID)
	if !found {
		return codingtask.Record{}, ErrCodingTaskNotFound
	}
	return record, nil
}

func (host *CodingTaskHost) Steer(
	ctx context.Context,
	taskID string,
	generationID string,
	workerID string,
	idempotencyKey string,
	text string,
	answer *worker.QuestionAnswerRef,
) (codingtask.Record, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	record, found := host.taskByIdentity(taskID, generationID)
	if !found {
		return codingtask.Record{}, ErrCodingTaskNotFound
	}
	if answer == nil {
		resume := codingtask.NewResumeRequest(taskID, generationID, workerID, text, idempotencyKey)
		if record.MatchesResumeRequest(resume) {
			return record, nil
		}
		if record.State == codingtask.StateIdle {
			return host.resumeIdle(ctx, resume)
		}
	} else if record.State == codingtask.StateIdle {
		return codingtask.Record{}, codingtask.ErrInvalidRequest
	}
	active, err := host.activeTask(taskID, generationID, workerID)
	if err != nil {
		return codingtask.Record{}, err
	}
	if err = active.process.Steer(ctx, idempotencyKey, text, answer); err != nil {
		if errors.Is(err, worker.ErrControlStreamUncertain) {
			host.settleControlUncertain(active)
		}
		return codingtask.Record{}, err
	}
	return host.updateTask(active.invocationID, func(next *codingtask.Record, _ int64) error {
		if answer != nil && next.State == codingtask.StateWaitingInput && next.Question != nil &&
			next.Question.QuestionID == answer.QuestionID && next.Question.Revision == answer.QuestionRevision {
			next.State = codingtask.StateRunning
			next.Activity = codingtask.ActivityRunning
			next.Question = nil
			next.Status = "coding task resumed"
		}
		return nil
	})
}

func (host *CodingTaskHost) resumeIdle(
	ctx context.Context,
	request codingtask.ResumeRequest,
) (codingtask.Record, error) {
	if request.Validate() != nil {
		return codingtask.Record{}, codingtask.ErrInvalidRequest
	}
	for {
		record, found := host.taskByIdentity(request.TaskID, request.TaskGenerationID)
		if !found {
			return codingtask.Record{}, ErrCodingTaskNotFound
		}
		if record.MatchesResumeRequest(request) {
			return record, nil
		}
		if record.State != codingtask.StateIdle ||
			record.WorkerGenerationID != request.PreviousWorkerGenerationID {
			return codingtask.Record{}, ErrCodingTaskNotIdle
		}
		policy, err := host.catalog.resolve(
			ctx,
			record.ProjectAlias,
			record.ProjectRevision,
			record.Mode,
		)
		if err != nil {
			return codingtask.Record{}, err
		}
		wait, active, err := host.reserveStart(
			record.InvocationID,
			record.TaskID,
			record.TaskGenerationID,
			record.ProjectAlias,
			policy.MaxConcurrentTasks,
		)
		if err != nil {
			return codingtask.Record{}, err
		}
		if wait != nil {
			if active {
				latest, latestFound := host.ledger.codingTask(record.InvocationID)
				if latestFound && latest.MatchesResumeRequest(request) {
					return latest, nil
				}
			}
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return codingtask.Record{}, ctx.Err()
			}
		}
		latest, found := host.ledger.codingTask(record.InvocationID)
		if !found {
			host.releaseStart(
				record.InvocationID,
				record.TaskID,
				record.TaskGenerationID,
				record.ProjectAlias,
				false,
			)
			return codingtask.Record{}, ErrCodingTaskNotFound
		}
		if latest.MatchesResumeRequest(request) {
			host.releaseStart(
				record.InvocationID,
				record.TaskID,
				record.TaskGenerationID,
				record.ProjectAlias,
				false,
			)
			return latest, nil
		}
		if latest.Revision != record.Revision || latest.State != codingtask.StateIdle ||
			latest.WorkerGenerationID != request.PreviousWorkerGenerationID {
			host.releaseStart(
				record.InvocationID,
				record.TaskID,
				record.TaskGenerationID,
				record.ProjectAlias,
				false,
			)
			continue
		}
		return host.resumeReserved(ctx, latest, request, policy)
	}
}

func (host *CodingTaskHost) resumeReserved(
	ctx context.Context,
	record codingtask.Record,
	request codingtask.ResumeRequest,
	policy CodingProjectPolicy,
) (result codingtask.Record, err error) {
	activated := false
	defer func() {
		host.releaseStart(
			record.InvocationID,
			record.TaskID,
			record.TaskGenerationID,
			record.ProjectAlias,
			activated,
		)
	}()
	setupContext, cancelSetup := context.WithCancel(ctx)
	stopHostCancellation := context.AfterFunc(host.startContext, cancelSetup)
	if host.startContext.Err() != nil {
		cancelSetup()
	}
	defer func() {
		stopHostCancellation()
		cancelSetup()
	}()

	result, existing, err := host.ledger.advanceCodingTaskWorker(
		record.InvocationID,
		record.Revision,
		request,
		"worker-"+host.newID(),
	)
	if err != nil || existing {
		return result, err
	}
	result, activated, err = host.activatePreparedTask(
		setupContext,
		result,
		policy,
		request.TurnIdempotencyKey,
		request.Text,
	)
	return result, err
}

func (host *CodingTaskHost) Cancel(
	ctx context.Context,
	taskID string,
	generationID string,
	workerID string,
	idempotencyKey string,
) (codingtask.Record, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	active, err := host.activeTask(taskID, generationID, workerID)
	if err != nil {
		return codingtask.Record{}, err
	}
	_, transitionErr := host.updateTask(active.invocationID, func(next *codingtask.Record, _ int64) error {
		if next.State == codingtask.StateWaitingInput {
			next.State = codingtask.StateRunning
			next.Question = nil
		}
		next.Activity = codingtask.ActivityInterrupting
		next.Status = "cancellation requested"
		return nil
	})
	if err = active.process.HardCancel(ctx, idempotencyKey); err != nil {
		if errors.Is(err, worker.ErrControlStreamUncertain) {
			host.settleControlUncertain(active)
		}
		return codingtask.Record{}, err
	}
	if transitionErr != nil {
		return codingtask.Record{}, transitionErr
	}
	return host.Status(taskID, generationID)
}

func (host *CodingTaskHost) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	host.mu.Lock()
	host.closed = true
	starting := make([]<-chan struct{}, 0, len(host.starting))
	for _, done := range host.starting {
		starting = append(starting, done)
	}
	host.mu.Unlock()
	host.cancelStarts()
	for _, done := range starting {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	host.mu.Lock()
	active := make([]*activeCodingTask, 0, len(host.active))
	for _, task := range host.active {
		active = append(active, task)
	}
	host.mu.Unlock()
	var result error
	for _, task := range active {
		controlContext, cancel := context.WithTimeout(ctx, host.controlTimeout)
		err := task.process.Shutdown(controlContext, "host-shutdown-"+task.workerID)
		cancel()
		if err == nil {
			timer := time.NewTimer(host.controlTimeout)
			select {
			case <-task.process.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			case <-ctx.Done():
				err = ctx.Err()
			case <-timer.C:
			}
			if err != nil || !codingTaskProcessDone(task.process) {
				terminationContext, cancelTermination := context.WithTimeout(
					context.Background(),
					host.controlTimeout,
				)
				err = errors.Join(err, task.process.Terminate(terminationContext))
				cancelTermination()
			}
		} else {
			terminationContext, cancelTermination := context.WithTimeout(
				context.Background(),
				host.controlTimeout,
			)
			err = errors.Join(err, task.process.Terminate(terminationContext))
			cancelTermination()
		}
		result = errors.Join(result, err)
	}
	for _, task := range active {
		select {
		case <-task.settled:
		case <-ctx.Done():
			result = errors.Join(result, ctx.Err())
			return errors.Join(result, host.settlementError())
		}
	}
	return errors.Join(result, host.settlementError())
}

func (host *CodingTaskHost) watch(active *activeCodingTask) {
	cursor := uint64(0)
	for {
		page := active.process.EventsAfter(cursor)
		if page.HistoryGap {
			snapshotContext, cancel := context.WithTimeout(context.Background(), host.controlTimeout)
			snapshot, err := active.process.Snapshot(snapshotContext)
			cancel()
			if err != nil {
				host.settleControlUncertain(active)
				return
			}
			if !codingControlMatches(active, snapshot.ControlIdentity) ||
				snapshot.Snapshot.ThreadID != active.threadID {
				host.settleControlUncertain(active)
				return
			}
			if !host.projectSnapshot(active, snapshot.Snapshot) {
				return
			}
		} else {
			for _, event := range page.Events {
				if !host.projectEvent(active, event.Record) {
					return
				}
			}
		}
		cursor = page.NextCursor
		select {
		case <-active.process.Done():
			host.settleProcess(active)
			return
		case <-active.taskContext.Done():
			host.expireProcess(active)
			<-active.process.Done()
			host.settleProcess(active)
			return
		case <-active.process.Wake():
		}
	}
}

func (host *CodingTaskHost) projectEvent(active *activeCodingTask, event worker.Record) bool {
	payload, err := worker.DecodeEventPayload(event.Event, event.Payload)
	if err != nil || !codingEventMatches(active, payload) {
		host.settleControlUncertain(active)
		return false
	}
	var projectionErr error
	switch typed := payload.(type) {
	case *worker.ItemUpdatedPayload:
		active.projectReportItem(typed.Item)
	case *worker.StatusChangedPayload:
		projectionErr = host.projectStatus(active, typed.Activity, typed.Status)
	case *worker.QuestionStatePayload:
		projectionErr = host.projectQuestion(active, typed.Question)
	}
	if projectionErr != nil {
		host.settleControlUncertain(active)
		return false
	}
	return true
}

func (host *CodingTaskHost) projectSnapshot(active *activeCodingTask, snapshot worker.Snapshot) bool {
	for _, item := range snapshot.Items {
		active.projectReportItem(item)
	}
	_, err := host.updateTask(active.invocationID, func(next *codingtask.Record, _ int64) error {
		if next.WorkerGenerationID != active.workerID {
			return ErrCodingTaskConflict
		}
		if next.State.Terminal() {
			return errCodingTaskNoChange
		}
		if snapshot.Question != nil {
			if snapshot.Activity != worker.ActivityWaitingInput {
				return errors.New("coding worker snapshot question does not match its activity")
			}
			next.State = codingtask.StateWaitingInput
			next.Activity = codingtask.ActivityWaitingInput
			next.Status = "waiting for input"
			next.Question = codingTaskQuestion(*snapshot.Question)
			return nil
		}
		next.Question = nil
		switch snapshot.Activity {
		case worker.ActivityIdle:
			next.State = codingtask.StateRunning
			next.Activity = codingtask.ActivityRunning
			next.Status = "coding worker awaiting finalization"
			return nil
		case worker.ActivityRunning, worker.ActivityInterrupting,
			worker.ActivityCompacting, worker.ActivityReviewing:
			mapped, _ := codingTaskActivity(snapshot.Activity)
			next.State = codingtask.StateRunning
			next.Activity = mapped
		default:
			return errors.New("coding worker snapshot lacks a projectable lifecycle")
		}
		next.Status = snapshot.Status
		return nil
	})
	if err != nil {
		host.settleControlUncertain(active)
		return false
	}
	return true
}

func (host *CodingTaskHost) projectStatus(
	active *activeCodingTask,
	activity worker.Activity,
	status string,
) error {
	mapped, usable := codingTaskActivity(activity)
	if !usable {
		return nil
	}
	_, err := host.updateTask(active.invocationID, func(next *codingtask.Record, _ int64) error {
		if next.WorkerGenerationID != active.workerID {
			return ErrCodingTaskConflict
		}
		if next.State.Terminal() || next.State == codingtask.StateWaitingInput {
			return errCodingTaskNoChange
		}
		next.Activity = mapped
		next.Status = status
		return nil
	})
	return err
}

func (host *CodingTaskHost) projectQuestion(active *activeCodingTask, question worker.QuestionState) error {
	_, err := host.updateTask(active.invocationID, func(next *codingtask.Record, _ int64) error {
		if next.WorkerGenerationID != active.workerID {
			return ErrCodingTaskConflict
		}
		if next.State.Terminal() {
			return errCodingTaskNoChange
		}
		if question.Status != worker.QuestionWaiting {
			if next.State == codingtask.StateWaitingInput && next.Question != nil &&
				next.Question.QuestionID == question.QuestionID && next.Question.Revision <= question.Revision {
				next.State = codingtask.StateRunning
				next.Activity = codingtask.ActivityRunning
				next.Question = nil
				return nil
			}
			return errCodingTaskNoChange
		}
		next.State = codingtask.StateWaitingInput
		next.Activity = codingtask.ActivityWaitingInput
		next.Status = "waiting for input"
		next.Question = codingTaskQuestion(question)
		return nil
	})
	return err
}

func (host *CodingTaskHost) settleProcess(active *activeCodingTask) {
	defer func() {
		active.cancelTask()
		host.removeActive(active)
	}()
	result, err := active.process.Wait(context.Background())
	if err != nil {
		result.outcome = codingTaskOutcomeUncertain
	}
	active.captureTerminalReportEvents()
	result.report = active.terminalReport(result)
	_, transitionErr := host.updateTask(active.invocationID, func(next *codingtask.Record, now int64) error {
		if next.WorkerGenerationID != active.workerID {
			return ErrCodingTaskConflict
		}
		if next.State.Terminal() {
			return errCodingTaskNoChange
		}
		applyCodingTaskOutcome(next, result, now, host.retention(next.ProjectAlias, next.ProjectRevision))
		return nil
	})
	if transitionErr != nil {
		host.recordActiveSettlement(active, host.failRetainedTask(
			active.invocationID,
			"WORKER_OUTCOME_UNCERTAIN",
			"coding worker outcome is uncertain",
			true,
		))
	}
}

func (host *CodingTaskHost) expireProcess(active *activeCodingTask) {
	controlContext, cancel := context.WithTimeout(context.Background(), host.controlTimeout)
	err := active.process.HardCancel(controlContext, "task-timeout-"+active.workerID)
	cancel()
	if err == nil {
		timer := time.NewTimer(host.controlTimeout)
		select {
		case <-active.process.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
	}
	terminationContext, cancelTermination := context.WithTimeout(context.Background(), host.controlTimeout)
	_ = active.process.Terminate(terminationContext)
	cancelTermination()
}

func codingTaskProcessDone(process codingTaskProcess) bool {
	select {
	case <-process.Done():
		return true
	default:
		return false
	}
}

func (host *CodingTaskHost) settleUncertainStart(active *activeCodingTask) {
	controlContext, cancel := context.WithTimeout(context.Background(), host.controlTimeout)
	_ = active.process.Terminate(controlContext)
	cancel()
	host.recordActiveSettlement(active, host.failRetainedTask(
		active.invocationID,
		"TURN_ACCEPTANCE_UNCERTAIN",
		"coding turn acceptance is uncertain",
		true,
	))
	_ = active.process.Close()
	host.releaseActiveWhenDone(active)
}

func (host *CodingTaskHost) settleControlUncertain(active *activeCodingTask) {
	controlContext, cancel := context.WithTimeout(context.Background(), host.controlTimeout)
	_ = active.process.Terminate(controlContext)
	cancel()
	host.recordActiveSettlement(active, host.failRetainedTask(
		active.invocationID,
		"WORKER_CONTROL_UNCERTAIN",
		"coding worker control is uncertain",
		true,
	))
	_ = active.process.Close()
	host.releaseActiveWhenDone(active)
}

func (host *CodingTaskHost) releaseActiveWhenDone(active *activeCodingTask) {
	active.cancelTask()
	if codingTaskProcessDone(active.process) {
		host.removeActive(active)
		return
	}
	go func() {
		<-active.process.Done()
		host.removeActive(active)
	}()
}

func (host *CodingTaskHost) failBeforeActivation(
	invocationID string,
	code string,
	message string,
	uncertain bool,
	cause error,
) error {
	if _, found := host.ledger.codingTask(invocationID); !found {
		return cause
	}
	settlementErr := host.failRetainedTask(invocationID, code, message, uncertain)
	host.recordSettlement(settlementErr)
	return errors.Join(cause, settlementErr)
}

func (host *CodingTaskHost) failRetainedTask(
	invocationID string,
	code string,
	message string,
	uncertain bool,
) error {
	_, err := host.updateTask(invocationID, func(next *codingtask.Record, now int64) error {
		if next.State.Terminal() {
			return errCodingTaskNoChange
		}
		if uncertain {
			next.State = codingtask.StateUncertain
		} else {
			next.State = codingtask.StateFailed
		}
		next.Activity = codingtask.ActivityFailed
		next.Status = message
		next.Question = nil
		next.Failure = &codingtask.Failure{Code: code, Message: message}
		next.RetainUntil = now + int64(host.retention(next.ProjectAlias, next.ProjectRevision))
		return nil
	})
	return err
}

func (host *CodingTaskHost) updateTask(
	invocationID string,
	update func(*codingtask.Record, int64) error,
) (codingtask.Record, error) {
	for {
		current, found := host.ledger.codingTask(invocationID)
		if !found {
			return codingtask.Record{}, ErrCodingTaskNotFound
		}
		next, err := host.ledger.updateCodingTask(invocationID, current.Revision, update)
		if errors.Is(err, errCodingTaskNoChange) {
			return current, nil
		}
		if !errors.Is(err, ErrCodingTaskConflict) {
			return next, err
		}
		latest, found := host.ledger.codingTask(invocationID)
		if !found || latest.Revision == current.Revision {
			return codingtask.Record{}, err
		}
	}
}

func (host *CodingTaskHost) recoverUnfinished() error {
	for _, record := range host.ledger.codingTaskRecords() {
		if record.State.Terminal() || record.State == codingtask.StateIdle {
			continue
		}
		if _, err := host.updateTask(record.InvocationID, func(next *codingtask.Record, now int64) error {
			next.State = codingtask.StateUncertain
			next.Activity = codingtask.ActivityFailed
			next.Status = "coding task host restarted"
			next.Question = nil
			next.Failure = &codingtask.Failure{
				Code: "HOST_RESTARTED", Message: "coding task host restarted without live worker evidence",
			}
			next.RetainUntil = now + int64(host.retention(next.ProjectAlias, next.ProjectRevision))
			return nil
		}); err != nil {
			return fmt.Errorf("recover coding task %s: %w", record.TaskID, err)
		}
	}
	return nil
}

func (host *CodingTaskHost) taskByIdentity(taskID string, generationID string) (codingtask.Record, bool) {
	for _, record := range host.ledger.codingTaskRecords() {
		if record.TaskID == taskID && record.TaskGenerationID == generationID {
			return record, true
		}
	}
	return codingtask.Record{}, false
}

func (host *CodingTaskHost) lookupStart(
	invocationID string,
	request codingtask.StartRequest,
) (codingtask.Record, bool, error) {
	if record, found := host.ledger.codingTask(invocationID); found {
		if !record.MatchesRequest(request) {
			return codingtask.Record{}, false, ErrCodingTaskConflict
		}
		return record, true, nil
	}
	if _, found := host.taskByIdentity(request.TaskID, request.TaskGenerationID); found {
		return codingtask.Record{}, false, ErrCodingTaskConflict
	}
	return codingtask.Record{}, false, nil
}

func (host *CodingTaskHost) classifyRetainedStart(
	record codingtask.Record,
	includeStarting bool,
) (codingtask.Record, <-chan struct{}, error) {
	for {
		host.mu.Lock()
		var wait <-chan struct{}
		if includeStarting {
			wait = host.starting[record.InvocationID]
		}
		_, active := host.active[record.InvocationID]
		host.mu.Unlock()
		if wait != nil {
			return record, wait, nil
		}
		if active || record.State.Terminal() || record.State == codingtask.StateIdle {
			return record, nil, nil
		}
		latest, found := host.ledger.codingTask(record.InvocationID)
		if !found {
			return codingtask.Record{}, nil, ErrCodingTaskConflict
		}
		if latest.Revision == record.Revision {
			return codingtask.Record{}, nil, fmt.Errorf(
				"%w: retained %s task has no starting or active owner",
				ErrCodingTaskUnsettled,
				record.State,
			)
		}
		record = latest
	}
}

func (host *CodingTaskHost) reserveStart(
	invocationID string,
	taskID string,
	generationID string,
	projectAlias string,
	maximum int,
) (<-chan struct{}, bool, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closed {
		return nil, false, ErrCodingTaskHostClosed
	}
	if wait, found := host.starting[invocationID]; found {
		return wait, false, nil
	}
	if active, found := host.active[invocationID]; found {
		return active.settled, true, nil
	}
	identity := codingTaskIdentity(taskID, generationID)
	if owner, found := host.startingTasks[identity]; found && owner != invocationID {
		return nil, false, ErrCodingTaskConflict
	}
	if host.projectActive[projectAlias] >= maximum {
		return nil, false, ErrCodingTaskBusy
	}
	host.starting[invocationID] = make(chan struct{})
	host.startingTasks[identity] = invocationID
	host.projectActive[projectAlias]++
	return nil, false, nil
}

func (host *CodingTaskHost) releaseStart(
	invocationID string,
	taskID string,
	generationID string,
	projectAlias string,
	activated bool,
) {
	host.mu.Lock()
	defer host.mu.Unlock()
	if wait, found := host.starting[invocationID]; found {
		delete(host.starting, invocationID)
		close(wait)
	}
	delete(host.startingTasks, codingTaskIdentity(taskID, generationID))
	if !activated {
		host.releaseProjectLocked(projectAlias)
	}
}

func (host *CodingTaskHost) installActive(active *activeCodingTask) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.active[active.invocationID] = active
}

func (host *CodingTaskHost) removeActive(active *activeCodingTask) {
	host.mu.Lock()
	current, found := host.active[active.invocationID]
	if !found || current != active {
		host.mu.Unlock()
		return
	}
	delete(host.active, active.invocationID)
	host.releaseProjectLocked(active.projectAlias)
	host.mu.Unlock()
	active.settleOnce.Do(func() { close(active.settled) })
}

func (host *CodingTaskHost) recordActiveSettlement(active *activeCodingTask, err error) {
	host.recordSettlement(err)
	active.recordSettlement(err)
}

func (active *activeCodingTask) recordSettlement(err error) {
	if active == nil || err == nil {
		return
	}
	active.settlementMu.Lock()
	active.settlementErr = errors.Join(active.settlementErr, err)
	active.settlementMu.Unlock()
}

func (active *activeCodingTask) settlementError() error {
	if active == nil {
		return nil
	}
	active.settlementMu.Lock()
	defer active.settlementMu.Unlock()
	return active.settlementErr
}

func (host *CodingTaskHost) recordSettlement(err error) {
	if host == nil || err == nil {
		return
	}
	host.mu.Lock()
	host.settlementErr = errors.Join(host.settlementErr, err)
	host.mu.Unlock()
}

func (host *CodingTaskHost) settlementError() error {
	if host == nil {
		return nil
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	return host.settlementErr
}

func (host *CodingTaskHost) releaseProjectLocked(alias string) {
	if host.projectActive[alias] <= 1 {
		delete(host.projectActive, alias)
		return
	}
	host.projectActive[alias]--
}

func (host *CodingTaskHost) activeTask(
	taskID string,
	generationID string,
	workerID string,
) (*activeCodingTask, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closed {
		return nil, ErrCodingTaskHostClosed
	}
	for _, active := range host.active {
		if active.taskID == taskID && active.generationID == generationID && active.workerID == workerID {
			return active, nil
		}
	}
	return nil, ErrCodingTaskNotRunning
}

func (host *CodingTaskHost) retention(alias string, revision string) time.Duration {
	if host.catalog != nil {
		if policy, found := host.catalog.projects[alias]; found && policy.descriptorRevision == revision &&
			policy.retention > 0 && policy.retention <= codingtask.MaxRetainDuration {
			return policy.retention
		}
	}
	return DefaultCodingTaskRetention
}

func codingTaskIdentity(taskID string, generationID string) string {
	return taskID + "\x00" + generationID
}

func codingTaskActivity(activity worker.Activity) (codingtask.Activity, bool) {
	switch activity {
	case worker.ActivityRunning:
		return codingtask.ActivityRunning, true
	case worker.ActivityInterrupting:
		return codingtask.ActivityInterrupting, true
	case worker.ActivityCompacting:
		return codingtask.ActivityCompacting, true
	case worker.ActivityReviewing:
		return codingtask.ActivityReviewing, true
	default:
		return "", false
	}
}

func codingTaskQuestion(question worker.QuestionState) *codingtask.QuestionState {
	result := &codingtask.QuestionState{
		QuestionID: question.QuestionID, Revision: question.Revision,
		Status: codingtask.QuestionStatus(question.Status), Prompt: question.Prompt,
		Options: make([]codingtask.QuestionOption, len(question.Options)),
	}
	for index, option := range question.Options {
		result.Options[index] = codingtask.QuestionOption{
			ID: option.ID, Label: option.Label, Description: option.Description,
		}
	}
	return result
}

func codingEventMatches(active *activeCodingTask, payload any) bool {
	var identity worker.ControlIdentity
	switch typed := payload.(type) {
	case *worker.WorkerReadyPayload:
		identity = typed.ControlIdentity
	case *worker.ItemUpdatedPayload:
		identity = typed.ControlIdentity
	case *worker.StatusChangedPayload:
		identity = typed.ControlIdentity
	case *worker.QuestionStatePayload:
		identity = typed.ControlIdentity
	case *worker.ContextUsagePayload:
		identity = typed.ControlIdentity
	case *worker.TurnTerminalPayload:
		identity = typed.ControlIdentity
	case *worker.WorkerStoppedPayload:
		identity = typed.ControlIdentity
	default:
		return false
	}
	return codingControlMatches(active, identity)
}

func codingControlMatches(active *activeCodingTask, identity worker.ControlIdentity) bool {
	return active != nil && identity.TaskID == active.taskID &&
		identity.TaskGenerationID == active.generationID && identity.WorkerGenerationID == active.workerID
}

func applyCodingTaskOutcome(
	record *codingtask.Record,
	result codingTaskProcessResult,
	now int64,
	retention time.Duration,
) {
	record.Question = nil
	record.TerminalReport = nil
	if record.Mode == codingtask.TaskModeMutate {
		if !codingTaskHandoffMatches(*record, result.handoff) {
			setCodingTaskFailure(
				record,
				now,
				retention,
				"WORKER_OUTCOME_UNCERTAIN",
				"coding worker outcome is uncertain",
				true,
			)
			return
		}
		record.HandoffID = result.handoff.HandoffID
		record.Branch = result.handoff.Branch
	} else if result.handoff != nil {
		setCodingTaskFailure(
			record,
			now,
			retention,
			"WORKER_OUTCOME_UNCERTAIN",
			"coding worker outcome is uncertain",
			true,
		)
		return
	}
	switch result.outcome {
	case codingTaskOutcomeCompleted:
		record.State = codingtask.StateCompleted
		record.Activity = codingtask.ActivityIdle
		record.Status = "coding task completed"
		record.Failure = nil
		record.TerminalReport = result.report
		record.RetainUntil = now + int64(retention)
	case codingTaskOutcomeCanceled:
		record.State = codingtask.StateCanceled
		record.Activity = codingtask.ActivityIdle
		record.Status = "coding task canceled"
		record.Failure = nil
		record.TerminalReport = result.report
		record.RetainUntil = now + int64(retention)
	case codingTaskOutcomeIdle:
		record.State = codingtask.StateIdle
		record.Activity = codingtask.ActivityIdle
		record.Status = "coding worker idle"
		record.Failure = nil
		record.RetainUntil = 0
	case codingTaskOutcomeFailed:
		setCodingTaskFailure(record, now, retention, "WORKER_FAILED", "coding worker failed", false)
		record.TerminalReport = result.report
	default:
		setCodingTaskFailure(
			record,
			now,
			retention,
			"WORKER_OUTCOME_UNCERTAIN",
			"coding worker outcome is uncertain",
			true,
		)
		record.TerminalReport = result.report
	}
}

func codingTaskHandoffMatches(record codingtask.Record, handoff *worktree.Handoff) bool {
	return handoff != nil && handoff.Validate() == nil && handoff.WorktreeID == record.WorktreeID &&
		handoff.TaskID == record.TaskID && handoff.TaskGenerationID == record.TaskGenerationID &&
		handoff.ThreadID == record.ThreadID && handoff.SourceProjectKey == record.Project.ProjectKey &&
		handoff.ExecutionRoot == record.ExecutionRoot &&
		handoff.ExecutionRootIdentity == record.ExecutionRootIdentity && handoff.Branch == record.Branch
}

func setCodingTaskFailure(
	record *codingtask.Record,
	now int64,
	retention time.Duration,
	code string,
	message string,
	uncertain bool,
) {
	if uncertain {
		record.State = codingtask.StateUncertain
	} else {
		record.State = codingtask.StateFailed
	}
	record.Activity = codingtask.ActivityFailed
	record.Status = message
	record.Failure = &codingtask.Failure{Code: code, Message: message}
	record.RetainUntil = now + int64(retention)
}

func codingFailureCode(uncertain bool, failed string, unknown string) string {
	if uncertain {
		return unknown
	}
	return failed
}

func codingFailureMessage(uncertain bool, failed string, unknown string) string {
	if uncertain {
		return unknown
	}
	return failed
}
