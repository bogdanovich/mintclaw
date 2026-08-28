package coding

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
)

// NewThreadsCommand creates explicit administrative coding-thread operations.
func NewThreadsCommand() *cobra.Command {
	return newThreadsCommand(defaultDependencies())
}

func newThreadsCommand(deps dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "threads",
		Short: "Manage durable coding threads",
	}
	cmd.AddCommand(newDeleteThreadCommand(deps))
	return cmd
}

func newDeleteThreadCommand(deps dependencies) *cobra.Command {
	deps = completeDependencies(deps)
	var confirmation string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "delete <thread-id>",
		Short: "Move one coding thread to recoverable MintClaw trash",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeleteThread(
				cmd.Context(), cmd.OutOrStdout(), deps, args[0], confirmation, jsonOutput,
			)
		},
	}
	cmd.Flags().StringVar(&confirmation, "confirm", "", "Confirm deletion by repeating the exact thread ID")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit machine-readable JSON")
	return cmd
}

type deleteThreadOutput struct {
	Action string              `json:"action"`
	Plan   thread.DeletePlan   `json:"plan"`
	Trash  *thread.TrashResult `json:"trash,omitempty"`
}

func runDeleteThread(
	ctx context.Context,
	out io.Writer,
	deps dependencies,
	threadID string,
	confirmation string,
	jsonOutput bool,
) (resultErr error) {
	threadID = strings.TrimSpace(threadID)
	project, store, err := resolveEnvironment(ctx, deps)
	if err != nil {
		return err
	}
	plan, err := store.PlanDeleteContext(ctx, threadID)
	if err != nil {
		return err
	}
	if plan.ProjectKey != project.ProjectKey {
		return fmt.Errorf("coding thread delete: thread belongs to project %q", plan.ProjectRoot)
	}
	if confirmation == "" {
		return renderDeleteThread(out, deleteThreadOutput{Action: "planned", Plan: plan}, jsonOutput)
	}
	if confirmation != threadID {
		return fmt.Errorf("coding thread delete: --confirm must exactly match thread ID %q", threadID)
	}
	lease, err := store.AcquireLease(threadID)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, lease.Release()) }()
	// Revalidate the complete plan after acquiring writer ownership.
	confirmed, err := store.PlanDeleteContext(ctx, threadID)
	if err != nil {
		return err
	}
	if confirmed.ProjectKey != project.ProjectKey {
		return fmt.Errorf("coding thread delete: project changed before confirmation")
	}
	trashed, err := store.TrashThread(ctx, lease, confirmation, deps.now())
	return finishDeleteThread(
		out,
		deleteThreadOutput{Action: "trashed", Plan: confirmed, Trash: &trashed},
		jsonOutput,
		err,
	)
}

func finishDeleteThread(out io.Writer, result deleteThreadOutput, jsonOutput bool, moveErr error) error {
	if moveErr != nil && !thread.IsCommittedTrashError(moveErr) {
		return moveErr
	}
	return errors.Join(moveErr, renderDeleteThread(out, result, jsonOutput))
}

func renderDeleteThread(out io.Writer, result deleteThreadOutput, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(out, result)
	}
	if result.Trash == nil {
		if _, err := fmt.Fprintf(
			out,
			"Delete coding thread %s (%q)\n\nOnly this MintClaw-owned external state will move to trash:\n  %s\n",
			result.Plan.ThreadID,
			result.Plan.Title,
			result.Plan.ThreadRoot,
		); err != nil {
			return err
		}
		for _, path := range result.Plan.OwnedPaths {
			if _, err := fmt.Fprintf(out, "  - %s\n", path); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(
			out,
			"\nProject files are outside this target and will not be touched.\n"+
				"Confirm with: mintclaw threads delete %s --confirm %s\n",
			result.Plan.ThreadID,
			result.Plan.ThreadID,
		)
		return err
	}
	_, err := fmt.Fprintf(
		out,
		"Coding thread %s moved to recoverable MintClaw trash.\nTrash ID: %s\nPath: %s\n",
		result.Trash.ThreadID,
		result.Trash.TrashID,
		result.Trash.Path,
	)
	return err
}
