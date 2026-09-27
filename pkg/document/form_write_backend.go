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

func validFormWriteFactsForSet(
	backends backendSet,
	request WorkerRequest,
	facts FormWriteFacts,
	worker WorkerArtifact,
) bool {
	if request.Fill == nil || !validFormWriteFactEnvelope(facts) || facts.SourceSHA256 != request.Input.SHA256 ||
		facts.RequestSHA256 != request.Fill.RequestSHA256 || !validDocumentDigest(facts.OutputSHA256) ||
		!equalPages(facts.AffectedPages, request.Fill.AffectedPages) ||
		facts.CheckedFields != len(request.Fill.Assignments) || !validFormWriteBackendsForSet(backends, facts) {
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
	if !validFormWriterIdentity(facts.Backend) || !validFormVisualIdentity(facts.VisualBackend) ||
		!validDocumentDigest(facts.SourceSHA256) ||
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
			len(facts.Output.Normalizations) != 0 {
			return false
		}
		if facts.VisualBackend == pdfiumWASMIdentity() {
			if facts.IndependentVisualBackend == (BackendIdentity{}) {
				if facts.IndependentVisualAssertions != 0 || facts.IndependentRenderedPages != 0 {
					return false
				}
			} else if !validPopplerIdentity(facts.IndependentVisualBackend) ||
				facts.IndependentRenderedPages != len(facts.AffectedPages) ||
				facts.IndependentVisualAssertions < facts.IndependentRenderedPages+facts.CheckedWidgets {
				return false
			}
		} else if !validLegacyStandardFormWriteBackends(facts) {
			return false
		}
	case FormOutputFlattenedPrint:
		if facts.StructuralAssertions != hybridWriteStructuralAssertionCount ||
			facts.Backend != pdfcpuIdentityWithIsolation(NativeBackendIsolationMode) ||
			!validPopplerIdentity(facts.VisualBackend) ||
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

func validFormWriterIdentity(identity BackendIdentity) bool {
	return identity == pdfcpuIdentity() || identity == pdfcpuIdentityWithIsolation(NativeBackendIsolationMode)
}

func validFormVisualIdentity(identity BackendIdentity) bool {
	return identity == pdfiumWASMIdentity() || validPopplerIdentity(identity)
}

func validLegacyStandardFormWriteBackends(facts FormWriteFacts) bool {
	return facts.Backend == pdfcpuIdentityWithIsolation(NativeBackendIsolationMode) &&
		validPopplerIdentity(facts.VisualBackend) &&
		facts.IndependentVisualBackend == (BackendIdentity{}) &&
		facts.IndependentVisualAssertions == 0 && facts.IndependentRenderedPages == 0
}

func validFormWriteBackendsForSet(backends backendSet, facts FormWriteFacts) bool {
	if facts.Output.Mode == FormOutputFlattenedPrint {
		return backends.admitsHybridForms() &&
			facts.Backend == pdfcpuIdentityWithIsolation(NativeBackendIsolationMode) &&
			facts.VisualBackend == popplerIdentity() &&
			validGhostscriptIdentity(facts.IndependentVisualBackend)
	}
	switch {
	case portableStandardFormTarget(backends.platform, backends.architecture):
		return facts.Backend == pdfcpuIdentityFor(backends.platform) &&
			facts.VisualBackend == pdfiumWASMIdentity() &&
			facts.IndependentVisualBackend == (BackendIdentity{})
	case backends.platform == "linux" && backends.architecture == "amd64":
		return facts.Backend == pdfcpuIdentityWithIsolation(NativeBackendIsolationMode) &&
			facts.VisualBackend == pdfiumWASMIdentity() && facts.IndependentVisualBackend == popplerIdentity()
	default:
		return false
	}
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
		case "xfa_removed", "acroform_flattened", "xfa_scripts_removed", "javascript_name_tree_removed",
			"usage_rights_removed", "encryption_preserved":
		default:
			return false
		}
	}
	return true
}
