package coding

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	codingpicker "github.com/bogdanovich/mintclaw/pkg/coding/picker"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

func TestPickerCatalogSourceScopesSearchPagesAndMapsEdgeStates(t *testing.T) {
	root := t.TempDir()
	store, err := thread.NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	current := pickerFixtureProject(t, filepath.Join(root, "current"))
	other := pickerFixtureProject(t, filepath.Join(root, "other"))
	base := time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC)
	first := pickerFixtureThread(t, store, current, "Parser newest", base.Add(3*time.Minute))
	second := pickerFixtureThread(t, store, current, "Parser older", base.Add(2*time.Minute))
	foreign := pickerFixtureThread(t, store, other, "Foreign parser", base.Add(time.Minute))
	transcript := pickerFixtureThread(t, store, current, "Neutral work", base.Add(4*time.Minute))
	matchedAt := base.Add(-time.Hour)
	writePickerHistory(t, store, transcript, []providers.Message{{
		Role: "assistant", Content: "a transcript-only-needle appears here", CreatedAt: &matchedAt,
	}})
	threadsRoot := filepath.Join(store.Root(), "threads")
	if err := os.MkdirAll(filepath.Join(threadsRoot, "corrupt-entry"), 0o700); err != nil {
		t.Fatal(err)
	}
	catalog, err := thread.NewCatalog(store, thread.CatalogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	searcher, err := thread.NewHistoricalSearcher(store, thread.HistoricalSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	source := &pickerCatalogSource{
		store:          store,
		catalog:        catalog,
		searcher:       searcher,
		currentProject: current,
		observeProject: func(_ context.Context, project thread.ProjectIdentity) pickerProjectObservation {
			if project.ProjectKey == other.ProjectKey {
				return pickerProjectObservation{location: codingpicker.LocationMissing}
			}
			return pickerProjectObservation{
				location: codingpicker.LocationAvailable, branch: "feature/live", repositoryKnown: true,
				dirty: true, currentBranch: "feature/live", currentHead: "new-head",
			}
		},
		inspectLease: func(threadID string) (thread.LeaseInspection, error) {
			if threadID == second.ThreadID {
				return thread.LeaseInspection{Busy: true, Owner: &thread.LeaseOwner{PID: 42, Hostname: "fixture"}}, nil
			}
			return thread.LeaseInspection{}, nil
		},
	}
	page, err := source.Page(t.Context(), codingpicker.Query{Search: "parser", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ThreadID != first.ThreadID || !page.Items[0].CurrentProject ||
		!page.Items[0].Dirty || !page.Items[0].Stale || page.Items[0].Branch != "feature/live" ||
		!page.HasMore || page.NextOffset != 1 || page.SkippedTotal != 1 {
		t.Fatalf("current picker page = %+v", page)
	}
	secondPage, err := source.Page(t.Context(), codingpicker.Query{Search: "parser", Offset: 1, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Items) != 1 || secondPage.Items[0].ThreadID != second.ThreadID ||
		!secondPage.Items[0].Locked || secondPage.Items[0].LockOwnerPID != 42 {
		t.Fatalf("second picker page = %+v", secondPage)
	}
	all, err := source.Page(t.Context(), codingpicker.Query{AllProjects: true, Search: "foreign", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Items) != 1 || all.Items[0].ThreadID != foreign.ThreadID || all.Items[0].CurrentProject ||
		all.Items[0].Location != codingpicker.LocationMissing {
		t.Fatalf("all-project picker page = %+v", all)
	}
	historical, err := source.Page(t.Context(), codingpicker.Query{Search: "transcript-only-needle", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(historical.Items) != 1 || historical.Items[0].ThreadID != transcript.ThreadID ||
		historical.Items[0].MatchKind != string(thread.HistoricalMatchTranscript) ||
		!historical.Items[0].MatchedAt.Equal(matchedAt) || historical.Items[0].MatchedMessage != 1 ||
		!strings.Contains(historical.Items[0].MatchSnippet, "transcript-only-needle") {
		t.Fatalf("historical picker page = %+v", historical)
	}
}

func TestPickerCatalogSourceObservesRealMissingAndLockedThreads(t *testing.T) {
	root := t.TempDir()
	store, err := thread.NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	project := pickerFixtureProject(t, filepath.Join(root, "project"))
	metadata := pickerFixtureThread(t, store, project, "Live thread", time.Now().UTC())
	lease, err := store.AcquireLease(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Release() }()
	source, err := newPickerCatalogSource(store, project)
	if err != nil {
		t.Fatal(err)
	}
	page, err := source.Page(t.Context(), codingpicker.Query{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Location != codingpicker.LocationAvailable ||
		!page.Items[0].CurrentProject || !page.Items[0].Locked || page.Items[0].LockOwnerPID != os.Getpid() {
		t.Fatalf("live picker observation = %+v", page)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	page, err = source.Page(t.Context(), codingpicker.Query{Limit: 20})
	if err != nil || len(page.Items) != 1 || page.Items[0].Locked {
		t.Fatalf("released picker observation = %+v, %v", page, err)
	}
	if err := os.RemoveAll(project.ProjectRoot); err != nil {
		t.Fatal(err)
	}
	allPage, err := source.Page(t.Context(), codingpicker.Query{AllProjects: true, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(allPage.Items) != 1 || allPage.Items[0].Location != codingpicker.LocationMissing {
		t.Fatalf("missing picker observation = %+v", allPage)
	}
}

func TestPickerCatalogSourceObservesRealDirtyAndStaleGitState(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "tracked.txt"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "picker@example.invalid"},
		{"config", "user.name", "Picker Test"},
		{"add", "."},
		{"commit", "-m", "initial"},
	} {
		runPickerGit(t, projectRoot, args...)
	}
	persisted := pickerFixtureProject(t, projectRoot)
	store, err := thread.NewStore(filepath.Join(root, "coding"))
	if err != nil {
		t.Fatal(err)
	}
	metadata := pickerFixtureThread(t, store, persisted, "Git thread", time.Now().UTC())
	runPickerGit(t, projectRoot, "switch", "-c", "feature/live")
	if err := os.WriteFile(filepath.Join(projectRoot, "untracked.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	current, err := thread.ResolveProject(t.Context(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	source, err := newPickerCatalogSource(store, current)
	if err != nil {
		t.Fatal(err)
	}
	page, err := source.Page(t.Context(), codingpicker.Query{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ThreadID != metadata.ThreadID ||
		page.Items[0].Branch != "feature/live" || !page.Items[0].RepositoryKnown ||
		!page.Items[0].Dirty || !page.Items[0].Stale || page.Items[0].StateIncomplete {
		t.Fatalf("Git picker observation = %+v", page)
	}
}

func pickerFixtureProject(t *testing.T, root string) thread.ProjectIdentity {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := thread.ResolveProject(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func pickerFixtureThread(
	t *testing.T,
	store *thread.Store,
	project thread.ProjectIdentity,
	title string,
	updated time.Time,
) thread.Metadata {
	t.Helper()
	metadata, err := thread.NewMetadata(thread.NewThreadID(), project, title, updated.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	metadata.UpdatedAt = updated
	if err := metadata.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(metadata); err != nil {
		t.Fatal(err)
	}
	return metadata
}

func writePickerHistory(
	t *testing.T,
	store *thread.Store,
	metadata thread.Metadata,
	history []providers.Message,
) {
	t.Helper()
	root, err := store.ThreadRoot(metadata.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := memory.NewJSONLStore(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	backend := session.NewJSONLBackend(canonical)
	if err := backend.ReplaceTurnHistory(t.Context(), metadata.SessionKey, history); err != nil {
		_ = backend.Close()
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPickerProjectStaleRequiresCompleteAvailableObservation(t *testing.T) {
	persisted := thread.ProjectIdentity{GitBranch: "main", GitHead: "old"}
	cases := []struct {
		name        string
		observation pickerProjectObservation
		want        bool
	}{
		{name: "changed", observation: pickerProjectObservation{
			location: codingpicker.LocationAvailable, currentBranch: "main", currentHead: "new",
		}, want: true},
		{name: "same", observation: pickerProjectObservation{
			location: codingpicker.LocationAvailable, currentBranch: "main", currentHead: "old",
		}},
		{name: "missing", observation: pickerProjectObservation{location: codingpicker.LocationMissing}},
		{name: "incomplete", observation: pickerProjectObservation{
			location: codingpicker.LocationAvailable, currentBranch: "other", stateIncomplete: true,
		}},
	}
	got := make([]bool, 0, len(cases))
	want := make([]bool, 0, len(cases))
	for _, testCase := range cases {
		got = append(got, pickerProjectStale(persisted, testCase.observation))
		want = append(want, testCase.want)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stale states = %v, want %v", got, want)
	}
}

func runPickerGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
