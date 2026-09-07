package document

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
