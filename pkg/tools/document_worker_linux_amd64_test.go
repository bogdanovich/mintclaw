//go:build linux && amd64

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/document"
)

// TestMain lets production-style document tool tests exercise the same
// one-shot worker protocol as the mintclaw executable.
func TestMain(main *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "document" && os.Args[2] == "_worker" {
		input := os.NewFile(document.WorkerInputFileDescriptor(), "document-snapshot")
		if input == nil {
			os.Exit(1)
		}
		err := document.ServeWorker(os.Stdin, input, os.Stdout)
		_ = input.Close()
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(main.Run())
}

func TestDocumentFormEvidenceUsesRetainedSourceWithoutDurableRawEvidence(t *testing.T) {
	if document.Capabilities().Operations["extract"].State != document.CapabilitySupported {
		if os.Getenv("MINTCLAW_REQUIRE_DOCUMENT_AGENT_E2E") == "1" {
			t.Fatal("required document read backend is unavailable")
		}
		t.Skip("document read backend is unavailable")
	}
	source, err := os.ReadFile(filepath.Join("..", "document", "testdata", "excessive-text.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	tool, job, _ := newDocumentDialogueJobWithSource(t, 2, document.FormFieldText, source)
	ctx := workflowToolContext(t, "retained-evidence", "retained-evidence-call", nil)
	result := tool.Execute(ctx, map[string]any{
		"action": "form", "form_action": "evidence", "job_id": job.JobID, "pages": []int{1},
	})
	view := decodeWorkflowResult(t, result.ForLLM)
	if result.IsError || view.Job == nil || view.Job.JobID != job.JobID || view.Evidence == nil ||
		view.Evidence.Source == nil || view.Evidence.Source.SHA256 != job.SourceDigest ||
		!strings.Contains(result.ContextText, "MINTCLAW_EXCESSIVE_TEXT") ||
		strings.Contains(result.ForLLM, "MINTCLAW_EXCESSIVE_TEXT") ||
		utf8.RuneCountInString(result.ContextText) > documentFormEvidenceTextLimit+512 ||
		len(result.ContextMedia) != 0 || len(result.Media) != 0 ||
		len(view.Mapping.CandidateFields) > documentFormCandidateLimit {
		t.Fatalf("retained text evidence = %#v", result)
	}
	if len(view.Evidence.Artifacts) != 1 || !view.Evidence.Artifacts[0].Truncated {
		t.Fatalf("read truncation was not reported: %#v", view.Evidence)
	}
	followup, err := tool.ToolResultFollowup(result)
	if err != nil || followup == nil || followup.ResponseOnly {
		t.Fatalf("read evidence lost the question fence: %#v, %v", followup, err)
	}
	rendered := tool.Execute(ctx, map[string]any{
		"action": "form", "form_action": "evidence", "job_id": job.JobID, "pages": []int{1}, "evidence_mode": "render",
	})
	renderView := decodeWorkflowResult(t, rendered.ForLLM)
	if rendered.IsError || renderView.Evidence == nil || len(renderView.Evidence.Artifacts) != 1 ||
		len(rendered.ContextMedia) != 1 || len(rendered.Media) != 0 {
		t.Fatalf("retained render evidence = %#v", rendered)
	}
	image := renderView.Evidence.Artifacts[0]
	if max(image.Width, image.Height) > documentFormEvidenceRenderEdge || image.Width <= 0 || image.Height <= 0 {
		t.Fatalf("planning render exceeds fixed bounds: %#v", image)
	}
}
