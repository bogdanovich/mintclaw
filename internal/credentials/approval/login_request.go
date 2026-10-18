package approval

import (
	"context"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

// LoginRequest binds runtime-owned caller and browser origin to current policy.
// It exposes no provider IDs and grants no credential access. A signed grant
// and live browser preparation remain mandatory before retrieval.
func (controller *Controller) LoginRequest(ctx context.Context, principal credentialbroker.Principal,
	alias, origin string,
) (credentialbroker.Request, credentialbroker.LoginFlow, error) {
	if controller == nil {
		return credentialbroker.Request{}, credentialbroker.LoginFlow{}, ErrUnavailable
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.ready(ctx) {
		return credentialbroker.Request{}, credentialbroker.LoginFlow{}, ErrUnavailable
	}
	request := credentialbroker.Request{
		Principal: principal, Account: alias, Origin: origin, PolicyRevision: controller.policy.Revision(),
	}
	flow, err := controller.policy.LoginFlow(request)
	if err != nil {
		return credentialbroker.Request{}, credentialbroker.LoginFlow{}, ErrDenied
	}
	return request, flow, nil
}
