package document

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	documentpkg "github.com/bogdanovich/mintclaw/pkg/document"
)

func TestCapabilitiesCommandWritesStableJSON(t *testing.T) {
	deps := commandDeps{
		capabilities: func() documentpkg.CapabilityReport {
			return documentpkg.CapabilityReport{
				SchemaVersion: documentpkg.CapabilitySchemaVersion,
				Platform:      "linux",
				Architecture:  "amd64",
				Operations: map[string]documentpkg.OperationCapability{
					"acquire": {State: documentpkg.CapabilitySupported},
				},
				Limits: documentpkg.Limits{MaxInputBytes: documentpkg.DefaultMaxInputBytes},
			}
		},
		acquire:     documentpkg.Acquire,
		scratchRoot: t.TempDir,
	}
	cmd := newDocumentCommand(deps)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"capabilities", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute capabilities: %v", err)
	}
	var report documentpkg.CapabilityReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output.String())
	}
	if report.SchemaVersion != documentpkg.CapabilitySchemaVersion || report.Platform != "linux" {
		t.Fatalf("report = %#v", report)
	}
}

func TestAcquireCommandWritesReportAndMapsExitClass(t *testing.T) {
	now := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	success := documentpkg.Report{
		SchemaVersion: documentpkg.ReportSchemaVersion,
		OperationID:   "document_operation_test",
		Operation:     "acquire",
		State:         documentpkg.StateSucceeded,
		Input: &documentpkg.DocumentRef{
			Ref: "document://local/test", OriginalFilename: "sample.pdf",
			ContentType: "application/pdf", Size: 12, SHA256: "abc",
			Authority: documentpkg.Authority{Kind: "local_operator"}, SourceKind: "local_file",
			CreatedAt: now, CleanupPolicy: "delete_on_operation_close",
		},
		Limits: documentpkg.Limits{MaxInputBytes: documentpkg.DefaultMaxInputBytes},
	}
	tests := []struct {
		name     string
		report   documentpkg.Report
		wantCode int
	}{
		{name: "success", report: success},
		{
			name: "unavailable",
			report: documentpkg.Report{
				SchemaVersion: documentpkg.ReportSchemaVersion, OperationID: "operation_unavailable",
				Operation: "acquire", State: documentpkg.StateUnavailable,
				Failure: &documentpkg.Failure{
					Code: documentpkg.FailureUnsupportedPlatform, Message: "not admitted",
				},
			},
			wantCode: 3,
		},
		{
			name: "invalid",
			report: documentpkg.Report{
				SchemaVersion: documentpkg.ReportSchemaVersion, OperationID: "operation_invalid",
				Operation: "acquire", State: documentpkg.StateFailed,
				Failure: &documentpkg.Failure{Code: documentpkg.FailureInvalidInput, Message: "invalid"},
			},
			wantCode: 4,
		},
		{
			name: "unsupported input type",
			report: documentpkg.Report{
				SchemaVersion: documentpkg.ReportSchemaVersion, OperationID: "operation_unsupported_type",
				Operation: "acquire", State: documentpkg.StateUnsupported,
				Failure: &documentpkg.Failure{
					Code: documentpkg.FailureUnsupportedType, Message: "input is not a PDF document",
				},
			},
			wantCode: 4,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := commandDeps{
				capabilities: documentpkg.Capabilities,
				acquire: func(
					context.Context,
					string,
					documentpkg.AcquireOptions,
				) (*documentpkg.Snapshot, documentpkg.Report) {
					return nil, test.report
				},
				scratchRoot: t.TempDir,
			}
			cmd := newDocumentCommand(deps)
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{"acquire", "--input", "/not/exposed.pdf", "--json"})
			err := cmd.Execute()
			if test.wantCode == 0 && err != nil {
				t.Fatalf("execute acquire: %v", err)
			}
			if test.wantCode != 0 {
				var exitErr *ExitError
				if !errors.As(err, &exitErr) || exitErr.Code != test.wantCode {
					t.Fatalf("error = %#v, want exit code %d", err, test.wantCode)
				}
			}
			var got documentpkg.Report
			if err := json.Unmarshal(output.Bytes(), &got); err != nil {
				t.Fatalf("decode output: %v\n%s", err, output.String())
			}
			if got.State != test.report.State || bytes.Contains(output.Bytes(), []byte("/not/exposed.pdf")) {
				t.Fatalf("output = %s", output.String())
			}
		})
	}
}

