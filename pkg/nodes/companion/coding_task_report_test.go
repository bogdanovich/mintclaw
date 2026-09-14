package companion

import (
	"encoding/json"
	"strings"
	"testing"

	codingtask "github.com/bogdanovich/mintclaw/pkg/coding/task"
	"github.com/bogdanovich/mintclaw/pkg/coding/worker"
	"github.com/bogdanovich/mintclaw/pkg/coding/worktree"
)

func TestCodingTerminalReportExcludesSensitiveWorkerContent(t *testing.T) {
	const secret = "sk-secret-value-1234567890"
	active := &activeCodingTask{reportItems: make(map[string]worker.Item)}
	active.projectReportItem(worker.Item{
		ID: "reasoning", Sequence: 1, Revision: 1,
		Message: &worker.Message{
			Kind: worker.MessageReasoning, Text: "private chain of thought",
		},
	})
	active.projectReportItem(worker.Item{
		ID: "command", Sequence: 2, Revision: 1,
		Tool: &worker.Tool{
			Name: "shell", Arguments: `{"token":"` + secret + `"}`, Output: "repository secret",
			Command: &worker.Command{
				Command: "go test ./... --token " + secret, CWD: "/Users/private/repo",
				Output: "secret output", Status: worker.CommandSucceeded,
			},
		},
	})
	active.projectReportItem(worker.Item{
		ID: "final", Sequence: 3, Revision: 1,
		Message: &worker.Message{
			Kind: worker.MessageAssistant, Phase: worker.AssistantPhaseFinal, Complete: true,
			Text: "Fixed /Users/private/repo/pkg/bug.go with token " + secret + ".",
		},
	})

	report := active.terminalReport(codingTaskProcessResult{
		outcome: codingTaskOutcomeCompleted,
		handoff: &worktree.Handoff{
			Head: "0123456789abcdef", Class: worktree.HandoffChanges,
			Changes: worktree.HandoffChangeset{
				Staged: []worktree.PathChange{{Path: "pkg/bug.go", Status: "M "}},
			},
		},
	})
	if report == nil || len(report.Validations) != 1 || report.Validations[0].Status != "succeeded" ||
		len(report.ChangedPaths) != 1 || report.ChangedPaths[0] != "pkg/bug.go" ||
		report.Commit != "0123456789abcdef" || report.CleanupState != "retained" {
		t.Fatalf("terminal report = %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"private chain of thought",
		"go test ./...",
		"repository secret",
		"secret output",
		"/Users/private/repo",
		secret,
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("terminal report leaked %q: %s", forbidden, encoded)
		}
	}
	if !strings.Contains(report.Summary, "[ABSOLUTE PATH REDACTED]") {
		t.Fatalf("terminal summary did not redact absolute path: %q", report.Summary)
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("TerminalReport.Validate() error = %v", err)
	}
}

func TestCodingTerminalChangedPathsIsBoundedAndDeduplicated(t *testing.T) {
	changes := worktree.HandoffChangeset{
		Staged: []worktree.PathChange{{Path: "a.go", Status: "M "}},
		Unstaged: []worktree.PathChange{
			{Path: "a.go", Status: " M"},
			{Path: "b.go", OriginalPath: "old-b.go", Status: "R "},
		},
	}
	paths, truncated := codingTerminalChangedPaths(changes)
	if truncated || strings.Join(paths, ",") != "a.go,b.go,old-b.go" {
		t.Fatalf("codingTerminalChangedPaths() = %#v, %v", paths, truncated)
	}
	for index := 0; index <= codingtask.MaxTerminalPaths; index++ {
		changes.Untracked = append(changes.Untracked, worktree.PathChange{
			Path: "generated/" + strings.Repeat("x", index+1), Status: "??",
		})
	}
	paths, truncated = codingTerminalChangedPaths(changes)
	if !truncated || len(paths) != codingtask.MaxTerminalPaths {
		t.Fatalf("bounded paths = %d, truncated %v", len(paths), truncated)
	}
}

func TestCodingTerminalReportCapturesRetainedFinalEventAtProcessExit(t *testing.T) {
	process := newHostTestProcess()
	identity := worker.ControlIdentity{
		TaskID: "task-one", TaskGenerationID: "generation-one", WorkerGenerationID: "worker-one",
	}
	process.identity = identity
	active := &activeCodingTask{
		taskID: identity.TaskID, generationID: identity.TaskGenerationID,
		workerID: identity.WorkerGenerationID, process: process,
		reportItems: make(map[string]worker.Item),
	}
	process.emit(t, worker.EventItemUpdated, worker.ItemUpdatedPayload{
		ControlIdentity: identity,
		Item: worker.Item{
			ID: "message:turn-1:item-1", TurnID: "turn-1", Sequence: 1, Revision: 1,
			Message: &worker.Message{
				Kind: worker.MessageAssistant, Phase: worker.AssistantPhaseFinal,
				Complete: true, Text: "Final retained report.",
			},
		},
	})
	active.captureTerminalReportEvents()
	report := active.terminalReport(codingTaskProcessResult{outcome: codingTaskOutcomeCompleted})
	if report.Summary != "Final retained report." {
		t.Fatalf("terminal report = %#v", report)
	}
}

func TestCodingTerminalReportRetainsBoundedNewestItems(t *testing.T) {
	active := &activeCodingTask{reportItems: make(map[string]worker.Item)}
	for index := 0; index < maxRetainedTerminalReportItems+10; index++ {
		active.projectReportItem(worker.Item{
			ID: "tool-" + strings.Repeat("x", index+1), Sequence: uint64(index + 1), Revision: 1,
			Tool: &worker.Tool{Command: &worker.Command{Status: worker.CommandSucceeded}},
		})
	}
	if len(active.reportItems) != maxRetainedTerminalReportItems {
		t.Fatalf("retained report items = %d", len(active.reportItems))
	}
	report := active.terminalReport(codingTaskProcessResult{outcome: codingTaskOutcomeCompleted})
	if len(report.Validations) != codingtask.MaxTerminalValidations || !report.ValidationsTruncated {
		t.Fatalf("bounded validation projection = %#v", report)
	}
}
