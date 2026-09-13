package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/coding/project"
)

func TestExecutableBuildIDUsesArtifactContent(t *testing.T) {
	content := []byte("#!/bin/sh\nexit 0\n")
	path := filepath.Join(t.TempDir(), "mintclaw")
	if err := os.WriteFile(path, content, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	want := "sha256:" + hex.EncodeToString(digest[:])
	if got, err := ExecutableBuildID(path); err != nil || got != want {
		t.Fatalf("ExecutableBuildID() = %q, %v; want %q", got, err, want)
	}
	if _, err := ExecutableBuildID(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("ExecutableBuildID() accepted a missing artifact")
	}
}

func TestStartRequestBindsAllContentToDigest(t *testing.T) {
	request := NewStartRequest(
		"task-one",
		"generation-one",
		"mintclaw",
		"revision-one",
		TaskModeInvestigate,
		"Inspect the repository.",
		"Return a concise root cause.",
		"turn-one",
	)
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	if request.Prompt() != "Inspect the repository.\n\nDone criteria:\nReturn a concise root cause." {
		t.Fatalf("Prompt() = %q", request.Prompt())
	}
	changed := request
	changed.DoneCriteria = "Change the repository."
	if err := changed.Validate(); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("changed request error = %v", err)
	}
	changed = request
	changed.ProjectAlias = "../repo"
	if err := changed.Validate(); err == nil {
		t.Fatal("Validate() accepted a path as project alias")
	}
	unsafe := NewStartRequest(
		"task-one", "generation-one", "mintclaw", "revision-one",
		TaskModeInvestigate, "inspect\x1b[2J", "", "turn-one",
	)
	if err := unsafe.Validate(); err == nil {
		t.Fatal("Validate() accepted terminal controls in the objective")
	}
	blankCriteria := NewStartRequest(
		"task-one", "generation-one", "mintclaw", "revision-one",
		TaskModeInvestigate, "Inspect the repository.", " \t", "turn-one",
	)
	if err := blankCriteria.Validate(); err == nil {
		t.Fatal("Validate() accepted blank done criteria")
	}
}

func TestRecordValidatesInvestigationAndMutationBoundaries(t *testing.T) {
	project := testGitProject(t)
	now := time.Now().UTC().UnixNano()
	investigation := testRecord(project, now)
	if err := investigation.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := investigation.WorkerBinding(); err != nil {
		t.Fatal(err)
	}

	mutation := investigation
	mutation.Mode = TaskModeMutate
	mutation.WorktreeID = WorktreeIDForThread(mutation.ThreadID)
	mutation.ExecutionRoot = ""
	mutation.ExecutionRootIdentity = ""
	mutation.State = StatePreparing
	mutation.Activity = ""
	mutation.Revision++
	if err := mutation.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := mutation.WorkerBinding(); err == nil {
		t.Fatal("WorkerBinding() accepted an unprepared mutation")
	}
	execution := filepath.Join(t.TempDir(), "worktree")
	mutation.ExecutionRoot = execution
	mutation.ExecutionRootIdentity = ExecutionRootIdentity(execution)
	mutation.Branch = "mintclaw/task-test"
	if err := mutation.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := mutation.WorkerBinding(); err != nil {
		t.Fatal(err)
	}
	descendant := mutation
	descendant.ExecutionRoot = filepath.Join(project.ProjectRoot, "nested-worktree")
	descendant.ExecutionRootIdentity = ExecutionRootIdentity(descendant.ExecutionRoot)
	if err := descendant.Validate(); err == nil {
		t.Fatal("Validate() accepted a mutation root inside the source checkout")
	}
	binding, err := mutation.WorkerBinding()
	if err != nil {
		t.Fatal(err)
	}
	binding.ExecutionRoot = descendant.ExecutionRoot
	binding.ExecutionRootIdentity = descendant.ExecutionRootIdentity
	if err := binding.Validate(); err == nil {
		t.Fatal("Binding.Validate() accepted a mutation root inside the source checkout")
	}
	ancestor := mutation
	ancestor.ExecutionRoot = filepath.Dir(project.ProjectRoot)
	ancestor.ExecutionRootIdentity = ExecutionRootIdentity(ancestor.ExecutionRoot)
	if err := ancestor.Validate(); err == nil {
		t.Fatal("Validate() accepted a mutation root containing the source checkout")
	}
	binding, err = mutation.WorkerBinding()
	if err != nil {
		t.Fatal(err)
	}
	binding.ExecutionRoot = ancestor.ExecutionRoot
	binding.ExecutionRootIdentity = ancestor.ExecutionRootIdentity
	if err := binding.Validate(); err == nil {
		t.Fatal("Binding.Validate() accepted a mutation root containing the source checkout")
	}
	relative := mutation
	relative.ExecutionRoot = filepath.Join("relative", "worktree")
	relative.ExecutionRootIdentity = ExecutionRootIdentity(relative.ExecutionRoot)
	if err := relative.Validate(); err == nil {
		t.Fatal("Validate() accepted a relative mutation root")
	}
	completed := mutation
	completed.State = StateCompleted
	completed.Activity = ActivityIdle
	completed.RetainUntil = now + int64(time.Hour)
	completed.Revision++
	if err := completed.Validate(); err == nil {
		t.Fatal("Validate() accepted a completed mutation without handoff evidence")
	}
	completed.HandoffID = strings.Repeat("c", 64)
	if err := completed.Validate(); err != nil {
		t.Fatal(err)
	}
	liveWithHandoff := mutation
	liveWithHandoff.State = StateRunning
	liveWithHandoff.Activity = ActivityRunning
	liveWithHandoff.HandoffID = strings.Repeat("c", 64)
	if err := liveWithHandoff.Validate(); err == nil {
		t.Fatal("Validate() accepted terminal handoff evidence on a live mutation")
	}

	escaped := investigation
	escaped.ExecutionRoot = t.TempDir()
	escaped.ExecutionRootIdentity = ExecutionRootIdentity(escaped.ExecutionRoot)
	if err := escaped.Validate(); err == nil {
		t.Fatal("Validate() accepted a distinct investigation execution root")
	}
}

