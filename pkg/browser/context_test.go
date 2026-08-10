package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testContextCatalog() ContextCatalog {
	return ContextCatalog{
		ID: "context_catalog_1", Generation: 1,
		SelectedTabID: "tab_primary",
		Tabs: []TabContext{
			{
				ID: "tab_primary", Kind: TabPrimary, CreationSequence: 1,
				DocumentGeneration: 3, URL: "https://example.com/main#section",
				Origin: "https://example.com", Title: "Main",
				Frames: []FrameContext{
					{
						ID: "frame_child", CreationSequence: 1, Depth: 1,
						DocumentGeneration: 2, URL: "https://example.com/frame",
						Origin: "https://example.com", Label: "Child",
						Availability: FrameReady,
					},
					{
						ID: "frame_nested", ParentFrameID: "frame_child",
						CreationSequence: 2, Depth: 2, DocumentGeneration: 1,
						URL: "https://frames.example.net/nested", Origin: "https://frames.example.net",
						Label: "Nested", Availability: FrameUnavailable,
						SafeFailure: "frame_policy_denied",
					},
				},
			},
			{
				ID: "tab_popup", Kind: TabPopup, CreationSequence: 2,
				OpenerTabID: "tab_primary", OpenerInvocationID: "invocation_popup_1",
				DocumentGeneration: 1, URL: initialBlankOrigin, Origin: initialBlankOrigin,
			},
		},
	}
}

