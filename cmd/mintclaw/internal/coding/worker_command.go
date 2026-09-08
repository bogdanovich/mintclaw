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
	if err := validateNativeWorkerBinding(ctx, deps, binding); err != nil {
		return nil, err
	}
	home := strings.TrimSpace(deps.home())
	if home == "" {
		return nil, fmt.Errorf("coding worker: MintClaw home is required")
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		return nil, err
	}
	metadata, lease, resumed, err := prepareNativeWorkerThread(ctx, deps, store, binding)
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

func validateNativeWorkerBinding(ctx context.Context, deps dependencies, binding worker.Binding) error {
	if err := binding.Validate(); err != nil {
		return err
	}
	if binding.ProviderProfile != nativeWorkerProviderProfile {
		return fmt.Errorf("coding worker: unsupported provider profile")
	}
	if binding.ExecutionRoot != binding.Project.ProjectRoot {
		return fmt.Errorf("coding worker: distinct execution roots require isolated-worktree support")
	}
	current, err := thread.ResolveProject(ctx, binding.Project.InvocationCWD)
	if err != nil {
		return fmt.Errorf("coding worker: resolve bound project: %w", err)
	}
	if current != binding.Project {
		return fmt.Errorf("coding worker: bound project identity no longer matches the execution root")
	}
	if deps.resolveModel == nil {
		return fmt.Errorf("coding worker: model resolver is unavailable")
	}
	model, provider, err := deps.resolveModel(binding.Model)
	if err != nil {
		return err
	}
	if model != binding.Model || provider != binding.Provider {
		return fmt.Errorf("coding worker: bound model selection no longer matches the provider profile")
	}
	if deps.home == nil || deps.now == nil || deps.newController == nil {
		return fmt.Errorf("coding worker: native runtime dependencies are unavailable")
	}
	return nil
}

func prepareNativeWorkerThread(
	ctx context.Context,
	deps dependencies,
	store *thread.Store,
	binding worker.Binding,
) (thread.Metadata, *thread.Lease, bool, error) {
	switch binding.ThreadOpenMode {
	case worker.ThreadOpenNew:
		metadata, lease, err := prepareNewNativeWorkerThread(ctx, deps, store, binding)
		return metadata, lease, false, err
	case worker.ThreadOpenResume:
		metadata, lease, err := prepareResumedNativeWorkerThread(ctx, deps, store, binding)
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
) (thread.Metadata, *thread.Lease, error) {
	metadata, err := thread.NewPendingMetadata(binding.ThreadID, binding.Project, deps.now())
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
	if reserveErr := store.ReserveThread(binding.ThreadID); reserveErr != nil {
		return thread.Metadata{}, nil, reserveErr
	}
	lease, err := store.AcquireLease(binding.ThreadID)
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
	inspection, err := thread.InspectLocation(ctx, metadata.Project, binding.Project.InvocationCWD)
	if err != nil {
		return thread.Metadata{}, lease, err
	}
	if inspection.State != thread.LocationAvailable || inspection.Current == nil ||
		*inspection.Current != binding.Project {
		return thread.Metadata{}, lease, fmt.Errorf("coding worker: stored thread does not match the bound project")
	}
	metadata.Project = binding.Project
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
