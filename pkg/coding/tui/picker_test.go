package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	codingpicker "github.com/bogdanovich/mintclaw/pkg/coding/picker"
)

type fakePickerSource struct {
	mu      sync.Mutex
	queries []codingpicker.Query
	page    func(codingpicker.Query) (codingpicker.Page, error)
}

type cancelAwarePickerSource struct {
	started  chan codingpicker.Query
	canceled chan codingpicker.Query
	page     codingpicker.Page
}

func (s *cancelAwarePickerSource) Page(ctx context.Context, query codingpicker.Query) (codingpicker.Page, error) {
	if query.Search != "blocking" {
		return s.page, nil
	}
	s.started <- query
	<-ctx.Done()
	s.canceled <- query
	return codingpicker.Page{}, ctx.Err()
}

func (s *fakePickerSource) Page(
	_ context.Context,
	query codingpicker.Query,
) (codingpicker.Page, error) {
	s.mu.Lock()
	s.queries = append(s.queries, query)
	s.mu.Unlock()
	return s.page(query)
}

func (s *fakePickerSource) latestQuery(t *testing.T) codingpicker.Query {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queries) == 0 {
		t.Fatal("picker source received no query")
	}
	return s.queries[len(s.queries)-1]
}

func TestPickerRendersBoundedAccessibleThreadAndCatalogueStates(t *testing.T) {
	now := time.Date(2026, time.August, 16, 10, 0, 0, 0, time.UTC)
	page := codingpicker.Page{
		Items: []codingpicker.Item{
			{
				ThreadID: "11111111-1111-1111-1111-111111111111", Title: "Fix parser", Preview: "Handle nested input",
				UpdatedAt: now.Add(-2 * time.Hour), ProjectRoot: "/work/mintclaw", InvocationCWD: "/work/mintclaw",
				Branch: "feature/parser", CurrentProject: true, Location: codingpicker.LocationAvailable,
				RepositoryKnown: true, Dirty: true, Stale: true,
			},
			{
				ThreadID: "22222222-2222-2222-2222-222222222222", Title: "Missing project", Preview: "Recover it",
				UpdatedAt: now.Add(-48 * time.Hour), ProjectRoot: "/missing", InvocationCWD: "/missing",
				Branch: "main", Location: codingpicker.LocationMissing, Locked: true, LockOwnerPID: 42,
			},
		},
		SkippedTotal: 3, ScanTruncated: true, Matched: 2,
	}
	model := newPickerModel(
		t.Context(),
		&fakePickerSource{},
		codingpicker.Query{Limit: 20},
		page,
		func() time.Time {
			return now
		},
	)
	model.width = 100
	model.height = 15
	view := model.View()
	for _, want := range []string{
		"MintClaw resume", "scope current project", "> 1. Fix parser", "2h ago", "11111111",
		"Handle nested input", "[dirty]", "[stale]", "branch feature/parser", "/work/mintclaw",
		"[missing]", "[locked]", "3 corrupt catalog entries skipped", "catalog scan truncated",
		"↑/↓ select", "/ search", "A scope", "Enter resume", "Q cancel",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("picker view omits %q: %q", want, view)
		}
	}
	model.width = 40
	for _, line := range strings.Split(model.View(), "\n") {
		if pickerLineWidth(line) > 40 {
			t.Fatalf("picker line exceeds 40 cells (%d): %q", pickerLineWidth(line), line)
		}
	}
}

