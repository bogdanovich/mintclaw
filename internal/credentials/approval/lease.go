package approval

import (
	"context"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/browserpolicy"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

// LoginLease holds an immutable provider binding and live grant generation for
// the trusted runtime consumer. It supplies no plaintext or model-facing projection.
// Consumers must watch Done and check trusted origin/frame/element authority
// before every retrieval, injection and cookie-backed session operation.
type LoginLease struct {
	grant   credentialbroker.GrantLease
	binding LoginBinding
	flow    *credentialbroker.LoginFlow
	origins []string
}

func (controller *Controller) LeaseLogin(ctx context.Context, grantID string,
	request credentialbroker.Request,
) (LoginLease, error) {
	if controller == nil {
		return LoginLease{}, ErrUnavailable
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.ready(ctx) {
		return LoginLease{}, ErrUnavailable
	}
	grant, err := controller.authority.Lease(ctx, grantID, request)
	if err != nil {
		return LoginLease{}, ErrDenied
	}
	account := controller.bindings[request.Account]
	lease := LoginLease{
		grant:   grant,
		binding: LoginBinding{account.vaultID, account.itemID, controller.policy.Revision()},
		origins: slices.Clone(account.origins),
	}
	if flow, flowErr := controller.policy.LoginFlow(request); flowErr == nil {
		lease.flow = &flow
	}
	return lease, nil
}

func (lease LoginLease) Binding(ctx context.Context, request credentialbroker.Request) (LoginBinding, error) {
	if lease.grant.Check(ctx, request) != nil {
		return LoginBinding{}, ErrDenied
	}
	return lease.binding, nil
}

func (lease LoginLease) Done() <-chan struct{} { return lease.grant.Done() }

func (lease LoginLease) Remaining(ctx context.Context, request credentialbroker.Request) (time.Duration, error) {
	return lease.grant.Remaining(ctx, request)
}

func (lease LoginLease) Origins(ctx context.Context, request credentialbroker.Request) ([]string, error) {
	if lease.grant.Check(ctx, request) != nil {
		return nil, ErrDenied
	}
	origins := make([]string, 0, len(lease.origins))
	for _, configured := range lease.origins {
		origin, err := browserpolicy.NormalizePublicOrigin(configured)
		if err != nil {
			return nil, ErrDenied
		}
		origins = append(origins, origin)
	}
	slices.Sort(origins)
	return origins, nil
}

// Flow returns the immutable configured browser flow only at its current entry
// origin. Legacy/connector bindings remain valid leases but have no browser flow.
// This metadata check is not proof of live document or element authority.
func (lease LoginLease) Flow(
	ctx context.Context,
	request credentialbroker.Request,
) (credentialbroker.LoginFlow, error) {
	if lease.grant.Check(ctx, request) != nil || lease.flow == nil {
		return credentialbroker.LoginFlow{}, ErrDenied
	}
	origin, err := browserpolicy.NormalizePublicOrigin(request.Origin)
	if err != nil || origin != lease.flow.EntryOrigin {
		return credentialbroker.LoginFlow{}, ErrDenied
	}
	return *lease.flow, nil
}

func (LoginLease) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private login lease]")
}
func (LoginLease) MarshalJSON() ([]byte, error) { return []byte(`"[private login lease]"`), nil }
func (LoginLease) MarshalText() ([]byte, error) { return []byte("[private login lease]"), nil }
