package coding

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	codingpicker "github.com/bogdanovich/mintclaw/pkg/coding/picker"
	"github.com/bogdanovich/mintclaw/pkg/coding/thread"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
)

const (
	pickerObservationConcurrency = 4
	pickerObservationTimeout     = 1500 * time.Millisecond
)

type pickerCatalogSource struct {
	store          *thread.Store
	catalog        *thread.Catalog
	searcher       *thread.HistoricalSearcher
	currentProject thread.ProjectIdentity
	observeProject func(context.Context, thread.ProjectIdentity) pickerProjectObservation
	inspectLease   func(string) (thread.LeaseInspection, error)
}

type pickerProjectObservation struct {
	location        codingpicker.Location
	branch          string
	repositoryKnown bool
	dirty           bool
	currentBranch   string
	currentHead     string
	stateIncomplete bool
}

func newPickerCatalogSource(
	store *thread.Store,
	currentProject thread.ProjectIdentity,
) (codingpicker.Source, error) {
	catalog, err := thread.NewCatalog(store, thread.CatalogOptions{})
	if err != nil {
		return nil, err
	}
	searcher, err := thread.NewHistoricalSearcher(store, thread.HistoricalSearchOptions{})
	if err != nil {
		return nil, err
	}
	source := &pickerCatalogSource{
		store:          store,
		catalog:        catalog,
		searcher:       searcher,
		currentProject: currentProject,
		observeProject: observePickerProject,
	}
	source.inspectLease = store.InspectLease
	return source, nil
}

func (s *pickerCatalogSource) Page(
	ctx context.Context,
	query codingpicker.Query,
) (codingpicker.Page, error) {
	if s == nil || s.store == nil || s.catalog == nil || s.searcher == nil {
		return codingpicker.Page{}, fmt.Errorf("coding resume picker source is unavailable")
	}
	metadataPage, matches, page, err := s.queryPage(ctx, query)
	if err != nil {
		return codingpicker.Page{}, err
	}
	items := make([]codingpicker.Item, len(metadataPage))
	groups := make(map[string][]int)
	projects := make(map[string]thread.ProjectIdentity)
	for index, metadata := range metadataPage {
		items[index] = codingpicker.Item{
			ThreadID:       metadata.ThreadID,
			Title:          metadata.Title,
			Preview:        metadata.Preview,
			UpdatedAt:      metadata.UpdatedAt,
			ProjectRoot:    metadata.Project.ProjectRoot,
			InvocationCWD:  metadata.Project.InvocationCWD,
			Branch:         pickerPersistedBranch(metadata.Project),
			CurrentProject: metadata.Project.ProjectKey == s.currentProject.ProjectKey,
			Location:       codingpicker.LocationUnknown,
		}
		if len(matches) > index {
			items[index].MatchKind = string(matches[index].Kind)
			items[index].MatchSnippet = matches[index].Snippet
			items[index].MatchedAt = matches[index].MatchedAt
			items[index].MatchedMessage = matches[index].Message
		}
		key := metadata.Project.ProjectKey + "\x00" + metadata.Project.InvocationCWD
		groups[key] = append(groups[key], index)
		projects[key] = metadata.Project
	}

	observations := s.observeGroups(ctx, projects)
	if err := ctx.Err(); err != nil {
		return codingpicker.Page{}, err
	}
	for key, indices := range groups {
		observation := observations[key]
		for _, index := range indices {
			metadata := metadataPage[index]
			items[index].Location = observation.location
			items[index].RepositoryKnown = observation.repositoryKnown
			items[index].Dirty = observation.dirty
			items[index].StateIncomplete = observation.stateIncomplete
			if observation.branch != "" {
				items[index].Branch = observation.branch
			}
			items[index].Stale = pickerProjectStale(metadata.Project, observation)
		}
	}
	for index := range items {
		inspection, inspectErr := s.inspectLease(items[index].ThreadID)
		if inspectErr != nil {
			items[index].StateIncomplete = true
			continue
		}
		items[index].Locked = inspection.Busy
		if inspection.Owner != nil {
			items[index].LockOwnerPID = inspection.Owner.PID
			items[index].LockOwnerHost = inspection.Owner.Hostname
		}
	}
	return codingpicker.Page{
		Items:                 items,
		SkippedTotal:          page.SkippedTotal,
		Scanned:               page.Scanned,
		Matched:               page.Matched,
		ScanTruncated:         page.ScanTruncated,
		HasMore:               page.HasMore,
		NextOffset:            page.NextOffset,
		ContentThreadsScanned: page.ContentThreadsScanned,
		ContentBytesScanned:   page.ContentBytesScanned,
		ContentScanTruncated:  page.ContentScanTruncated,
	}, nil
}

type pickerSourcePage struct {
	SkippedTotal          int
	Scanned               int
	Matched               int
	ScanTruncated         bool
	HasMore               bool
	NextOffset            int
	ContentThreadsScanned int
	ContentBytesScanned   int64
	ContentScanTruncated  bool
}

