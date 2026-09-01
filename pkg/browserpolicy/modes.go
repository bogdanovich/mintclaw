package browserpolicy

const (
	CapabilityFullAccess = "full_access"
	CapabilityRestricted = "restricted"

	ApprovalNone           = "none"
	ApprovalModelRequested = "model_requested"
	ApprovalAlwaysCommit   = "always_commit"
	ApprovalPolicy         = "policy"

	ConfirmationRequest = "request"
)

func CapabilityModeValid(mode string) bool {
	switch mode {
	case CapabilityFullAccess, CapabilityRestricted:
		return true
	default:
		return false
	}
}

func ApprovalModeValid(mode string) bool {
	switch mode {
	case ApprovalNone, ApprovalModelRequested, ApprovalAlwaysCommit, ApprovalPolicy:
		return true
	default:
		return false
	}
}

func ConfirmationValid(confirmation string) bool {
	return confirmation == "" || confirmation == ConfirmationRequest
}

// RequiresApproval keeps effect classification separate from authority. The
// model may request a pause, but cannot lower an operator-required pause.
func RequiresApproval(mode, effect, confirmation string) bool {
	switch mode {
	case ApprovalNone:
		return false
	case ApprovalModelRequested:
		return confirmation == ConfirmationRequest
	case ApprovalAlwaysCommit:
		return confirmation == ConfirmationRequest || effect == "external_commit" || effect == "unknown"
	default:
		// Restricted policy decisions are evaluated and bound separately. Any
		// caller that reaches this generic helper without that binding fails closed.
		return true
	}
}

// RestrictedRequiresApproval keeps an explicit model confirmation request
// additive to an operator policy decision. Invalid bindings fail closed; the
// surrounding action contracts reject them before dispatch.
func RestrictedRequiresApproval(decision, confirmation string) bool {
	if !DecisionValid(decision) || !ConfirmationValid(confirmation) {
		return true
	}
	return decision != DecisionAllow || confirmation == ConfirmationRequest
}

// FillRoleAllowed admits the accessibility roles supported by the fill
// driver. Restricted semantic policy is evaluated separately before dispatch.
func FillRoleAllowed(role string) bool {
	return role == "textbox" || role == "searchbox" || role == "combobox" || role == "spinbutton"
}
