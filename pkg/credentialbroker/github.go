package credentialbroker

import (
	"regexp"
	"slices"
	"strings"
)

const GitHubAPIOrigin = "https://api.github.com"

// GitHubReadScope permits only listing open pull requests in named repositories.
// Its complete repository allowlist contributes to the approval revision.
type GitHubReadScope struct {
	Repositories []string `json:"repositories"`
}

var githubRepositoryPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,38}/[a-zA-Z0-9_.-]{1,100}$`)

func NormalizeGitHubRepository(repository string) (string, error) {
	if !githubRepositoryPattern.MatchString(repository) {
		return "", ErrDenied
	}
	_, name, _ := strings.Cut(repository, "/")
	if name == "." || name == ".." {
		return "", ErrDenied
	}
	return strings.ToLower(repository), nil
}

func normalizeGitHubReadScope(scope GitHubReadScope) (GitHubReadScope, error) {
	if len(scope.Repositories) < 1 || len(scope.Repositories) > 16 {
		return GitHubReadScope{}, ErrInvalidPolicy
	}
	repositories := make([]string, 0, len(scope.Repositories))
	for _, raw := range scope.Repositories {
		repository, err := NormalizeGitHubRepository(raw)
		if err != nil || slices.Contains(repositories, repository) {
			return GitHubReadScope{}, ErrInvalidPolicy
		}
		repositories = append(repositories, repository)
	}
	slices.Sort(repositories)
	return GitHubReadScope{Repositories: repositories}, nil
}

func (policy *Policy) GitHubRepository(request Request, repository string) (string, error) {
	if !policy.permits(request) || request.Origin != GitHubAPIOrigin {
		return "", ErrDenied
	}
	scope := policy.accounts[request.Account].github
	canonical, err := NormalizeGitHubRepository(repository)
	if err != nil || scope == nil || !slices.Contains(scope.Repositories, canonical) {
		return "", ErrDenied
	}
	return canonical, nil
}
