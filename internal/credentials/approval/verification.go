package approval

import (
	"context"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

// BeginVerification proves an already accepted owner key works on this device.
// It cannot enroll a key, request account access, or issue a credential grant.
func (controller *Controller) BeginVerification(ctx context.Context) (Ceremony, error) {
	if controller == nil {
		return Ceremony{}, ErrUnavailable
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if !controller.ready(ctx) {
		return Ceremony{}, ErrUnavailable
	}
	controller.prune()
	// Reopening the setup page must not allocate an unbounded set of challenges.
	for id, pending := range controller.ceremonies {
		if pending.kind == verify && controller.ceremonyLive(pending) {
			return publicCeremony(id, pending), nil
		}
	}
	return controller.begin(verify, Scope{
		Principal:      credentialbroker.Principal{OwnerID: controller.ownerID},
		PolicyRevision: controller.policy.Revision(),
	}, "")
}

// FinishVerification durably acknowledges the accepted key without granting
// account access. Reloaded or rejected tentative enrollment remains ineligible.
func (controller *Controller) FinishVerification(ctx context.Context, id string, response []byte) error {
	if controller == nil {
		return ErrUnavailable
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	ceremony, err := controller.claim(ctx, id, verify)
	if err != nil {
		return err
	}
	return controller.verifyAssertion(ctx, ceremony, response)
}
