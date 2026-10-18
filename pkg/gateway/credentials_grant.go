package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/bogdanovich/mintclaw/internal/credentials/approval"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

var errCredentialApprovalRequired = errors.New("credential approval required")

// Browser and connector operations share the existing approval controller and
// notification route. Waiting retains no values and creates no second cache.
func (runtime *credentialRuntime) requestCredentialLease(ctx context.Context,
	scope credentialbroker.Request,
) (approval.LoginLease, error) {
	if runtime == nil || runtime.controller == nil || ctx == nil || ctx.Err() != nil {
		return approval.LoginLease{}, errCredentialsUnavailable
	}
	if lease, err := runtime.controller.ActiveLogin(ctx, scope); err == nil {
		return lease, nil
	}
	if runtime.notify == nil {
		return approval.LoginLease{}, errCredentialsUnavailable
	}
	reference, err := runtime.controller.Request(ctx, scope.Principal, []string{scope.Account},
		credentialbroker.MaxGrantLifetime)
	if err != nil || runtime.notify(ctx, runtime.origin+credentialPagePath+"#request="+reference) != nil {
		return approval.LoginLease{}, errCredentialsUnavailable
	}
	waitCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if lease, err := runtime.controller.ActiveLogin(waitCtx, scope); err == nil {
			return lease, nil
		}
		select {
		case <-waitCtx.Done():
			return approval.LoginLease{}, errCredentialApprovalRequired
		case <-ticker.C:
		}
	}
}