func (s *pickerCatalogSource) queryPage(
	ctx context.Context,
	query codingpicker.Query,
) ([]thread.Metadata, []thread.HistoricalSearchMatch, pickerSourcePage, error) {
	if query.Search != "" {
		searchQuery := thread.HistoricalSearchQuery{
			All: query.AllProjects, Archived: query.Archived, Text: query.Search,
			Offset: query.Offset, Limit: query.Limit,
		}
		if !query.AllProjects {
			searchQuery.ProjectKey = s.currentProject.ProjectKey
		}
		page, err := s.searcher.Query(ctx, searchQuery)
		if err != nil {
			return nil, nil, pickerSourcePage{}, err
		}
		metadata := make([]thread.Metadata, len(page.Matches))
		for index := range page.Matches {
			metadata[index] = page.Matches[index].Metadata
		}
		return metadata, page.Matches, pickerSourcePage{
			SkippedTotal: page.SkippedTotal, Scanned: page.Scanned, Matched: page.Matched,
			ScanTruncated: page.ScanTruncated, HasMore: page.HasMore, NextOffset: page.NextOffset,
			ContentThreadsScanned: page.ContentThreadsScanned,
			ContentBytesScanned:   page.ContentBytesScanned,
			ContentScanTruncated:  page.ContentScanTruncated,
		}, nil
	}
	catalogQuery := thread.CatalogQuery{
		All: query.AllProjects, Archived: query.Archived, Offset: query.Offset, Limit: query.Limit,
	}
	if !query.AllProjects {
		catalogQuery.ProjectKey = s.currentProject.ProjectKey
	}
	page, err := s.catalog.Query(ctx, catalogQuery)
	if err != nil {
		return nil, nil, pickerSourcePage{}, err
	}
	return page.Threads, nil, pickerSourcePage{
		SkippedTotal: page.SkippedTotal, Scanned: page.Scanned, Matched: page.Matched,
		ScanTruncated: page.ScanTruncated, HasMore: page.HasMore, NextOffset: page.NextOffset,
	}, nil
}

func (s *pickerCatalogSource) observeGroups(
	ctx context.Context,
	projects map[string]thread.ProjectIdentity,
) map[string]pickerProjectObservation {
	type result struct {
		key         string
		observation pickerProjectObservation
	}
	results := make(chan result, len(projects))
	semaphore := make(chan struct{}, pickerObservationConcurrency)
	var wait sync.WaitGroup
	for key, project := range projects {
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				results <- result{key: key, observation: incompletePickerObservation()}
				return
			}
			defer func() { <-semaphore }()
			observationCtx, cancel := context.WithTimeout(ctx, pickerObservationTimeout)
			defer cancel()
			results <- result{key: key, observation: s.observeProject(observationCtx, project)}
		}()
	}
	wait.Wait()
	close(results)
	observations := make(map[string]pickerProjectObservation, len(projects))
	for result := range results {
		observations[result.key] = result.observation
	}
	return observations
}

func observePickerProject(ctx context.Context, persisted thread.ProjectIdentity) pickerProjectObservation {
	inspection, err := thread.InspectLocation(ctx, persisted, "")
	if err != nil {
		return incompletePickerObservation()
	}
	observation := pickerProjectObservation{location: pickerLocation(inspection.State)}
	if inspection.State != thread.LocationAvailable || inspection.Current == nil {
		return observation
	}
	observation.currentBranch = inspection.Current.GitBranch
	observation.currentHead = inspection.Current.GitHead
	observation.branch = pickerPersistedBranch(*inspection.Current)
	snapshot := codingworkspace.Capture(ctx, persisted.ProjectRoot, persisted.InvocationCWD, codingworkspace.Limits{
		ChangedPaths: 1,
		CommandBytes: 64 << 10,
		PromptBytes:  1,
		Timeout:      750 * time.Millisecond,
	})
	observation.repositoryKnown = snapshot.Git.Available && snapshot.Git.StatusAvailable
	observation.dirty = observation.repositoryKnown && snapshot.Git.Dirty
	observation.stateIncomplete = ctx.Err() != nil || snapshot.Warning != "" ||
		snapshot.Git.Available && !snapshot.Git.StatusAvailable
	return observation
}

func pickerLocation(state thread.LocationState) codingpicker.Location {
	switch state {
	case thread.LocationAvailable:
		return codingpicker.LocationAvailable
	case thread.LocationMissing:
		return codingpicker.LocationMissing
	case thread.LocationMoved:
		return codingpicker.LocationMoved
	default:
		return codingpicker.LocationUnknown
	}
}

func incompletePickerObservation() pickerProjectObservation {
	return pickerProjectObservation{
		location:        codingpicker.LocationUnknown,
		stateIncomplete: true,
	}
}

func pickerProjectStale(persisted thread.ProjectIdentity, observation pickerProjectObservation) bool {
	if observation.location != codingpicker.LocationAvailable || observation.stateIncomplete {
		return false
	}
	return strings.TrimSpace(persisted.GitBranch) != strings.TrimSpace(observation.currentBranch) ||
		strings.TrimSpace(persisted.GitHead) != strings.TrimSpace(observation.currentHead)
}

func pickerPersistedBranch(project thread.ProjectIdentity) string {
	if branch := strings.TrimSpace(project.GitBranch); branch != "" {
		return branch
	}
	if head := strings.TrimSpace(project.GitHead); head != "" {
		if len(head) > 8 {
			head = head[:8]
		}
		return "detached@" + head
	}
	if project.Kind == thread.ProjectKindDirectory {
		return "non-git"
	}
	return "unknown"
}
