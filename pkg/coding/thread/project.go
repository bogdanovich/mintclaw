package thread

import (
	"context"
	"encoding/hex"

	"github.com/bogdanovich/mintclaw/pkg/coding/project"
)

// Project identity is implemented in a lightweight package so external
// worker supervisors do not inherit the thread/provider runtime dependency
// graph. These aliases preserve the established thread API.
type ProjectKind = project.ProjectKind

const (
	ProjectKindDirectory   = project.ProjectKindDirectory
	ProjectKindGitWorktree = project.ProjectKindGitWorktree
)

type (
	ProjectIdentity = project.ProjectIdentity
	LocationState   = project.LocationState
)

const (
	LocationAvailable = project.LocationAvailable
	LocationMissing   = project.LocationMissing
	LocationMoved     = project.LocationMoved
	LocationMismatch  = project.LocationMismatch
)

type LocationInspection = project.LocationInspection

func ResolveProject(ctx context.Context, cwd string) (ProjectIdentity, error) {
	return project.ResolveProject(ctx, cwd)
}

func InspectLocation(
	ctx context.Context,
	persisted ProjectIdentity,
	candidateCWD string,
) (LocationInspection, error) {
	return project.InspectLocation(ctx, persisted, candidateCWD)
}

func projectKey(kind ProjectKind, root string) string {
	return project.ProjectKey(kind, root)
}

func sanitizeGitRemote(remote string) string {
	return project.SanitizeGitRemote(remote)
}

func isHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}
