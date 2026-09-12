package coding

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/coding/worker"
	"github.com/bogdanovich/mintclaw/pkg/coding/worktree"
)

const (
	nativeWorkerProviderProfile  = "default"
	workerControllerCloseTimeout = 5 * time.Second
)

type workerCommandError struct {
	cause error
}

func (err *workerCommandError) Error() string {
	return "coding worker terminated"
}

func (err *workerCommandError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

func newCodeWorkerCommand(deps dependencies) *cobra.Command {
	return &cobra.Command{
		Use:    "_worker",
		Short:  "Run one private coding worker",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			buildID, err := deps.workerBuildID()
			if err != nil {
				return &workerCommandError{cause: err}
			}
			server, err := worker.NewServer(worker.ServerConfig{
				BuildID: buildID,
				Open: func(ctx context.Context, binding worker.Binding) (worker.TaskController, error) {
					return openNativeWorkerController(ctx, deps, binding)
				},
			})
			if err != nil {
				return &workerCommandError{cause: err}
			}
			input, ok := cmd.InOrStdin().(io.ReadCloser)
			if !ok {
				input = io.NopCloser(cmd.InOrStdin())
			}
			if err := server.Serve(cmd.Context(), input, cmd.OutOrStdout()); err != nil {
				return &workerCommandError{cause: err}
			}
			return nil
		},
	}
}

func openNativeWorkerController(
	ctx context.Context,
	deps dependencies,
	binding worker.Binding,
) (worker.TaskController, error) {
	if deps.home == nil {
		return nil, fmt.Errorf("coding worker: MintClaw home is required")
	}
	home := strings.TrimSpace(deps.home())
	if home == "" {
		return nil, fmt.Errorf("coding worker: MintClaw home is required")
	}
	executionProject, err := validateNativeWorkerBinding(ctx, deps, home, binding)
	if err != nil {
		return nil, err
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		return nil, err
	}
	metadata, lease, resumed, err := prepareNativeWorkerThread(
		ctx,
		deps,
		store,
		binding,
		executionProject,
	)
	if err != nil {
		return nil, err
	}
	controllerInstance, err := deps.newController(codingTurnRequest{
		Store:         store,
		Lease:         lease,
		Metadata:      metadata,
		ExecutionRoot: binding.ExecutionRoot,
		ReadOnly:      binding.Mode == worker.TaskModeInvestigate,
	}, resumed)
	if err != nil {
		return nil, errors.Join(err, lease.Release())
	}
	if controllerInstance == nil {
		return nil, errors.Join(
			fmt.Errorf("coding worker: native controller is unavailable"),
			lease.Release(),
		)
	}
	taskController, ok := controllerInstance.(worker.TaskController)
	if !ok {
		closeCtx, cancel := context.WithTimeout(context.Background(), workerControllerCloseTimeout)
		defer cancel()
		return nil, errors.Join(
			fmt.Errorf("coding worker: native controller lacks task control capabilities"),
			controllerInstance.Close(closeCtx),
		)
	}
	return taskController, nil
}

func validateNativeWorkerBinding(
	ctx context.Context,
	deps dependencies,
	home string,
	binding worker.Binding,
) (thread.ProjectIdentity, error) {
	if err := binding.Validate(); err != nil {
		return thread.ProjectIdentity{}, err
	}
	if binding.ProviderProfile != nativeWorkerProviderProfile {
		return thread.ProjectIdentity{}, fmt.Errorf("coding worker: unsupported provider profile")
	}
	if deps.resolveModel == nil {
		return thread.ProjectIdentity{}, fmt.Errorf("coding worker: model resolver is unavailable")
	}
	model, provider, err := deps.resolveModel(binding.Model)
	if err != nil {
		return thread.ProjectIdentity{}, err
	}
	if model != binding.Model || provider != binding.Provider {
		return thread.ProjectIdentity{}, fmt.Errorf(
			"coding worker: bound model selection no longer matches the provider profile",
		)
	}
	if deps.now == nil || deps.newController == nil {
		return thread.ProjectIdentity{}, fmt.Errorf("coding worker: native runtime dependencies are unavailable")
	}
	switch binding.Mode {
	case worker.TaskModeInvestigate:
		current, resolveErr := thread.ResolveProject(ctx, binding.Project.InvocationCWD)
		if resolveErr != nil {
			return thread.ProjectIdentity{}, fmt.Errorf("coding worker: resolve bound project: %w", resolveErr)
		}
		if current != binding.Project {
			return thread.ProjectIdentity{}, fmt.Errorf(
				"coding worker: bound project identity no longer matches the execution root",
			)
		}
		return current, nil
	case worker.TaskModeMutate:
		manager, managerErr := worktree.OpenManager(worktree.Config{
			StateRoot: filepath.Join(home, "coding"), WorktreeParent: filepath.Dir(binding.ExecutionRoot),
		})
		if managerErr != nil {
			return thread.ProjectIdentity{}, fmt.Errorf("coding worker: open worktree allocation: %w", managerErr)
		}
		allocation, ownerErr := manager.RequireActiveOwner(ctx, worktree.OwnerRequest{
			WorktreeID: worktree.IDForThread(binding.ThreadID), TaskID: binding.TaskID,
			TaskGenerationID: binding.TaskGenerationID, ThreadID: binding.ThreadID,
			WorkerGenerationID: binding.WorkerGenerationID,
		})
		if ownerErr != nil {
			return thread.ProjectIdentity{}, fmt.Errorf("coding worker: require worktree owner: %w", ownerErr)
		}
		if allocation.Source != binding.Project || allocation.Execution == nil ||
			allocation.ExecutionRoot != binding.ExecutionRoot ||
			allocation.ExecutionRootIdentity != binding.ExecutionRootIdentity ||
			*allocation.Execution == binding.Project {
			return thread.ProjectIdentity{}, fmt.Errorf("coding worker: allocation does not match the bound project")
		}
		return *allocation.Execution, nil
	default:
		return thread.ProjectIdentity{}, fmt.Errorf("coding worker: unsupported task mode")
	}
}

