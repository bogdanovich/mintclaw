package worktree

import (
	"context"
	"fmt"

	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
)

func (manager *Manager) validateSourceAuthority(ctx context.Context, allocation Allocation) error {
	current, err := thread.ResolveProject(ctx, allocation.Source.InvocationCWD)
	if err != nil {
		return fmt.Errorf("resolve source repository: %w", err)
	}
	if !sameSourceAuthority(current, allocation.Source) {
		return fmt.Errorf("source repository paths no longer match the allocation")
	}
	identity, err := inspectDirectoryIdentity(current.GitCommonDir)
	if err != nil {
		return fmt.Errorf("inspect source common directory: %w", err)
	}
	if identity != allocation.SourceCommonDirIdentity {
		return fmt.Errorf("source common directory was replaced")
	}
	return nil
}

func validateExecutionRootAuthority(allocation Allocation) error {
	identity, err := inspectDirectoryIdentity(allocation.ExecutionRoot)
	if err != nil {
		return fmt.Errorf("inspect execution root: %w", err)
	}
	if identity != allocation.ExecutionRootFileIdentity {
		return fmt.Errorf("execution root directory was replaced")
	}
	return nil
}

func sameSourceAuthority(current, persisted thread.ProjectIdentity) bool {
	return current.Kind == persisted.Kind &&
		current.ProjectKey == persisted.ProjectKey &&
		current.ProjectRoot == persisted.ProjectRoot &&
		current.InvocationCWD == persisted.InvocationCWD &&
		current.GitWorktreeRoot == persisted.GitWorktreeRoot &&
		current.GitDir == persisted.GitDir &&
		current.GitCommonDir == persisted.GitCommonDir
}
