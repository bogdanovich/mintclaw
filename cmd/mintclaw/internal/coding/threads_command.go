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
	cmd.AddCommand(newDeleteThreadCommand(deps), newForkThreadCommand(deps))
	return cmd
}

func newForkThreadCommand(deps dependencies) *cobra.Command {
	deps = completeDependencies(deps)
	var atTurn int
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "fork <thread-id>",
		Short: "Fork bounded conversation history into an independent coding thread",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runForkThread(cmd.Context(), cmd.OutOrStdout(), deps, args[0], atTurn, jsonOutput)
		},
	}
	cmd.Flags().IntVar(&atTurn, "at-turn", 0, "Fork through this one-based user turn (default latest)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit machine-readable JSON")
	return cmd
}

type forkThreadOutput struct {
	Action        string            `json:"action"`
	Fork          thread.ForkResult `json:"fork"`
	Metadata      thread.Metadata   `json:"metadata"`
	ResumeCommand string            `json:"resume_command"`
	Notice        string            `json:"notice"`
}

func runForkThread(
	ctx context.Context,
	out io.Writer,
	deps dependencies,
	sourceThreadID string,
	atTurn int,
	jsonOutput bool,
) (resultErr error) {
	sourceThreadID = strings.TrimSpace(sourceThreadID)
	project, store, err := resolveEnvironment(ctx, deps)
	if err != nil {
		return err
	}
	sourceLease, err := store.AcquireLease(sourceThreadID)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, sourceLease.Release()) }()
	child, forked, forkErr := store.ForkThread(ctx, sourceLease, thread.ForkOptions{
		TargetThreadID: deps.newThreadID(),
		Project:        project,
		AtTurn:         atTurn,
		At:             deps.now(),
	})
	if forkErr != nil && !thread.IsCommittedForkError(forkErr) {
		return forkErr
	}
	result := forkThreadOutput{
		Action:        "forked",
		Fork:          forked,
		Metadata:      child,
		ResumeCommand: "mintclaw resume " + child.ThreadID,
		Notice:        "Conversation history was copied; the fork uses the current live filesystem and did not roll files back.",
	}
	return errors.Join(forkErr, renderForkThread(out, result, jsonOutput))
}

func renderForkThread(out io.Writer, result forkThreadOutput, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(out, result)
	}
	_, err := fmt.Fprintf(
		out,
		"Forked coding thread %s from %s through user turn %d (%d messages).\n"+
			"The fork uses the current live filesystem; no project files were rolled back.\nResume with: %s\n",
		result.Fork.ThreadID,
		result.Fork.SourceThreadID,
		result.Fork.SourceTurn,
		result.Fork.CopiedMessages,
		result.ResumeCommand,
	)
	return err
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
