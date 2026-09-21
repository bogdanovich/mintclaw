package document

const (
	filledCandidateArtifactName         = "filled.pdf"
	filledCandidateArtifactKind         = "filled_pdf_candidate"
	formWriteStructuralAssertionCount   = 8
	hybridWriteStructuralAssertionCount = 14
	maxHybridFlattenPages               = 16
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
	if request.Fill == nil || !validFormWriteFactEnvelope(facts) || facts.SourceSHA256 != request.Input.SHA256 ||
		facts.RequestSHA256 != request.Fill.RequestSHA256 || !validDocumentDigest(facts.OutputSHA256) ||
		!equalPages(facts.AffectedPages, request.Fill.AffectedPages) ||
		facts.CheckedFields != len(request.Fill.Assignments) {
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

func validFormWriteFactEnvelope(facts FormWriteFacts) bool {
	if facts.Backend.Name != PDFCPUBackendName || facts.Backend.Version != PDFCPUBackendVersion ||
		facts.Backend.Role != "production" || facts.Backend.IsolationMode != "one_shot_process" ||
		!validPopplerIdentity(facts.VisualBackend) || !validDocumentDigest(facts.SourceSHA256) ||
		!validDocumentDigest(facts.RequestSHA256) || !validDocumentDigest(facts.OutputSHA256) ||
		facts.OutputSize <= 0 || facts.OutputSize > DefaultMaxArtifactBytes ||
		!validFormWriteGenerationPages(facts.AffectedPages) || facts.CheckedFields <= 0 ||
		facts.CheckedWidgets < facts.CheckedFields || facts.CheckedWidgets > DefaultMaxFieldWidgets ||
		facts.UnchangedFields < 0 || facts.UnchangedFields > DefaultMaxFormFields ||
		facts.CheckedFields+facts.UnchangedFields > DefaultMaxFormFields ||
		facts.AppearanceWidgets != facts.CheckedWidgets ||
		facts.VisualAssertions < facts.RenderedPages+facts.CheckedWidgets || !validFormOutputFacts(facts.Output) {
		return false
	}
	switch facts.Output.Mode {
	case FormOutputEditableAcroForm:
		if facts.StructuralAssertions != formWriteStructuralAssertionCount ||
			facts.RenderedPages != len(facts.AffectedPages) || facts.Output.PageCount < facts.RenderedPages ||
			facts.Output.AcroForm != FactPresent || facts.Output.XFA != FactAbsent ||
			facts.Output.ContentSignatures != FactAbsent || facts.Output.UsageRights != FactAbsent ||
			facts.IndependentVisualBackend != (BackendIdentity{}) ||
			facts.IndependentVisualAssertions != 0 || facts.IndependentRenderedPages != 0 ||
			len(facts.Output.Normalizations) != 0 {
			return false
		}
	case FormOutputFlattenedPrint:
		if facts.StructuralAssertions != hybridWriteStructuralAssertionCount ||
			!validGhostscriptIdentity(facts.IndependentVisualBackend) ||
			facts.Output.PageCount < 1 || facts.Output.PageCount > maxHybridFlattenPages ||
			facts.RenderedPages != facts.Output.PageCount ||
			facts.IndependentRenderedPages != facts.Output.PageCount ||
			facts.IndependentVisualAssertions < facts.IndependentRenderedPages ||
			facts.Output.AcroForm != FactAbsent || facts.Output.XFA != FactAbsent ||
			facts.Output.ContentSignatures != FactAbsent || facts.Output.UsageRights != FactAbsent ||
			facts.Output.Actions != FactAbsent || !validHybridNormalizations(facts.Output.Normalizations) {
			return false
		}
	default:
		return false
	}
	return true
}

func validFormOutputFacts(facts FormOutputFacts) bool {
	if facts.PageCount < 1 || facts.PageCount > DefaultMaxPages ||
		!validOperationPermissions(facts.OperationPermissions) {
		return false
	}
	allAllowed := OperationPermissionFacts{
		Print: PermissionAllowed, FormFill: PermissionAllowed, Modify: PermissionAllowed,
		Assemble: PermissionAllowed,
	}
	switch facts.Encryption {
	case FactAbsent:
		return facts.OperationPermissions == allAllowed
	case FactPresent:
		return facts.OperationPermissions.Print != PermissionUnknown &&
			facts.OperationPermissions.FormFill != PermissionUnknown &&
			facts.OperationPermissions.Modify != PermissionUnknown &&
			facts.OperationPermissions.Assemble != PermissionUnknown
	default:
		return false
	}
}

func validHybridNormalizations(values []string) bool {
	if len(values) < 2 || values[0] != "xfa_removed" || values[1] != "acroform_flattened" {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] {
			return false
		}
		seen[value] = true
		switch value {
		case "xfa_removed", "acroform_flattened", "usage_rights_removed", "encryption_preserved":
		default:
			return false
		}
	}
	return true
}
