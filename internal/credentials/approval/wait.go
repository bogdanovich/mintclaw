package approval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
	"github.com/bogdanovich/mintclaw/pkg/credentialbroker/intent"
)

// Wait is safe durable correlation, never a phone-link capability or a grant.
// Only the originating principal may resolve it, against current live state.
type Wait struct {
	Reference string
	Deadline  time.Time
}

func withinRequestLifetime(now, issued time.Time) bool {
	return !now.Before(issued) && now.Before(issued.Add(RequestLifetime))
}

func requestWaitReference(id string) string {
	digest := sha256.Sum256([]byte(id))
	return hex.EncodeToString(digest[:])
}

func (controller *Controller) requestStatusLocked(
	principal credentialbroker.Principal,
	id string,
) (intent.Code, string) {
	request := controller.requests[id]
	if !intent.ValidReference(id) || request == nil || request.scope.Principal != principal ||
		request.scope.PolicyRevision != controller.policy.Revision() || !controller.permits(principal, request.scope.Accounts) {
		return intent.Denied, ""
	}
	if request.grant != "" && controller.grantActive(request.grant) {
		return intent.GrantActive, request.grant
	}
	if request.attempted {
		return intent.Denied, ""
	}
	return intent.ApprovalPending, ""
}

// WaitReference converts one private phone locator into non-authorizing
// metadata. Keeping the two references separate prevents durable conditions
// from becoming another way to open the phone approval endpoint.
func (controller *Controller) WaitReference(
	ctx context.Context, principal credentialbroker.Principal, id string,
) (Wait, error) {
	if controller == nil {
		return Wait{}, ErrUnavailable
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.ready(ctx) {
		return Wait{}, ErrUnavailable
	}
	controller.prune()
	status, _ := controller.requestStatusLocked(principal, id)
	if status != intent.ApprovalPending && status != intent.GrantActive {
		return Wait{}, ErrDenied
	}
	return Wait{Reference: requestWaitReference(id), Deadline: controller.requests[id].issued.Add(RequestLifetime)}, nil
}

// ResolveWait checks the sole bounded request map and verified live grants.
// It never retrieves a vault item, issues a grant or accepts a chat answer.
func (controller *Controller) ResolveWait(
	ctx context.Context, principal credentialbroker.Principal, reference string,
) intent.Code {
	if controller == nil {
		return intent.NotReady
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.ready(ctx) {
		return intent.NotReady
	}
	controller.prune()
	if !intent.ValidReference(reference) {
		return intent.Denied
	}
	for id := range controller.requests {
		if requestWaitReference(id) == reference {
			request := controller.requests[id]
			if request.scope.Principal != principal || request.scope.PolicyRevision != controller.policy.Revision() ||
				!controller.permits(principal, request.scope.Accounts) {
				return intent.Denied
			}
			// Approval grants cover their displayed accounts, not one suspended task.
			// Another verified grant for this exact principal/scope can release a wait.
			for grantID, grant := range controller.grants {
				if grant.Principal != principal || grant.PolicyRevision != request.scope.PolicyRevision ||
					!controller.grantActive(grantID) {
					continue
				}
				covered := true
				for _, account := range request.scope.Accounts {
					if !slices.Contains(grant.Accounts, account) {
						covered = false
						break
					}
				}
				if covered {
					return intent.GrantActive
				}
			}
			status, _ := controller.requestStatusLocked(principal, id)
			return status
		}
	}
	return intent.Denied
}