func TestRecordRequiresQuestionAndTerminalRetentionConsistency(t *testing.T) {
	now := time.Now().UTC().UnixNano()
	record := testRecord(testGitProject(t), now)
	record.State = StateWaitingInput
	record.Activity = ActivityWaitingInput
	record.Question = &QuestionState{
		QuestionID: "question-one", Revision: 1, Status: QuestionWaiting,
		Prompt: "Which package should be inspected?",
	}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	record.Question = nil
	if err := record.Validate(); err == nil {
		t.Fatal("Validate() accepted waiting state without a question")
	}
	record.Question = &QuestionState{
		QuestionID: "question-one", Revision: 1, Status: QuestionAnswered,
		Prompt: "Which package should be inspected?",
	}
	if err := record.Validate(); err == nil {
		t.Fatal("Validate() accepted a non-waiting question as the active blocker")
	}

	record = testRecord(testGitProject(t), now)
	record.State = StateUncertain
	record.Activity = ActivityFailed
	record.Failure = &Failure{Code: "WORKER_LOST", Message: "coding worker outcome is uncertain"}
	record.RetainUntil = now + int64(time.Hour)
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	record.RetainUntil = 0
	if err := record.Validate(); err == nil {
		t.Fatal("Validate() accepted a terminal record without retention")
	}
}

func TestRecordCloneAndTransitionRules(t *testing.T) {
	if !StateAccepted.CanTransitionTo(StatePreparing) || StateAccepted.CanTransitionTo(StateRunning) ||
		StateUncertain.CanTransitionTo(StateCompleted) || StateCompleted.CanTransitionTo(StateRunning) {
		t.Fatal("unexpected task transition rules")
	}
	now := time.Now().UTC().UnixNano()
	record := testRecord(testGitProject(t), now)
	record.State = StateWaitingInput
	record.Activity = ActivityWaitingInput
	record.Question = &QuestionState{
		QuestionID: "question-one", Revision: 1, Status: QuestionWaiting,
		Prompt: "Choose", Options: []QuestionOption{{ID: "one", Label: "One"}},
	}
	cloned := record.Clone()
	cloned.Question.Options[0].Label = "Changed"
	if record.Question.Options[0].Label != "One" {
		t.Fatal("Clone() retained caller-owned question options")
	}
}

