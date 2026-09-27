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
		inspection: implementation, nativeReader: implementation, portableReader: implementation,
		formFields: implementation, formWriter: implementation,
	}

	tests := []struct {
		name             string
		input            backendSetInput
		readState        string
		portableState    string
		readMode         string
		readPrimary      string
		fillMode         string
		fillVerifier     string
		expectInspection bool
		expectReader     bool
	}{
		{
			name: "linux with qualified native backends",
			input: backendSetInput{
				goos: "linux", goarch: "amd64", processWorkerAvailable: true,
				inspectionAvailable: true, nativeReaderAvailable: true, portableReaderAvailable: true,
				formFieldsAvailable: true, formWriterAvailable: true, portablePDFiumAvailable: true,
				native: declaredNativeBackends("linux", "amd64"), implementations: availableImplementations,
			},
			readState: CapabilitySupported, portableState: CapabilitySupported,
			readMode: CapabilityModeNativeOnly, readPrimary: PopplerBackendName,
			fillMode: CapabilityModeIndependentlyVerified, fillVerifier: PopplerBackendName,
			expectInspection: true, expectReader: true,
		},
		{
			name: "linux without qualified native backends",
			input: backendSetInput{
				goos: "linux", goarch: "amd64", processWorkerAvailable: true,
				inspectionAvailable: true, nativeReaderAvailable: true, portableReaderAvailable: true,
				formFieldsAvailable: true, formWriterAvailable: true, portablePDFiumAvailable: true,
				implementations: availableImplementations,
			},
			readState: CapabilityUnavailable, portableState: CapabilitySupported,
			expectInspection: true, expectReader: false,
		},
		{
			name: "macOS AMD64 portable read primary",
			input: backendSetInput{
				goos: "darwin", goarch: "amd64", processWorkerAvailable: true,
				inspectionAvailable: true, portableReaderAvailable: true, portablePDFiumAvailable: true,
				implementations: backendImplementations{inspection: implementation, portableReader: implementation},
			},
			readState: CapabilitySupported, portableState: CapabilitySupported,
			readMode: CapabilityModePortable, readPrimary: PDFiumWASMBackendName,
			expectInspection: true, expectReader: true,
		},
		{
			name: "macOS ARM64 portable read primary",
			input: backendSetInput{
				goos: "darwin", goarch: "arm64", processWorkerAvailable: true,
				inspectionAvailable: true, portableReaderAvailable: true, portablePDFiumAvailable: true,
				implementations: backendImplementations{inspection: implementation, portableReader: implementation},
			},
			readState: CapabilitySupported, portableState: CapabilitySupported,
			readMode: CapabilityModePortable, readPrimary: PDFiumWASMBackendName,
			expectInspection: true, expectReader: true,
		},
		{
			name: "unqualified Linux architecture",
			input: backendSetInput{
				goos: "linux", goarch: "arm64", portableReaderAvailable: true,
				portablePDFiumAvailable: true, implementations: availableImplementations,
			},
			readState: CapabilityUnavailable, portableState: CapabilityUnavailable,
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
			fill := report.Operations[operationFill]
			if fill.Mode != test.fillMode || firstBackendName(fill.Verifiers) != test.fillVerifier {
				t.Fatalf("fill capability = %#v", fill)
			}
			portable, found := backendCapabilityByName(report.Backends, PDFiumWASMBackendName)
			if !found || portable.State != test.portableState {
				t.Fatalf("portable backend = %#v, found=%t", portable, found)
			}
			if (set.inspection != nil) != test.expectInspection || (set.reader != nil) != test.expectReader {
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

func firstBackendName(identities []BackendIdentity) string {
	if len(identities) == 0 {
		return ""
	}
	return identities[0].Name
}