func TestInspectCommandWritesStableReportAndMapsExitClass(t *testing.T) {
	pageCount := 2
	fieldCount := 1
	success := documentpkg.Report{
		SchemaVersion: documentpkg.ReportSchemaVersion,
		OperationID:   "document_operation_inspect",
		Operation:     "inspect",
		State:         documentpkg.StateSucceeded,
		Input: &documentpkg.DocumentRef{
			Ref: "document://local/inspect", OriginalFilename: "sample.pdf",
			ContentType: "application/pdf", Size: 12, SHA256: "abc",
			Authority: documentpkg.Authority{Kind: "local_operator"}, SourceKind: "local_file",
			CleanupPolicy: "delete_on_operation_close",
		},
		Limits: documentpkg.Limits{
			MaxInputBytes:     documentpkg.DefaultMaxInputBytes,
			MaxPages:          documentpkg.DefaultMaxPages,
			MaxContentBytes:   documentpkg.DefaultMaxContentBytes,
			MaxObjects:        documentpkg.DefaultMaxObjects,
			MaxRecursionDepth: documentpkg.DefaultMaxRecursionDepth,
		},
		Inspection: &documentpkg.InspectionFacts{
			Backend:    documentpkg.BackendIdentity{Name: "pdfcpu", Version: "v0.15.0", Role: "production"},
			PDFVersion: documentpkg.StringFact{State: documentpkg.FactPresent, Value: "1.7"},
			PageCount:  documentpkg.IntegerFact{State: documentpkg.FactPresent, Value: &pageCount},
			Encryption: documentpkg.EncryptionFacts{State: documentpkg.FactAbsent},
			Signatures: documentpkg.SignatureFacts{State: documentpkg.FactAbsent},
			AcroForm: documentpkg.AcroFormFacts{
				State: documentpkg.FactPresent, FieldCount: documentpkg.IntegerFact{
					State: documentpkg.FactPresent, Value: &fieldCount,
				},
			},
			XFA:             documentpkg.XFAFacts{State: documentpkg.FactAbsent},
			ExtractableText: documentpkg.TextFacts{State: documentpkg.FactMixed},
		},
	}
	tests := []struct {
		name     string
		report   documentpkg.Report
		wantCode int
	}{
		{name: "success", report: success},
		{
			name: "password required",
			report: documentpkg.Report{
				SchemaVersion: documentpkg.ReportSchemaVersion, OperationID: "operation_password",
				Operation: "inspect", State: documentpkg.StateUnsupported,
				Failure: &documentpkg.Failure{
					Code: documentpkg.FailurePasswordRequired, Message: "password required",
				},
			},
			wantCode: 3,
		},
		{
			name: "malformed",
			report: documentpkg.Report{
				SchemaVersion: documentpkg.ReportSchemaVersion, OperationID: "operation_malformed",
				Operation: "inspect", State: documentpkg.StateFailed,
				Failure: &documentpkg.Failure{Code: documentpkg.FailureMalformedPDF, Message: "malformed"},
			},
			wantCode: 4,
		},
		{
			name: "limit",
			report: documentpkg.Report{
				SchemaVersion: documentpkg.ReportSchemaVersion, OperationID: "operation_limit",
				Operation: "inspect", State: documentpkg.StateFailed,
				Failure: &documentpkg.Failure{Code: documentpkg.FailureInspectionLimit, Message: "limit"},
			},
			wantCode: 5,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := commandDeps{
				capabilities: documentpkg.Capabilities,
				inspect: func(
					context.Context,
					string,
					documentpkg.AcquireOptions,
				) (*documentpkg.Snapshot, documentpkg.Report) {
					return nil, test.report
				},
				scratchRoot: t.TempDir,
			}
			cmd := newDocumentCommand(deps)
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{"inspect", "--input", "/not/exposed.pdf", "--json"})
			err := cmd.Execute()
			if test.wantCode == 0 && err != nil {
				t.Fatalf("execute inspect: %v", err)
			}
			if test.wantCode != 0 {
				var exitErr *ExitError
				if !errors.As(err, &exitErr) || exitErr.Code != test.wantCode {
					t.Fatalf("error = %#v, want exit code %d", err, test.wantCode)
				}
			}
			var got documentpkg.Report
			if err := json.Unmarshal(output.Bytes(), &got); err != nil {
				t.Fatalf("decode output: %v\n%s", err, output.String())
			}
			if got.State != test.report.State || bytes.Contains(output.Bytes(), []byte("/not/exposed.pdf")) {
				t.Fatalf("output = %s", output.String())
			}
		})
	}
}

