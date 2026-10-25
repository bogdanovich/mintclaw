package approval

import (
	"slices"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

// restoreGrants runs only at startup, before the controller is published. A
// changed policy is durably cleared so A-B-A across restarts cannot revive it.
func (controller *Controller) restoreGrants() error {
	owner := controller.owner
	if len(owner.Grants) == 0 {
		return nil
	}
	if owner.EnrollmentPending || owner.GrantPolicyRevision == "" ||
		len(owner.Grants) > credentialbroker.MaxActiveGrants {
		return ErrUnavailable
	}
	if owner.GrantPolicyRevision == controller.policy.Revision() {
		if controller.authority.Restore(owner.Grants) != nil {
			return ErrUnavailable
		}
		for _, grant := range owner.Grants {
			if controller.authority.Active(grant.ID) {
				controller.grants[grant.ID] = cloneGrant(grant)
			}
		}
	}
	return controller.persistGrants()
}

// persistGrants requires the controller lock. Acknowledgement follows both
// file and directory sync; any uncertain write disables all live consumers.
// Close intentionally leaves this snapshot intact for the next trusted runtime.
func (controller *Controller) persistGrants() error {
	// Snapshot live grants only. Pruning unrelated requests here can retire
	// the request whose verified approval is still completing this transaction.
	owner := controller.owner
	owner.GrantPolicyRevision = controller.policy.Revision()
	owner.Grants = make([]credentialbroker.Grant, 0, len(controller.grants))
	for _, grant := range controller.grants {
		if controller.grantActive(grant.ID) {
			owner.Grants = append(owner.Grants, cloneGrant(grant))
		}
	}
	slices.SortFunc(owner.Grants, func(a, b credentialbroker.Grant) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	if controller.store.commit(owner) != nil {
		controller.fail()
		return ErrUnavailable
	}
	controller.owner = owner
	return nil
}
