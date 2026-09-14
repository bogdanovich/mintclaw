package document

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/media"
)

func TestFieldsUsesImmutableSnapshotAndReturnsBoundedMetadata(t *testing.T) {
	root := directTempDir(t)
	inputPath := filepath.Join(root, "form.pdf")
	data, err := os.ReadFile(filepath.Join("testdata", "acroform.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, inputPath, data)
	worker := &recordingFormFieldsWorker{facts: successfulTestFormFields()}

	snapshot, report := fieldsWithWorker(
		t.Context(),
		inputPath,
		AcquireOptions{ScratchRoot: filepath.Join(root, "protected")},
		"linux",
		"amd64",
		worker,
	)
	if snapshot == nil || report.State != StateSucceeded || report.Input == nil || report.Fields == nil ||
		report.Inspection == nil {
		t.Fatalf("report = %#v, snapshot = %#v", report, snapshot)
	}
	if worker.snapshotPath != snapshot.Path() || worker.input == nil || worker.limits != report.Limits {
		t.Fatalf("worker input = %#v, path = %q, limits = %#v", worker.input, worker.snapshotPath, worker.limits)
	}
	if report.Operation != operationFields || report.Fields.Fields[0].Name != "display-name" {
		t.Fatalf("field report = %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{root, inputPath, snapshot.Path(), "private field value"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("report leaked %q: %s", forbidden, encoded)
		}
	}
	if err = snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	assertEmptyDirectory(t, filepath.Join(root, "protected"))
}

func TestFieldsRejectsInvalidWorkerMetadataAndCleansSnapshot(t *testing.T) {
	root := directTempDir(t)
	inputPath := filepath.Join(root, "form.pdf")
	writeFixture(t, inputPath, []byte("%PDF-1.7\nsynthetic\n%%EOF\n"))
	facts := successfulTestFormFields()
	facts.Fields[0].Name = "unsafe\x00name"
	worker := &recordingFormFieldsWorker{facts: facts}

	snapshot, report := fieldsWithWorker(
		t.Context(),
		inputPath,
		AcquireOptions{ScratchRoot: filepath.Join(root, "protected")},
		"linux",
		"amd64",
		worker,
	)
	if snapshot != nil {
		t.Fatal("invalid worker response retained snapshot")
	}
	assertFailureWithInput(t, report, StateFailed, FailureWorkerProtocol)
	assertEmptyDirectory(t, filepath.Join(root, "protected"))
}

func TestFieldsMediaPreservesOwnerAuthorityAndDeniesMismatch(t *testing.T) {
	root := directTempDir(t)
	inputPath := filepath.Join(root, "inbound.pdf")
	data, err := os.ReadFile(filepath.Join("testdata", "acroform.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, inputPath, data)
	store := media.NewFileMediaStore()
	ref, err := store.Store(inputPath, media.MediaMeta{Filename: "inbound.pdf"}, "inbound")
	if err != nil {
		t.Fatal(err)
	}
	owner := testMediaOwner(t)
	if err = store.BindOwner(ref, owner); err != nil {
		t.Fatal(err)
	}
	worker := &recordingFormFieldsWorker{facts: successfulTestFormFields()}
	snapshot, report := fieldsMediaWithWorker(
		t.Context(), store, ref, owner, AcquireOptions{ScratchRoot: filepath.Join(root, "protected")},
		"linux", "amd64", worker,
	)
	if snapshot == nil || report.State != StateSucceeded || report.Input == nil || report.Fields == nil {
		t.Fatalf("report = %#v, snapshot = %#v", report, snapshot)
	}
	if report.Input.SourceRef != ref || report.Input.Authority != documentAuthority(owner) {
		t.Fatalf("fields authority = %#v", report.Input)
	}
	if err = snapshot.Close(); err != nil {
		t.Fatal(err)
	}

	mismatch := owner
	mismatch.RouteID += "-other"
	deniedWorker := &recordingFormFieldsWorker{facts: successfulTestFormFields()}
	snapshot, report = fieldsMediaWithWorker(
		t.Context(), store, ref, mismatch, AcquireOptions{ScratchRoot: filepath.Join(root, "denied")},
		"linux", "amd64", deniedWorker,
	)
	if snapshot != nil || deniedWorker.input != nil {
		t.Fatalf("authority mismatch reached fields worker: %#v %#v", snapshot, deniedWorker.input)
	}
	assertFailure(t, report, StateDenied, FailureSourceUnauthorized)
}

func TestFieldsUnsupportedPlatformDoesNotOpenInput(t *testing.T) {
	root := directTempDir(t)
	scratch := filepath.Join(root, "must-not-exist")
	worker := &recordingFormFieldsWorker{facts: successfulTestFormFields()}
	snapshot, report := fieldsWithWorker(
		t.Context(), filepath.Join(root, "missing.pdf"), AcquireOptions{ScratchRoot: scratch},
		"darwin", "arm64", worker,
	)
	if snapshot != nil || worker.input != nil {
		t.Fatalf("unsupported platform reached fields worker: %#v %#v", snapshot, worker.input)
	}
	assertFailure(t, report, StateUnavailable, FailureUnsupportedPlatform)
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("unsupported platform touched scratch: %v", err)
	}
}

func TestServeWorkerFieldsUsesInspectionGateAndVerifiedBytes(t *testing.T) {
	data := []byte("%PDF-1.7\nform fixture\n%%EOF\n")
	request := testWorkerRequest(data)
	request.Operation = workerOperationFields
	requestBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	inspection := &recordingBackend{result: backendInspection{
		State: StateSucceeded, Facts: successfulTestAcroFormInspection(),
	}}
	backend := &recordingFormFieldsBackend{facts: successfulTestFormFields()}
	var output bytes.Buffer
	if err = serveWorkerWithAllBackends(
		bytes.NewReader(requestBytes),
		bytes.NewReader(data),
		&output,
		inspection,
		nil,
		backend,
	); err != nil {
		t.Fatal(err)
	}
	result, err := decodeWorkerResult(output.Bytes(), request)
	if err != nil {
		t.Fatalf("decode result: %v\n%s", err, output.String())
	}
	if result.State != StateSucceeded || result.Fields == nil || result.Inspection == nil ||
		!bytes.Equal(backend.data, data) || backend.sourceSHA256 != request.Input.SHA256 {
		t.Fatalf("result = %#v backend = %#v", result, backend)
	}
}

func TestServeWorkerFieldsRefusesUnsafeClassesBeforeFormBackend(t *testing.T) {
	data := []byte("%PDF-1.7\nform fixture\n%%EOF\n")
	request := testWorkerRequest(data)
	request.Operation = workerOperationFields
	requestBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*InspectionFacts)
		code   FailureCode
	}{
		{name: "no form", mutate: func(facts *InspectionFacts) {
			facts.AcroForm = AcroFormFacts{State: FactAbsent, FieldCount: testKnownInteger(0)}
		}, code: FailureFormNotPresent},
		{name: "XFA", mutate: func(facts *InspectionFacts) {
			facts.XFA = XFAFacts{
				State: FactPresent, Representation: StringFact{State: FactPresent, Value: "stream"},
				Rendering: StringFact{State: FactPresent, Value: "static"},
			}
		}, code: FailureFormUnsupported},
		{name: "signed", mutate: func(facts *InspectionFacts) {
			facts.Signatures = SignatureFacts{
				State: FactPresent, Count: testKnownInteger(1), Certified: FactAbsent, Timestamped: FactUnknown,
			}
		}, code: FailureFormUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts := successfulTestAcroFormInspection()
			test.mutate(facts)
			inspection := &recordingBackend{result: backendInspection{State: StateSucceeded, Facts: facts}}
			backend := &recordingFormFieldsBackend{facts: successfulTestFormFields()}
			var output bytes.Buffer
			if serveErr := serveWorkerWithAllBackends(
				bytes.NewReader(requestBytes),
				bytes.NewReader(data),
				&output,
				inspection,
				nil,
				backend,
			); serveErr != nil {
				t.Fatal(serveErr)
			}
			result, decodeErr := decodeWorkerResult(output.Bytes(), request)
			if decodeErr != nil {
				t.Fatalf("decode result: %v\n%s", decodeErr, output.String())
			}
			assertWorkerFailureWithInput(t, result, StateUnsupported, test.code)
			if backend.calls != 0 {
				t.Fatalf("unsafe input reached fields backend: %d calls", backend.calls)
			}
		})
	}
}

