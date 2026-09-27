package document

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type backendSetTestImplementation struct{}

func (*backendSetTestImplementation) Inspect(io.ReadSeeker, Limits) backendInspection {
	return backendInspection{}
}

func (*backendSetTestImplementation) Extract([]byte, WorkerRequest) backendRead { return backendRead{} }

func (*backendSetTestImplementation) Render([]byte, WorkerRequest) backendRead { return backendRead{} }

func (*backendSetTestImplementation) Fields(io.ReadSeeker, Limits, string) backendFormFields {
	return backendFormFields{}
}

func (*backendSetTestImplementation) Fill([]byte, WorkerRequest) backendFormWrite {
	return backendFormWrite{}
}

func TestResolveBackendSetFreezesPlatformComposition(t *testing.T) {
	implementation := &backendSetTestImplementation{}
	availableImplementations := backendImplementations{
		inspection: implementation, portableReader: implementation,
		formFields: implementation, formWriter: implementation,
	}

	tests := []struct {
		name             string
		input            backendSetInput
		readState        string
		portableState    string
		readMode         string
		readPrimary      string
		fieldState       string
		fillState        string
		fillMode         string
		fillPrimary      string
		fillVerifiers    []string
		expectInspection bool
		expectReader     bool
		expectFormFields bool
	}{
		{
			name: "linux with qualified native backends",
			input: backendSetInput{
				goos: "linux", goarch: "amd64", processWorkerAvailable: true,
				inspectionAvailable: true, portableReaderAvailable: true,
				formFieldsAvailable: true, formWriterAvailable: true, portablePDFiumAvailable: true,
				native: declaredNativeBackends("linux", "amd64"), implementations: availableImplementations,
			},
			readState: CapabilitySupported, portableState: CapabilitySupported,
			readMode: CapabilityModePortable, readPrimary: PDFiumWASMBackendName,
			fieldState: CapabilitySupported,
			fillState:  CapabilitySupported, fillMode: CapabilityModeIndependentlyVerified,
			fillPrimary: PDFCPUBackendName, fillVerifiers: []string{PDFiumWASMBackendName, PopplerBackendName},
			expectInspection: true, expectReader: true, expectFormFields: true,
		},
		{
			name: "linux without qualified native backends",
			input: backendSetInput{
				goos: "linux", goarch: "amd64", processWorkerAvailable: true,
				inspectionAvailable: true, portableReaderAvailable: true,
				formFieldsAvailable: true, formWriterAvailable: true, portablePDFiumAvailable: true,
				implementations: availableImplementations,
			},
			readState: CapabilitySupported, portableState: CapabilitySupported,
			readMode: CapabilityModePortable, readPrimary: PDFiumWASMBackendName,
			fieldState:       CapabilitySupported,
			fillState:        CapabilityUnavailable,
			expectInspection: true, expectReader: true, expectFormFields: true,
		},
		{
			name: "linux does not fall back when portable backend is unavailable",
			input: backendSetInput{
				goos: "linux", goarch: "amd64", processWorkerAvailable: true,
				inspectionAvailable: true, portableReaderAvailable: true,
				formFieldsAvailable: true, formWriterAvailable: true,
				native: declaredNativeBackends("linux", "amd64"), implementations: availableImplementations,
			},
			readState: CapabilityUnavailable, portableState: CapabilityUnavailable,
			fieldState: CapabilitySupported, fillState: CapabilityUnavailable,
			expectInspection: true, expectReader: false, expectFormFields: true,
		},
		{
			name: "macOS AMD64 portable read primary",
			input: backendSetInput{
				goos: "darwin", goarch: "amd64", processWorkerAvailable: true,
				inspectionAvailable: true, portableReaderAvailable: true, formFieldsAvailable: true,
				formWriterAvailable:     true,
				portablePDFiumAvailable: true,
				implementations: backendImplementations{
					inspection: implementation, portableReader: implementation, formFields: implementation,
					formWriter: implementation,
				},
			},
			readState: CapabilitySupported, portableState: CapabilitySupported,
			readMode: CapabilityModePortable, readPrimary: PDFiumWASMBackendName,
			fieldState: CapabilitySupported,
			fillState:  CapabilitySupported, fillMode: CapabilityModeIndependentlyVerified,
			fillPrimary: PDFCPUBackendName, fillVerifiers: []string{PDFiumWASMBackendName},
			expectInspection: true, expectReader: true, expectFormFields: true,
		},
		{
			name: "macOS ARM64 portable read primary",
			input: backendSetInput{
				goos: "darwin", goarch: "arm64", processWorkerAvailable: true,
				inspectionAvailable: true, portableReaderAvailable: true, formFieldsAvailable: true,
				formWriterAvailable:     true,
				portablePDFiumAvailable: true,
				implementations: backendImplementations{
					inspection: implementation, portableReader: implementation, formFields: implementation,
					formWriter: implementation,
				},
			},
			readState: CapabilitySupported, portableState: CapabilitySupported,
			readMode: CapabilityModePortable, readPrimary: PDFiumWASMBackendName,
			fieldState: CapabilitySupported,
			fillState:  CapabilitySupported, fillMode: CapabilityModeIndependentlyVerified,
			fillPrimary: PDFCPUBackendName, fillVerifiers: []string{PDFiumWASMBackendName},
			expectInspection: true, expectReader: true, expectFormFields: true,
		},
		{
			name: "Windows AMD64 portable composition",
			input: backendSetInput{
				goos: "windows", goarch: "amd64", processWorkerAvailable: true,
				inspectionAvailable: true, portableReaderAvailable: true, formFieldsAvailable: true,
				formWriterAvailable: true, portablePDFiumAvailable: true,
				implementations: availableImplementations,
			},
			readState: CapabilitySupported, portableState: CapabilitySupported,
			readMode: CapabilityModePortable, readPrimary: PDFiumWASMBackendName,
			fieldState: CapabilitySupported,
			fillState:  CapabilitySupported, fillMode: CapabilityModeIndependentlyVerified,
			fillPrimary: PDFCPUBackendName, fillVerifiers: []string{PDFiumWASMBackendName},
			expectInspection: true, expectReader: true, expectFormFields: true,
		},
		{
			name: "unqualified Linux architecture",
			input: backendSetInput{
				goos: "linux", goarch: "arm64", portableReaderAvailable: true,
				portablePDFiumAvailable: true, implementations: availableImplementations,
			},
			readState: CapabilityUnavailable, portableState: CapabilityUnavailable,
			fieldState: CapabilityUnavailable, fillState: CapabilityUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			set := resolveBackendSet(test.input)
			report := set.capabilityReport()
			read := report.Operations[operationExtract]
			if read.State != test.readState || read.Mode != test.readMode {
				t.Fatalf("read capability = %#v", read)
			}
			if backendName(read.Primary) != test.readPrimary {
				t.Fatalf("read primary = %#v, want %q", read.Primary, test.readPrimary)
			}
			render := report.Operations[operationRender]
			if render.State != test.readState || render.Mode != test.readMode ||
				backendName(render.Primary) != test.readPrimary {
				t.Fatalf("render capability = %#v", render)
			}
			fields := report.Operations[operationFields]
			if fields.State != test.fieldState ||
				(test.fieldState == CapabilitySupported && (fields.Mode != CapabilityModePortable ||
					fields.Primary == nil || *fields.Primary != pdfcpuIdentityFor(test.input.goos))) {
				t.Fatalf("field capability = %#v, want %q", fields, test.fieldState)
			}
			fill := report.Operations[operationFill]
			if fill.State != test.fillState || fill.Mode != test.fillMode ||
				backendName(fill.Primary) != test.fillPrimary ||
				!equalBackendNames(fill.Verifiers, test.fillVerifiers) {
				t.Fatalf("fill capability = %#v", fill)
			}
			portable, found := backendCapabilityByName(report.Backends, PDFiumWASMBackendName)
			if !found || portable.State != test.portableState {
				t.Fatalf("portable backend = %#v, found=%t", portable, found)
			}
			if (set.inspection != nil) != test.expectInspection || (set.reader != nil) != test.expectReader ||
				(set.formFields != nil) != test.expectFormFields {
				t.Fatalf("resolved implementations = %#v", set)
			}
		})
	}
}

