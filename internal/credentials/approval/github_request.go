package approval

import (
	"context"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

// GitHubRequest derives fixed API authority from current operator policy. It
// grants no access; private retrieval still requires a live signed lease.
func (controller *Controller) GitHubRequest(ctx context.Context, principal credentialbroker.Principal,
	alias, repository string,
) (credentialbroker.Request, string, error) {
	if controller == nil {
		return credentialbroker.Request{}, "", ErrUnavailable
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.ready(ctx) {
		return credentialbroker.Request{}, "", ErrUnavailable
	}
	request := credentialbroker.Request{
		Principal: principal, Account: alias, Origin: credentialbroker.GitHubAPIOrigin,
		PolicyRevision: controller.policy.Revision(),
	}
	canonical, err := controller.policy.GitHubRepository(request, repository)
	if err != nil {
		return credentialbroker.Request{}, "", ErrDenied
	}
	return request, canonical, nil
}