func TestFieldsCommandWritesStableReportAndMapsExitClass(t *testing.T) {
	success := documentpkg.Report{
		SchemaVersion: documentpkg.ReportSchemaVersion,
		OperationID:   "document_operation_fields",
		Operation:     "fields",
		State:         documentpkg.StateSucceeded,
		Input: &documentpkg.DocumentRef{
			Ref: "document://local/fields", OriginalFilename: "sample.pdf",
			ContentType: "application/pdf", Size: 12, SHA256: "abc",
			Authority: documentpkg.Authority{Kind: "local_operator"}, SourceKind: "local_file",
			CleanupPolicy: "delete_on_operation_close",
		},
		Fields: &documentpkg.FormFieldsFacts{
			Backend: documentpkg.BackendIdentity{
				Name: "pdfcpu", Version: "v0.15.0", Role: "production", IsolationMode: "one_shot_process",
			},
			Limits: documentpkg.FormFieldLimits{
				MaxFields: documentpkg.DefaultMaxFormFields, MaxWidgets: documentpkg.DefaultMaxFieldWidgets,
				MaxOptions: documentpkg.DefaultMaxFieldOptions, MaxTextBytes: documentpkg.DefaultMaxFieldTextBytes,
				MaxReportBytes: documentpkg.DefaultMaxFormReportBytes,
			},
			Fields: []documentpkg.FormField{{
				ID:   "field_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				Name: "full_name", Kind: documentpkg.FormFieldText,
				Widgets: []documentpkg.FormFieldWidget{{
					ID:   "widget_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
					Page: 1, Ordinal: 1,
				}},
			}},
		},
	}
	tests := []struct {
		name     string
		report   documentpkg.Report
		wantCode int
	}{
		{name: "success", report: success},
		{
			name: "no form",
			report: documentpkg.Report{
				SchemaVersion: documentpkg.ReportSchemaVersion, OperationID: "operation_no_form",
				Operation: "fields", State: documentpkg.StateUnsupported,
				Failure: &documentpkg.Failure{
					Code: documentpkg.FailureFormNotPresent, Message: "no form",
				},
			},
			wantCode: 4,
		},
		{
			name: "unsupported form",
			report: documentpkg.Report{
				SchemaVersion: documentpkg.ReportSchemaVersion, OperationID: "operation_unsupported_form",
				Operation: "fields", State: documentpkg.StateUnsupported,
				Failure: &documentpkg.Failure{
					Code: documentpkg.FailureFormUnsupported, Message: "unsupported form",
				},
			},
			wantCode: 4,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := commandDeps{
				capabilities: documentpkg.Capabilities,
				fields: func(
					context.Context,
					string,
					documentpkg.AcquireOptions,
				) (*documentpkg.Snapshot, documentpkg.Report) {
					return nil, test.report
				},
				scratchRoot: t.TempDir,
			}
			cmd := newDocumentCommand(deps)
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{"fields", "--input", "/not/exposed.pdf", "--json"})
			err := cmd.Execute()
			if test.wantCode == 0 && err != nil {
				t.Fatalf("execute fields: %v", err)
			}
			if test.wantCode != 0 {
				var exitErr *ExitError
				if !errors.As(err, &exitErr) || exitErr.Code != test.wantCode {
					t.Fatalf("error = %#v, want exit code %d", err, test.wantCode)
				}
			}
			var got documentpkg.Report
			if err := json.Unmarshal(output.Bytes(), &got); err != nil {
				t.Fatalf("decode output: %v\n%s", err, output.String())
			}
			if got.State != test.report.State || bytes.Contains(output.Bytes(), []byte("/not/exposed.pdf")) {
				t.Fatalf("output = %s", output.String())
			}
		})
	}
}