func TestBackendSetCapabilityProjectionIsDefensive(t *testing.T) {
	set := declaredBackendSet("linux", "amd64")
	first := set.capabilityReport()
	first.Operations[operationExtract] = OperationCapability{State: "mutated"}
	first.Backends[0].Identity.Name = "mutated"
	first.Backends[len(first.Backends)-1].Executables[0].Path = "/mutated"

	second := set.capabilityReport()
	if second.Operations[operationExtract].State != CapabilitySupported ||
		second.Backends[0].Identity.Name != PDFCPUBackendName ||
		second.Backends[len(second.Backends)-1].Executables[0].Path == "/mutated" {
		t.Fatalf("backend set was mutated through capability projection: %#v", second)
	}
}

func TestBackendSetRejectsImplementationOutsideFrozenComposition(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "text.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	request := testWorkerRequest(data)
	request.Operation = workerOperationExtract
	request.Read = &WorkerReadRequest{Pages: []int{1}, Limits: defaultReadLimits(workerOperationExtract)}
	requestBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}

	implementation := &backendSetTestImplementation{}
	var output bytes.Buffer
	err = serveWorkerWithBackendSet(bytes.NewReader(requestBytes), bytes.NewReader(data), &output, backendSet{
		operations: map[string]OperationCapability{
			operationExtract: unavailableOperation("native backend is unavailable"),
		},
		reader: implementation,
	})
	if err != nil {
		t.Fatalf("serve worker: %v", err)
	}
	result, err := decodeWorkerResult(output.Bytes(), request)
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	assertWorkerFailure(t, result, StateUnavailable, FailureBackendUnavailable)
}

func backendName(identity *BackendIdentity) string {
	if identity == nil {
		return ""
	}
	return identity.Name
}

func equalBackendNames(identities []BackendIdentity, names []string) bool {
	if len(identities) != len(names) {
		return false
	}
	for index := range identities {
		if identities[index].Name != names[index] {
			return false
		}
	}
	return true
}
