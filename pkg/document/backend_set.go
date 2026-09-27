package document

import "runtime"

const (
	CapabilityModePortable              = "portable"
	CapabilityModeIndependentlyVerified = "independently_verified"
	CapabilityModeNativeOnly            = "native_only"

	PDFCPUBackendPackage       = "github.com/pdfcpu/pdfcpu"
	WorkerIsolationMode        = "one_shot_process_descriptor_input"
	WindowsWorkerIsolationMode = "one_shot_job_object_inherited_handle_input_v1"
	documentWorkerName         = "mintclaw-document-worker"
	documentWorkerVersion      = "v1"
)

type backendImplementations struct {
	inspection     inspectionBackend
	portableReader readBackend
	formFields     formFieldsBackend
	formWriter     formWriteBackend
}

type backendSetInput struct {
	goos                    string
	goarch                  string
	processWorkerAvailable  bool
	inspectionAvailable     bool
	portableReaderAvailable bool
	formFieldsAvailable     bool
	formWriterAvailable     bool
	portablePDFiumAvailable bool
	native                  []BackendCapability
	implementations         backendImplementations
}

type backendSet struct {
	platform     string
	architecture string
	operations   map[string]OperationCapability
	backends     []BackendCapability
	inspection   inspectionBackend
	reader       readBackend
	formFields   formFieldsBackend
	formWriter   formWriteBackend
}

func resolveRuntimeBackendSet() backendSet {
	implementations := backendImplementations{
		inspection:     newInspectionBackend(),
		portableReader: newPortableReadBackend(newPortablePDFiumPool),
		formFields:     newFormFieldsBackend(),
		formWriter:     newFormWriteBackend(),
	}
	return resolveBackendSet(backendSetInput{
		goos:                    runtime.GOOS,
		goarch:                  runtime.GOARCH,
		processWorkerAvailable:  processWorkerAvailable(),
		inspectionAvailable:     implementations.inspection != nil,
		portableReaderAvailable: implementations.portableReader != nil,
		formFieldsAvailable:     implementations.formFields != nil,
		formWriterAvailable:     implementations.formWriter != nil,
		portablePDFiumAvailable: implementations.portableReader != nil,
		native:                  nativeBackendCapabilities(),
		implementations:         implementations,
	})
}

func declaredBackendSet(goos, goarch string) backendSet {
	portableWorker := portablePDFiumTarget(goos, goarch)
	return resolveBackendSet(backendSetInput{
		goos:                    goos,
		goarch:                  goarch,
		processWorkerAvailable:  portableWorker,
		inspectionAvailable:     portableWorker,
		portableReaderAvailable: portablePDFiumTarget(goos, goarch),
		formFieldsAvailable:     portableWorker,
		formWriterAvailable:     portableWorker,
		portablePDFiumAvailable: portablePDFiumTarget(goos, goarch),
		native:                  declaredNativeBackends(goos, goarch),
	})
}