func TestFillCommandForwardsPrivateMapWithoutEchoingIt(t *testing.T) {
	root := t.TempDir()
	fieldsPath := filepath.Join(root, "private-fill-map.json")
	privateValue := "private CLI value"
	if err := os.WriteFile(fieldsPath, []byte(`{
  "schema_version":"mintclaw.document_fill_map.v1",
  "assignments":[{
    "field_id":"field_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
    "value":{"type":"text","text":"private CLI value"}
  }]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotFill documentpkg.FillMap
	var gotOptions documentpkg.FormWriteOptions
	deps := commandDeps{
		fill: func(
			_ context.Context,
			_ string,
			fill documentpkg.FillMap,
			options documentpkg.FormWriteOptions,
		) (*documentpkg.Snapshot, documentpkg.Report) {
			gotFill = fill
			gotOptions = options
			return nil, documentpkg.Report{
				SchemaVersion: documentpkg.ReportSchemaVersion,
				OperationID:   "document_operation_invalid_fill",
				Operation:     "fill",
				State:         documentpkg.StateFailed,
				Failure: &documentpkg.Failure{
					Code: documentpkg.FailureFieldValueInvalid, Message: "document fill field value is invalid",
				},
			}
		},
		scratchRoot: func() string { return filepath.Join(root, "scratch") },
		writeRoot:   func() string { return filepath.Join(root, "writes") },
	}
	cmd := newDocumentCommand(deps)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"fill",
		"--input", "/not/exposed.pdf",
		"--fields", fieldsPath,
		"--output", filepath.Join(root, "must-not-exist.pdf"),
		"--operation-id", "document_write_0123456789abcdef0123456789abcdef",
		"--json",
	})
	err := cmd.Execute()
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 4 {
		t.Fatalf("fill error = %#v", err)
	}
	if len(gotFill.Assignments) != 1 || gotFill.Assignments[0].Value.Text == nil ||
		*gotFill.Assignments[0].Value.Text != privateValue ||
		gotOptions.OperationID != "document_write_0123456789abcdef0123456789abcdef" ||
		gotOptions.Acquire.ScratchRoot != filepath.Join(root, "scratch") ||
		gotOptions.StateRoot != filepath.Join(root, "writes") {
		t.Fatalf("fill = %#v, options=%#v", gotFill, gotOptions)
	}
	if strings.Contains(output.String(), privateValue) || strings.Contains(output.String(), root) ||
		strings.Contains(output.String(), "/not/exposed.pdf") {
		t.Fatalf("fill output leaked protected data: %s", output.String())
	}
}

func TestFormCommandsRejectInvalidPrivateJSONBeforeCallingService(t *testing.T) {
	root := t.TempDir()
	invalidPath := filepath.Join(root, "invalid.json")
	if err := os.WriteFile(invalidPath, []byte(`{"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fillCalls := 0
	verifyCalls := 0
	deps := commandDeps{
		fill: func(
			context.Context,
			string,
			documentpkg.FillMap,
			documentpkg.FormWriteOptions,
		) (*documentpkg.Snapshot, documentpkg.Report) {
			fillCalls++
			return nil, documentpkg.Report{}
		},
		verify: func(
			context.Context,
			string,
			documentpkg.FormWriteExpectation,
			documentpkg.FormWriteOptions,
		) (*documentpkg.Snapshot, documentpkg.Report) {
			verifyCalls++
			return nil, documentpkg.Report{}
		},
		scratchRoot: func() string { return filepath.Join(root, "scratch") },
		writeRoot:   func() string { return filepath.Join(root, "writes") },
	}
	for _, args := range [][]string{
		{"fill", "--input", "input.pdf", "--fields", invalidPath, "--output", "output.pdf"},
		{"verify", "--input", "output.pdf", "--expect", invalidPath},
	} {
		cmd := newDocumentCommand(deps)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		err := cmd.Execute()
		var exitErr *ExitError
		if !errors.As(err, &exitErr) || exitErr.Code != 4 {
			t.Fatalf("args=%v error=%#v", args, err)
		}
	}
	if fillCalls != 0 || verifyCalls != 0 {
		t.Fatalf("invalid JSON reached services: fill=%d verify=%d", fillCalls, verifyCalls)
	}
}

func TestPrivateWorkerCommandIsHiddenAndUsesInheritedInput(t *testing.T) {
	request := documentpkg.WorkerRequest{
		SchemaVersion: documentpkg.WorkerRequestSchemaVersion,
		OperationID:   "document_operation_cli_worker",
		Operation:     "verify_snapshot",
		Input: documentpkg.WorkerInput{
			ContentType: "application/pdf",
			Size:        4,
			SHA256:      "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
		Limits: documentpkg.Limits{
			MaxInputBytes:     documentpkg.DefaultMaxInputBytes,
			MaxPages:          documentpkg.DefaultMaxPages,
			MaxContentBytes:   documentpkg.DefaultMaxContentBytes,
			MaxObjects:        documentpkg.DefaultMaxObjects,
			MaxRecursionDepth: documentpkg.DefaultMaxRecursionDepth,
		},
	}
	requestBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var gotSnapshot string
	deps := commandDeps{
		serveWorker: func(requestReader io.Reader, snapshotReader io.Reader, output io.Writer) error {
			data, readErr := io.ReadAll(snapshotReader)
			if readErr != nil {
				return readErr
			}
			gotSnapshot = string(data)
			_, readErr = io.Copy(output, requestReader)
			return readErr
		},
		workerInput: func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte("%PDF"))), nil
		},
	}
	cmd := newDocumentCommand(deps)
	worker, _, err := cmd.Find([]string{"_worker"})
	if err != nil {
		t.Fatalf("find worker command: %v", err)
	}
	if !worker.Hidden {
		t.Fatal("private document worker is visible in CLI help")
	}
	var output bytes.Buffer
	cmd.SetIn(bytes.NewReader(requestBytes))
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"_worker"})
	if err = cmd.Execute(); err != nil {
		t.Fatalf("execute private worker: %v", err)
	}
	if gotSnapshot != "%PDF" || !bytes.Equal(output.Bytes(), requestBytes) {
		t.Fatalf("snapshot = %q, output = %s", gotSnapshot, output.Bytes())
	}
}
