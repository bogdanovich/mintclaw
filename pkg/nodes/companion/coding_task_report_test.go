package companion

import (
	"encoding/json"
	"fmt"
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

func TestProjectYoloTerminalReportProjectsVerifiedAndUncertainEffects(t *testing.T) {
	active := &activeCodingTask{
		profile: codingtask.TaskModeProjectYolo, baseGitHead: "1111111111111111111111111111111111111111",
		branch: "mintclaw/project-yolo", reportItems: make(map[string]worker.Item),
	}
	items := []worker.Item{
		{
			ID: "push", Sequence: 1, Revision: 1,
			Tool: &worker.Tool{Command: &worker.Command{
				Command: "git push origin HEAD", Status: worker.CommandSucceeded,
				Output: "remote mentioned https://github.com/example/repository/pull/42",
			}},
		},
		{
			ID: "pr", Sequence: 2, Revision: 1,
			Tool: &worker.Tool{Command: &worker.Command{
				Command: "gh pr create --fill", Status: worker.CommandSucceeded,
				Output: "created https://github.com/example/repository/pull/42",
			}},
		},
		{
			ID: "deploy", Sequence: 3, Revision: 1,
			Tool: &worker.Tool{Command: &worker.Command{
				Command: "fake-deploy production --token secret", Status: worker.CommandRunning,
				Output: "started https://deploy.example/runs/17",
			}},
		},
	}
	for _, item := range items {
		active.projectReportItem(item)
	}
	report := active.terminalReport(codingTaskProcessResult{
		outcome: codingTaskOutcomeCompleted,
		handoff: &worktree.Handoff{
			Head: "2222222222222222222222222222222222222222", Class: worktree.HandoffReady,
		},
	})
	if len(report.ExternalEffects) != 4 || report.EffectsTruncated {
		t.Fatalf("external effects = %#v", report.ExternalEffects)
	}
	want := []codingtask.ExternalEffectReceipt{
		{
			Kind: codingtask.ExternalEffectCommit, Outcome: codingtask.ExternalEffectVerified,
			Reference: "2222222222222222222222222222222222222222",
		},
		{
			Kind: codingtask.ExternalEffectPush, Outcome: codingtask.ExternalEffectVerified,
			Reference: "mintclaw/project-yolo@222222222222",
		},
		{
			Kind: codingtask.ExternalEffectPullRequest, Outcome: codingtask.ExternalEffectVerified,
			Reference: "https://github.com/example/repository/pull/42",
		},
		{
			Kind: codingtask.ExternalEffectDeployment, Outcome: codingtask.ExternalEffectUncertain,
			Reference: "https://deploy.example/runs/17",
		},
	}
	if fmt.Sprint(report.ExternalEffects) != fmt.Sprint(want) ||
		report.Unresolved != "one or more external effects require operator verification" {
		t.Fatalf("terminal report = %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"git push", "gh pr create", "fake-deploy", "--token", "secret"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("external-effect report leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestMachineYoloTerminalReportStatesNoRollbackAndProjectsMachineEffects(t *testing.T) {
	active := &activeCodingTask{
		profile: codingtask.TaskModeMachineYolo, reportItems: make(map[string]worker.Item),
	}
	commands := []string{
		"git init project",
		"pipx install demo-tool",
		"systemctl --user start demo.service",
		"nohup demo-worker &",
	}
	for index, command := range commands {
		output := "completed"
		if index == 1 {
			output = "downloaded https://downloads.example/demo"
		}
		active.projectReportItem(worker.Item{
			ID: fmt.Sprintf("machine-%d", index), Sequence: uint64(index + 1), Revision: 1,
			Tool: &worker.Tool{Command: &worker.Command{
				Command: command, Status: worker.CommandSucceeded,
				Output: output,
			}},
		})
	}
	report := active.terminalReport(codingTaskProcessResult{outcome: codingTaskOutcomeCompleted})
	want := []codingtask.ExternalEffectReceipt{
		{
			Kind:      codingtask.ExternalEffectRepository,
			Outcome:   codingtask.ExternalEffectVerified,
			Reference: "repository",
		},
		{Kind: codingtask.ExternalEffectPackage, Outcome: codingtask.ExternalEffectVerified, Reference: "package"},
		{Kind: codingtask.ExternalEffectService, Outcome: codingtask.ExternalEffectVerified, Reference: "service"},
		{Kind: codingtask.ExternalEffectProcess, Outcome: codingtask.ExternalEffectUncertain, Reference: "process"},
	}
	if fmt.Sprint(report.ExternalEffects) != fmt.Sprint(want) ||
		report.RollbackState != codingtask.RollbackUnavailable || report.CleanupState != "not_applicable" ||
		!strings.Contains(report.Unresolved, "require operator verification") {
		t.Fatalf("machine-yolo terminal report = %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"git init", "pipx", "systemctl", "nohup", "downloads.example"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("machine-effect report leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestProjectYoloCompoundEffectOutcomesFailClosed(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		status    worker.CommandStatus
		wantKinds int
		want      codingtask.ExternalEffectOutcome
	}{
		{
			name: "or can skip push", command: "true || git push origin HEAD",
			status: worker.CommandSucceeded, wantKinds: 1, want: codingtask.ExternalEffectUncertain,
		},
		{
			name: "and can fail after push", command: "git push origin HEAD && false",
			status: worker.CommandFailed, wantKinds: 1, want: codingtask.ExternalEffectUncertain,
		},
		{
			name: "pipeline reports last process", command: "git push origin HEAD | true",
			status: worker.CommandSucceeded, wantKinds: 1, want: codingtask.ExternalEffectUncertain,
		},
		{
			name: "semicolon reports last process", command: "git push origin HEAD; true",
			status: worker.CommandSucceeded, wantKinds: 1, want: codingtask.ExternalEffectUncertain,
		},
		{
			name:    "single quoted backslash cannot hide following effect",
			command: `printf x 'abc\'; git push origin HEAD`,
			status:  worker.CommandSucceeded, wantKinds: 1, want: codingtask.ExternalEffectUncertain,
		},
		{
			name: "background effect has no terminal outcome", command: "git push origin HEAD &",
			status: worker.CommandSucceeded, wantKinds: 1, want: codingtask.ExternalEffectUncertain,
		},
		{
			name: "malformed trailing operator cannot prove execution", command: "git push origin HEAD &&",
			status: worker.CommandFailed, wantKinds: 1, want: codingtask.ExternalEffectUncertain,
		},
		{
			name: "successful and chain proves every segment", command: "git push origin HEAD && gh pr create --fill",
			status: worker.CommandSucceeded, wantKinds: 2, want: codingtask.ExternalEffectVerified,
		},
		{
			name: "single failed effect is known", command: "git push origin HEAD",
			status: worker.CommandFailed, wantKinds: 1, want: codingtask.ExternalEffectFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projection := projectExternalEffectCommand(test.command)
			if len(projection.kinds) != test.wantKinds || projection.outcome(test.status) != test.want {
				t.Fatalf("projection = %#v, outcome %q", projection, projection.outcome(test.status))
			}
		})
	}
}

func TestProjectYoloReceiptCapPreservesOmittedUncertainty(t *testing.T) {
	active := &activeCodingTask{
		profile: codingtask.TaskModeProjectYolo, branch: "mintclaw/project-yolo",
		reportItems: make(map[string]worker.Item),
	}
	for index := 0; index <= codingtask.MaxTerminalEffects; index++ {
		status := worker.CommandSucceeded
		if index == codingtask.MaxTerminalEffects {
			status = worker.CommandRunning
		}
		active.projectReportItem(worker.Item{
			ID: fmt.Sprintf("pr-%02d", index), Sequence: uint64(index + 1), Revision: 1,
			Tool: &worker.Tool{Command: &worker.Command{
				Command: "gh pr create --fill", Status: status,
				Output: fmt.Sprintf("https://github.com/example/repository/pull/%d", index+1),
			}},
		})
	}
	report := active.terminalReport(codingTaskProcessResult{outcome: codingTaskOutcomeCompleted})
	if len(report.ExternalEffects) != codingtask.MaxTerminalEffects || !report.EffectsTruncated ||
		!strings.Contains(report.Unresolved, "require operator verification") {
		t.Fatalf("truncated external effects lost uncertainty: %#v", report)
	}
}

func TestCodingTerminalReportByteBoundMarksEffectsTruncated(t *testing.T) {
	report := &codingtask.TerminalReport{Summary: strings.Repeat("s", codingtask.MaxTerminalSummaryBytes)}
	for index := 0; index < codingtask.MaxTerminalEffects; index++ {
		report.ExternalEffects = append(report.ExternalEffects, codingtask.ExternalEffectReceipt{
			Kind: codingtask.ExternalEffectDeployment, Outcome: codingtask.ExternalEffectVerified,
			Reference: fmt.Sprintf("https://deploy.example/runs/%02d/%s", index, strings.Repeat("x", 985)),
		})
	}
	boundCodingTerminalReport(report)
	if !report.EffectsTruncated || len(report.ExternalEffects) >= codingtask.MaxTerminalEffects ||
		report.Validate() != nil {
		t.Fatalf(
			"byte-bounded external effects = %d, truncated %v, validation %v",
			len(report.ExternalEffects),
			report.EffectsTruncated,
			report.Validate(),
		)
	}
}

func TestMutationTerminalReportDoesNotClaimExternalEffects(t *testing.T) {
	active := &activeCodingTask{
		profile: codingtask.TaskModeMutate, branch: "mintclaw/mutate",
		reportItems: make(map[string]worker.Item),
	}
	active.projectReportItem(worker.Item{
		ID: "push", Sequence: 1, Revision: 1,
		Tool: &worker.Tool{Command: &worker.Command{
			Command: "git push origin HEAD", Status: worker.CommandSucceeded,
		}},
	})
	report := active.terminalReport(codingTaskProcessResult{outcome: codingTaskOutcomeCompleted})
	if len(report.ExternalEffects) != 0 {
		t.Fatalf("mutate report claimed project-yolo effects: %#v", report.ExternalEffects)
	}
}

func TestExternalEffectReferenceRejectsCredentialBearingURL(t *testing.T) {
	const secret = "sk-secret-value-1234567890"
	command := &worker.Command{
		Output: "created https://deploy.example/runs/" + secret,
	}
	if reference := firstSafeExternalEffectURL(command); reference != "" {
		t.Fatalf("credential-bearing external-effect URL was retained: %q", reference)
	}
}

func TestSafeCodingTerminalSummaryRedactsCommonAbsolutePathForms(t *testing.T) {
	tests := map[string]string{
		"assignment":        "cwd=/Users/name/repo/pkg/file.go",
		"colon":             "path:/private/repo/pkg/file.go",
		"brackets":          "opened [/home/name/repo/pkg/file.go]",
		"windows slash":     "cwd=C:/Users/name/repo/pkg/file.go",
		"windows backslash": `cwd=C:\Users\name\repo\pkg\file.go`,
		"windows UNC":       `cwd=\\server\share\private\repo`,
		"windows extended":  `opened [\\?\C:\private\repo]`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			summary, _ := safeCodingTerminalSummary(input)
			if !strings.Contains(summary, "[ABSOLUTE PATH REDACTED]") ||
				strings.Contains(summary, "Users/name") || strings.Contains(summary, `Users\name`) ||
				strings.Contains(summary, "/private/repo") || strings.Contains(summary, "/home/name") ||
				strings.Contains(summary, `server\share`) || strings.Contains(summary, `?\C:`) {
				t.Fatalf("safeCodingTerminalSummary(%q) = %q", input, summary)
			}
		})
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

func TestCodingTerminalReportFitsEncodedNodeBudget(t *testing.T) {
	changes := worktree.HandoffChangeset{}
	for index := 0; index < worktree.MaxHandoffPaths; index++ {
		changes.Untracked = append(changes.Untracked, worktree.PathChange{
			Path:   fmt.Sprintf("generated/%03d-%s.go", index, strings.Repeat("x", 1800)),
			Status: "??",
		})
	}
	active := &activeCodingTask{reportItems: make(map[string]worker.Item)}
	active.projectReportItem(worker.Item{
		ID: "final", Sequence: 1, Revision: 1,
		Message: &worker.Message{
			Kind: worker.MessageAssistant, Phase: worker.AssistantPhaseFinal,
			Complete: true, Text: strings.Repeat("summary ", 4096),
		},
	})
	report := active.terminalReport(codingTaskProcessResult{
		outcome: codingTaskOutcomeCompleted,
		handoff: &worktree.Handoff{
			Head: "0123456789abcdef", Class: worktree.HandoffChanges, Changes: changes,
		},
	})
	if err := report.Validate(); err != nil {
		t.Fatalf("bounded terminal report validation = %v", err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > codingtask.MaxTerminalReportBytes || !report.PathsTruncated {
		t.Fatalf("bounded terminal report = %d bytes, paths_truncated=%v", len(encoded), report.PathsTruncated)
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
