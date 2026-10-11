package approval

import (
	"context"
	"fmt"
	"io"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

// LoginLease holds an immutable provider binding and live grant generation for
// the isolated consumer. It supplies no plaintext or model-facing projection.
// Consumers must watch Done and check trusted origin/frame/element authority
// before every retrieval, injection and cookie-backed session operation.
type LoginLease struct {
	grant   credentialbroker.GrantLease
	binding LoginBinding
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
	return LoginLease{grant, LoginBinding{account.vaultID, account.itemID, controller.policy.Revision()}}, nil
}

func (lease LoginLease) Binding(ctx context.Context, request credentialbroker.Request) (LoginBinding, error) {
	if lease.grant.Check(ctx, request) != nil {
		return LoginBinding{}, ErrDenied
	}
	return lease.binding, nil
}

func (lease LoginLease) Done() <-chan struct{} { return lease.grant.Done() }

func (LoginLease) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private login lease]")
}
func (LoginLease) MarshalJSON() ([]byte, error) { return []byte(`"[private login lease]"`), nil }
func (LoginLease) MarshalText() ([]byte, error) { return []byte("[private login lease]"), nil }