func resolveBackendSet(input backendSetInput) backendSet {
	set := backendSet{
		platform:     input.goos,
		architecture: input.goarch,
		operations:   make(map[string]OperationCapability, 8),
		inspection:   input.implementations.inspection,
		formFields:   input.implementations.formFields,
		formWriter:   input.implementations.formWriter,
	}

	pdfcpu := pdfcpuBackendCapability(input)
	pdfium := pdfiumBackendCapability(input)
	set.backends = append(set.backends, pdfcpu, pdfium)
	set.backends = append(set.backends, cloneBackendCapabilities(input.native)...)

	workerIdentity := documentWorkerIdentityFor(input.goos)
	pdfcpuIdentity := pdfcpuIdentityFor(input.goos)
	if input.processWorkerAvailable {
		set.operations[operationAcquire] = supportedOperation(CapabilityModePortable, workerIdentity)
	} else {
		set.operations[operationAcquire] = unavailableOperation(
			"immutable document acquisition has no admitted worker transport on this platform",
		)
	}
	if input.processWorkerAvailable && input.inspectionAvailable {
		set.operations[operationInspect] = supportedOperation(CapabilityModePortable, pdfcpuIdentity)
	} else {
		set.operations[operationInspect] = unavailableOperation(
			"document inspection has no admitted worker and pdfcpu composition on this platform",
		)
		set.inspection = nil
	}
	if input.processWorkerAvailable && input.formFieldsAvailable {
		set.operations[operationFields] = supportedOperation(CapabilityModePortable, pdfcpuIdentity)
	} else {
		set.operations[operationFields] = unavailableOperation(
			"AcroForm field discovery has no admitted worker and pdfcpu composition on this platform",
		)
		set.formFields = nil
	}

	poppler, popplerFound := backendCapabilityByName(input.native, PopplerBackendName)
	popplerAvailable := popplerFound && poppler.State == CapabilitySupported
	portableReadAvailable := input.processWorkerAvailable && input.inspectionAvailable &&
		input.portableReaderAvailable && pdfium.State == CapabilitySupported
	if portableReadAvailable {
		set.operations[operationExtract] = supportedOperation(CapabilityModePortable, pdfium.Identity)
		set.operations[operationRender] = supportedOperation(CapabilityModePortable, pdfium.Identity)
		set.reader = input.implementations.portableReader
	} else {
		reason := portableBackendUnavailableReason(pdfium)
		set.operations[operationExtract] = unavailableOperation(reason)
		set.operations[operationRender] = unavailableOperation(reason)
		set.reader = nil
	}
	portableFormWriteAvailable := input.processWorkerAvailable && input.formWriterAvailable &&
		pdfium.State == CapabilitySupported
	if portableFormWriteAvailable && input.goos == "linux" && input.goarch == "amd64" && popplerAvailable {
		formWriterIdentity := pdfcpuIdentityWithIsolation(NativeBackendIsolationMode)
		set.operations[operationFill] = verifiedOperation(formWriterIdentity, pdfium.Identity, poppler.Identity)
		set.operations[operationVerifyFormWrite] = verifiedOperation(
			formWriterIdentity,
			pdfium.Identity,
			poppler.Identity,
		)
	} else if portableFormWriteAvailable && portableStandardFormTarget(input.goos, input.goarch) {
		set.operations[operationFill] = verifiedOperation(pdfcpuIdentity, pdfium.Identity)
		set.operations[operationVerifyFormWrite] = verifiedOperation(pdfcpuIdentity, pdfium.Identity)
	} else {
		reason := portableBackendUnavailableReason(pdfium)
		if portableFormWriteAvailable && input.goos == "linux" && input.goarch == "amd64" {
			reason = nativeBackendUnavailableReason(poppler, popplerFound)
		}
		set.operations[operationFill] = unavailableOperation(reason)
		set.operations[operationVerifyFormWrite] = unavailableOperation(reason)
		set.formWriter = nil
	}
	set.operations["flatten"] = OperationCapability{
		State:  CapabilityUnavailable,
		Reason: "form flattening is withheld because no native-only composition passed independent visual verification",
		Mode:   CapabilityModeNativeOnly,
	}

	return set
}

func (set backendSet) capabilityReport() CapabilityReport {
	operations := make(map[string]OperationCapability, len(set.operations))
	for name, capability := range set.operations {
		operations[name] = cloneOperationCapability(capability)
	}
	return CapabilityReport{
		SchemaVersion: CapabilitySchemaVersion,
		Platform:      set.platform,
		Architecture:  set.architecture,
		Operations:    operations,
		Backends:      cloneBackendCapabilities(set.backends),
		Limits: Limits{
			MaxInputBytes:     DefaultMaxInputBytes,
			MaxPages:          DefaultMaxPages,
			MaxContentBytes:   DefaultMaxContentBytes,
			MaxObjects:        DefaultMaxObjects,
			MaxRecursionDepth: DefaultMaxRecursionDepth,
		},
		ReadLimits: map[string]ReadLimits{
			operationExtract: defaultReadLimits(workerOperationExtract),
			operationRender:  defaultReadLimits(workerOperationRender),
		},
		FormLimits: map[string]FormFieldLimits{
			operationFields: defaultFormFieldLimits(),
			operationFill:   defaultFormFieldLimits(),
		},
	}
}

func (set backendSet) workerOperationAvailable(operation string) bool {
	if operation == workerOperationVerify {
		return true
	}
	capability, found := set.operations[publicOperationForWorker(operation)]
	return found && capability.State == CapabilitySupported
}

func (set backendSet) admitsHybridForms() bool {
	return set.platform == "linux" && set.architecture == "amd64"
}

func publicOperationForWorker(operation string) string {
	switch operation {
	case workerOperationVerify:
		return operationAcquire
	case workerOperationFillCandidate:
		return operationFill
	default:
		return operation
	}
}

func supportedOperation(mode string, primary BackendIdentity) OperationCapability {
	primaryCopy := primary
	return OperationCapability{State: CapabilitySupported, Mode: mode, Primary: &primaryCopy}
}

