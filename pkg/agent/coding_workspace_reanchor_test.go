package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

func TestCodingPromptReanchorsCompactedSummaryToFreshWorkspace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCodingWorkspaceTestFile(t, filepath.Join(project, "tracked.txt"), "baseline\n")
	runCodingWorkspaceGit(t, project, "init", "-b", "main")
	runCodingWorkspaceGit(t, project, "config", "user.email", "mintclaw-tests@example.invalid")
	runCodingWorkspaceGit(t, project, "config", "user.name", "MintClaw Tests")
	runCodingWorkspaceGit(t, project, "add", "tracked.txt")
	runCodingWorkspaceGit(t, project, "commit", "-m", "initial")

	layout, err := NewCodingRuntimeLayout(
		"thread-reanchor",
		project,
		filepath.Join(root, "state"),
		[]string{project},
	)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := newCodingContextBuilder(layout)
	if err != nil {
		t.Fatal(err)
	}
	staleSummary := "Repository state: Branch: old-summary-branch; HEAD: old-head; Status: clean."
	first := builder.BuildMessagesFromPrompt(PromptBuildRequest{
		Summary:        staleSummary,
		CurrentMessage: "inspect",
	})
	if len(first) == 0 || !strings.Contains(first[0].Content, "Branch: main") {
		t.Fatalf("initial coding prompt = %#v", first)
	}

	runCodingWorkspaceGit(t, project, "switch", "-c", "external-change")
	writeCodingWorkspaceTestFile(t, filepath.Join(project, "outside.txt"), "changed outside MintClaw\n")
	second := builder.BuildMessagesFromPrompt(PromptBuildRequest{
		Summary:        staleSummary,
		CurrentMessage: "resume",
	})
	if len(second) == 0 {
		t.Fatal("resumed coding prompt is empty")
	}
	system := second[0].Content
	summaryIndex := strings.Index(system, "CONTEXT_SUMMARY:")
	snapshotIndex := strings.Index(system, "# Live workspace snapshot")
	if summaryIndex < 0 || snapshotIndex <= summaryIndex {
		t.Fatalf("coding context order does not re-anchor the summary:\n%s", system)
	}
	for _, want := range []string{
		"model-generated compacted summary",
		"Branch: external-change",
		"Status: dirty",
		`?? "outside.txt"`,
	} {
		if !strings.Contains(system, want) {
			t.Fatalf("resumed coding prompt missing %q:\n%s", want, system)
		}
	}
	if len(second[0].SystemParts) < 3 {
		t.Fatalf("coding system parts = %#v", second[0].SystemParts)
	}
	lastTwo := second[0].SystemParts[len(second[0].SystemParts)-2:]
	if lastTwo[0].PromptSource != string(PromptSourceSummary) ||
		lastTwo[1].PromptSource != string(PromptSourceRuntime) {
		t.Fatalf("coding context sources = %#v, want model summary then deterministic runtime", lastTwo)
	}
}

func TestCodingPromptSurfacesBoundedWorkspaceCaptureFailure(t *testing.T) {
	root := t.TempDir()
	builder := NewContextBuilder(root)
	builder.codingPrompt = true
	builder.codingWorkspace = codingworkspace.NewObserver(
		root,
		root,
		codingworkspace.Limits{PromptBytes: 320, Timeout: time.Nanosecond},
	)

	messages := builder.BuildMessagesFromPrompt(PromptBuildRequest{
		Summary:        "The repository was healthy.",
		CurrentMessage: "resume",
	})
	if len(messages) == 0 || len(messages[0].SystemParts) < 2 {
		t.Fatalf("coding prompt = %#v", messages)
	}
	runtimePart := messages[0].SystemParts[len(messages[0].SystemParts)-1]
	snapshotIndex := strings.Index(runtimePart.Text, "# Live workspace snapshot")
	if snapshotIndex < 0 {
		t.Fatalf("failed snapshot is missing: %#v", runtimePart)
	}
	snapshot := runtimePart.Text[snapshotIndex:]
	if runtimePart.PromptSource != string(PromptSourceRuntime) || len(snapshot) > 320 ||
		(!strings.Contains(snapshot, "Git: unavailable") &&
			!strings.Contains(snapshot, "Snapshot warning:") &&
			!strings.Contains(snapshot, "Snapshot status: prompt truncated")) {
		t.Fatalf("bounded failed snapshot = %#v", runtimePart)
	}
}

func TestPersonalPromptKeepsRuntimeBeforeSummary(t *testing.T) {
	builder := NewContextBuilder(t.TempDir())
	messages := builder.BuildMessagesFromPrompt(PromptBuildRequest{
		Summary:        "personal summary",
		CurrentMessage: "continue",
	})
	if len(messages) == 0 {
		t.Fatal("personal prompt is empty")
	}
	system := messages[0].Content
	runtimeIndex := strings.Index(system, "## Current Time")
	summaryIndex := strings.Index(system, "CONTEXT_SUMMARY:")
	if runtimeIndex < 0 || summaryIndex <= runtimeIndex ||
		strings.Contains(system, "model-generated compacted summary") {
		t.Fatalf("personal context order or wording changed:\n%s", system)
	}
}
