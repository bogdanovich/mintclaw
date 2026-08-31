package browserpolicy

const (
	CapabilityFullAccess   = "full_access"
	CapabilityRestricted   = "restricted"
	CapabilityLegacyStrict = "legacy_strict"

	ApprovalNone           = "none"
	ApprovalModelRequested = "model_requested"
	ApprovalAlwaysCommit   = "always_commit"
	ApprovalPolicy         = "policy"

	ConfirmationRequest = "request"
)

func EffectiveCapabilityMode(mode string) string {
	if mode == "" {
		return CapabilityLegacyStrict
	}
	return mode
}

func EffectiveApprovalMode(mode string) string {
	if mode == "" {
		return ApprovalAlwaysCommit
	}
	return mode
}

func CapabilityModeValid(mode string) bool {
	switch EffectiveCapabilityMode(mode) {
	case CapabilityFullAccess, CapabilityRestricted, CapabilityLegacyStrict:
		return true
	default:
		return false
	}
}

func ApprovalModeValid(mode string) bool {
	switch EffectiveApprovalMode(mode) {
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
	switch EffectiveApprovalMode(mode) {
	case ApprovalNone:
		return false
	case ApprovalModelRequested:
		return confirmation == ConfirmationRequest
	case ApprovalAlwaysCommit:
		return confirmation == ConfirmationRequest || effect == "external_commit" || effect == "unknown"
	default:
		// Restricted policy decisions are added in P1 and fail closed until then.
		return true
	}
}

// FillFieldAllowed applies only the semantic admission layer. Driver-side DOM
// checks still require a live writable control before dispatch.
func FillFieldAllowed(capabilityMode, role, name string, sensitiveTerms []string) bool {
	switch EffectiveCapabilityMode(capabilityMode) {
	case CapabilityFullAccess:
		// These are the accessibility roles Playwright may expose for controls
		// supported by locator.fill(). In particular, input[type=number] is a
		// spinbutton, which is common for price fields. Driver-side DOM checks
		// remain the final mechanical authority.
		return role == "textbox" || role == "searchbox" || role == "combobox" || role == "spinbutton"
	case CapabilityLegacyStrict:
		return OrdinaryFillField(role, name, sensitiveTerms)
	default:
		return false
	}
}
