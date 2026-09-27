package document

const (
	GhostscriptBackendName    = "ghostscript"
	GhostscriptBackendVersion = "10.02.1"
)

func ghostscriptIdentity() BackendIdentity {
	return nativeBackendIdentity(GhostscriptBackendName)
}

func validGhostscriptIdentity(identity BackendIdentity) bool {
	return identity == ghostscriptIdentity()
}