func verifiedOperation(primary BackendIdentity, verifiers ...BackendIdentity) OperationCapability {
	capability := supportedOperation(CapabilityModeIndependentlyVerified, primary)
	capability.Verifiers = append([]BackendIdentity(nil), verifiers...)
	return capability
}

func unavailableOperation(reason string) OperationCapability {
	return OperationCapability{State: CapabilityUnavailable, Reason: reason}
}

func cloneOperationCapability(capability OperationCapability) OperationCapability {
	copy := capability
	if capability.Primary != nil {
		primary := *capability.Primary
		copy.Primary = &primary
	}
	copy.Verifiers = append([]BackendIdentity(nil), capability.Verifiers...)
	return copy
}

func cloneBackendCapabilities(backends []BackendCapability) []BackendCapability {
	copy := make([]BackendCapability, len(backends))
	for index, backend := range backends {
		copy[index] = backend
		copy[index].Executables = append([]BackendExecutableCapability(nil), backend.Executables...)
	}
	return copy
}

func pdfcpuIdentity() BackendIdentity {
	return pdfcpuIdentityFor(runtime.GOOS)
}

func pdfcpuIdentityFor(goos string) BackendIdentity {
	return pdfcpuIdentityWithIsolation(workerIsolationModeForPlatform(goos))
}

func pdfcpuIdentityWithIsolation(isolationMode string) BackendIdentity {
	return BackendIdentity{
		Name:            PDFCPUBackendName,
		Version:         PDFCPUBackendVersion,
		Package:         PDFCPUBackendPackage,
		PackageRevision: PDFCPUBackendVersion,
		Role:            "production",
		IsolationMode:   isolationMode,
	}
}

func documentWorkerIdentityFor(goos string) BackendIdentity {
	return BackendIdentity{
		Name:          documentWorkerName,
		Version:       documentWorkerVersion,
		Role:          "production",
		IsolationMode: workerIsolationModeForPlatform(goos),
	}
}

func workerIsolationModeForPlatform(goos string) string {
	if goos == "windows" {
		return WindowsWorkerIsolationMode
	}
	return WorkerIsolationMode
}

func pdfcpuBackendCapability(input backendSetInput) BackendCapability {
	capability := BackendCapability{Identity: pdfcpuIdentityFor(input.goos), State: CapabilitySupported}
	if !input.inspectionAvailable && !input.formFieldsAvailable && !input.formWriterAvailable {
		capability.State = CapabilityUnavailable
		capability.Reason = "pdfcpu is not admitted through a document worker on this platform"
	}
	return capability
}

func pdfiumBackendCapability(input backendSetInput) BackendCapability {
	capability := BackendCapability{Identity: pdfiumWASMIdentity(), State: CapabilitySupported}
	if !portablePDFiumTarget(input.goos, input.goarch) {
		capability.State = CapabilityUnavailable
		capability.Reason = "PDFium/WASM is qualified only on linux/amd64, darwin/amd64 or arm64, and windows/amd64"
	} else if !input.portablePDFiumAvailable {
		capability.State = CapabilityUnavailable
		capability.Reason = "the admitted PDFium/WASM runtime is not linked"
	}
	return capability
}

func portablePDFiumTarget(goos, goarch string) bool {
	return (goos == "linux" && goarch == "amd64") ||
		(goos == "darwin" && (goarch == "amd64" || goarch == "arm64")) ||
		(goos == "windows" && goarch == "amd64")
}

func portableStandardFormTarget(goos, goarch string) bool {
	return (goos == "darwin" && (goarch == "amd64" || goarch == "arm64")) ||
		(goos == "windows" && goarch == "amd64")
}

func declaredNativeBackends(goos, goarch string) []BackendCapability {
	if goos != "linux" || goarch != "amd64" {
		return nil
	}
	backends := make([]BackendCapability, 0, len(admittedNativeBackendManifest))
	for _, backend := range admittedNativeBackendManifest {
		backends = append(backends, BackendCapability{
			Identity:    backend.identity,
			State:       CapabilitySupported,
			Executables: append([]BackendExecutableCapability(nil), backend.executables...),
		})
	}
	return backends
}

func nativeBackendUnavailableReason(backend BackendCapability, found bool) string {
	if found && backend.Reason != "" {
		return backend.Reason
	}
	return "the qualified Poppler independent verifier is unavailable"
}

func portableBackendUnavailableReason(backend BackendCapability) string {
	if backend.Reason != "" {
		return backend.Reason
	}
	return "the admitted PDFium/WASM backend is unavailable"
}
