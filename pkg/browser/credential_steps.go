package browser

func resolveCredentialStepRefs(fields *CredentialFields, refs map[string]DriverElement) bool {
	var selected []*string
	switch fields.Step {
	case "", "credentials":
		selected = []*string{&fields.UsernameRef, &fields.PasswordRef, &fields.SubmitRef}
	case "username":
		selected = []*string{&fields.UsernameRef, &fields.SubmitRef}
	case "password":
		selected = []*string{&fields.PasswordRef, &fields.SubmitRef}
	case "totp":
		selected = []*string{&fields.OTPRef, &fields.SubmitRef}
	case "verify":
		selected = []*string{&fields.AuthenticatedRef}
	default:
		return false
	}
	seen := make(map[string]bool, len(selected))
	for _, destination := range selected {
		ref := *destination
		element, found := refs[ref]
		// Native refs may carry a main-document prefix. Exact document/element
		// readiness is established privately before any value retrieval.
		if !found || seen[ref] || !playwrightTargetPattern.MatchString(element.Target) {
			return false
		}
		seen[ref] = true
		*destination = element.Target
	}
	return true
}
