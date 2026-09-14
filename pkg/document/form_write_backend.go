package document

const (
	filledCandidateArtifactName       = "filled.pdf"
	filledCandidateArtifactKind       = "filled_pdf_candidate"
	formWriteStructuralAssertionCount = 8
)

type formWriteBackend interface {
	Fill([]byte, WorkerRequest) backendFormWrite
}

type backendFormWrite struct {
	State     State
	Facts     *FormWriteFacts
	Artifacts []WorkerArtifact
	Candidate []byte
	Failure   *Failure
}

func failedFormWrite(state State, code FailureCode, message string) backendFormWrite {
	return backendFormWrite{State: state, Failure: &Failure{Code: code, Message: message}}
}

func validFormWriteFacts(request WorkerRequest, facts FormWriteFacts, worker WorkerArtifact) bool {
	if request.Fill == nil || facts.Backend.Name != PDFCPUBackendName ||
		facts.Backend.Version != PDFCPUBackendVersion || facts.Backend.Role != "production" ||
		facts.Backend.IsolationMode != "one_shot_process" || facts.SourceSHA256 != request.Input.SHA256 ||
		facts.RequestSHA256 != request.Fill.RequestSHA256 || !validDocumentDigest(facts.OutputSHA256) ||
		facts.OutputSize <= 0 || facts.OutputSize > DefaultMaxArtifactBytes ||
		!equalPages(facts.AffectedPages, request.Fill.AffectedPages) ||
		facts.StructuralAssertions != formWriteStructuralAssertionCount ||
		facts.CheckedFields != len(request.Fill.Assignments) || facts.CheckedFields <= 0 ||
		facts.CheckedWidgets < facts.CheckedFields || facts.CheckedWidgets > DefaultMaxFieldWidgets ||
		facts.UnchangedFields < 0 || facts.UnchangedFields > DefaultMaxFormFields ||
		facts.CheckedFields+facts.UnchangedFields > DefaultMaxFormFields ||
		facts.AppearanceWidgets != facts.CheckedWidgets {
		return false
	}
	artifact := worker.Artifact
	return worker.Name == filledCandidateArtifactName &&
		artifact.Ref == workerArtifactRef(request.OperationID, filledCandidateArtifactName) &&
		artifact.Kind == filledCandidateArtifactKind && artifact.ContentType == "application/pdf" &&
		artifact.Size == facts.OutputSize && artifact.SHA256 == facts.OutputSHA256 &&
		artifact.SourceSHA256 == request.Input.SHA256 && equalPages(artifact.Pages, facts.AffectedPages) &&
		artifact.Width == 0 && artifact.Height == 0 && !artifact.Truncated
}
