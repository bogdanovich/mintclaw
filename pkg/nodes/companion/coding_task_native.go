package companion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bogdanovich/mintclaw/pkg/coding/privilege"
	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
	"github.com/bogdanovich/mintclaw/pkg/coding/worker"
	"github.com/bogdanovich/mintclaw/pkg/coding/workerprocess"
	"github.com/bogdanovich/mintclaw/pkg/coding/worktree"
)

type nativeCodingTaskBackend struct{}

func (nativeCodingTaskBackend) Prepare(
	ctx context.Context,
	policy CodingScopePolicy,
	record codingtask.Record,
	parentBuildID string,
) (codingPreparedTask, error) {
	if err := validateCodingTaskProcessAuthority(record.Profile); err != nil {
		return codingPreparedTask{}, err
	}
	launcher, err := workerprocess.NewLauncher(workerprocess.LauncherConfig{
		ExecutablePath: policy.WorkerExecutable,
		ParentBuildID:  parentBuildID,
		Environment:    codingWorkerEnvironment(policy.MintClawHome),
	})
	if err != nil {
		return codingPreparedTask{}, err
	}
	var privilegedBinding *privilege.Binding
	if record.Profile.Privileged() {
		privilegedBinding, err = prepareCodingPrivilegeBinding(ctx, policy.PrivilegedExecutor)
		if err != nil {
			return codingPreparedTask{}, err
		}
	}
	if !record.Profile.UsesIsolatedWorktree() {
		return codingPreparedTask{
			record: record,
			launch: func(launchContext context.Context) (codingTaskProcess, error) {
				binding, bindingErr := codingWorkerBinding(record, privilegedBinding)
				if bindingErr != nil {
					return nil, bindingErr
				}
				process, launchErr := launcher.Launch(launchContext, binding)
				if launchErr != nil {
					return nil, launchErr
				}
				return &nativeCodingProcess{Process: process}, nil
			},
			abort: func() error { return nil },
		}, nil
	}

	manager, err := worktree.NewManager(worktree.Config{
		StateRoot: filepath.Join(policy.MintClawHome, "coding"), WorktreeParent: policy.WorktreeParent,
	})
	if err != nil {
		return codingPreparedTask{}, err
	}
	allocation, err := manager.Allocate(ctx, worktree.Request{
		TaskID: record.TaskID, TaskGenerationID: record.TaskGenerationID,
		ThreadID: record.ThreadID, Source: record.Project, BaseRevision: record.Project.GitHead,
	})
	if err != nil {
		return codingPreparedTask{}, err
	}
	owner, err := manager.AcquireOwner(ctx, worktree.OwnerRequest{
		WorktreeID: allocation.WorktreeID, TaskID: record.TaskID,
		TaskGenerationID: record.TaskGenerationID, ThreadID: record.ThreadID,
		WorkerGenerationID: record.WorkerGenerationID,
	})
	if err != nil {
		return codingPreparedTask{}, err
	}
	record.ExecutionRoot = allocation.ExecutionRoot
	record.ExecutionRootIdentity = allocation.ExecutionRootIdentity
	record.Branch = allocation.Branch
	state := &nativeCodingPreparationState{owner: owner}
	return codingPreparedTask{
		record: record,
		launch: func(launchContext context.Context) (codingTaskProcess, error) {
			binding, bindingErr := codingWorkerBinding(record, nil)
			if bindingErr != nil {
				return nil, bindingErr
			}
			process, launchErr := launcher.LaunchOwned(launchContext, binding, owner)
			state.recordLaunch(process, launchErr)
			if launchErr != nil {
				return nil, launchErr
			}
			return &nativeOwnedCodingProcess{OwnedProcess: process}, nil
		},
		abort: state.abort,
	}, nil
}

type nativeCodingPreparationState struct {
	owner *worktree.Owner

	mu          sync.Mutex
	transferred bool
	pending     *workerprocess.OwnedLaunchError
}

func (state *nativeCodingPreparationState) recordLaunch(
	process *workerprocess.OwnedProcess,
	err error,
) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if process != nil {
		state.transferred = true
	}
	var pending *workerprocess.OwnedLaunchError
	if errors.As(err, &pending) {
		state.transferred = true
		state.pending = pending
	}
}