func TestContextCatalogValidation(t *testing.T) {
	valid := testContextCatalog()
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*ContextCatalog)
	}{
		{name: "stale selected tab", mutate: func(c *ContextCatalog) { c.SelectedTabID = "tab_missing" }},
		{name: "unavailable selected frame", mutate: func(c *ContextCatalog) {
			c.SelectedFrameID = "frame_nested"
		}},
		{name: "duplicate tab", mutate: func(c *ContextCatalog) { c.Tabs[1].ID = "tab_primary" }},
		{name: "unordered tab", mutate: func(c *ContextCatalog) { c.Tabs[1].CreationSequence = 1 }},
		{name: "popup without correlation", mutate: func(c *ContextCatalog) {
			c.Tabs[1].OpenerInvocationID = ""
		}},
		{name: "popup with unknown opener", mutate: func(c *ContextCatalog) {
			c.Tabs[1].OpenerTabID = "tab_missing"
		}},
		{name: "tab with popup correlation", mutate: func(c *ContextCatalog) {
			c.Tabs[0].OpenerTabID = "tab_popup"
		}},
		{name: "duplicate frame", mutate: func(c *ContextCatalog) { c.Tabs[0].Frames[1].ID = "frame_child" }},
		{name: "parent after child", mutate: func(c *ContextCatalog) {
			c.Tabs[0].Frames[0], c.Tabs[0].Frames[1] = c.Tabs[0].Frames[1], c.Tabs[0].Frames[0]
		}},
		{name: "wrong depth", mutate: func(c *ContextCatalog) { c.Tabs[0].Frames[1].Depth = 3 }},
		{name: "ready failure", mutate: func(c *ContextCatalog) {
			c.Tabs[0].Frames[0].SafeFailure = "frame_policy_denied"
		}},
		{name: "unbounded title", mutate: func(c *ContextCatalog) {
			c.Tabs[0].Title = strings.Repeat("x", MaxContextLabelBytes+1)
		}},
		{name: "inconsistent truncation", mutate: func(c *ContextCatalog) { c.Truncated = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog := cloneContextCatalog(valid)
			test.mutate(&catalog)
			if err := catalog.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestContextCatalogRejectsSerializedCatalogAboveBound(t *testing.T) {
	catalog := testContextCatalog()
	catalog.Tabs[0].Frames = make([]FrameContext, MaxContextFramesPerTab)
	for index := range catalog.Tabs[0].Frames {
		catalog.Tabs[0].Frames[index] = FrameContext{
			ID:                 fmt.Sprintf("frame_%d", index),
			CreationSequence:   uint64(index + 1),
			Depth:              1,
			DocumentGeneration: 1,
			URL:                "https://example.com/" + strings.Repeat("x", MaxURLBytes-20),
			Origin:             "https://example.com",
			Label:              strings.Repeat("x", MaxContextLabelBytes),
			Availability:       FrameReady,
		}
	}
	encoded, err := json.Marshal(catalog)
	if err != nil || len(encoded) <= MaxContextCatalogBytes {
		t.Fatalf("encoded catalog size = %d, %v; want above %d", len(encoded), err, MaxContextCatalogBytes)
	}
	if err := catalog.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate() error = %v, want ErrInvalid", err)
	}
}

func TestContextBindingRequiresExactCurrentAuthority(t *testing.T) {
	catalog := testContextCatalog()
	session := testOpeningSession(testOwner())
	session.ContextAuthority = &catalog
	if !validContextBinding("", catalog.ID, catalog.Generation) ||
		validContextBinding("frame_child", "", catalog.Generation) ||
		validContextBinding("", catalog.ID, 0) {
		t.Fatal("validContextBinding() accepted a partial binding")
	}
	if !sessionMatchesContextBinding(session, "", catalog.ID, catalog.Generation) ||
		sessionMatchesContextBinding(session, "", catalog.ID, catalog.Generation+1) ||
		sessionMatchesContextBinding(session, "frame_child", catalog.ID, catalog.Generation) {
		t.Fatal("sessionMatchesContextBinding() did not require exact current authority")
	}
	legacy := testOpeningSession(testOwner())
	if !sessionMatchesContextBinding(legacy, "", "", 0) ||
		sessionMatchesContextBinding(legacy, "", catalog.ID, catalog.Generation) {
		t.Fatal("legacy session accepted context authority")
	}
}

func TestSessionsEqualIncludesContextCatalogValue(t *testing.T) {
	catalog := testContextCatalog()
	left := testOpeningSession(testOwner())
	left.ContextAuthority = &catalog
	right := cloneSession(left)
	if !sessionsEqual(left, right) {
		t.Fatal("sessionsEqual() rejected an equal defensive copy")
	}
	right.ContextAuthority.Generation++
	if sessionsEqual(left, right) {
		t.Fatal("sessionsEqual() accepted a different context generation")
	}
}

func TestUpdateSessionExactReconcilesCopiedContextCatalog(t *testing.T) {
	memory := NewMemoryStore()
	session := createReadySession(t, memory, testOwner())
	catalog := testContextCatalog()
	session.ContextAuthority = &catalog
	session.Revision++
	session.UpdatedAt++
	session.LastActivityAt++
	if err := memory.UpdateSession(t.Context(), 2, session); err != nil {
		t.Fatal(err)
	}
	store := &committedWarningSessionUpdateStore{
		MemoryStore:     memory,
		warnControllers: map[ControllerState]int{ControllerAgent: 1},
	}
	broker := &Broker{store: store}
	next := cloneSession(session)
	next.Revision++
	next.UpdatedAt++
	next.LastActivityAt++
	got, err := broker.updateSessionExact(t.Context(), session.Revision, next)
	if err != nil || !sessionsEqual(got, next) {
		t.Fatalf("updateSessionExact() = %#v, %v; want reconciled context copy", got, err)
	}
}

func TestBrokerBindsPreparedActionToCurrentContextCatalog(t *testing.T) {
	store := NewMemoryStore()
	broker, _, session := openActionTestBroker(t, store)
	catalog := testContextCatalog()
	session.ContextAuthority = &catalog
	session.Revision++
	session.UpdatedAt++
	session.LastActivityAt++
	if err := store.UpdateSession(t.Context(), session.Revision-1, session); err != nil {
		t.Fatal(err)
	}
	observation, err := broker.Observe(t.Context(), testOwner(), session.ID, session.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if observation.ContextCatalogID != catalog.ID || observation.ContextGeneration != catalog.Generation ||
		observation.FrameID != "" {
		t.Fatalf("observation context binding = %#v", observation)
	}
	request := PrepareActionRequest{
		Owner: testOwner(), RequestID: "request_context_fill", SessionID: session.ID,
		TabID: session.TabID, SnapshotID: observation.SnapshotID,
		SnapshotGeneration: observation.SnapshotGeneration,
		Action:             Action{Kind: ActionFill, Ref: onlyVisibleRef(t, observation.Snapshot), Value: "value"},
	}
	if _, err = broker.PrepareAction(t.Context(), request); !errors.Is(err, ErrStale) {
		t.Fatalf("PrepareAction() without context binding error = %v, want ErrStale", err)
	}
	request.ContextCatalogID = catalog.ID
	request.ContextGeneration = catalog.Generation
	prepared, err := broker.PrepareAction(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Action.ContextCatalogID != catalog.ID ||
		prepared.Action.ContextGeneration != catalog.Generation || prepared.Action.FrameID != "" {
		t.Fatalf("prepared context binding = %#v", prepared.Action)
	}
}

func TestBrokerRejectsDurableChildFramePreparationBeforeWorkerUse(t *testing.T) {
	store := NewMemoryStore()
	broker, worker, session := openActionTestBroker(t, store)
	catalog := testContextCatalog()
	catalog.SelectedFrameID = "frame_child"
	session.ContextAuthority = &catalog
	session.FrameID = catalog.SelectedFrameID
	session.Revision++
	session.UpdatedAt++
	session.LastActivityAt++
	if err := store.UpdateSession(t.Context(), session.Revision-1, session); err != nil {
		t.Fatal(err)
	}
	session.SnapshotID = "snapshot_child_frame"
	session.SnapshotGeneration = 1
	session.SnapshotOrigin = "https://example.com"
	session.Revision++
	session.UpdatedAt++
	session.LastActivityAt++
	if err := store.UpdateSession(t.Context(), session.Revision-1, session); err != nil {
		t.Fatal(err)
	}

	created := broker.now().UTC()
	prepared := PreparedAction{
		RequestID: "request_seeded_child_frame", SessionID: session.ID, Owner: session.Owner,
		Target: session.Target, Profile: session.Profile,
		ControllerGeneration: session.ControllerGeneration, TabID: session.TabID,
		FrameID: session.FrameID, ContextCatalogID: catalog.ID, ContextGeneration: catalog.Generation,
		SnapshotID: session.SnapshotID, SnapshotGeneration: session.SnapshotGeneration,
		CurrentOrigin: session.SnapshotOrigin,
		Action:        Action{Kind: ActionScroll, Direction: "down", Amount: 1}, Effect: EffectRead,
		DryRun: session.DryRun, PolicyRevision: session.PolicyRevision,
		CatalogRevision: worker.CatalogRevision(), CreatedAt: created.UnixNano(),
		ExpiresAt: created.Add(time.Minute).UnixNano(),
	}
	prepared.ID = derivedIdentifier("prepared", prepared.Owner, prepared.SessionID, prepared.RequestID)
	var err error
	prepared.ActionHash, err = hashPreparedAction(prepared)
	if err != nil {
		t.Fatal(err)
	}
	invocation := Invocation{
		ID:               derivedIdentifier("invocation", prepared.Owner, prepared.SessionID, prepared.RequestID),
		PreparedActionID: prepared.ID, SessionID: prepared.SessionID, Owner: prepared.Owner,
		ActionHash: prepared.ActionHash, Effect: prepared.Effect, State: InvocationPrepared,
		Revision: 1, CreatedAt: prepared.CreatedAt, UpdatedAt: prepared.CreatedAt,
		ExpiresAt: prepared.ExpiresAt,
	}
	if err = store.CreatePreparation(t.Context(), prepared, invocation); err != nil {
		t.Fatal(err)
	}
	worker.observeErr = errors.New("worker must not be observed")
	worker.observeCalls = 0
	worker.catalogCalls = 0
	if _, err = broker.ExecuteAction(
		t.Context(),
		session.Owner,
		prepared.ID,
		nil,
	); !errors.Is(
		err,
		ErrDriverIncompatible,
	) {
		t.Fatalf("ExecuteAction() error = %v, want ErrDriverIncompatible", err)
	}
	if worker.observeCalls != 0 || worker.catalogCalls != 0 || len(worker.actions) != 0 ||
		worker.resolveCalls != 0 || len(worker.uploads) != 0 {
		t.Fatalf("child-frame preparation reached worker: %#v", worker)
	}
	storedInvocation, err := store.GetInvocation(t.Context(), invocation.ID)
	if err != nil || storedInvocation.State != InvocationPrepared {
		t.Fatalf("stored invocation = %#v, %v; want prepared", storedInvocation, err)
	}
}

func TestMemoryStoreContextAuthorityTransitionsAndCopies(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	session := createReadySession(t, store, testOwner())
	catalog := testContextCatalog()
	session.ContextAuthority = &catalog
	session.Revision++
	session.UpdatedAt++
	session.LastActivityAt++
	if err := store.UpdateSession(ctx, 2, session); err != nil {
		t.Fatalf("initialize context authority: %v", err)
	}

	catalog.Tabs[0].Title = "mutated caller"
	stored, err := store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ContextAuthority.Tabs[0].Title != "Main" {
		t.Fatalf("stored catalog was aliased: %#v", stored.ContextAuthority)
	}
	stored.ContextAuthority.Tabs[0].Title = "mutated result"
	again, _ := store.GetSession(ctx, session.ID)
	if again.ContextAuthority.Tabs[0].Title != "Main" {
		t.Fatalf("returned catalog was aliased: %#v", again.ContextAuthority)
	}

	selected := cloneSession(again)
	selected.ContextAuthority.Generation++
	selected.ContextAuthority.SelectedTabID = "tab_popup"
	selected.TabID = "tab_popup"
	selected.Revision++
	selected.UpdatedAt++
	selected.LastActivityAt++
	if err = store.UpdateSession(ctx, again.Revision, selected); err != nil {
		t.Fatalf("select popup context: %v", err)
	}

	withoutGeneration := cloneSession(selected)
	withoutGeneration.ContextAuthority.SelectedTabID = "tab_primary"
	withoutGeneration.ContextAuthority.SelectedFrameID = "frame_child"
	withoutGeneration.TabID = "tab_primary"
	withoutGeneration.FrameID = "frame_child"
	withoutGeneration.Revision++
	withoutGeneration.UpdatedAt++
	withoutGeneration.LastActivityAt++
	if err = store.UpdateSession(ctx, selected.Revision, withoutGeneration); !errors.Is(err, ErrConflict) {
		t.Fatalf("selection without generation error = %v, want ErrConflict", err)
	}

	skipped := cloneSession(selected)
	skipped.ContextAuthority.Generation += 2
	skipped.Revision++
	skipped.UpdatedAt++
	skipped.LastActivityAt++
	if err = store.UpdateSession(ctx, selected.Revision, skipped); !errors.Is(err, ErrConflict) {
		t.Fatalf("skipped generation error = %v, want ErrConflict", err)
	}

	observed := cloneSession(selected)
	observed.SnapshotID = "snapshot_context_1"
	observed.SnapshotGeneration = 1
	observed.SnapshotOrigin = initialBlankOrigin
	observed.Revision++
	observed.UpdatedAt++
	observed.LastActivityAt++
	if err = store.UpdateSession(ctx, selected.Revision, observed); err != nil {
		t.Fatalf("context observation update: %v", err)
	}
	retainedSnapshot := cloneSession(observed)
	retainedSnapshot.ContextAuthority.Generation++
	retainedSnapshot.Revision++
	retainedSnapshot.UpdatedAt++
	retainedSnapshot.LastActivityAt++
	if err = store.UpdateSession(ctx, observed.Revision, retainedSnapshot); !errors.Is(err, ErrConflict) {
		t.Fatalf("context change retaining snapshot error = %v, want ErrConflict", err)
	}
}

func TestFileStorePersistsContextAuthorityWithoutAliasing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "browser.json")
	store, err := NewFileStore(path, 32, DefaultFileStoreBytes)
	if err != nil {
		t.Fatal(err)
	}
	session := testOpeningSession(testOwner())
	catalog := testContextCatalog()
	session.ContextAuthority = &catalog
	if err = store.CreateSession(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	catalog.Tabs[0].Title = "mutated caller"
	stored, err := store.GetSession(t.Context(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ContextAuthority.Tabs[0].Title != "Main" {
		t.Fatalf("file store catalog was aliased: %#v", stored.ContextAuthority)
	}
	store.Close()

	reopened, err := NewFileStore(path, 32, DefaultFileStoreBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reloaded, err := reopened.GetSession(t.Context(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.ContextAuthority, stored.ContextAuthority) {
		t.Fatalf("reloaded context authority = %#v, want %#v", reloaded.ContextAuthority, stored.ContextAuthority)
	}
}
