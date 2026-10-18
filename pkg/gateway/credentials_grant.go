package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/bogdanovich/mintclaw/internal/credentials/approval"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

var errCredentialApprovalRequired = errors.New("credential approval required")

// requestCredentialLease retains the bounded inline wait for connector calls.
// Browser tools use credentialLeaseOrApproval and transfer waiting to the
// existing durable interaction lifecycle without holding a browser or values.
func (runtime *credentialRuntime) requestCredentialLease(
	ctx context.Context,
	scope credentialbroker.Request,
) (approval.LoginLease, error) {
	lease, _, err := runtime.credentialLeaseOrApproval(ctx, scope)
	if !errors.Is(err, errCredentialApprovalRequired) {
		return lease, err
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

// credentialLeaseOrApproval returns immediately when phone action is needed.
// The controller remains the sole request/grant owner; no second cache is made.
func (runtime *credentialRuntime) credentialLeaseOrApproval(
	ctx context.Context,
	scope credentialbroker.Request,
) (approval.LoginLease, approval.Wait, error) {
	if runtime == nil || runtime.controller == nil || ctx == nil || ctx.Err() != nil {
		return approval.LoginLease{}, approval.Wait{}, errCredentialsUnavailable
	}
	if lease, err := runtime.controller.ActiveLogin(ctx, scope); err == nil {
		return lease, approval.Wait{}, nil
	}
	if runtime.notify == nil {
		return approval.LoginLease{}, approval.Wait{}, errCredentialsUnavailable
	}
	reference, err := runtime.controller.Request(
		ctx,
		scope.Principal,
		[]string{scope.Account},
		credentialbroker.MaxGrantLifetime,
	)
	if err != nil {
		return approval.LoginLease{}, approval.Wait{}, errCredentialsUnavailable
	}
	waiting, err := runtime.controller.WaitReference(ctx, scope.Principal, reference)
	if err != nil || runtime.notify(ctx, runtime.origin+credentialPagePath+"#request="+reference) != nil {
		runtime.controller.DiscardPendingRequest(scope.Principal, reference)
		return approval.LoginLease{}, approval.Wait{}, errCredentialsUnavailable
	}
	return approval.LoginLease{}, waiting, errCredentialApprovalRequired
}