func (state *nativeCodingPreparationState) abort() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pending != nil {
		ctx, cancel := context.WithTimeout(context.Background(), workerprocess.DefaultHandoffTimeout)
		_, err := state.pending.RetryFinalization(ctx)
		cancel()
		if err == nil {
			state.pending = nil
		}
		return err
	}
	if state.transferred || state.owner == nil {
		return nil
	}
	err := state.owner.Release()
	if err == nil {
		state.owner = nil
	}
	return err
}

type nativeCodingProcess struct {
	*workerprocess.Process
}

func (process *nativeCodingProcess) Wait(ctx context.Context) (codingTaskProcessResult, error) {
	result, err := process.Process.Wait(ctx)
	return codingTaskProcessResult{outcome: codingTaskOutcomeForWorker(result.Outcome())}, err
}

type nativeOwnedCodingProcess struct {
	*workerprocess.OwnedProcess
}

func (process *nativeOwnedCodingProcess) Wait(ctx context.Context) (codingTaskProcessResult, error) {
	result, err := process.OwnedProcess.Wait(ctx)
	return codingTaskProcessResult{
		outcome: codingTaskOutcomeForWorker(result.Process.Outcome()), handoff: result.Handoff,
	}, errors.Join(err, result.FinalizationError)
}

func codingWorkerBinding(
	record codingtask.Record,
	privilegedBinding *privilege.Binding,
) (worker.Binding, error) {
	binding, err := record.WorkerBinding()
	if err != nil {
		return worker.Binding{}, err
	}
	result := worker.Binding{
		TaskID: binding.TaskID, TaskGenerationID: binding.TaskGenerationID,
		WorkerGenerationID: binding.WorkerGenerationID, ThreadID: binding.ThreadID,
		ThreadOpenMode: worker.ThreadOpenMode(binding.ThreadOpenMode), Project: binding.Project,
		ExecutionRoot: binding.ExecutionRoot, ExecutionRootIdentity: binding.ExecutionRootIdentity,
		Profile: binding.Profile, ProviderProfile: binding.ProviderProfile,
		Model: binding.Model, Provider: binding.Provider,
		ExpectedWorkerBuildID: binding.ExpectedWorkerBuildID,
	}
	if privilegedBinding != nil {
		cloned := *privilegedBinding
		result.Privilege = &cloned
	}
	if err := result.Validate(); err != nil {
		return worker.Binding{}, err
	}
	return result, nil
}

func codingTaskOutcomeForWorker(outcome workerprocess.Outcome) codingTaskOutcome {
	switch outcome {
	case workerprocess.OutcomeCompleted:
		return codingTaskOutcomeCompleted
	case workerprocess.OutcomeInterrupted, workerprocess.OutcomeShutdown:
		return codingTaskOutcomeCanceled
	case workerprocess.OutcomeIdle:
		return codingTaskOutcomeIdle
	case workerprocess.OutcomeFailed:
		return codingTaskOutcomeFailed
	default:
		return codingTaskOutcomeUncertain
	}
}

func codingWorkerEnvironment(mintClawHome string) []string {
	environment := os.Environ()
	filtered := environment[:0]
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if found && strings.EqualFold(key, "MINTCLAW_HOME") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, "MINTCLAW_HOME="+mintClawHome)
}

func codingPreparationUncertain(err error) bool {
	return errors.Is(err, worktree.ErrAllocationUncertain) ||
		errors.Is(err, worktree.ErrOwnerBusy) ||
		errors.Is(err, worktree.ErrFinalizationPending)
}

func codingLaunchUncertain(err error) bool {
	var pending *workerprocess.OwnedLaunchError
	return errors.As(err, &pending) || errors.Is(err, worktree.ErrFinalizationPending) ||
		errors.Is(err, worker.ErrControlStreamUncertain)
}

var (
	_ codingTaskBackend = nativeCodingTaskBackend{}
	_ codingTaskProcess = (*nativeCodingProcess)(nil)
	_ codingTaskProcess = (*nativeOwnedCodingProcess)(nil)
)