func TestPickerSearchPagingScopeAndStrictUnavailableSelection(t *testing.T) {
	available := codingpicker.Item{
		ThreadID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Title: "Available", Preview: "ready",
		ProjectRoot: "/work/current", InvocationCWD: "/work/current", CurrentProject: true,
		Location: codingpicker.LocationAvailable,
	}
	locked := available
	locked.ThreadID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	locked.Title = "Locked"
	locked.Locked = true
	locked.LockOwnerPID = 99
	source := &fakePickerSource{page: func(query codingpicker.Query) (codingpicker.Page, error) {
		items := []codingpicker.Item{locked, available}
		if query.Search == "available" {
			items = items[1:]
		}
		return codingpicker.Page{
			Items: items, Matched: 3, HasMore: query.Offset == 0, NextOffset: 2,
		}, nil
	}}
	query := codingpicker.Query{Limit: 2}
	initial, err := source.Page(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	model := newPickerModel(t.Context(), source, query, initial, time.Now)

	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*pickerModel)
	if command != nil || model.selectedThreadID != "" || !strings.Contains(model.notice, "pid 99") {
		t.Fatalf("locked selection was admitted: id=%q notice=%q", model.selectedThreadID, model.notice)
	}
	model, _ = updatePicker(t, model, tea.KeyMsg{Type: tea.KeyDown})
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*pickerModel)
	if model.selectedThreadID != available.ThreadID || command == nil {
		t.Fatalf("available selection = id %q command %v", model.selectedThreadID, command)
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatalf("selection command did not quit: %T", command())
	}

	model = newPickerModel(t.Context(), source, query, initial, time.Now)
	model, _ = updatePicker(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if latest := source.latestQuery(t); !latest.AllProjects || latest.Offset != 0 {
		t.Fatalf("all-project query = %+v", latest)
	}
	model, _ = updatePicker(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "available" {
		model, _ = updatePicker(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	model, _ = updatePicker(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if latest := source.latestQuery(t); latest.Search != "available" || latest.Offset != 0 {
		t.Fatalf("search query = %+v", latest)
	}
	_, _ = updatePicker(t, model, tea.KeyMsg{Type: tea.KeyPgDown})
	if latest := source.latestQuery(t); latest.Offset != 2 {
		t.Fatalf("next-page query = %+v", latest)
	}
}

func TestPickerEmptyAndErrorPagesRemainUsable(t *testing.T) {
	injected := fmt.Errorf("catalog unavailable")
	source := &fakePickerSource{page: func(query codingpicker.Query) (codingpicker.Page, error) {
		if query.Search == "fail" {
			return codingpicker.Page{}, injected
		}
		return codingpicker.Page{}, nil
	}}
	model := newPickerModel(
		t.Context(), source, codingpicker.Query{Limit: 20}, codingpicker.Page{}, time.Now,
	)
	if view := model.View(); !strings.Contains(view, "No coding threads found") || !strings.Contains(view, "Q cancel") {
		t.Fatalf("empty picker is not usable: %q", view)
	}
	model.search.SetValue("fail")
	model.searching = true
	model, _ = updatePicker(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.err == nil || !strings.Contains(model.View(), "R retry") || !strings.Contains(model.View(), "Q cancel") {
		t.Fatalf("error picker is not usable: error=%v view=%q", model.err, model.View())
	}
}

func TestPickerLatestRequestWinsAndFailedPageCannotBeSelected(t *testing.T) {
	item := codingpicker.Item{
		ThreadID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Title: "Current", Preview: "ready",
		ProjectRoot: "/work", InvocationCWD: "/work", CurrentProject: true,
		Location: codingpicker.LocationAvailable,
	}
	latest := item
	latest.ThreadID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	latest.Title = "Latest"
	injected := fmt.Errorf("catalog unavailable")
	source := &fakePickerSource{page: func(query codingpicker.Query) (codingpicker.Page, error) {
		switch query.Search {
		case "latest":
			return codingpicker.Page{
				Items: []codingpicker.Item{latest}, Matched: 2, HasMore: true, NextOffset: 20,
			}, nil
		case "fail":
			return codingpicker.Page{}, injected
		default:
			return codingpicker.Page{Items: []codingpicker.Item{item}, Matched: 1}, nil
		}
	}}
	initialQuery := codingpicker.Query{Limit: 20}
	model := newPickerModel(
		t.Context(), source, initialQuery, codingpicker.Page{Items: []codingpicker.Item{item}, Matched: 1}, time.Now,
	)

	staleCommand := model.load(codingpicker.Query{Search: "stale", Limit: 20})
	latestQuery := codingpicker.Query{Search: "latest", Limit: 20}
	latestCommand := model.load(latestQuery)
	updated, _ := model.Update(latestCommand())
	model = updated.(*pickerModel)
	updated, _ = model.Update(staleCommand())
	model = updated.(*pickerModel)
	if model.query != latestQuery || model.pageQuery != latestQuery || len(model.page.Items) != 1 ||
		model.page.Items[0].ThreadID != latest.ThreadID {
		t.Fatalf(
			"stale request replaced latest page: query=%+v pageQuery=%+v page=%+v",
			model.query,
			model.pageQuery,
			model.page,
		)
	}

	failedQuery := codingpicker.Query{Search: "fail", Limit: 20}
	failedCommand := model.load(failedQuery)
	generation := model.requestGeneration
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	model = updated.(*pickerModel)
	if command != nil || model.query != failedQuery || model.requestGeneration != generation {
		t.Fatalf(
			"pending query paginated from stale page: query=%+v generation=%d",
			model.query,
			model.requestGeneration,
		)
	}
	updated, _ = model.Update(failedCommand())
	model = updated.(*pickerModel)
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	model = updated.(*pickerModel)
	if command != nil || model.query != failedQuery || model.requestGeneration != generation {
		t.Fatalf(
			"failed query paginated from stale page: query=%+v generation=%d",
			model.query,
			model.requestGeneration,
		)
	}
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*pickerModel)
	if model.err == nil || model.query != failedQuery || model.pageQuery == failedQuery ||
		model.selectedThreadID != "" || command != nil {
		t.Fatalf(
			"failed page remained selectable: error=%v query=%+v pageQuery=%+v selected=%q command=%v",
			model.err,
			model.query,
			model.pageQuery,
			model.selectedThreadID,
			command,
		)
	}
}

func TestPickerCancelsSupersededLoad(t *testing.T) {
	latestQuery := codingpicker.Query{Search: "latest", Limit: 20}
	latestPage := codingpicker.Page{Matched: 1}
	source := &cancelAwarePickerSource{
		started: make(chan codingpicker.Query, 1), canceled: make(chan codingpicker.Query, 1), page: latestPage,
	}
	model := newPickerModel(
		t.Context(), source, codingpicker.Query{Limit: 20}, codingpicker.Page{}, time.Now,
	)
	blockingCommand := model.load(codingpicker.Query{Search: "blocking", Limit: 20})
	blockingResult := make(chan tea.Msg, 1)
	go func() { blockingResult <- blockingCommand() }()
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("blocking picker request did not start")
	}

	latestCommand := model.load(latestQuery)
	select {
	case <-source.canceled:
	case <-time.After(time.Second):
		t.Fatal("superseded picker request was not canceled")
	}
	updated, _ := model.Update(<-blockingResult)
	model = updated.(*pickerModel)
	if !model.loading || model.query != latestQuery {
		t.Fatalf("stale cancellation changed latest request: loading=%t query=%+v", model.loading, model.query)
	}
	updated, _ = model.Update(latestCommand())
	model = updated.(*pickerModel)
	if model.loading || model.err != nil || model.pageQuery != latestQuery || model.page.Matched != latestPage.Matched {
		t.Fatalf("latest request did not complete: %+v", model)
	}
}

func TestRunPickerReturnsSelectedOrCanceledThread(t *testing.T) {
	item := codingpicker.Item{
		ThreadID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Title: "Thread", Preview: "ready",
		ProjectRoot: "/work", InvocationCWD: "/work", CurrentProject: true,
		Location: codingpicker.LocationAvailable,
	}
	source := &fakePickerSource{page: func(codingpicker.Query) (codingpicker.Page, error) {
		return codingpicker.Page{Items: []codingpicker.Item{item}, Matched: 1}, nil
	}}
	selection, err := RunPicker(t.Context(), source, PickerOptions{
		newProgram: func(model tea.Model, _ ...tea.ProgramOption) program {
			picker := model.(*pickerModel)
			updated, _ := picker.Update(tea.KeyMsg{Type: tea.KeyEnter})
			return fakeProgram{model: updated}
		},
	})
	if err != nil || selection.ThreadID != item.ThreadID || selection.Canceled {
		t.Fatalf("RunPicker() = %+v, %v", selection, err)
	}
}

func updatePicker(t *testing.T, model *pickerModel, message tea.Msg) (*pickerModel, tea.Cmd) {
	t.Helper()
	updated, command := model.Update(message)
	model = updated.(*pickerModel)
	if command == nil {
		return model, nil
	}
	result := command()
	if _, quit := result.(tea.QuitMsg); quit {
		return model, command
	}
	updated, next := model.Update(result)
	return updated.(*pickerModel), next
}
