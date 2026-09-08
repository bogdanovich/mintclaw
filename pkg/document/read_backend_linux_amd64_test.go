//go:build linux && amd64

package document

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadBackendFixtureOutcomes(t *testing.T) {
	if !readBackendAvailable() {
		t.Skip("pinned Poppler 24.02.0 backend is unavailable")
	}
	tests := []struct {
		name          string
		file          string
		operation     string
		pages         []int
		markers       []string
		state         State
		failure       FailureCode
		truncated     bool
		width         int
		height        int
		maxCharacters int
	}{
		{
			name: "mixed", file: "mixed-pages.pdf", operation: operationExtract, pages: []int{1, 2},
			markers: []string{"MintClaw mixed text page"}, state: StateSucceeded,
		},
		{
			name: "mixed image-only page", file: "mixed-pages.pdf", operation: operationExtract, pages: []int{2},
			state: StateUnsupported, failure: FailureTextUnavailable,
		},
		{
			name: "unicode", file: "unicode.pdf", operation: operationExtract, pages: []int{1},
			markers: []string{"MintClaw Café résumé"}, state: StateSucceeded,
		},
		{
			name: "ambiguous order", file: "ambiguous-reading-order.pdf", operation: operationExtract, pages: []int{1},
			markers: []string{"MINTCLAW_FIRST_VISUAL", "MINTCLAW_SECOND_VISUAL"}, state: StateSucceeded,
		},
		{
			name: "AcroForm appearance", file: "acroform.pdf", operation: operationExtract, pages: []int{1},
			markers: []string{"Synthetic AcroForm"}, state: StateSucceeded,
		},
		{
			name: "signed classification", file: "signed-certified.pdf", operation: operationExtract, pages: []int{1},
			markers: []string{"Synthetic signed fixture"}, state: StateSucceeded,
		},
		{
			name: "character truncation", file: "excessive-text.pdf", operation: operationExtract, pages: []int{1},
			markers: []string{"MINTCLAW_EXCESSIVE_TEXT"}, state: StateSucceeded, truncated: true,
			maxCharacters: 64,
		},
		{
			name: "scan render", file: "image-only.pdf", operation: operationRender, pages: []int{1},
			state: StateSucceeded, width: 1_224, height: 1_584,
		},
		{
			name: "rotated crop render", file: "rotated-crop.pdf", operation: operationRender, pages: []int{1},
			state: StateSucceeded, width: 792, height: 612,
		},
		{
			name: "extreme dimension render", file: "extreme-dimensions.pdf", operation: operationRender,
			pages: []int{1}, state: StateFailed, failure: FailureRenderLimit,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := directTempDir(t)
			input := filepath.Join("testdata", test.file)
			options := ReadOptions{
				Acquire: AcquireOptions{ScratchRoot: filepath.Join(root, "protected")},
				Pages:   test.pages,
				Limits:  ReadLimits{MaxCharacters: test.maxCharacters},
			}
			worker := testProcessWorker("serve")
			worker.timeout = 20 * time.Second
			var snapshot *Snapshot
			var report Report
			if test.operation == operationExtract {
				snapshot, report = readWithWorkers(
					t.Context(), input, options, "linux", "amd64", operationExtract,
					worker, worker, nil,
				)
			} else {
				snapshot, report = readWithWorkers(
					t.Context(), input, options, "linux", "amd64", operationRender,
					worker, nil, worker,
				)
			}
			if report.State != test.state {
				t.Fatalf("report state = %q: %#v", report.State, report.Failure)
			}
			if test.failure != "" {
				if report.Failure == nil || report.Failure.Code != test.failure || snapshot != nil {
					t.Fatalf("failure report = %#v snapshot=%#v", report, snapshot)
				}
				return
			}
			if snapshot == nil || len(report.Artifacts) == 0 {
				t.Fatalf("success report = %#v snapshot=%#v", report, snapshot)
			}
			if test.operation == operationExtract {
				reader, err := snapshot.OpenArtifact(report.Artifacts[0].Ref)
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(reader)
				_ = reader.Close()
				if err != nil {
					t.Fatal(err)
				}
				for _, marker := range test.markers {
					if !strings.Contains(string(data), marker) {
						t.Fatalf("artifact lacks marker %q", marker)
					}
				}
				if report.Extraction == nil || report.Extraction.Truncated != test.truncated {
					t.Fatalf("extraction facts = %#v", report.Extraction)
				}
			} else if report.Rendering == nil || report.Rendering.Pages[0].Width != test.width ||
				report.Rendering.Pages[0].Height != test.height {
				t.Fatalf("rendering facts = %#v", report.Rendering)
			}
			if err := snapshot.Close(); err != nil {
				t.Fatal(err)
			}
			if entries, err := os.ReadDir(filepath.Join(root, "protected")); err != nil || len(entries) != 0 {
				t.Fatalf("protected scratch was retained: %v, %v", entries, err)
			}
		})
	}
}

func TestBoundExtractedPageTextDoesNotTruncateExactFinalPage(t *testing.T) {
	const text = "123456789012345678901"
	bounded, characters, truncated := boundExtractedPageText(text, 21, false)
	if bounded != text || characters != 21 || truncated {
		t.Fatalf("exact final page = %q, %d, %t", bounded, characters, truncated)
	}
	if _, _, truncated = boundExtractedPageText(text, 21, true); !truncated {
		t.Fatal("exact budget with a later selected page was not marked truncated")
	}
	bounded, characters, truncated = boundExtractedPageText(text+"2", 21, false)
	if bounded != text || characters != 21 || !truncated {
		t.Fatalf("over-budget page = %q, %d, %t", bounded, characters, truncated)
	}
}
