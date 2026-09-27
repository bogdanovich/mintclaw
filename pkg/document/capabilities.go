package document

const (
	CapabilitySupported   = "supported"
	CapabilityUnavailable = "unavailable"
)

func Capabilities() CapabilityReport {
	return resolveRuntimeBackendSet().capabilityReport()
}

func capabilitiesFor(goos, goarch string) CapabilityReport {
	return declaredBackendSet(goos, goarch).capabilityReport()
}
