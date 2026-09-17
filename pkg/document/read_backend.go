package document

const (
	PopplerBackendName    = "poppler"
	PopplerBackendVersion = "24.02.0"
)

type readBackend interface {
	Extract([]byte, WorkerRequest) backendRead
	Render([]byte, WorkerRequest) backendRead
}

type backendRead struct {
	State      State
	Extraction *ExtractionFacts
	Rendering  *RenderingFacts
	Artifacts  []WorkerArtifact
	Failure    *Failure
}

func popplerIdentity() BackendIdentity {
	return BackendIdentity{
		Name: PopplerBackendName, Version: PopplerBackendVersion, Role: "production",
		IsolationMode: "one_shot_child",
	}
}

func workerArtifactRef(operationID, name string) string {
	return "document-artifact://" + operationID + "/" + name
}
