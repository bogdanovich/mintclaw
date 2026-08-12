package coding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

func TestCodeAndResumePersistOutsideProjectAcrossCommands(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.August, 10, 10, 0, 0, 0, time.UTC)
	deps := testDependencies(home, project, &now)

	createdOutput := executeCommand(t, newCodeCommand(deps), "fix", "the", "parser", "--model", "gpt-fixture", "--json")
	var created commandResult
	if err := json.Unmarshal(createdOutput, &created); err != nil {
		t.Fatalf("decode created result: %v\n%s", err, createdOutput)
	}
	if created.Action != "created" || !created.PromptStored || created.Model != "gpt-fixture" {
		t.Fatalf("created result = %#v", created)
	}
	canonicalProject, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	canonicalHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(created.ProjectRoot) != filepath.Clean(canonicalProject) ||
		!strings.HasPrefix(created.StateRoot, filepath.Join(canonicalHome, "coding", "threads")) {
		t.Fatalf("created roots = project %q state %q", created.ProjectRoot, created.StateRoot)
	}
	entries, err := os.ReadDir(project)
	if err != nil || len(entries) != 0 {
		t.Fatalf("coding command wrote into project: entries=%v error=%v", entries, err)
	}

	listOutput := executeCommand(t, newResumeCommand(deps), "--json")
	var listed listResult
	if err := json.Unmarshal(listOutput, &listed); err != nil {
		t.Fatalf("decode list result: %v\n%s", err, listOutput)
	}
	if listed.AllProjects || len(listed.Threads) != 1 || listed.Threads[0].ThreadID != created.ThreadID {
		t.Fatalf("restarted list = %#v", listed)
	}

	now = now.Add(time.Hour)
	resumedOutput := executeCommand(
		t,
		newResumeCommand(deps),
		created.ThreadID,
		"--prompt",
		"add a regression test",
		"--model",
		"gpt-next",
		"--json",
	)
	var resumed commandResult
	if err := json.Unmarshal(resumedOutput, &resumed); err != nil {
		t.Fatalf("decode resumed result: %v\n%s", err, resumedOutput)
	}
	if resumed.Action != "resumed" || !resumed.PromptStored || resumed.Model != "gpt-next" {
		t.Fatalf("resumed result = %#v", resumed)
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := store.Load(created.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Model != "gpt-next" || metadata.Preview != "add a regression test" || !metadata.UpdatedAt.Equal(now) {
		t.Fatalf("resumed metadata = %#v", metadata)
	}
	history := readHistory(t, created.StateRoot, created.SessionKey)
	if len(history) != 2 || history[0] != "fix the parser" || history[1] != "add a regression test" {
		t.Fatalf("restarted history = %#v", history)
	}
}

func TestResumeSelectorsAndProjectMismatchAreExplicit(t *testing.T) {
	home := t.TempDir()
	firstProject := t.TempDir()
	secondProject := t.TempDir()
	now := time.Date(2026, time.August, 10, 11, 0, 0, 0, time.UTC)
	firstDeps := testDependencies(home, firstProject, &now)
	secondDeps := testDependencies(home, secondProject, &now)

	var first commandResult
	if err := json.Unmarshal(executeCommand(t, newCodeCommand(firstDeps), "first", "--json"), &first); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	var second commandResult
	if err := json.Unmarshal(executeCommand(t, newCodeCommand(secondDeps), "second", "--json"), &second); err != nil {
		t.Fatal(err)
	}

	var all listResult
	if err := json.Unmarshal(executeCommand(t, newResumeCommand(firstDeps), "--all", "--json"), &all); err != nil {
		t.Fatal(err)
	}
	if !all.AllProjects || len(all.Threads) != 2 || all.Threads[0].ThreadID != second.ThreadID {
		t.Fatalf("all-project list = %#v", all)
	}
	canonicalSecondProject, err := filepath.EvalSymlinks(secondProject)
	if err != nil {
		t.Fatal(err)
	}
	_, mismatchErr := executeCommandError(newResumeCommand(firstDeps), second.ThreadID)
	if mismatchErr == nil || !strings.Contains(mismatchErr.Error(), "change directory") ||
		!strings.Contains(mismatchErr.Error(), canonicalSecondProject) {
		t.Fatalf("project mismatch error = %v", mismatchErr)
	}
	_, globalLastErr := executeCommandError(newResumeCommand(firstDeps), "--all", "--last")
	if globalLastErr == nil || !strings.Contains(globalLastErr.Error(), "change directory") {
		t.Fatalf("global last mismatch error = %v", globalLastErr)
	}
	lastOutput := executeCommand(t, newResumeCommand(firstDeps), "--last", "--json")
	var last commandResult
	if err := json.Unmarshal(lastOutput, &last); err != nil {
		t.Fatal(err)
	}
	if last.ThreadID != first.ThreadID || last.PromptStored {
		t.Fatalf("project last = %#v", last)
	}
	_, paginationErr := executeCommandError(newResumeCommand(firstDeps), first.ThreadID, "--limit", "1")
	if paginationErr == nil || !strings.Contains(paginationErr.Error(), "list-only") {
		t.Fatalf("selection pagination error = %v", paginationErr)
	}
	movedProject := firstProject + "-moved"
	if err := os.Rename(firstProject, movedProject); err != nil {
		t.Fatal(err)
	}
	_, movedErr := executeCommandError(newResumeCommand(secondDeps), first.ThreadID)
	if movedErr == nil || !strings.Contains(movedErr.Error(), "explicit relocation is required") ||
		strings.Contains(movedErr.Error(), "change directory") {
		t.Fatalf("moved project error = %v", movedErr)
	}
}

func TestResumeErrorsAreActionableAndLeaseAware(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	deps := testDependencies(home, project, &now)

	_, unknownErr := executeCommandError(newResumeCommand(deps), uuid.NewString())
	if unknownErr == nil || !strings.Contains(unknownErr.Error(), "was not found") ||
		!strings.Contains(unknownErr.Error(), "mintclaw resume --all") {
		t.Fatalf("unknown ID error = %v", unknownErr)
	}
	_, selectionErr := executeCommandError(newResumeCommand(deps), "--prompt", "not selected")
	if selectionErr == nil || !strings.Contains(selectionErr.Error(), "require a thread ID or --last") {
		t.Fatalf("selector error = %v", selectionErr)
	}

	var created commandResult
	if err := json.Unmarshal(executeCommand(t, newCodeCommand(deps), "lease fixture", "--json"), &created); err != nil {
		t.Fatal(err)
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(created.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	_, busyErr := executeCommandError(newResumeCommand(deps), created.ThreadID)
	if !errors.Is(busyErr, thread.ErrLeaseBusy) || !strings.Contains(busyErr.Error(), "owner pid") {
		t.Fatalf("busy resume error = %v", busyErr)
	}
}

func TestResumeSelectedThreadReloadsAuthoritativeMetadataUnderLease(t *testing.T) {
	home := t.TempDir()
	projectRoot := t.TempDir()
	now := time.Date(2026, time.August, 10, 12, 30, 0, 0, time.UTC)
	deps := testDependencies(home, projectRoot, &now)

	var created commandResult
	if err := json.Unmarshal(executeCommand(t, newCodeCommand(deps), "initial", "--json"), &created); err != nil {
		t.Fatal(err)
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := store.Load(created.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	metadata.Model = "newer-writer-model"
	metadata.UpdatedAt = now.Add(time.Minute)
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Hour)
	result, err := resumeSelectedThread(
		t.Context(),
		store,
		metadata.Project,
		deps,
		created.ThreadID,
		resumeOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != "newer-writer-model" {
		t.Fatalf("resume used stale selected metadata: %#v", result)
	}
	persisted, err := store.Load(created.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Model != "newer-writer-model" || !persisted.UpdatedAt.Equal(now) {
		t.Fatalf("authoritative metadata after resume = %#v", persisted)
	}
}

func TestCodeRejectsMintClawHomeInsideProjectBeforeWrite(t *testing.T) {
	project := t.TempDir()
	home := filepath.Join(project, ".mintclaw")
	now := time.Date(2026, time.August, 10, 13, 0, 0, 0, time.UTC)
	deps := testDependencies(home, project, &now)
	_, err := executeCommandError(newCodeCommand(deps), "must remain external")
	if err == nil || !strings.Contains(err.Error(), "state root must be outside the execution root") {
		t.Fatalf("in-project state error = %v", err)
	}
	if _, statErr := os.Stat(home); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rejected command created in-project state: %v", statErr)
	}
}

func TestInvalidPromptAndResumeMetadataFailBeforeCanonicalAppend(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.August, 10, 14, 0, 0, 0, time.UTC)
	deps := testDependencies(home, project, &now)
	_, oversizedErr := executeCommandError(newCodeCommand(deps), strings.Repeat("x", thread.MaxPromptBytes+1))
	if oversizedErr == nil || !strings.Contains(oversizedErr.Error(), "within") {
		t.Fatalf("oversized new prompt error = %v", oversizedErr)
	}
	if _, statErr := os.Stat(filepath.Join(home, "coding")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("oversized new prompt created coding state: %v", statErr)
	}

	var created commandResult
	if err := json.Unmarshal(executeCommand(t, newCodeCommand(deps), "accepted", "--json"), &created); err != nil {
		t.Fatal(err)
	}
	_, invalidModelErr := executeCommandError(
		newResumeCommand(deps),
		created.ThreadID,
		"--prompt",
		"must not commit",
		"--model",
		strings.Repeat("m", 257),
	)
	if invalidModelErr == nil || !strings.Contains(invalidModelErr.Error(), "model") {
		t.Fatalf("invalid resume metadata error = %v", invalidModelErr)
	}
	history := readHistory(t, created.StateRoot, created.SessionKey)
	if len(history) != 1 || history[0] != "accepted" {
		t.Fatalf("invalid resume mutated history = %#v", history)
	}
	_, emptyPromptErr := executeCommandError(newResumeCommand(deps), created.ThreadID, "--prompt", "")
	if emptyPromptErr == nil || !strings.Contains(emptyPromptErr.Error(), "prompt is required") {
		t.Fatalf("explicit empty resume prompt error = %v", emptyPromptErr)
	}
}

func TestResumeRejectsUnknownModelBeforeChangingDurableSelection(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.August, 10, 14, 30, 0, 0, time.UTC)
	deps := testDependencies(home, project, &now)
	var created commandResult
	if err := json.Unmarshal(
		executeCommand(t, newCodeCommand(deps), "accepted", "--model", "known", "--json"),
		&created,
	); err != nil {
		t.Fatal(err)
	}
	deps.resolveModel = func(model string) (string, string, error) {
		return "", "", fmt.Errorf("model %q not found", model)
	}

	for _, args := range [][]string{
		{created.ThreadID, "--model", "unknown"},
		{created.ThreadID, "--model", "unknown", "--prompt", "must not commit"},
	} {
		if _, err := executeCommandError(newResumeCommand(deps), args...); err == nil ||
			!strings.Contains(err.Error(), `model "unknown" not found`) {
			t.Fatalf("resume %v error = %v", args, err)
		}
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := store.Load(created.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Model != "known" || metadata.Provider != "fixture" {
		t.Fatalf("durable selection changed: model %q provider %q", metadata.Model, metadata.Provider)
	}
	if history := readHistory(t, created.StateRoot, created.SessionKey); len(history) != 1 || history[0] != "accepted" {
		t.Fatalf("unknown override mutated history = %#v", history)
	}
}

func TestCodePersistsDefaultSelectionBeforePromptAdmission(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.August, 10, 14, 45, 0, 0, time.UTC)
	deps := testDependencies(home, project, &now)
	deps.resolveModel = func(model string) (string, string, error) {
		if strings.TrimSpace(model) != "" {
			t.Fatalf("default resolution input = %q, want empty", model)
		}
		return "default-coding", "default-provider", nil
	}
	deps.turnRunner = codingTurnRunnerFunc(func(
		ctx context.Context,
		request codingTurnRequest,
	) (codingTurnOutcome, error) {
		persisted, err := request.Store.Load(request.Metadata.ThreadID)
		if err != nil {
			t.Fatal(err)
		}
		if persisted.Model != "default-coding" || persisted.Provider != "default-provider" {
			t.Fatalf("pre-admission selection = model %q provider %q", persisted.Model, persisted.Provider)
		}
		err = request.Store.AppendUserMessage(ctx, request.Lease, request.Metadata, request.Prompt)
		return codingTurnOutcome{
			Model:        request.Metadata.Model,
			Provider:     request.Metadata.Provider,
			PromptStored: appendOutcomeAllowsMetadataSave(err),
		}, err
	})

	var created commandResult
	if err := json.Unmarshal(
		executeCommand(t, newCodeCommand(deps), "use the default", "--json"),
		&created,
	); err != nil {
		t.Fatal(err)
	}
	if created.Model != "default-coding" || created.Provider != "default-provider" {
		t.Fatalf("created selection = %#v", created)
	}
}

func TestResumePersistsMissingSelectionBeforePromptAdmission(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.August, 10, 14, 50, 0, 0, time.UTC)
	deps := testDependencies(home, project, &now)
	var created commandResult
	if err := json.Unmarshal(executeCommand(t, newCodeCommand(deps), "initial", "--json"), &created); err != nil {
		t.Fatal(err)
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := store.Load(created.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	metadata.Model = ""
	metadata.Provider = ""
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	deps.resolveModel = func(model string) (string, string, error) {
		return "recovered-default", "recovered-provider", nil
	}
	deps.turnRunner = codingTurnRunnerFunc(func(
		ctx context.Context,
		request codingTurnRequest,
	) (codingTurnOutcome, error) {
		persisted, loadErr := request.Store.Load(request.Metadata.ThreadID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if persisted.Model != "recovered-default" || persisted.Provider != "recovered-provider" {
			t.Fatalf("legacy pre-admission selection = model %q provider %q", persisted.Model, persisted.Provider)
		}
		appendErr := request.Store.AppendUserMessage(ctx, request.Lease, request.Metadata, request.Prompt)
		return codingTurnOutcome{
			Model:        request.Metadata.Model,
			Provider:     request.Metadata.Provider,
			PromptStored: appendOutcomeAllowsMetadataSave(appendErr),
		}, appendErr
	})

	result := executeCommand(
		t,
		newResumeCommand(deps),
		created.ThreadID,
		"--prompt",
		"continue safely",
		"--json",
	)
	var resumed commandResult
	if err := json.Unmarshal(result, &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.Model != "recovered-default" || resumed.Provider != "recovered-provider" {
		t.Fatalf("resumed selection = %#v", resumed)
	}
}

func TestCodeAdmissionAndFailurePublication(t *testing.T) {
	t.Run("lease busy", func(t *testing.T) {
		home := t.TempDir()
		project := t.TempDir()
		now := time.Date(2026, time.August, 10, 15, 0, 0, 0, time.UTC)
		deps := testDependencies(home, project, &now)
		threadID := uuid.NewString()
		deps.newThreadID = func() string { return threadID }
		store, err := thread.NewStore(filepath.Join(home, "coding"))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ProvisionThread(threadID); err != nil {
			t.Fatal(err)
		}
		lease, err := store.AcquireLease(threadID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = lease.Release() })

		_, runErr := executeCommandError(newCodeCommand(deps), "must not publish")
		if !errors.Is(runErr, thread.ErrLeaseBusy) {
			t.Fatalf("code with busy lease error = %v", runErr)
		}
		if _, err := store.Load(threadID); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("busy creation published metadata: %v", err)
		}
	})

	t.Run("append fails", func(t *testing.T) {
		home := t.TempDir()
		project := t.TempDir()
		now := time.Date(2026, time.August, 10, 15, 30, 0, 0, time.UTC)
		deps := testDependencies(home, project, &now)
		threadID := uuid.NewString()
		deps.newThreadID = func() string { return threadID }
		store, err := thread.NewStore(filepath.Join(home, "coding"))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ProvisionThread(threadID); err != nil {
			t.Fatal(err)
		}
		threadRoot, err := store.ThreadRoot(threadID)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(threadRoot, "sessions"), []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}

		_, runErr := executeCommandError(newCodeCommand(deps), "must not publish")
		if runErr == nil || !strings.Contains(runErr.Error(), "sessions directory") {
			t.Fatalf("code with append failure error = %v", runErr)
		}
		metadata, loadErr := store.Load(threadID)
		if loadErr != nil || metadata.ThreadID != threadID {
			t.Fatalf("failed turn did not preserve inspectable metadata: %#v, %v", metadata, loadErr)
		}
		if !strings.Contains(runErr.Error(), "remains inspectable") ||
			!strings.Contains(runErr.Error(), "mintclaw resume "+threadID) {
			t.Fatalf("failed turn error is not actionable: %v", runErr)
		}
	})
}

func TestPlainListReportsPaginationAndScanTruncationTogether(t *testing.T) {
	var output bytes.Buffer
	err := renderList(
		&output,
		thread.ProjectIdentity{ProjectRoot: "/fixture"},
		thread.CatalogPage{HasMore: true, NextOffset: 100, ScanTruncated: true},
		resumeOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "--offset 100") ||
		!strings.Contains(text, "scan was truncated") {
		t.Fatalf("plain bounded warnings = %q", text)
	}
}

func TestPlainResultDistinguishesProjectAndWorkingDirectory(t *testing.T) {
	var output bytes.Buffer
	err := renderResult(
		&output,
		commandResult{
			Action:        "resumed",
			ThreadID:      "thread-id",
			ProjectRoot:   "/project",
			InvocationCWD: "/project/subdirectory",
			StateRoot:     "/state/thread-id",
		},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, `Project: "/project"`) ||
		!strings.Contains(text, `Working directory: "/project/subdirectory"`) {
		t.Fatalf("plain result = %q", text)
	}
}

func TestCodingInstructionRootsIncludeGlobalProjectAndInvocationCWD(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	cwd := filepath.Join(project, "nested")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := thread.NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	roots := codingInstructionRoots(store, thread.Metadata{Project: thread.ProjectIdentity{
		ProjectRoot:   project,
		InvocationCWD: cwd,
	}})
	want := []string{filepath.Join(store.Root(), "config"), project, cwd}
	if len(roots) != len(want) {
		t.Fatalf("instruction roots = %#v, want %#v", roots, want)
	}
	for index := range want {
		if roots[index] != want[index] {
			t.Fatalf("instruction root %d = %q, want %q", index, roots[index], want[index])
		}
	}
}

func TestAppendOutcomeAllowsMetadataSave(t *testing.T) {
	cause := errors.New("fixture")
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "success", want: true},
		{
			name: "durable append with later failure",
			err:  &thread.CommittedPromptError{ThreadID: uuid.NewString(), Err: cause},
			want: true,
		},
		{
			name: "indeterminate durability",
			err:  &thread.IndeterminatePromptError{ThreadID: uuid.NewString(), Err: cause},
			want: false,
		},
		{name: "ordinary failure", err: cause, want: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := appendOutcomeAllowsMetadataSave(testCase.err); got != testCase.want {
				t.Fatalf("appendOutcomeAllowsMetadataSave() = %t, want %t", got, testCase.want)
			}
		})
	}
}

func TestCommittedPromptStateSurvivesOutputFailure(t *testing.T) {
	t.Run("code", func(t *testing.T) {
		home := t.TempDir()
		project := t.TempDir()
		now := time.Date(2026, time.August, 10, 16, 0, 0, 0, time.UTC)
		deps := testDependencies(home, project, &now)
		threadID := uuid.NewString()
		deps.newThreadID = func() string { return threadID }
		outputErr := errors.New("injected output failure")
		err := runNew(t.Context(), failingWriter{err: outputErr}, deps, "committed code prompt", "", true)
		if !errors.Is(err, outputErr) || !thread.IsCommittedPromptError(err) ||
			!strings.Contains(err.Error(), "do not blindly retry") {
			t.Fatalf("runNew(output failure) error = %v", err)
		}
		store, err := thread.NewStore(filepath.Join(home, "coding"))
		if err != nil {
			t.Fatal(err)
		}
		metadata, err := store.Load(threadID)
		if err != nil {
			t.Fatal(err)
		}
		stateRoot, err := store.ThreadRoot(threadID)
		if err != nil {
			t.Fatal(err)
		}
		if history := readHistory(t, stateRoot, metadata.SessionKey); len(history) != 1 ||
			history[0] != "committed code prompt" {
			t.Fatalf("committed code history = %#v", history)
		}
	})

	t.Run("resume prompt", func(t *testing.T) {
		home := t.TempDir()
		project := t.TempDir()
		now := time.Date(2026, time.August, 10, 16, 30, 0, 0, time.UTC)
		deps := testDependencies(home, project, &now)
		var created commandResult
		if err := json.Unmarshal(executeCommand(t, newCodeCommand(deps), "first", "--json"), &created); err != nil {
			t.Fatal(err)
		}
		outputErr := errors.New("injected output failure")
		err := runResume(
			t.Context(),
			failingWriter{err: outputErr},
			deps,
			resumeOptions{threadID: created.ThreadID, prompt: "committed resume prompt", promptSet: true, json: true},
		)
		if !errors.Is(err, outputErr) || !thread.IsCommittedPromptError(err) ||
			!strings.Contains(err.Error(), "do not blindly retry") {
			t.Fatalf("runResume(output failure) error = %v", err)
		}
		if history := readHistory(t, created.StateRoot, created.SessionKey); len(history) != 2 ||
			history[1] != "committed resume prompt" {
			t.Fatalf("committed resume history = %#v", history)
		}
	})
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func testDependencies(home, cwd string, now *time.Time) dependencies {
	return dependencies{
		home:        func() string { return home },
		cwd:         func() (string, error) { return cwd, nil },
		now:         func() time.Time { return *now },
		newThreadID: thread.NewThreadID,
		resolveModel: func(model string) (string, string, error) {
			resolved := strings.TrimSpace(model)
			if resolved == "" {
				resolved = "fixture-alias"
			}
			return resolved, "fixture", nil
		},
		turnRunner: codingTurnRunnerFunc(func(
			ctx context.Context,
			request codingTurnRequest,
		) (codingTurnOutcome, error) {
			err := request.Store.AppendUserMessage(ctx, request.Lease, request.Metadata, request.Prompt)
			return codingTurnOutcome{
				Model:        request.Metadata.Model,
				Provider:     request.Metadata.Provider,
				Response:     "fixture response",
				PromptStored: appendOutcomeAllowsMetadataSave(err),
			}, err
		}),
	}
}

func executeCommand(t *testing.T, command interface {
	SetArgs([]string)
	SetOut(io.Writer)
	SetErr(io.Writer)
	Execute() error
}, args ...string,
) []byte {
	t.Helper()
	output, err := executeCommandError(command, args...)
	if err != nil {
		t.Fatalf("command %q error = %v\n%s", args, err, output)
	}
	return output
}

func executeCommandError(command interface {
	SetArgs([]string)
	SetOut(io.Writer)
	SetErr(io.Writer)
	Execute() error
}, args ...string,
) ([]byte, error) {
	var output bytes.Buffer
	command.SetArgs(args)
	command.SetOut(&output)
	command.SetErr(&output)
	err := command.Execute()
	return output.Bytes(), err
}

func readHistory(t *testing.T, stateRoot, sessionKey string) []string {
	t.Helper()
	canonical, err := memory.NewJSONLStore(filepath.Join(stateRoot, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	backend := session.NewJSONLBackend(canonical)
	t.Cleanup(func() { _ = backend.Close() })
	history, err := backend.ReadTurnHistory(t.Context(), sessionKey)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]string, len(history))
	for index := range history {
		result[index] = history[index].Content
	}
	return result
}
