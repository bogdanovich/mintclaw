package approval

import (
	"context"
	"errors"

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

// DiscoveryRequest checks trusted caller/origin metadata for a configured vault
// scope. It never chooses an item or issues a grant. A name or model-supplied
// URL cannot provide this authority; the caller supplies live browser state.
func (controller *Controller) DiscoveryRequest(ctx context.Context, principal credentialbroker.Principal,
	alias, origin string,
) (credentialbroker.Request, string, error) {
	if controller == nil {
		return credentialbroker.Request{}, "", ErrUnavailable
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.ready(ctx) || !controller.bindings[alias].discoverLogins {
		return credentialbroker.Request{}, "", ErrDenied
	}
	if _, err := discoveryDomain(origin); err != nil {
		return credentialbroker.Request{}, "", ErrDenied
	}
	request := credentialbroker.Request{
		Principal: principal, Account: alias, Origin: origin, PolicyRevision: controller.policy.Revision(),
	}
	if !errors.Is(controller.authority.Authorize("", request), credentialbroker.ErrApprovalRequired) {
		return credentialbroker.Request{}, "", ErrDenied
	}
	return request, controller.bindings[alias].vaultID, nil
}
