package document

import "runtime"

const (
	CapabilitySupported   = "supported"
	CapabilityUnavailable = "unavailable"
)

func Capabilities() CapabilityReport {
	return capabilitiesFor(runtime.GOOS, runtime.GOARCH)
}

func capabilitiesFor(goos, goarch string) CapabilityReport {
	acquire := OperationCapability{
		State:  CapabilityUnavailable,
		Reason: "immutable document acquisition is initially admitted only on linux/amd64",
	}
	if goos == "linux" && goarch == "amd64" {
		acquire = OperationCapability{State: CapabilitySupported}
	}
	inspect := OperationCapability{
		State:  CapabilityUnavailable,
		Reason: "document inspection is admitted only with the packaged linux/amd64 backend",
	}
	if goos == "linux" && goarch == "amd64" {
		inspect = OperationCapability{State: CapabilitySupported}
	}
	laterReason := "document extraction and rendering are not implemented yet"
	return CapabilityReport{
		SchemaVersion: CapabilitySchemaVersion,
		Platform:      goos,
		Architecture:  goarch,
		Operations: map[string]OperationCapability{
			"acquire": acquire,
			"inspect": inspect,
			"extract": {State: CapabilityUnavailable, Reason: laterReason},
			"render":  {State: CapabilityUnavailable, Reason: laterReason},
			"fields":  {State: CapabilityUnavailable, Reason: "AcroForm support is not implemented yet"},
			"fill":    {State: CapabilityUnavailable, Reason: "AcroForm support is not implemented yet"},
			"verify":  {State: CapabilityUnavailable, Reason: "document verification is not implemented yet"},
			"flatten": {State: CapabilityUnavailable, Reason: "document transformation is not implemented yet"},
		},
		Limits: Limits{
			MaxInputBytes:     DefaultMaxInputBytes,
			MaxPages:          DefaultMaxPages,
			MaxContentBytes:   DefaultMaxContentBytes,
			MaxObjects:        DefaultMaxObjects,
			MaxRecursionDepth: DefaultMaxRecursionDepth,
		},
	}
}
