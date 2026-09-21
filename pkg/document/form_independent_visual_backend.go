package document

const (
	GhostscriptBackendName    = "ghostscript"
	GhostscriptBackendVersion = "10.02.1"
)

func ghostscriptIdentity() BackendIdentity {
	return BackendIdentity{
		Name: GhostscriptBackendName, Version: GhostscriptBackendVersion, Role: "independent_verifier",
		IsolationMode: "one_shot_child",
	}
}

func validGhostscriptIdentity(identity BackendIdentity) bool {
	return identity == ghostscriptIdentity()
}