func prepareNativeWorkerThread(
	ctx context.Context,
	deps dependencies,
	store *thread.Store,
	binding worker.Binding,
	executionProject thread.ProjectIdentity,
) (thread.Metadata, *thread.Lease, bool, error) {
	switch binding.ThreadOpenMode {
	case worker.ThreadOpenNew:
		metadata, lease, err := prepareNewNativeWorkerThread(ctx, deps, store, binding, executionProject)
		return metadata, lease, false, err
	case worker.ThreadOpenResume:
		metadata, lease, err := prepareResumedNativeWorkerThread(ctx, deps, store, binding, executionProject)
		return metadata, lease, true, err
	default:
		return thread.Metadata{}, nil, false, fmt.Errorf("coding worker: unsupported thread open mode")
	}
}

func prepareNewNativeWorkerThread(
	ctx context.Context,
	deps dependencies,
	store *thread.Store,
	binding worker.Binding,
	executionProject thread.ProjectIdentity,
) (thread.Metadata, *thread.Lease, error) {
	metadata, err := thread.NewPendingMetadata(binding.ThreadID, executionProject, deps.now())
	if err != nil {
		return thread.Metadata{}, nil, err
	}
	metadata.Model = binding.Model
	metadata.Provider = binding.Provider
	if validationErr := metadata.Validate(); validationErr != nil {
		return thread.Metadata{}, nil, validationErr
	}
	if _, layoutErr := runtimeLayoutForExecutionRoot(store, metadata, binding.ExecutionRoot); layoutErr != nil {
		return thread.Metadata{}, nil, layoutErr
	}
	lease, err := store.ReserveThreadLease(binding.ThreadID)
	if err != nil {
		return thread.Metadata{}, nil, err
	}
	if err := captureAndPublishRepositoryBaseline(ctx, store, lease, metadata, deps.now()); err != nil {
		return thread.Metadata{}, nil, errors.Join(err, lease.Release())
	}
	if err := store.Save(metadata); err != nil {
		return thread.Metadata{}, nil, errors.Join(err, lease.Release())
	}
	return metadata, lease, nil
}

func prepareResumedNativeWorkerThread(
	ctx context.Context,
	deps dependencies,
	store *thread.Store,
	binding worker.Binding,
	executionProject thread.ProjectIdentity,
) (metadata thread.Metadata, lease *thread.Lease, resultErr error) {
	lease, err := store.AcquireLease(binding.ThreadID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return thread.Metadata{}, nil, resumeThreadNotFoundError(binding.ThreadID)
		}
		return thread.Metadata{}, nil, err
	}
	admitted := false
	defer func() {
		if !admitted {
			resultErr = errors.Join(resultErr, lease.Release())
			lease = nil
		}
	}()
	metadata, err = store.Load(binding.ThreadID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return thread.Metadata{}, lease, resumeThreadNotFoundError(binding.ThreadID)
		}
		return thread.Metadata{}, lease, err
	}
	inspection, err := thread.InspectLocation(ctx, metadata.Project, executionProject.InvocationCWD)
	if err != nil {
		return thread.Metadata{}, lease, err
	}
	if inspection.State != thread.LocationAvailable || inspection.Current == nil ||
		*inspection.Current != executionProject {
		return thread.Metadata{}, lease, fmt.Errorf("coding worker: stored thread does not match the bound project")
	}
	metadata.Project = executionProject
	metadata.Model = binding.Model
	metadata.Provider = binding.Provider
	metadata.UpdatedAt = deps.now().UTC()
	if err := metadata.Validate(); err != nil {
		return thread.Metadata{}, lease, err
	}
	if _, err := runtimeLayoutForExecutionRoot(store, metadata, binding.ExecutionRoot); err != nil {
		return thread.Metadata{}, lease, err
	}
	if _, err := store.LoadRepositoryBaselineWithLease(ctx, lease, metadata); err != nil {
		return thread.Metadata{}, lease, fmt.Errorf("coding worker: load repository baseline: %w", err)
	}
	if err := store.Save(metadata); err != nil {
		return thread.Metadata{}, lease, err
	}
	admitted = true
	return metadata, lease, nil
}
