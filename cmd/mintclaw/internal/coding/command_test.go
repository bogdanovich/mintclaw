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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/coding/tui"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
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
	baseline, err := store.LoadRepositoryBaseline(created.ThreadID)
	if err != nil || !baseline.CapturedAt.Equal(
		time.Date(2026, time.August, 10, 10, 0, 0, 0, time.UTC),
	) {
		t.Fatalf("created repository baseline = %#v / %v", baseline, err)
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
	resumedBaseline, err := store.LoadRepositoryBaseline(created.ThreadID)
	if err != nil || resumedBaseline.BaselineID != baseline.BaselineID {
		t.Fatalf("resume replaced repository baseline = %#v / %v", resumedBaseline, err)
	}
}

func TestResumeArchivedViewIsExplicitAndReversible(t *testing.T) {
	home := t.TempDir()
	projectRoot := t.TempDir()
	now := time.Date(2026, time.August, 27, 10, 0, 0, 0, time.UTC)
	deps := testDependencies(home, projectRoot, &now)
	createdOutput := executeCommand(t, newCodeCommand(deps), "archive this", "--json")
	var created commandResult
	if err := json.Unmarshal(createdOutput, &created); err != nil {
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
	metadata, err = metadata.SetArchived(true, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	var active, archived listResult
	if err := json.Unmarshal(executeCommand(t, newResumeCommand(deps), "--json"), &active); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(
		executeCommand(t, newResumeCommand(deps), "--archived", "--json"),
		&archived,
	); err != nil {
		t.Fatal(err)
	}
	if len(active.Threads) != 0 || len(archived.Threads) != 1 ||
		archived.Threads[0].ThreadID != created.ThreadID {
		t.Fatalf("active = %+v archived = %+v", active, archived)
	}
	if _, err := executeCommandError(newResumeCommand(deps), created.ThreadID, "--archived"); err == nil {
		t.Fatal("explicit thread ID accepted redundant --archived")
	}
}

func TestResumeHistoricalSearchIsProjectPrivateAndExplicitlyExpandable(t *testing.T) {
	home := t.TempDir()
	currentProject := t.TempDir()
	foreignProject := t.TempDir()
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	currentDeps := testDependencies(home, currentProject, &now)
	foreignDeps := testDependencies(home, foreignProject, &now)
	var current, foreign commandResult
	if err := json.Unmarshal(
		executeCommand(t, newCodeCommand(currentDeps), "neutral current title", "--json"),
		&current,
	); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(
		executeCommand(t, newCodeCommand(foreignDeps), "neutral foreign title", "--json"),
		&foreign,
	); err != nil {
		t.Fatal(err)
	}
	matchedAt := now.Add(time.Minute)
	appendHistoryMessage(t, current.StateRoot, current.SessionKey, providers.Message{
		Role: "assistant", Content: "current transcript-only-needle", CreatedAt: &matchedAt,
	})
	appendHistoryMessage(t, foreign.StateRoot, foreign.SessionKey, providers.Message{
		Role: "assistant", Content: "foreign transcript-only-needle", CreatedAt: &matchedAt,
	})
	var scoped listResult
	if err := json.Unmarshal(executeCommand(
		t,
		newResumeCommand(currentDeps),
		"--search",
		"transcript-only-needle",
		"--json",
	), &scoped); err != nil {
		t.Fatal(err)
	}
	if scoped.AllProjects || scoped.Search != "transcript-only-needle" || len(scoped.SearchMatches) != 1 ||
		scoped.SearchMatches[0].Metadata.ThreadID != current.ThreadID ||
		scoped.SearchMatches[0].Kind != thread.HistoricalMatchTranscript ||
		!scoped.SearchMatches[0].MatchedAt.Equal(matchedAt) {
		t.Fatalf("scoped historical search = %+v", scoped)
	}
	var all listResult
	if err := json.Unmarshal(executeCommand(
		t,
		newResumeCommand(currentDeps),
		"--search",
		"transcript-only-needle",
		"--all",
		"--json",
	), &all); err != nil {
		t.Fatal(err)
	}
	if !all.AllProjects || len(all.SearchMatches) != 2 {
		t.Fatalf("all-project historical search = %+v", all)
	}
	human := string(executeCommand(
		t,
		newResumeCommand(currentDeps),
		"--search",
		"transcript-only-needle",
	))
	if !strings.Contains(human, current.ThreadID) || !strings.Contains(human, matchedAt.Format(time.RFC3339Nano)) ||
		!strings.Contains(human, "transcript:message-2") ||
		!strings.Contains(human, "current transcript-only-needle") || strings.Contains(human, foreign.ThreadID) {
		t.Fatalf("human scoped search = %q", human)
	}
	metadataHuman := string(executeCommand(t, newResumeCommand(currentDeps), "--search", "neutral current"))
	if !strings.Contains(metadataHuman, "metadata:title") {
		t.Fatalf("human metadata search omitted source identity: %q", metadataHuman)
	}
	metaPath := filepath.Join(
		current.StateRoot,
		"sessions",
		strings.ReplaceAll(current.SessionKey, ":", "_")+".meta.json",
	)
	if err := os.Remove(metaPath); err != nil {
		t.Fatal(err)
	}
	incomplete := string(executeCommand(t, newResumeCommand(currentDeps), "--search", "not-present"))
	if !strings.Contains(incomplete, "No coding threads match") ||
		!strings.Contains(incomplete, "Search coverage was incomplete or bounded") {
		t.Fatalf("incomplete human search omitted coverage warning: %q", incomplete)
	}
	for _, args := range [][]string{
		{"--search", ""},
		{current.ThreadID, "--search", "needle"},
		{"--last", "--search", "needle"},
		{"--search", "needle", "--prompt", "mutate"},
	} {
		if _, err := executeCommandError(newResumeCommand(currentDeps), args...); err == nil {
			t.Fatalf("invalid search options accepted: %v", args)
		}
	}
}

func TestThreadsDeleteRequiresExactPlanConfirmation(t *testing.T) {
	home := t.TempDir()
	projectRoot := t.TempDir()
	now := time.Date(2026, time.August, 28, 10, 0, 0, 0, time.UTC)
	deps := testDependencies(home, projectRoot, &now)
	createdOutput := executeCommand(t, newCodeCommand(deps), "delete safely", "--json")
	var created commandResult
	if err := json.Unmarshal(createdOutput, &created); err != nil {
		t.Fatal(err)
	}
	var planned deleteThreadOutput
	if err := json.Unmarshal(
		executeCommand(t, newThreadsCommand(deps), "delete", created.ThreadID, "--json"),
		&planned,
	); err != nil {
		t.Fatal(err)
	}
	if planned.Action != "planned" || planned.Plan.ThreadID != created.ThreadID || planned.Trash != nil ||
		planned.Plan.ProjectKey == "" || len(planned.Plan.OwnedPaths) == 0 {
		t.Fatalf("delete plan = %+v", planned)
	}
	if _, err := executeCommandError(
		newThreadsCommand(deps), "delete", created.ThreadID, "--confirm", "wrong",
	); err == nil || !strings.Contains(err.Error(), "exactly match") {
		t.Fatalf("wrong confirmation error = %v", err)
	}
	now = now.Add(time.Minute)
	var deleted deleteThreadOutput
	if err := json.Unmarshal(executeCommand(
		t,
		newThreadsCommand(deps),
		"delete",
		created.ThreadID,
		"--confirm",
		created.ThreadID,
		"--json",
	), &deleted); err != nil {
		t.Fatal(err)
	}
	if deleted.Action != "trashed" || deleted.Trash == nil || deleted.Trash.ThreadID != created.ThreadID ||
		!deleted.Trash.At.Equal(now) {
		t.Fatalf("deleted output = %+v", deleted)
	}
	if _, err := os.Stat(deleted.Trash.Path); err != nil {
		t.Fatalf("recoverable trash path: %v", err)
	}
	var listed listResult
	if err := json.Unmarshal(executeCommand(t, newResumeCommand(deps), "--json"), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Threads) != 0 {
		t.Fatalf("deleted thread remained active: %+v", listed.Threads)
	}
	entries, err := os.ReadDir(projectRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("delete touched project: entries=%v err=%v", entries, err)
	}
}

func TestThreadsDeleteRejectsChangedProjectIdentityAtSameRoot(t *testing.T) {
	home := t.TempDir()
	projectRoot := t.TempDir()
	now := time.Date(2026, time.August, 28, 10, 0, 0, 0, time.UTC)
	deps := testDependencies(home, projectRoot, &now)
	createdOutput := executeCommand(t, newCodeCommand(deps), "keep project scope", "--json")
	var created commandResult
	if err := json.Unmarshal(createdOutput, &created); err != nil {
		t.Fatal(err)
	}
	runPickerGit(t, projectRoot, "init")
	if _, err := executeCommandError(
		newThreadsCommand(deps), "delete", created.ThreadID, "--json",
	); err == nil || !strings.Contains(err.Error(), "belongs to project") {
		t.Fatalf("changed project identity error = %v", err)
	}
}

func TestThreadsGCPlansThenRequiresExactStoreWideConfirmation(t *testing.T) {
	home := t.TempDir()
	projectRoot := t.TempDir()
	now := time.Date(2026, time.August, 28, 10, 0, 0, 0, time.UTC)
	deps := testDependencies(home, projectRoot, &now)
	project, err := thread.ResolveProject(t.Context(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := thread.NewMetadata(thread.NewThreadID(), project, "gc fixture", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "orphan.txt")
	if err := os.WriteFile(source, []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	attachment, err := store.AdmitAttachment(t.Context(), lease, metadata, thread.AttachmentInput{
		Path: source, At: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveAttachmentRefs(t.Context(), lease, metadata, []string{attachment.Ref}); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	blobPath := filepath.Join(store.Root(), "blobs", "sha256", attachment.SHA256[:2], attachment.SHA256)
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(blobPath, old, old); err != nil {
		t.Fatal(err)
	}

	var planned gcThreadsOutput
	if err := json.Unmarshal(executeCommand(
		t,
		newThreadsCommand(deps),
		"gc",
		"--older-than",
		"24h",
		"--json",
	), &planned); err != nil {
		t.Fatal(err)
	}
	if planned.Action != "planned" || len(planned.Result.Candidates) != 1 ||
		planned.Result.DeletedBlobs != 0 || !strings.Contains(planned.Notice, "every project") {
		t.Fatalf("GC plan = %+v", planned)
	}
	if _, err := os.Stat(blobPath); err != nil {
		t.Fatalf("plan deleted blob: %v", err)
	}
	if _, err := executeCommandError(
		newThreadsCommand(deps), "gc", "--confirm", "wrong",
	); err == nil || !strings.Contains(err.Error(), "exactly match") {
		t.Fatalf("wrong GC confirmation error = %v", err)
	}
	if _, err := executeCommandError(
		newThreadsCommand(deps), "gc", "--confirm", " "+attachmentGCConfirmation+" ",
	); err == nil || !strings.Contains(err.Error(), "exactly match") {
		t.Fatalf("whitespace GC confirmation error = %v", err)
	}
	var collected gcThreadsOutput
	if err := json.Unmarshal(executeCommand(
		t,
		newThreadsCommand(deps),
		"gc",
		"--older-than",
		"24h",
		"--confirm",
		attachmentGCConfirmation,
		"--json",
	), &collected); err != nil {
		t.Fatal(err)
	}
	if collected.Action != "collected" || collected.Result.DeletedBlobs != 1 ||
		collected.Result.DeletedBytes != attachment.Size {
		t.Fatalf("GC result = %+v", collected)
	}
	if _, err := os.Stat(blobPath); !os.IsNotExist(err) {
		t.Fatalf("confirmed GC retained blob: %v", err)
	}
}

func TestThreadsGCIsEmptyBeforeCodingStoreExists(t *testing.T) {
	home := t.TempDir()
	projectRoot := t.TempDir()
	now := time.Date(2026, time.August, 28, 10, 0, 0, 0, time.UTC)
	deps := testDependencies(home, projectRoot, &now)
	storeRoot := filepath.Join(home, "coding")
	for _, args := range [][]string{
		{"gc", "--json"},
		{"gc", "--confirm", attachmentGCConfirmation, "--json"},
	} {
		var output gcThreadsOutput
		if err := json.Unmarshal(executeCommand(t, newThreadsCommand(deps), args...), &output); err != nil {
			t.Fatal(err)
		}
		if output.Result.ScannedManifests != 0 || output.Result.ScannedBlobs != 0 ||
			output.Result.DeletedBlobs != 0 || output.Result.Candidates == nil {
			t.Fatalf("uncreated-store command %v = %+v", args, output)
		}
		if _, err := os.Stat(storeRoot); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("uncreated-store command %v materialized state: %v", args, err)
		}
	}
}

func TestThreadsForkHistoricalConversationUsesLiveFilesystemAndIndependentWriter(t *testing.T) {
	home := t.TempDir()
	projectRoot := t.TempDir()
	sentinel := filepath.Join(projectRoot, "live.txt")
	if err := os.WriteFile(sentinel, []byte("live workspace\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 28, 11, 0, 0, 0, time.UTC)
	deps := testDependencies(home, projectRoot, &now)
	createdOutput := executeCommand(t, newCodeCommand(deps), "first request", "--model", "gpt-fork", "--json")
	var created commandResult
	if err := json.Unmarshal(createdOutput, &created); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	executeCommand(t, newResumeCommand(deps), created.ThreadID, "--prompt", "second request", "--json")

	childID := thread.NewThreadID()
	deps.newThreadID = func() string { return childID }
	now = now.Add(time.Minute)
	forkOutput := executeCommand(
		t,
		newThreadsCommand(deps),
		"fork",
		created.ThreadID,
		"--at-turn",
		"1",
		"--json",
	)
	var forked forkThreadOutput
	if err := json.Unmarshal(forkOutput, &forked); err != nil {
		t.Fatalf("decode fork result: %v\n%s", err, forkOutput)
	}
	if forked.Action != "forked" || forked.Fork.ThreadID != childID || forked.Fork.SourceTurn != 1 ||
		forked.Fork.CopiedMessages != 1 || !forked.Fork.LiveFilesystem ||
		forked.Metadata.ParentThread != created.ThreadID || forked.Metadata.Fork == nil ||
		forked.ResumeCommand != "mintclaw resume "+childID || !strings.Contains(forked.Notice, "live filesystem") {
		t.Fatalf("fork result = %+v", forked)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "live workspace\n" {
		t.Fatalf("fork changed live project file: %q / %v", data, err)
	}
	if got := readHistory(t, forked.Fork.StateRoot, forked.Fork.SessionKey); !reflect.DeepEqual(
		got,
		[]string{"first request"},
	) {
		t.Fatalf("historical child history = %#v", got)
	}

	now = now.Add(time.Minute)
	executeCommand(t, newResumeCommand(deps), childID, "--prompt", "child only", "--json")
	if got := readHistory(t, created.StateRoot, created.SessionKey); !reflect.DeepEqual(
		got,
		[]string{"first request", "second request"},
	) {
		t.Fatalf("source history changed = %#v", got)
	}
	if got := readHistory(t, forked.Fork.StateRoot, forked.Fork.SessionKey); !reflect.DeepEqual(
		got,
		[]string{"first request", "child only"},
	) {
		t.Fatalf("child history = %#v", got)
	}

	deps.newThreadID = thread.NewThreadID
	plain := executeCommand(t, newThreadsCommand(deps), "fork", created.ThreadID)
	if !strings.Contains(string(plain), "current live filesystem") ||
		!strings.Contains(string(plain), "no project files were rolled back") ||
		!strings.Contains(string(plain), "mintclaw resume ") {
		t.Fatalf("plain fork output = %q", plain)
	}
}

func TestDeleteRendersRecoveryPathAfterCommittedDurabilityWarning(t *testing.T) {
	trash := thread.TrashResult{
		ThreadID: thread.NewThreadID(),
		TrashID:  "trash-id",
		Path:     filepath.Join(t.TempDir(), "recoverable-thread"),
		At:       time.Now(),
	}
	warning := &thread.CommittedTrashError{Result: trash, Err: errors.New("directory sync failed")}
	var output bytes.Buffer

	err := finishDeleteThread(
		&output,
		deleteThreadOutput{Action: "trashed", Trash: &trash},
		false,
		warning,
	)

	if !thread.IsCommittedTrashError(err) || !strings.Contains(output.String(), trash.Path) {
		t.Fatalf("error = %v, output = %q", err, output.String())
	}

	output.Reset()
	ordinary := errors.New("rename failed")
	err = finishDeleteThread(
		&output,
		deleteThreadOutput{Action: "trashed", Trash: &trash},
		false,
		ordinary,
	)
	if !errors.Is(err, ordinary) || output.Len() != 0 {
		t.Fatalf("ordinary error = %v, output = %q", err, output.String())
	}

	output.Reset()
	preMoveCommitted := &fileutil.CommittedWriteError{Err: errors.New("trash directory sync failed")}
	err = finishDeleteThread(
		&output,
		deleteThreadOutput{Action: "trashed", Trash: &thread.TrashResult{}},
		false,
		preMoveCommitted,
	)
	if !errors.Is(err, preMoveCommitted) || output.Len() != 0 {
		t.Fatalf("pre-move committed error = %v, output = %q", err, output.String())
	}
}

func TestForkCompletionPreservesCommittedClassificationForDeferredFailures(t *testing.T) {
	result := thread.ForkResult{ThreadID: thread.NewThreadID()}
	for _, test := range []struct {
		name       string
		forkErr    error
		renderErr  error
		releaseErr error
	}{
		{name: "render", renderErr: errors.New("render failed")},
		{name: "release", releaseErr: errors.New("release failed")},
		{
			name: "already committed",
			forkErr: &thread.CommittedForkError{
				Result: result,
				Err:    errors.New("durability warning"),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := classifyForkCompletion(result, test.forkErr, test.renderErr, test.releaseErr)
			var committed *thread.CommittedForkError
			if !errors.As(err, &committed) || committed.Result.ThreadID != result.ThreadID {
				t.Fatalf("classification = %v", err)
			}
		})
	}
	if err := classifyForkCompletion(result, nil, nil, nil); err != nil {
		t.Fatalf("successful completion error = %v", err)
	}
}

type interactiveLeaseController struct {
	*frontend.Projector
	lease *thread.Lease
}

func (*interactiveLeaseController) Submit(context.Context, frontend.TurnInput) error { return nil }
func (*interactiveLeaseController) Interrupt(context.Context) error                  { return nil }
func (*interactiveLeaseController) HardCancel(context.Context) error                 { return nil }
func (*interactiveLeaseController) Compact(context.Context) error                    { return nil }
func (*interactiveLeaseController) Rename(context.Context, string) error             { return nil }
func (*interactiveLeaseController) SetArchived(context.Context, bool) error          { return nil }
func (*interactiveLeaseController) NewThread(context.Context) error                  { return nil }
func (c *interactiveLeaseController) Close(context.Context) error                    { return c.lease.Release() }

func TestCodeUsesInteractiveShellOnlyForCapableTerminal(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.August, 15, 10, 0, 0, 0, time.UTC)
	deps := testDependencies(home, project, &now)
	deps.terminal = func(io.Reader, io.Writer, bool) tui.TerminalCapabilities {
		return tui.TerminalCapabilities{Interactive: true, Color: true}
	}
	var request codingTurnRequest
	deps.newController = func(candidate codingTurnRequest, resumed bool) (frontend.Controller, error) {
		if resumed {
			t.Fatal("new coding thread was marked resumed")
		}
		request = candidate
		projector, err := frontend.NewProjector(candidate.Metadata.ThreadID, frontend.ProjectionLimits{})
		if err != nil {
			return nil, err
		}
		projector.Open(false)
		return &interactiveLeaseController{Projector: projector, lease: candidate.Lease}, nil
	}
	runs := 0
	attachmentPath := filepath.Join(project, "screenshot.png")
	deps.runTUI = func(ctx context.Context, controller frontend.Controller, options tui.Options) error {
		runs++
		if options.InitialInput.Text != "fix the terminal" || len(options.InitialInput.Attachments) != 1 ||
			options.InitialInput.Attachments[0].Path != attachmentPath ||
			!options.AlternateScreen || !options.ReportFocus {
			t.Fatalf("TUI options = %+v", options)
		}
		return controller.Close(ctx)
	}

	executeCommand(t, newCodeCommand(deps), "fix", "the", "terminal", "--attach", attachmentPath)
	if runs != 1 || request.Metadata.ThreadID == "" {
		t.Fatalf("TUI runs=%d request=%+v", runs, request)
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(request.Metadata.ThreadID)
	if err != nil {
		t.Fatalf("interactive shell did not release lease: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestCodeWithoutPromptOpensInteractiveComposer(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.August, 29, 17, 0, 0, 0, time.UTC)
	deps := testDependencies(home, project, &now)
	deps.terminal = func(io.Reader, io.Writer, bool) tui.TerminalCapabilities {
		return tui.TerminalCapabilities{Interactive: true, Color: true}
	}
	var request codingTurnRequest
	deps.newController = func(candidate codingTurnRequest, resumed bool) (frontend.Controller, error) {
		if resumed {
			t.Fatal("new coding thread was marked resumed")
		}
		request = candidate
		projector, err := frontend.NewProjector(candidate.Metadata.ThreadID, frontend.ProjectionLimits{})
		if err != nil {
			return nil, err
		}
		projector.Open(false)
		return &interactiveLeaseController{Projector: projector, lease: candidate.Lease}, nil
	}
	runs := 0
	deps.runTUI = func(ctx context.Context, controller frontend.Controller, options tui.Options) error {
		runs++
		if options.InitialInput.Text != "" || len(options.InitialInput.Attachments) != 0 ||
			!options.AlternateScreen || !options.ReportFocus {
			t.Fatalf("TUI options = %+v", options)
		}
		return controller.Close(ctx)
	}

	executeCommand(t, newCodeCommand(deps))
	if runs != 1 || request.Metadata.ThreadID == "" || request.Metadata.Title != "New coding thread" {
		t.Fatalf("TUI runs=%d request=%+v", runs, request)
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := store.Load(request.Metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Title != thread.PendingThreadTitle || metadata.Preview != thread.PendingThreadTitle ||
		!metadata.PendingFirstPrompt {
		t.Fatalf("empty-start metadata = %+v", metadata)
	}
}

func TestCodeWithoutPromptRejectsPlainAndJSONModes(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.August, 29, 17, 0, 0, 0, time.UTC)
	deps := testDependencies(home, project, &now)
	for _, testCase := range []struct {
		name        string
		args        []string
		interactive bool
	}{
		{name: "plain"},
		{name: "json", args: []string{"--json"}, interactive: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			caseDeps := deps
			if testCase.interactive {
				caseDeps.terminal = func(io.Reader, io.Writer, bool) tui.TerminalCapabilities {
					return tui.TerminalCapabilities{Interactive: true, Color: true}
				}
			}
			_, err := executeCommandError(newCodeCommand(caseDeps), testCase.args...)
			if err == nil || !strings.Contains(err.Error(), "prompt or --attach is required") {
				t.Fatalf("empty code error = %v", err)
			}
		})
	}
}

func TestCodeAttachFlagsCreateStructuredAttachmentOnlyTurn(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.August, 29, 22, 30, 0, 0, time.UTC)
	deps := testDependencies(home, project, &now)
	first := filepath.Join(project, "build.log")
	second := filepath.Join(project, "screen.png")
	var received frontend.TurnInput
	deps.turnRunner = codingTurnRunnerFunc(func(
		ctx context.Context,
		request codingTurnRequest,
	) (codingTurnOutcome, error) {
		received = request.Input.Clone()
		err := request.Store.AppendUserMessage(
			ctx,
			request.Lease,
			request.Metadata,
			turnDisplayContent(request.Input),
		)
		return codingTurnOutcome{PromptStored: appendOutcomeAllowsMetadataSave(err)}, err
	})

	var created commandResult
	if err := json.Unmarshal(executeCommand(
		t,
		newCodeCommand(deps),
		"--attach",
		first,
		"--attach",
		second,
		"--json",
	), &created); err != nil {
		t.Fatal(err)
	}
	if received.Text != "" || len(received.Attachments) != 2 ||
		received.Attachments[0].Path != first || received.Attachments[1].Path != second {
		t.Fatalf("structured input = %+v", received)
	}
	if !created.PromptStored {
		t.Fatalf("created result = %+v", created)
	}
}

func TestResumeAttachFlagBuildsOneStructuredTurn(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	now := time.Date(2026, time.August, 29, 22, 35, 0, 0, time.UTC)
	deps := testDependencies(home, project, &now)
	var created commandResult
	if err := json.Unmarshal(executeCommand(t, newCodeCommand(deps), "initial", "--json"), &created); err != nil {
		t.Fatal(err)
	}
	attachmentPath := filepath.Join(project, "failure.log")
	var received frontend.TurnInput
	deps.turnRunner = codingTurnRunnerFunc(func(
		ctx context.Context,
		request codingTurnRequest,
	) (codingTurnOutcome, error) {
		received = request.Input.Clone()
		err := request.Store.AppendUserMessage(
			ctx,
			request.Lease,
			request.Metadata,
			turnDisplayContent(request.Input),
		)
		return codingTurnOutcome{PromptStored: appendOutcomeAllowsMetadataSave(err)}, err
	})
	executeCommand(
		t,
		newResumeCommand(deps),
		created.ThreadID,
		"--attach",
		attachmentPath,
		"--json",
	)
	if received.Text != "" || len(received.Attachments) != 1 ||
		received.Attachments[0].Path != attachmentPath {
		t.Fatalf("structured resumed input = %+v", received)
	}
}

func TestResumePromptPromotesPendingThreadMetadata(t *testing.T) {
	home := t.TempDir()
	projectRoot := t.TempDir()
	now := time.Date(2026, time.August, 29, 17, 30, 0, 0, time.UTC)
	deps := testDependencies(home, projectRoot, &now)
	_, store, metadata, lease, err := prepareNewThread(t.Context(), deps, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}

	executeCommand(
		t,
		newResumeCommand(deps),
		metadata.ThreadID,
		"--prompt",
		"Inspect repository carefully",
		"--json",
	)
	persisted, err := store.Load(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.PendingFirstPrompt || persisted.Title != "Inspect repository carefully" ||
		persisted.Preview != "Inspect repository carefully" {
		t.Fatalf("resumed pending metadata = %+v", persisted)
	}
	baseline, err := store.LoadRepositoryBaseline(metadata.ThreadID)
	if err != nil || baseline.ProjectKey != metadata.Project.ProjectKey {
		t.Fatalf("pending thread baseline = %#v / %v", baseline, err)
	}
}

func TestResumeRejectsThreadWithoutRepositoryBaseline(t *testing.T) {
	home := t.TempDir()
	projectRoot := t.TempDir()
	now := time.Date(2026, time.August, 29, 17, 45, 0, 0, time.UTC)
	deps := testDependencies(home, projectRoot, &now)
	deps.turnRunner = codingTurnRunnerFunc(func(
		context.Context,
		codingTurnRequest,
	) (codingTurnOutcome, error) {
		t.Fatal("turn runner called without a repository baseline")
		return codingTurnOutcome{}, nil
	})
	project, err := thread.ResolveProject(t.Context(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := thread.NewStore(filepath.Join(home, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := thread.NewPendingMetadata(thread.NewThreadID(), project, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}

	_, err = executeCommandError(
		newResumeCommand(deps),
		metadata.ThreadID,
		"--prompt",
		"must not be admitted",
		"--json",
	)
	if err == nil || !errors.Is(err, os.ErrNotExist) ||
		!strings.Contains(err.Error(), "resume: load repository baseline") {
		t.Fatalf("resume without baseline error = %v", err)
	}
	if _, loadErr := store.LoadRepositoryBaseline(metadata.ThreadID); !errors.Is(loadErr, os.ErrNotExist) {
		t.Fatalf("resume created missing baseline: %v", loadErr)
	}
	persisted, loadErr := store.Load(metadata.ThreadID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if !persisted.UpdatedAt.Equal(metadata.UpdatedAt) || !persisted.PendingFirstPrompt {
		t.Fatalf("failed resume mutated metadata = %#v", persisted)
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
		baseline, err := request.Store.LoadRepositoryBaselineWithLease(
			ctx,
			request.Lease,
			request.Metadata,
		)
		if err != nil || baseline.ProjectKey != request.Metadata.Project.ProjectKey {
			t.Fatalf("pre-admission repository baseline = %#v / %v", baseline, err)
		}
		err = request.Store.AppendUserMessage(ctx, request.Lease, request.Metadata, request.Input.Text)
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
		appendErr := request.Store.AppendUserMessage(ctx, request.Lease, request.Metadata, request.Input.Text)
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
		err := runNew(
			t.Context(),
			failingWriter{err: outputErr},
			deps,
			frontend.TurnInput{Text: "committed code prompt"},
			"",
			true,
		)
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
			err := request.Store.AppendUserMessage(ctx, request.Lease, request.Metadata, request.Input.Text)
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

func appendHistoryMessage(t *testing.T, stateRoot, sessionKey string, message providers.Message) {
	t.Helper()
	canonical, err := memory.NewJSONLStore(filepath.Join(stateRoot, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	backend := session.NewJSONLBackend(canonical)
	if err := backend.AppendTurnMessage(t.Context(), sessionKey, message); err != nil {
		_ = backend.Close()
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
}