func TestValidBranchRejectsUnusableHandoffRefs(t *testing.T) {
	for _, branch := range []string{"mintclaw/task-test", "topic/feature-1", "release@candidate", "feature]name"} {
		if !ValidBranch(branch) {
			t.Errorf("ValidBranch(%q) = false", branch)
		}
	}
	for _, branch := range []string{
		"", "HEAD", "-topic", "bad branch", "topic..name", "topic.lock", "topic.LOCK",
		"topic/.hidden", "topic//name", "topic/@{name", "topic/name.", "topic\\name", "topic~name",
	} {
		if ValidBranch(branch) {
			t.Errorf("ValidBranch(%q) = true", branch)
		}
	}
}

func TestRecordRejectsLifecycleAndStructuralDrift(t *testing.T) {
	now := time.Now().UTC().UnixNano()
	record := testRecord(testGitProject(t), now)
	record.Activity = ""
	if err := record.Validate(); err == nil {
		t.Fatal("Validate() accepted running state without running activity")
	}
	record = testRecord(testGitProject(t), now)
	record.Model = " gpt-test"
	if err := record.Validate(); err == nil {
		t.Fatal("Validate() accepted a non-canonical model")
	}
	record = testRecord(testGitProject(t), now)
	record.ExpectedWorkerBuildID = strings.Repeat("b", 64)
	if err := record.Validate(); err == nil {
		t.Fatal("Validate() accepted an untyped worker build identity")
	}
	record = testRecord(testGitProject(t), now)
	record.Mode = TaskModeMutate
	record.WorktreeID = WorktreeIDForThread(record.ThreadID)
	record.ExecutionRoot = filepath.Join(t.TempDir(), "worktree")
	record.ExecutionRootIdentity = ExecutionRootIdentity(record.ExecutionRoot)
	record.Branch = "bad branch"
	record.HandoffID = strings.Repeat("c", 64)
	record.State = StateCompleted
	record.Activity = ActivityIdle
	record.RetainUntil = now + int64(time.Hour)
	if err := record.Validate(); err == nil {
		t.Fatal("Validate() accepted a completed handoff with an invalid Git branch")
	}
	if validPath("/" + strings.Repeat("x", MaxPathBytes)) {
		t.Fatal("validPath() accepted an oversized execution root")
	}
	if validPath("/tmp/repository\nother") {
		t.Fatal("validPath() accepted a control character")
	}
	for _, path := range []string{`\\?\C:\repo`, `\\.\C:\repo`, `\??\C:\repo`, `//?/C:/repo`} {
		if supportedPathNamespace(path) {
			t.Fatalf("supportedPathNamespace(%q) accepted a Windows device path", path)
		}
	}
	record = testRecord(testGitProject(t), now)
	record.State = StateFailed
	record.Activity = ActivityFailed
	record.Failure = &Failure{Code: "WORKER_FAILED", Message: " "}
	record.RetainUntil = now + int64(time.Hour)
	if err := record.Validate(); err == nil {
		t.Fatal("Validate() accepted a whitespace-only failure")
	}
	record.Failure = &Failure{Code: strings.Repeat("X", MaxFailureCodeBytes+1), Message: "worker failed"}
	if err := record.Validate(); err == nil {
		t.Fatal("Validate() accepted an oversized failure code")
	}
}

func testRecord(project project.ProjectIdentity, now int64) Record {
	threadID := uuid.NewString()
	return Record{
		SchemaVersion: SchemaVersion, InvocationID: "invocation-one",
		RequestDigest: strings.Repeat("a", 64), TaskID: "task-one",
		TaskGenerationID: "generation-one", ProjectAlias: "mintclaw",
		ProjectRevision: "revision-one", Mode: TaskModeInvestigate,
		ThreadID: threadID, ThreadOpenMode: ThreadOpenNew,
		WorkerGenerationID: "worker-one", Project: project,
		ExecutionRoot:         project.ProjectRoot,
		ExecutionRootIdentity: ExecutionRootIdentity(project.ProjectRoot),
		ProviderProfile:       "default", Model: "gpt-test", Provider: "openai",
		ExpectedWorkerBuildID: "sha256:" + strings.Repeat("b", 64),
		State:                 StateRunning, Activity: ActivityRunning,
		Revision: 1, AcceptedAt: now, UpdatedAt: now,
	}
}

func testGitProject(t *testing.T) project.ProjectIdentity {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "MintClaw Test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "README.md")
	runGit(t, root, "commit", "-m", "fixture")
	identity, err := project.ResolveProject(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func runGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}
