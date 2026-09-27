//go:build linux && amd64

package document

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUnsafeHybridFieldsReturnTypedRefusalThroughProcess(t *testing.T) {
	for _, fixture := range []string{"hybrid-xfa-page-growth.pdf", "hybrid-xfa-malformed.pdf"} {
		t.Run(fixture, func(t *testing.T) {
			root := directTempDir(t)
			input := filepath.Join(root, fixture)
			data, err := os.ReadFile(filepath.Join("testdata", fixture))
			if err != nil {
				t.Fatal(err)
			}
			writeFixture(t, input, data)
			worker := testProcessWorker("serve")
			worker.timeout = 20 * time.Second
			snapshot, report := fieldsWithWorker(
				t.Context(), input, AcquireOptions{ScratchRoot: filepath.Join(root, "scratch")},
				"linux", "amd64", worker,
			)
			if snapshot != nil || report.State != StateUnsupported || report.Failure == nil ||
				report.Failure.Code != FailureFormUnsupported || report.Inspection == nil ||
				report.FormEligibility == nil || report.FormEligibility.State != FormBlocked {
				t.Fatalf("typed hybrid refusal = %#v, snapshot = %#v", report, snapshot)
			}
		})
	}
}
