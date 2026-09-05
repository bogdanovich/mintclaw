package coding

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	codingreview "github.com/bogdanovich/mintclaw/pkg/coding/review"
)

const headlessReviewCloseTimeout = 10 * time.Second

type reviewCommandOptions struct {
	threadID string
	last     bool
	target   codingreview.Target
	json     bool
}

type reviewCommandResult struct {
	Action      string              `json:"action"`
	ThreadID    string              `json:"thread_id"`
	ProjectRoot string              `json:"project_root"`
	Review      codingreview.Result `json:"review"`
}

// NewReviewCommand creates the headless native repository-review command.
func NewReviewCommand() *cobra.Command {
	return newReviewCommand(defaultDependencies())
}

func newReviewCommand(deps dependencies) *cobra.Command {
	deps = completeDependencies(deps)
	var last bool
	var targetKind string
	var ref string
	var instructions string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "review [thread-id]",
		Short: "Run a read-only review for a project coding thread",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			threadID := ""
			if len(args) == 1 {
				threadID = args[0]
			}
			target := codingreview.Target{
				Kind:         codingreview.TargetKind(strings.ToLower(strings.TrimSpace(targetKind))),
				Ref:          strings.TrimSpace(ref),
				Instructions: strings.TrimSpace(instructions),
			}
			options := reviewCommandOptions{threadID: threadID, last: last, target: target, json: jsonOutput}
			if err := validateReviewCommandOptions(options); err != nil {
				return err
			}
			return runReviewCommand(cmd.Context(), cmd.OutOrStdout(), deps, options)
		},
	}
	cmd.Flags().BoolVar(&last, "last", false, "Review the most recently updated active thread in this project")
	cmd.Flags().
		StringVar(&targetKind, "target", string(codingreview.TargetCurrent), "Review current, base, or commit changes")
	cmd.Flags().StringVar(&ref, "ref", "", "Local base or commit ref required by that target")
	cmd.Flags().StringVar(&instructions, "instructions", "", "Bounded custom review instructions")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit the schema-versioned review result as JSON")
	return cmd
}

func validateReviewCommandOptions(options reviewCommandOptions) error {
	if options.threadID != "" && options.last {
		return errors.New("review: thread ID and --last are mutually exclusive")
	}
	if err := options.target.Validate(); err != nil {
		return fmt.Errorf("review: %w", err)
	}
	return nil
}

func runReviewCommand(
	ctx context.Context,
	out io.Writer,
	deps dependencies,
	options reviewCommandOptions,
) (resultErr error) {
	project, store, err := resolveEnvironment(ctx, deps)
	if err != nil {
		return err
	}
	threadID := options.threadID
	if threadID == "" {
		threadID, err = selectLastResumeThread(ctx, store, project, false, false)
		if err != nil {
			return err
		}
	}
	metadata, lease, err := prepareResumedThread(
		ctx,
		store,
		project,
		deps,
		threadID,
		resumeOptions{threadID: threadID},
		true,
	)
	if err != nil {
		return err
	}
	controller, err := deps.newController(codingTurnRequest{
		Store: store, Lease: lease, Metadata: metadata,
	}, true)
	if err != nil {
		return errors.Join(err, lease.Release())
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), headlessReviewCloseTimeout)
		defer cancel()
		resultErr = errors.Join(resultErr, controller.Close(closeCtx))
	}()
	result, err := executeHeadlessReview(ctx, controller, options.target)
	if err != nil {
		return err
	}
	output := reviewCommandResult{
		Action: "reviewed", ThreadID: metadata.ThreadID, ProjectRoot: metadata.Project.ProjectRoot, Review: result,
	}
	if options.json {
		return writeJSON(out, output)
	}
	if _, err := fmt.Fprintf(out, "Coding thread %s\nProject: %q\n\n%s\n", metadata.ThreadID,
		metadata.Project.ProjectRoot, codingreview.RenderResultPlain(result)); err != nil {
		return fmt.Errorf("render coding review: %w", err)
	}
	return nil
}

func executeHeadlessReview(
	ctx context.Context,
	controller frontend.Controller,
	target codingreview.Target,
) (codingreview.Result, error) {
	reviewer, ok := controller.(frontend.Reviewer)
	if !ok {
		return codingreview.Result{}, frontend.ErrCommandUnsupported
	}
	if err := reviewer.Review(ctx, target); err != nil {
		return codingreview.Result{}, fmt.Errorf("review: admit: %w", err)
	}
	observeCtx, cancelObserve := context.WithCancel(ctx)
	defer cancelObserve()
	snapshot, updates, err := controller.Subscribe(observeCtx)
	if err != nil {
		return codingreview.Result{}, fmt.Errorf("review: observe admitted review: %w", err)
	}
	reviewID := ""
	if snapshot.Review != nil && snapshot.Review.Target == target {
		reviewID = snapshot.Review.ReviewID
	}
	for {
		if snapshot.Review != nil && snapshot.Review.Target == target &&
			(reviewID == "" || snapshot.Review.ReviewID == reviewID) {
			if reviewID == "" {
				reviewID = snapshot.Review.ReviewID
			}
			switch snapshot.Review.Phase {
			case codingreview.PhaseCompleted, codingreview.PhaseStale:
				if snapshot.Review.Result == nil {
					return codingreview.Result{}, errors.New("review completed without a result")
				}
				return snapshot.Review.Result.Clone(), nil
			case codingreview.PhaseInterrupted:
				return codingreview.Result{}, errors.New("review was interrupted before completion")
			}
		}
		select {
		case next, ok := <-updates:
			if !ok {
				return codingreview.Result{}, errors.New("review observation ended before completion")
			}
			snapshot = next
		case <-ctx.Done():
			interruptCtx, cancel := context.WithTimeout(context.Background(), headlessReviewCloseTimeout)
			_ = controller.Interrupt(interruptCtx)
			cancel()
			return codingreview.Result{}, ctx.Err()
		}
	}
}
