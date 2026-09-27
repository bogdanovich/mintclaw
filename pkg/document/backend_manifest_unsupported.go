//go:build !linux || !amd64

package document

func nativeBackendCapabilities() []BackendCapability { return nil }