func TestFormFieldValidationRejectsUntrustedMetadata(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*FormFieldsFacts)
	}{
		{name: "backend", mutate: func(facts *FormFieldsFacts) { facts.Backend.Name = "other" }},
		{name: "field id", mutate: func(facts *FormFieldsFacts) { facts.Fields[0].ID = "field_/private/path" }},
		{name: "widget id", mutate: func(facts *FormFieldsFacts) {
			facts.Fields[0].Widgets[0].ID = "widget_secret"
		}},
		{name: "control name", mutate: func(facts *FormFieldsFacts) { facts.Fields[0].Name = "name\nsecret" }},
		{name: "duplicate option", mutate: func(facts *FormFieldsFacts) {
			facts.Fields[0].Kind = FormFieldRadio
			facts.Fields[0].Options = []FormFieldOption{
				{Export: "yes", Display: "Yes"},
				{Export: "yes", Display: "Oui"},
			}
		}},
		{name: "wrong ordinal", mutate: func(facts *FormFieldsFacts) { facts.Fields[0].Widgets[0].Ordinal = 2 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts := successfulTestFormFields()
			test.mutate(facts)
			if validFormFieldsFacts(*facts) {
				t.Fatalf("accepted invalid fields: %#v", facts)
			}
		})
	}
}

func TestFormFieldsReportLimitBoundsAggregateMetadata(t *testing.T) {
	facts := successfulTestFormFields()
	facts.Fields[0].Name = strings.Repeat("x", facts.Limits.MaxTextBytes)
	for len(facts.Fields) < facts.Limits.MaxFields {
		facts.Fields = append(facts.Fields, facts.Fields[0])
	}
	if formFieldsReportWithinLimit(*facts) {
		t.Fatalf("accepted %d fields exceeding %d report bytes", len(facts.Fields), facts.Limits.MaxReportBytes)
	}
}

type recordingFormFieldsWorker struct {
	facts        *FormFieldsFacts
	input        *DocumentRef
	snapshotPath string
	limits       Limits
}

func (w *recordingFormFieldsWorker) Fields(
	_ context.Context,
	snapshot *Snapshot,
	input DocumentRef,
	limits Limits,
) WorkerResult {
	w.input = &input
	w.snapshotPath = snapshot.Path()
	w.limits = limits
	expected := newWorkerOperationRequest(input, limits, workerOperationFields).Input
	return WorkerResult{
		SchemaVersion: WorkerResultSchemaVersion,
		OperationID:   strings.TrimPrefix(input.Ref, "document://local/"),
		State:         StateSucceeded,
		Input:         &expected,
		Inspection:    successfulTestAcroFormInspection(),
		Fields:        w.facts,
	}
}

type recordingFormFieldsBackend struct {
	facts        *FormFieldsFacts
	data         []byte
	limits       Limits
	sourceSHA256 string
	calls        int
}

func (b *recordingFormFieldsBackend) Fields(
	reader io.ReadSeeker,
	limits Limits,
	sourceSHA256 string,
) backendFormFields {
	b.calls++
	b.data, _ = io.ReadAll(reader)
	b.limits = limits
	b.sourceSHA256 = sourceSHA256
	return backendFormFields{State: StateSucceeded, Facts: b.facts}
}

func successfulTestFormFields() *FormFieldsFacts {
	digest := sha256.Sum256([]byte("field"))
	fieldID := "field_" + hex.EncodeToString(digest[:])
	widgetDigest := sha256.Sum256([]byte("widget"))
	return &FormFieldsFacts{
		Backend: BackendIdentity{
			Name: PDFCPUBackendName, Version: PDFCPUBackendVersion, Role: "production",
			IsolationMode: "one_shot_process",
		},
		Limits: defaultFormFieldLimits(),
		Fields: []FormField{{
			ID: fieldID, Name: "display-name", Kind: FormFieldText,
			Widgets: []FormFieldWidget{{
				ID: "widget_" + hex.EncodeToString(widgetDigest[:]), Page: 1, Ordinal: 1,
			}},
		}},
	}
}

func successfulTestAcroFormInspection() *InspectionFacts {
	facts := defaultInspectionFacts()
	facts.Encryption = EncryptionFacts{
		State: FactAbsent, PasswordRequired: FactAbsent, Permissions: StringFact{State: FactAbsent},
	}
	facts.Signatures = SignatureFacts{
		State: FactAbsent, Count: testKnownInteger(0), Certified: FactAbsent, Timestamped: FactAbsent,
	}
	facts.Restrictions = RestrictionFacts{
		State: FactAbsent, EncryptedPermissions: FactAbsent, DocMDP: FactAbsent,
		FieldMDP: FactAbsent, UsageRights: FactAbsent, ReaderExtensions: FactAbsent,
	}
	facts.AcroForm = AcroFormFacts{State: FactPresent, FieldCount: testKnownInteger(1)}
	facts.XFA = XFAFacts{
		State: FactAbsent, Representation: StringFact{State: FactAbsent}, Rendering: StringFact{State: FactAbsent},
	}
	return facts
}

func testKnownInteger(value int) IntegerFact {
	return IntegerFact{State: FactPresent, Value: &value}
}

func assertWorkerFailureWithInput(t *testing.T, result WorkerResult, state State, code FailureCode) {
	t.Helper()
	if result.State != state || result.Input == nil || result.Failure == nil || result.Failure.Code != code {
		t.Fatalf("worker result = %#v, want state %q and code %q", result, state, code)
	}
}
